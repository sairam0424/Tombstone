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

  loadSnapshot(snapshot: FlagSnapshot): void {
    const next = new Map<string, FlagEnvironmentState>();
    for (const flag of snapshot.flags) {
      next.set(flag.flagKey, {
        prerequisites: [],
        ...flag,
        targetingRules: Array.isArray(flag.targetingRules)
          ? [...flag.targetingRules]
          : [],
        // flag-api's real snapshot response has no per-flag "prerequisites
        // last changed" timestamp -- the snapshot's own top-level ts is the
        // correct "known-good as of" value: any live prerequisites_updated
        // event older than this fetch is necessarily already superseded.
        prerequisitesUpdatedAt: snapshot.ts,
      });
    }
    this.memory = next;
    this.snapshot = { ...snapshot, flags: [...snapshot.flags] };
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
