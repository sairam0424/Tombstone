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

    // Immutable update — never mutates existing map
    public void applyEvent(String flagKey, boolean enabled, int rolloutPct, long ts) {
        Map<String, FlagEnvironmentState> current = cache.get();
        FlagEnvironmentState existing = current.get(flagKey);
        if (existing == null) return;
        FlagEnvironmentState updated = new FlagEnvironmentState(
            existing.flagId(), existing.flagKey(), existing.environment(),
            enabled, rolloutPct, existing.safeDefault(), ts
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
