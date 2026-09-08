package io.tombstone.client;

import io.tombstone.types.FlagEnvironmentState;
import io.tombstone.types.FlagPrerequisite;
import io.tombstone.types.TargetingRule;
import java.util.*;
import java.util.concurrent.atomic.AtomicReference;

// Disclosed, pre-existing, NOT introduced or fixed by the SDK-4
// cache-wiping fix (confirmed via git diff -- this get/build/set skeleton
// is byte-identical before and after): applyEvent and loadSnapshot both
// do a non-atomic read-modify-write on `state` (get() -> build a new
// CacheState from that snapshot -> set()) instead of a CAS loop. Two
// concurrent mutations -- e.g. the SSE listener's applyEvent racing a
// lag-recovery loadSnapshot -- can both read the same `current`, and
// whichever set() lands second silently discards the OTHER's entire
// state, not just the key it touched. Concretely: if a lag-triggered
// loadSnapshot recovers several dropped flag updates but a concurrent
// applyEvent (reading the stale pre-recovery snapshot) sets() after it,
// the whole recovered snapshot is clobbered -- defeating the very
// lag-recovery mechanism this exists for. Found by adversarial review of
// the SDK-4 cache-wiping PR; left unfixed here since it's a separate,
// latent concurrency bug, not something that PR's own change touches or
// makes newly reachable -- a real fix needs a CAS loop
// (AtomicReference.updateAndGet or compareAndSet) and is its own,
// independent piece of work.
public class FlagCache {
    // Combines the flag map, BOTH live-event-provenance maps (prerequisites
    // and targetingRules), and the last-applied-snapshot ts into ONE object
    // swapped via a single AtomicReference -- see the field's own comment
    // for why this combination is load-bearing, not just a style
    // preference. targetingRulesFromLiveEvent is included here from the
    // START (not added as a separate field later): the concurrency-tear
    // lesson that forced prerequisitesFromLiveEvent into this same
    // CacheState (found by a SECOND round of adversarial review, see that
    // field's own history) applies identically to any second live-event
    // provenance map, so there is no reason to repeat the discovery.
    private record CacheState(
        Map<String, FlagEnvironmentState> flags,
        Map<String, Boolean> prerequisitesFromLiveEvent,
        Map<String, Boolean> targetingRulesFromLiveEvent,
        long lastSnapshotTs
    ) {}

    // Sentinel meaning "no snapshot has been loaded yet" -- a real flag-api
    // snapshot ts (time.Now().Unix()) will never be this low, so any real
    // incoming ts trivially passes the very first loadSnapshot call.
    private static final long NO_SNAPSHOT_YET = Long.MIN_VALUE;

    // flags and prerequisitesFromLiveEvent used to be two SEPARATE fields
    // (a Map<String, FlagEnvironmentState> cache and a
    // Map<String, Boolean> prerequisitesFromLiveEvent), each updated by
    // its own independent statement. Found by a second round of
    // adversarial review of the prerequisites-live-event tie-breaking fix:
    // a real JVM thread (e.g. the SSE listener calling applyPrerequisitesEvent
    // concurrently with the lag-recovery scheduler calling loadSnapshot --
    // exactly the scenario this class's own top-of-file comment already
    // names) can observe the two fields in an INCONSISTENT combination --
    // e.g. `flags` already reflects a live-updated prerequisite, but
    // `prerequisitesFromLiveEvent` hasn't yet been marked true for that
    // flag -- which silently defeats the tie-break protection itself, not
    // just the pre-existing "lose an update" risk the class already
    // discloses above. Combining both into one CacheState, swapped via a
    // SINGLE AtomicReference, guarantees any read always sees a mutually
    // consistent pair -- the same lock-free atomic-swap pattern this class
    // already used for `cache` alone, just widened to cover the second
    // field too. This does NOT fix the pre-existing, disclosed "lost
    // update" race above (two concurrent writers both reading the same old
    // CacheState, whichever set() lands second wins outright) -- that
    // remains exactly as accepted/deferred as before.
    private final AtomicReference<CacheState> state =
        new AtomicReference<>(new CacheState(
            Collections.emptyMap(), Collections.emptyMap(), Collections.emptyMap(), NO_SNAPSHOT_YET));

    /** Convenience overload for tests that don't care about prerequisites-streaming timing semantics. */
    public void loadSnapshot(List<FlagEnvironmentState> flags) {
        loadSnapshot(flags, 0L);
    }

    // flag-api's snapshot ts is simply time.Now().Unix() at response
    // generation (services/flag-api/internal/api/v1/environments.go), not a
    // per-flag "last changed" value -- so a SLOWER, already-in-flight
    // fetchSnapshot() call (e.g. a lag-triggered refetch racing a
    // reconnect-triggered one) can resolve AFTER a newer one already
    // loaded. Rejecting an incoming snapshot whose own ts is older than the
    // last one actually applied prevents that older response from silently
    // clobbering fresher state for every flag, not just prerequisites.
    public void loadSnapshot(List<FlagEnvironmentState> flags, long snapshotTs) {
        CacheState currentState = state.get();
        if (currentState.lastSnapshotTs() != NO_SNAPSHOT_YET && snapshotTs < currentState.lastSnapshotTs()) {
            return;
        }
        Map<String, FlagEnvironmentState> current = currentState.flags();
        Map<String, Boolean> currentFromLiveEvent = currentState.prerequisitesFromLiveEvent();
        Map<String, Boolean> currentTargetingRulesFromLiveEvent = currentState.targetingRulesFromLiveEvent();
        Map<String, FlagEnvironmentState> m = new HashMap<>();
        Map<String, Boolean> nextFromLiveEvent = new HashMap<>();
        Map<String, Boolean> nextTargetingRulesFromLiveEvent = new HashMap<>();
        for (FlagEnvironmentState f : flags) {
            FlagEnvironmentState existing = current.get(f.flagKey());
            // A live prerequisites_updated event may have already advanced
            // this flag's prerequisitesUpdatedAt to OR PAST this snapshot's
            // own ts if the snapshot fetch was still in flight when the live
            // event arrived and applied -- in that case the snapshot
            // reflects an OLDER (or, on an exact-tie second, no LATER)
            // point in time for THIS flag specifically, even though the
            // snapshot as a whole passed the monotonicity check above
            // (which only compares against the last *snapshot's* ts, not
            // any per-flag live update). Uses >=, not >: flag-api's
            // snapshot endpoint and its prerequisites-event publisher both
            // derive ts from time.Now().Unix() (1-second resolution), so a
            // live event and a racing snapshot fetch landing in the same
            // wall-clock second get an IDENTICAL ts even though the
            // snapshot's DB read can predate the event's own commit --
            // applyPrerequisitesEvent's own staleness guard (strict "<")
            // already treats a tie as "fresh enough to apply", so this
            // preservation check must treat the SAME tie as "fresh enough
            // to keep", or the two guards disagree on who wins a tie and
            // this one silently loses (found by adversarial review of the
            // Ruby SDK's identical fix, PR #238).
            //
            // Also requires prerequisitesFromLiveEvent to be true: without
            // it, a SECOND snapshot sharing the exact same ts as a FIRST
            // snapshot (no live event involved at all) would incorrectly
            // take this same "preserve" branch and freeze prerequisites on
            // the first snapshot's value forever (found by adversarial
            // review of this fix's own first draft).
            boolean keepLivePrerequisites =
                existing != null &&
                Boolean.TRUE.equals(currentFromLiveEvent.get(f.flagKey())) &&
                existing.prerequisitesUpdatedAt() >= snapshotTs;
            // Identical reasoning to keepLivePrerequisites above, applied
            // to targetingRules against services/flag-api/internal/api/v1/
            // targeting_rules.go's TargetingRulesEvent -- see
            // applyTargetingRulesEvent's own doc comment.
            boolean keepLiveTargetingRules =
                existing != null &&
                Boolean.TRUE.equals(currentTargetingRulesFromLiveEvent.get(f.flagKey())) &&
                existing.targetingRulesUpdatedAt() >= snapshotTs;
            m.put(f.flagKey(), new FlagEnvironmentState(
                f.flagId(), f.flagKey(), f.environment(), f.enabled(), f.rolloutPct(),
                f.safeDefault(), f.updatedAt(),
                keepLivePrerequisites ? existing.prerequisites() : f.prerequisites(),
                keepLiveTargetingRules ? existing.targetingRules() : f.targetingRules(),
                f.targetList(), f.hashVersion(),
                keepLivePrerequisites ? existing.prerequisitesUpdatedAt() : snapshotTs,
                keepLiveTargetingRules ? existing.targetingRulesUpdatedAt() : snapshotTs
            ));
            // ONE-SHOT consumption, always false here (never
            // keepLivePrerequisites/keepLiveTargetingRules): this
            // loadSnapshot call has now fully resolved the race between
            // the live event and ITS OWN specific in-flight snapshot.
            // Re-propagating true would make the protection "sticky",
            // vetoing a later, independent snapshot that merely happens to
            // tie the same coarse-resolution second too (found by a second
            // round of adversarial review of this same fix). Residual,
            // accepted limitation: two snapshot fetches that were BOTH
            // already in flight when the SAME live event fired will only
            // have the first-arriving one correctly blocked; solving that
            // fully would require a signal finer than flag-api's
            // 1-second-resolution wall-clock ts.
            nextFromLiveEvent.put(f.flagKey(), false);
            nextTargetingRulesFromLiveEvent.put(f.flagKey(), false);
        }
        state.set(new CacheState(
            Collections.unmodifiableMap(m),
            Collections.unmodifiableMap(nextFromLiveEvent),
            Collections.unmodifiableMap(nextTargetingRulesFromLiveEvent),
            snapshotTs
        ));
    }

    // Immutable update — never mutates existing map. Threads existing's
    // prerequisites/targetingRules/targetList/hashVersion through
    // unchanged: flag-api's real FlagEvent (services/flag-api/internal/
    // api/v1/flags.go) never carries any of them, so building via
    // FlagEnvironmentState.simple(...) here previously wiped all four to
    // empty/default on EVERY event for a flag -- a kill-switch, a
    // rollback step, literally any enabled/rollout_pct change -- silently
    // disabling prerequisite-gating and rule-matching client-side until
    // the next full snapshot refetch restored them (the same bug class
    // found and fixed in the Python SDK's client.py _apply_event).
    public void applyEvent(String flagKey, boolean enabled, int rolloutPct, long ts) {
        CacheState currentState = state.get();
        Map<String, FlagEnvironmentState> current = currentState.flags();
        FlagEnvironmentState existing = current.get(flagKey);
        if (existing == null) return;
        FlagEnvironmentState updated = new FlagEnvironmentState(
            existing.flagId(), existing.flagKey(), existing.environment(),
            enabled, rolloutPct, existing.safeDefault(), ts,
            existing.prerequisites(), existing.targetingRules(), existing.targetList(),
            existing.hashVersion(), existing.prerequisitesUpdatedAt(), existing.targetingRulesUpdatedAt()
        );
        Map<String, FlagEnvironmentState> next = new HashMap<>(current);
        next.put(flagKey, updated);
        state.set(new CacheState(
            Collections.unmodifiableMap(next),
            currentState.prerequisitesFromLiveEvent(),
            currentState.targetingRulesFromLiveEvent(),
            currentState.lastSnapshotTs()
        ));
    }

    /**
     * Applies a live "prerequisites_updated" SSE event -- full replacement of
     * a flag's prerequisite list, not a delta. No-ops for a flag with no
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
    public void applyPrerequisitesEvent(String flagKey, List<FlagPrerequisite> prerequisites, long ts) {
        CacheState currentState = state.get();
        Map<String, FlagEnvironmentState> current = currentState.flags();
        FlagEnvironmentState existing = current.get(flagKey);
        if (existing == null) return;
        if (ts < existing.prerequisitesUpdatedAt()) return;
        FlagEnvironmentState updated = new FlagEnvironmentState(
            existing.flagId(), existing.flagKey(), existing.environment(),
            existing.enabled(), existing.rolloutPct(), existing.safeDefault(), existing.updatedAt(),
            List.copyOf(prerequisites), existing.targetingRules(), existing.targetList(),
            existing.hashVersion(), ts, existing.targetingRulesUpdatedAt()
        );
        Map<String, FlagEnvironmentState> next = new HashMap<>(current);
        next.put(flagKey, updated);
        // Marks this flag's prerequisites as LIVE-sourced -- see
        // prerequisitesFromLiveEvent's own field comment (on CacheState's
        // declaration above) for why loadSnapshot needs this distinction,
        // not just a ts comparison, to decide whether a tied-or-older
        // incoming snapshot should be allowed to overwrite it. Committed to
        // `state` in the SAME set() call as the cache update, so a
        // concurrent reader can never observe one without the other.
        Map<String, Boolean> nextFromLiveEvent = new HashMap<>(currentState.prerequisitesFromLiveEvent());
        nextFromLiveEvent.put(flagKey, true);
        state.set(new CacheState(
            Collections.unmodifiableMap(next),
            Collections.unmodifiableMap(nextFromLiveEvent),
            currentState.targetingRulesFromLiveEvent(),
            currentState.lastSnapshotTs()
        ));
    }

    /**
     * Applies a live "targeting_rules_updated" SSE event -- full replacement
     * of a flag's targeting-rule list FOR THIS ENVIRONMENT, not a delta
     * (matching TargetingRulesEvent's own documented design on the flag-api
     * side -- services/flag-api/internal/api/v1/targeting_rules.go).
     * No-ops for a flag with no existing cache entry, mirrors
     * applyPrerequisitesEvent's own staleness guard exactly (strict "&lt;",
     * comparing against targetingRulesUpdatedAt).
     */
    public void applyTargetingRulesEvent(String flagKey, List<TargetingRule> targetingRules, long ts) {
        CacheState currentState = state.get();
        Map<String, FlagEnvironmentState> current = currentState.flags();
        FlagEnvironmentState existing = current.get(flagKey);
        if (existing == null) return;
        if (ts < existing.targetingRulesUpdatedAt()) return;
        FlagEnvironmentState updated = new FlagEnvironmentState(
            existing.flagId(), existing.flagKey(), existing.environment(),
            existing.enabled(), existing.rolloutPct(), existing.safeDefault(), existing.updatedAt(),
            existing.prerequisites(), List.copyOf(targetingRules), existing.targetList(),
            existing.hashVersion(), existing.prerequisitesUpdatedAt(), ts
        );
        Map<String, FlagEnvironmentState> next = new HashMap<>(current);
        next.put(flagKey, updated);
        // Marks this flag's targetingRules as LIVE-sourced -- see
        // targetingRulesFromLiveEvent's own field comment (on CacheState's
        // declaration above).
        Map<String, Boolean> nextTargetingRulesFromLiveEvent =
            new HashMap<>(currentState.targetingRulesFromLiveEvent());
        nextTargetingRulesFromLiveEvent.put(flagKey, true);
        state.set(new CacheState(
            Collections.unmodifiableMap(next),
            currentState.prerequisitesFromLiveEvent(),
            Collections.unmodifiableMap(nextTargetingRulesFromLiveEvent),
            currentState.lastSnapshotTs()
        ));
    }

    public Optional<FlagEnvironmentState> get(String flagKey) {
        return Optional.ofNullable(state.get().flags().get(flagKey));
    }

    public Set<String> flagKeys() {
        return state.get().flags().keySet();
    }

    public int size() {
        return state.get().flags().size();
    }
}
