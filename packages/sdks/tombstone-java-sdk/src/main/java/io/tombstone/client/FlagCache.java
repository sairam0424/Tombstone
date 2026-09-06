package io.tombstone.client;

import io.tombstone.types.FlagEnvironmentState;
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
// or compareAndSet) and is its own, independent piece of work.
public class FlagCache {
    private final AtomicReference<Map<String, FlagEnvironmentState>> cache =
        new AtomicReference<>(Collections.emptyMap());

    public void loadSnapshot(List<FlagEnvironmentState> flags) {
        Map<String, FlagEnvironmentState> m = new HashMap<>();
        for (FlagEnvironmentState f : flags) {
            m.put(f.flagKey(), f);
        }
        cache.set(Collections.unmodifiableMap(m));
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
            existing.hashVersion()
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
