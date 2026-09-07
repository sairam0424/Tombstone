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
    int hashVersion,
    // Unix seconds these prerequisites were last known-good as of -- either
    // the snapshot fetch that loaded them, or a live prerequisites_updated
    // event applied since. Lets an incoming live event be compared against
    // what's already cached and rejected if it's older -- see FlagCache.
    // applyPrerequisitesEvent's own doc comment.
    long prerequisitesUpdatedAt
) {
    /** Convenience factory for flags with no prerequisites/rules/target-list — hashVersion defaults to 1 (MurmurHash3). */
    public static FlagEnvironmentState simple(
        String flagId, String flagKey, String environment,
        boolean enabled, int rolloutPct, String safeDefault, long updatedAt
    ) {
        return new FlagEnvironmentState(
            flagId, flagKey, environment, enabled, rolloutPct, safeDefault, updatedAt,
            List.of(), List.of(), List.of(), 1, 0L
        );
    }
}
