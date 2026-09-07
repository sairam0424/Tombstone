package io.tombstone.client;

import io.tombstone.types.FlagEnvironmentState;
import io.tombstone.types.FlagPrerequisite;
import java.util.*;
import java.util.concurrent.atomic.AtomicReference;

// Disclosed, pre-existing, NOT introduced or fixed by the SDK-4
// cache-wiping fix (confirmed via git diff -- this get/build/set skeleton
// is byte-identical before and after): applyEvent and loadSnapshot both
// do a non-atomic read-modify-write on `cache` (get() -> build a new map
// from that snapshot -> set()) instead of a CAS loop. Two concurrent
// mutations -- e.g. the SSE listener's applyEvent racing a lag-recovery
// loadSnapshot -- can both read the same `current`, and whichever set()
// lands second silently discards the OTHER's entire map, not just the
// key it touched. Concretely: if a lag-triggered loadSnapshot recovers
// several dropped flag updates but a concurrent applyEvent (reading the
// stale pre-recovery snapshot) sets() after it, the whole recovered
// snapshot is clobbered -- defeating the very lag-recovery mechanism
// this exists for. Found by adversarial review of the SDK-4 cache-
// wiping PR; left unfixed here since it's a separate, latent concurrency
// bug, not something that PR's own change touches or makes newly
// reachable -- a real fix needs a CAS loop (AtomicReference.updateAndGet
// or compareAndSet) and is its own, independent piece of work. lastSnapshotTs
// (added for prerequisites-streaming) and applyPrerequisitesEvent inherit
// the exact same non-atomic-read-modify-write shape and are left consistent
// with the rest of this class rather than singled out for a CAS fix.
public class FlagCache {
    private final AtomicReference<Map<String, FlagEnvironmentState>> cache =
        new AtomicReference<>(Collections.emptyMap());
    // Sentinel meaning "no snapshot has been loaded yet" -- a real flag-api
    // snapshot ts (time.Now().Unix()) will never be this low, so any real
    // incoming ts trivially passes the very first loadSnapshot call.
    private volatile long lastSnapshotTs = Long.MIN_VALUE;

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
        if (lastSnapshotTs != Long.MIN_VALUE && snapshotTs < lastSnapshotTs) {
            return;
        }
        Map<String, FlagEnvironmentState> current = cache.get();
        Map<String, FlagEnvironmentState> m = new HashMap<>();
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
            boolean keepLivePrerequisites =
                existing != null && existing.prerequisitesUpdatedAt() >= snapshotTs;
            m.put(f.flagKey(), new FlagEnvironmentState(
                f.flagId(), f.flagKey(), f.environment(), f.enabled(), f.rolloutPct(),
                f.safeDefault(), f.updatedAt(),
                keepLivePrerequisites ? existing.prerequisites() : f.prerequisites(),
                f.targetingRules(), f.targetList(), f.hashVersion(),
                keepLivePrerequisites ? existing.prerequisitesUpdatedAt() : snapshotTs
            ));
        }
        cache.set(Collections.unmodifiableMap(m));
        lastSnapshotTs = snapshotTs;
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
        Map<String, FlagEnvironmentState> current = cache.get();
        FlagEnvironmentState existing = current.get(flagKey);
        if (existing == null) return;
        FlagEnvironmentState updated = new FlagEnvironmentState(
            existing.flagId(), existing.flagKey(), existing.environment(),
            enabled, rolloutPct, existing.safeDefault(), ts,
            existing.prerequisites(), existing.targetingRules(), existing.targetList(),
            existing.hashVersion(), existing.prerequisitesUpdatedAt()
        );
        Map<String, FlagEnvironmentState> next = new HashMap<>(current);
        next.put(flagKey, updated);
        cache.set(Collections.unmodifiableMap(next));
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
        Map<String, FlagEnvironmentState> current = cache.get();
        FlagEnvironmentState existing = current.get(flagKey);
        if (existing == null) return;
        if (ts < existing.prerequisitesUpdatedAt()) return;
        FlagEnvironmentState updated = new FlagEnvironmentState(
            existing.flagId(), existing.flagKey(), existing.environment(),
            existing.enabled(), existing.rolloutPct(), existing.safeDefault(), existing.updatedAt(),
            List.copyOf(prerequisites), existing.targetingRules(), existing.targetList(),
            existing.hashVersion(), ts
        );
        Map<String, FlagEnvironmentState> next = new HashMap<>(current);
        next.put(flagKey, updated);
        cache.set(Collections.unmodifiableMap(next));
    }

    public Optional<FlagEnvironmentState> get(String flagKey) {
        return Optional.ofNullable(cache.get().get(flagKey));
    }

    public Set<String> flagKeys() {
        return cache.get().keySet();
    }

    public int size() {
        return cache.get().size();
    }
}
