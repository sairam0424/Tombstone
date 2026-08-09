package io.tombstone.types;

import java.util.List;

public record FlagEnvironmentState(
    String flagId,
    String flagKey,
    String environment,
    boolean enabled,
    int rolloutPct,
    String safeDefault,
    long updatedAt,
    List<FlagPrerequisite> prerequisites,
    List<TargetingRule> targetingRules,
    List<String> targetList,
    int hashVersion
) {
    /** Convenience factory for flags with no prerequisites/rules/target-list — hashVersion defaults to 1 (MurmurHash3). */
    public static FlagEnvironmentState simple(
        String flagId, String flagKey, String environment,
        boolean enabled, int rolloutPct, String safeDefault, long updatedAt
    ) {
        return new FlagEnvironmentState(
            flagId, flagKey, environment, enabled, rolloutPct, safeDefault, updatedAt,
            List.of(), List.of(), List.of(), 1
        );
    }
}
