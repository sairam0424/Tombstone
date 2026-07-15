package io.tombstone.types;
public record FlagEnvironmentState(
    String flagId,
    String flagKey,
    String environment,
    boolean enabled,
    int rolloutPct,
    String safeDefault,
    long updatedAt
) {}
