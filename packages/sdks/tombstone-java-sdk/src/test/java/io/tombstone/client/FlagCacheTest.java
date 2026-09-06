package io.tombstone.client;

import io.tombstone.types.FlagEnvironmentState;
import io.tombstone.types.FlagPrerequisite;
import io.tombstone.types.PropertyCondition;
import io.tombstone.types.TargetingRule;
import org.junit.jupiter.api.Test;

import java.util.List;

import static org.junit.jupiter.api.Assertions.*;

/** Regression tests for a real, live bug (SDK-4 investigation): flag-api's
 *  real FlagEvent (services/flag-api/internal/api/v1/flags.go) never
 *  carries prerequisites/targetingRules/targetList/hashVersion, so
 *  FlagCache.applyEvent previously rebuilt the cached entry via
 *  FlagEnvironmentState.simple(...) -- its OWN doc comment says "no
 *  prerequisites/rules/target-list" -- wiping all four to empty/default on
 *  EVERY event for a flag (a kill-switch, a rollback step, literally any
 *  enabled/rollout_pct change), silently disabling prerequisite-gating and
 *  rule-matching client-side until the next full snapshot refetch. The
 *  same bug class already found and fixed in the Python SDK's
 *  client.py _apply_event. */
public class FlagCacheTest {

    @Test
    void applyEventPreservesPrerequisitesTargetingRulesTargetListAndHashVersion() {
        var cache = new FlagCache();
        var rule = new TargetingRule(
            "r1",
            List.of(new PropertyCondition("country", "eq", List.of("US"), false)),
            100.0, "variant-a", 0
        );
        var prereq = new FlagPrerequisite("parent-flag", "true", true);
        var seeded = new FlagEnvironmentState(
            "flag-id", "my-flag", "prod", true, 50, "false", 0L,
            List.of(prereq), List.of(rule), List.of("vip-user"), 2
        );
        cache.loadSnapshot(List.of(seeded));

        // A real SSE event as flag-api actually publishes it -- just an
        // enabled/rollout_pct/ts change, nothing else.
        cache.applyEvent("my-flag", true, 75, 12345L);

        var updated = cache.get("my-flag").orElseThrow();
        assertEquals(75, updated.rolloutPct(), "the event's own field must still apply");
        assertEquals(12345L, updated.updatedAt(), "the event's own field must still apply");
        assertEquals(List.of(prereq), updated.prerequisites(), "prerequisites must survive an unrelated event");
        assertEquals(List.of(rule), updated.targetingRules(), "targetingRules must survive an unrelated event");
        assertEquals(List.of("vip-user"), updated.targetList(), "targetList must survive an unrelated event");
        assertEquals(2, updated.hashVersion(), "hashVersion must survive an unrelated event");
    }

    @Test
    void applyEventForAnUnknownFlagIsANoOp() {
        var cache = new FlagCache();
        cache.applyEvent("never-seen", true, 100, 1L);
        assertTrue(cache.get("never-seen").isEmpty());
    }
}
