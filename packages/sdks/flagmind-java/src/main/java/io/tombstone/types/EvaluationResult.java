package io.tombstone.types;
public record EvaluationResult<T>(
    T value,
    EvaluationReason reason,
    boolean fromCache,
    String flagKey
) {}
