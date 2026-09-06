package io.tombstone.client;

import io.tombstone.types.FlagEnvironmentState;
import java.util.*;
import java.util.concurrent.atomic.AtomicReference;

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
