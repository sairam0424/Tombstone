import type {
  FlagEnvironmentState,
  FlagEvent,
  FlagPrerequisite,
  FlagSnapshot,
  TargetingRule,
} from "./types.js";

// Three-tier immutable flag cache: memory → (Redis in relay mode) → defaults
// IMMUTABILITY RULE: all updates create new objects, never mutate in-place.
export class FlagCache {
  private memory: Map<string, FlagEnvironmentState> = new Map();
  private snapshot: FlagSnapshot | null = null;
  // Tracks which flag keys' CURRENT prerequisites/prerequisitesUpdatedAt
  // came from a live prerequisites_updated event (true) rather than a
  // snapshot load (false/absent). A tied ts alone cannot distinguish "a
  // live event that must win a tie against a slower, still-in-flight
  // snapshot" from "two DIFFERENT snapshots that happen to share flag-api's
  // coarse 1-second-resolution ts, where the second (newer LOAD, regardless
  // of ts) must still win" -- without this, loadSnapshot's own >= tie-break
  // (added to fix the first case) would incorrectly freeze prerequisites on
  // the FIRST of two same-ts snapshots forever, discarding the second
  // snapshot's genuinely different data even though every other field on
  // the same flag correctly takes it (found by adversarial review of this
  // fix's own first draft). Rebuilt fresh on every loadSnapshot call,
  // mirroring `memory`'s own freshness, so a flag dropped from a later
  // snapshot can't leave a stale entry behind.
  private prerequisitesFromLiveEvent: Map<string, boolean> = new Map();

  loadSnapshot(snapshot: FlagSnapshot): void {
    // flag-api's snapshot.ts is simply time.Now().Unix() at response
    // generation (services/flag-api/internal/api/v1/environments.go), not a
    // per-flag "last changed" value -- so a SLOWER, already-in-flight
    // fetchSnapshot() call (e.g. a lag-triggered refetch racing an
    // onReconnect-triggered one, both fire-and-forget/unawaited by
    // client.ts) can resolve AFTER a newer one already loaded. Rejecting an
    // incoming snapshot whose own ts is older than the last one actually
    // applied prevents that older response from silently clobbering fresher
    // state for every flag, not just prerequisites. Number.isFinite guards
    // against a malformed/non-numeric wire ts becoming NaN, which would
    // otherwise defeat every future comparison (NaN < x and x < NaN are
    // both always false).
    const snapshotTs = Number.isFinite(snapshot.ts) ? snapshot.ts : 0;
    if (this.snapshot !== null && snapshotTs < this.snapshot.ts) {
      return;
    }

    const next = new Map<string, FlagEnvironmentState>();
    const nextFromLiveEvent = new Map<string, boolean>();
    for (const flag of snapshot.flags) {
      const existing = this.memory.get(flag.flagKey);
      // A live prerequisites_updated event may have already advanced this
      // flag's prerequisitesUpdatedAt to OR PAST this snapshot's own ts if
      // the snapshot fetch was still in flight when the live event arrived
      // and applied -- in that case the snapshot reflects an OLDER (or, on
      // an exact-tie second, no LATER) point in time for THIS flag
      // specifically, even though the snapshot as a whole passed the
      // monotonicity check above (which only compares against the last
      // *snapshot's* ts, not any per-flag live update). Uses >=, not >:
      // flag-api's snapshot endpoint and its prerequisites-event publisher
      // both derive ts from time.Now().Unix() (1-second resolution), so a
      // live event and a racing snapshot fetch landing in the same
      // wall-clock second get an IDENTICAL ts even though the snapshot's DB
      // read can predate the event's own commit -- applyPrerequisitesEvent's
      // own staleness guard (strict "<") already treats a tie as "fresh
      // enough to apply", so this preservation check must treat the SAME
      // tie as "fresh enough to keep", or the two guards disagree on who
      // wins a tie and this one silently loses (found by adversarial review
      // of the Ruby SDK's identical fix, PR #238).
      //
      // Also requires prerequisitesFromLiveEvent to be true: without it, a
      // SECOND snapshot sharing the exact same ts as a FIRST snapshot (no
      // live event involved at all) would incorrectly take this same
      // "preserve" branch and freeze prerequisites on the first snapshot's
      // value forever, discarding the second snapshot's genuinely different
      // data (found by adversarial review of this fix's own first draft).
      const keepLivePrerequisites =
        existing?.prerequisitesUpdatedAt !== undefined &&
        this.prerequisitesFromLiveEvent.get(flag.flagKey) === true &&
        existing.prerequisitesUpdatedAt >= snapshotTs;
      next.set(flag.flagKey, {
        ...flag,
        targetingRules: Array.isArray(flag.targetingRules)
          ? [...flag.targetingRules]
          : [],
        prerequisites: keepLivePrerequisites
          ? existing.prerequisites
          : (flag.prerequisites ?? []),
        prerequisitesUpdatedAt: keepLivePrerequisites
          ? existing.prerequisitesUpdatedAt
          : snapshotTs,
      });
      nextFromLiveEvent.set(flag.flagKey, keepLivePrerequisites);
    }
    this.memory = next;
    this.prerequisitesFromLiveEvent = nextFromLiveEvent;
    this.snapshot = { ...snapshot, ts: snapshotTs, flags: [...snapshot.flags] };
  }

  applyEvent(event: FlagEvent): void {
    const existing = this.memory.get(event.flagKey);
    if (!existing) return;
    const updated: FlagEnvironmentState = {
      ...existing,
      enabled: event.enabled,
      rolloutPct: event.rolloutPct,
      updatedAt: event.ts,
      prerequisites: existing.prerequisites ?? [],
      targetingRules: existing.targetingRules ?? [],
      prerequisitesUpdatedAt: existing.prerequisitesUpdatedAt,
    };
    const next = new Map(this.memory);
    next.set(event.flagKey, updated);
    this.memory = next;
  }

  /**
   * Applies a live "prerequisites_updated" SSE event -- full replacement of
   * a flag's prerequisite list, not a delta (matching PrerequisitesEvent's
   * own documented design on the flag-api side). No-ops for a flag with no
   * existing cache entry (nothing to merge a partial update into -- the
   * next full snapshot refetch is what correctly picks up a flag this
   * client has never seen before).
   *
   * Rejects an incoming event whose ts is OLDER than the currently-cached
   * prerequisitesUpdatedAt: services/flag-api/internal/api/v1/
   * prerequisites.go's own PrerequisitesEvent doc comment discloses that
   * publishPrerequisitesUpdated's SELECT-then-XAdd has no per-flag lock, so
   * concurrent AddPrerequisite/DeletePrerequisite calls on the SAME flag
   * can have their events arrive here out of real commit order under
   * scheduling delays -- comparing ts against what's already cached
   * (rather than unconditionally overwriting on arrival order) closes that
   * gap at the point where staleness actually matters.
   */
  applyPrerequisitesEvent(
    flagKey: string,
    prerequisites: FlagPrerequisite[],
    ts: number,
  ): void {
    const existing = this.memory.get(flagKey);
    if (!existing) return;
    // A malformed/non-numeric wire ts (streaming.ts's Number(raw["ts"] ?? 0)
    // coerces anything non-numeric to NaN, not an error) can never be
    // judged fresher than what's cached -- storing it would corrupt
    // prerequisitesUpdatedAt with NaN, permanently defeating every FUTURE
    // staleness comparison for this flag (NaN < x and x < NaN are both
    // always false), until the next full snapshot reload. Reject outright.
    if (!Number.isFinite(ts)) {
      return;
    }
    if (ts < (existing.prerequisitesUpdatedAt ?? 0)) {
      return;
    }
    const updated: FlagEnvironmentState = {
      ...existing,
      prerequisites: [...prerequisites],
      prerequisitesUpdatedAt: ts,
    };
    const next = new Map(this.memory);
    next.set(flagKey, updated);
    this.memory = next;
    // Marks this flag's prerequisites as LIVE-sourced -- see
    // prerequisitesFromLiveEvent's own field comment for why loadSnapshot
    // needs this distinction, not just a ts comparison, to decide whether a
    // tied-or-older incoming snapshot should be allowed to overwrite it.
    this.prerequisitesFromLiveEvent = new Map(
      this.prerequisitesFromLiveEvent,
    ).set(flagKey, true);
  }

  setTargetingRules(flagKey: string, rules: TargetingRule[]): void {
    const existing = this.memory.get(flagKey);
    if (!existing) return;
    const updated: FlagEnvironmentState = {
      ...existing,
      targetingRules: [...rules],
    };
    const next = new Map(this.memory);
    next.set(flagKey, updated);
    this.memory = next;
  }

  get(flagKey: string): FlagEnvironmentState | undefined {
    return this.memory.get(flagKey);
  }

  getHash(): string | null {
    return this.snapshot?.hash ?? null;
  }

  keys(): string[] {
    return Array.from(this.memory.keys());
  }

  size(): number {
    return this.memory.size;
  }
}
