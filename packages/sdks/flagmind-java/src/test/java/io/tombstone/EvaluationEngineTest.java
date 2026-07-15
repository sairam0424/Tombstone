package io.tombstone;

import io.tombstone.evaluation.EvaluationEngine;
import io.tombstone.types.*;
import org.junit.jupiter.api.Test;
import static org.junit.jupiter.api.Assertions.*;
import java.util.Collections;

public class EvaluationEngineTest {
    private final EvaluationEngine engine = new EvaluationEngine();
    private final EvaluationContext ctx = EvaluationContext.of("user-abc-123");

    private FlagEnvironmentState flag(boolean enabled, int pct) {
        return new FlagEnvironmentState("id-1", "test-flag", "test", enabled, pct, "false", 0L);
    }

    @Test void testDisabledReturnsOff() {
        var r = engine.evaluate(flag(false, 100), Collections.emptyList(), ctx, false, "test-flag");
        assertEquals(EvaluationReason.OFF, r.reason());
        assertFalse((Boolean) r.value());
    }

    @Test void test100PctReturnsTrue() {
        var r = engine.evaluate(flag(true, 100), Collections.emptyList(), ctx, false, "test-flag");
        assertEquals(EvaluationReason.FALLTHROUGH, r.reason());
        assertTrue((Boolean) r.value());
    }

    @Test void test0PctReturnsFalse() {
        var r = engine.evaluate(flag(true, 0), Collections.emptyList(), ctx, false, "test-flag");
        assertFalse((Boolean) r.value());
    }

    @Test void testNullFlagReturnsError() {
        var r = engine.evaluate(null, Collections.emptyList(), ctx, false, "missing");
        assertEquals(EvaluationReason.ERROR, r.reason());
    }

    @Test void testStickinessConsistency() {
        var f = flag(true, 50);
        var results = new java.util.HashSet<Object>();
        for (int i = 0; i < 20; i++) {
            results.add(engine.evaluate(f, Collections.emptyList(), ctx, false, "test-flag").value());
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
            var r = engine.evaluate(f, Collections.emptyList(),
                EvaluationContext.of(v.userId()), false, v.flagKey());
            boolean actual = Boolean.TRUE.equals(r.value());
            assertEquals(v.expected(), actual,
                String.format("Hash parity FAILED: (%s, %s, %d%%) expected=%b got=%b",
                    v.flagKey(), v.userId(), v.pct(), v.expected(), actual));
        }
    }
}
