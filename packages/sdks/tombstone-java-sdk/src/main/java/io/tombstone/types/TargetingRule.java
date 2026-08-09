package io.tombstone.types;

import java.util.List;

public record TargetingRule(
    String id,
    List<PropertyCondition> conditions,
    double rolloutPct,
    String variation,
    int priority
) {}
