package io.tombstone;

import io.tombstone.evaluation.EvaluationEngine;
import io.tombstone.types.*;
import org.junit.jupiter.api.Test;
import static org.junit.jupiter.api.Assertions.*;

public class EvaluationEngineTest {
    private final EvaluationEngine engine = new EvaluationEngine();
    private final EvaluationContext ctx = EvaluationContext.of("user-abc-123");

    private FlagEnvironmentState flag(boolean enabled, int pct) {
        return FlagEnvironmentState.simple("id-1", "test-flag", "test", enabled, pct, "false", 0L);
    }

    @Test void testDisabledReturnsOff() {
        var r = engine.evaluate(flag(false, 100), ctx, false, "test-flag");
        assertEquals(EvaluationReason.OFF, r.reason());
        assertFalse((Boolean) r.value());
    }

    @Test void test100PctReturnsTrue() {
        var r = engine.evaluate(flag(true, 100), ctx, false, "test-flag");
        assertEquals(EvaluationReason.FALLTHROUGH, r.reason());
        assertTrue((Boolean) r.value());
    }

    @Test void test0PctReturnsFalse() {
        var r = engine.evaluate(flag(true, 0), ctx, false, "test-flag");
        assertFalse((Boolean) r.value());
    }

    @Test void testNullFlagReturnsError() {
        var r = engine.evaluate(null, ctx, false, "missing");
        assertEquals(EvaluationReason.ERROR, r.reason());
    }

    @Test void testStickinessConsistency() {
        var f = flag(true, 50);
        var results = new java.util.HashSet<Object>();
        for (int i = 0; i < 20; i++) {
            results.add(engine.evaluate(f, ctx, false, "test-flag").value());
        }
        assertEquals(1, results.size(), "Same user must always get same flag value");
    }

    @Test void testMurmurHash3ParityWithTypeScript() {
        // Vectors match TypeScript: murmurhash.v3(flagKey + userId) >>> 0 % 100
        record Vec(String flagKey, String userId, int pct, boolean expected) {}
        var vectors = new Vec[]{
            new Vec("checkout-v2", "user-abc-123", 100, true),
            new Vec("checkout-v2", "user-abc-123", 0, false),
            new Vec("checkout-v2", "user-xyz-789", 50, false),
        };
        for (var v : vectors) {
            var f = flag(true, v.pct());
            var r = engine.evaluate(f,
                EvaluationContext.of(v.userId()), false, v.flagKey());
            boolean actual = Boolean.TRUE.equals(r.value());
            assertEquals(v.expected(), actual,
                String.format("Hash parity FAILED: (%s, %s, %d%%) expected=%b got=%b",
                    v.flagKey(), v.userId(), v.pct(), v.expected(), actual));
        }
    }

    @Test void testPrerequisiteHardGateBlocksEvaluation() {
        var baseFlag = FlagEnvironmentState.simple("id-2", "base-flag", "test", false, 0, "false", 0L);
        var prereq = new io.tombstone.types.FlagPrerequisite("base-flag", "true", true);
        var parentFlag = new FlagEnvironmentState(
            "id-1", "parent-flag", "test", true, 100, "false", 0L,
            java.util.List.of(prereq), java.util.List.of(), java.util.List.of(), 1
        );
        java.util.function.Function<String, FlagEnvironmentState> lookup =
            key -> "base-flag".equals(key) ? baseFlag : null;

        var result = engine.evaluate(parentFlag, ctx, false, "parent-flag", lookup, new java.util.HashMap<>(), new java.util.HashSet<>());

        assertEquals(EvaluationReason.PREREQUISITE_FAILED, result.reason());
        assertFalse((Boolean) result.value());
    }

    @Test void testTargetListMatchReturnsTrue() {
        var flag = new FlagEnvironmentState(
            "id-1", "test-flag", "test", true, 0, "false", 0L,
            java.util.List.of(), java.util.List.of(), java.util.List.of("user-abc-123"), 1
        );

        var result = engine.evaluate(flag, ctx, false, "test-flag");

        assertEquals(EvaluationReason.TARGET_MATCH, result.reason());
        assertTrue((Boolean) result.value());
    }

    @Test void testRuleMatchReturnsRuleVariation() {
        var condition = new io.tombstone.types.PropertyCondition("plan", "eq", java.util.List.of("pro"), false);
        var rule = new io.tombstone.types.TargetingRule("r1", java.util.List.of(condition), 100.0, "matched-variation", 0);
        var flag = new FlagEnvironmentState(
            "id-1", "test-flag", "test", true, 0, "false", 0L,
            java.util.List.of(), java.util.List.of(rule), java.util.List.of(), 1
        );
        var proContext = new EvaluationContext("u1", "", java.util.Map.of("plan", "pro"));

        var result = engine.evaluate(flag, proContext, "default-value", "test-flag");

        assertEquals(EvaluationReason.RULE_MATCH, result.reason());
        assertEquals("matched-variation", result.value());
    }

    @Test void testHashVersion2UsesFnv1a() {
        // Vector from test-contract/vectors.json: checkout-v2/user-abc-123, v2, expected_bucket=0.343.
        // rollout_pct=30 -> bucket 0.343 >= 0.30 -> NOT in cohort -> default returned.
        var flag = new FlagEnvironmentState(
            "id-1", "checkout-v2", "test", true, 30, "false", 0L,
            java.util.List.of(), java.util.List.of(), java.util.List.of(), 2
        );
        var context = new EvaluationContext("user-abc-123", "", java.util.Map.of());

        var result = engine.evaluate(flag, context, false, "checkout-v2");

        assertFalse((Boolean) result.value());
        assertEquals(EvaluationReason.FALLTHROUGH, result.reason());
    }
}
