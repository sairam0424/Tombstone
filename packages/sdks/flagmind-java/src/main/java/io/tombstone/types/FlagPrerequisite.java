package io.tombstone.types;

public record FlagPrerequisite(
    String flagKey,
    String requiredVariation,
    boolean gate
) {}
