package io.tombstone.evaluation;

import io.tombstone.types.*;
import org.junit.jupiter.api.Test;
import java.util.*;
import java.util.function.Function;
import static org.junit.jupiter.api.Assertions.*;

public class PrerequisiteCheckerTest {
    private final EvaluationEngine engine = new EvaluationEngine();
    private final EvaluationContext ctx = new EvaluationContext("u1", "", Map.of());

    @Test void testHardGateUnmetBlocks() {
        var baseFlag = FlagEnvironmentState.simple("id-2", "base-flag", "test", false, 0, "false", 0L);
        Function<String, FlagEnvironmentState> lookup = key -> "base-flag".equals(key) ? baseFlag : null;
        var prereq = new FlagPrerequisite("base-flag", "true", true);

        boolean satisfied = PrerequisiteChecker.checkAll(
            List.of(prereq), lookup, new HashMap<>(), new HashSet<>(), "parent-flag", engine, ctx);

        assertFalse(satisfied);
    }

    @Test void testHardGateMetPasses() {
        var baseFlag = FlagEnvironmentState.simple("id-2", "base-flag", "test", true, 100, "false", 0L);
        Function<String, FlagEnvironmentState> lookup = key -> "base-flag".equals(key) ? baseFlag : null;
        var prereq = new FlagPrerequisite("base-flag", "true", true);

        boolean satisfied = PrerequisiteChecker.checkAll(
            List.of(prereq), lookup, new HashMap<>(), new HashSet<>(), "parent-flag", engine, ctx);

        assertTrue(satisfied);
    }

    @Test void testSoftGateUnmetStillPasses() {
        var baseFlag = FlagEnvironmentState.simple("id-2", "base-flag", "test", false, 0, "false", 0L);
        Function<String, FlagEnvironmentState> lookup = key -> "base-flag".equals(key) ? baseFlag : null;
        var prereq = new FlagPrerequisite("base-flag", "true", false);

        boolean satisfied = PrerequisiteChecker.checkAll(
            List.of(prereq), lookup, new HashMap<>(), new HashSet<>(), "parent-flag", engine, ctx);

        assertTrue(satisfied);
    }

    @Test void testCycleDetectedFailsOpen() {
        Function<String, FlagEnvironmentState> lookup = key -> null; // unreachable — cycle short-circuits before lookup
        var prereq = new FlagPrerequisite("self-ref", "true", true);
        var seen = new HashSet<>(Set.of("self-ref"));

        boolean satisfied = PrerequisiteChecker.checkAll(
            List.of(prereq), lookup, new HashMap<>(), seen, "self-ref", engine, ctx);

        assertTrue(satisfied);
    }

    @Test void testMissingPrerequisiteFlagWithHardGateBlocks() {
        Function<String, FlagEnvironmentState> lookup = key -> null;
        var prereq = new FlagPrerequisite("nonexistent", "true", true);

        boolean satisfied = PrerequisiteChecker.checkAll(
            List.of(prereq), lookup, new HashMap<>(), new HashSet<>(), "parent-flag", engine, ctx);

        assertFalse(satisfied);
    }

    @Test void testMissingPrerequisiteFlagWithSoftGatePasses() {
        Function<String, FlagEnvironmentState> lookup = key -> null;
        var prereq = new FlagPrerequisite("nonexistent", "true", false);

        boolean satisfied = PrerequisiteChecker.checkAll(
            List.of(prereq), lookup, new HashMap<>(), new HashSet<>(), "parent-flag", engine, ctx);

        assertTrue(satisfied);
    }

    @Test void testMemoizationPreventsRedundantEvaluation() {
        var callCount = new int[]{0};
        var baseFlag = FlagEnvironmentState.simple("id-2", "base-flag", "test", true, 100, "false", 0L);
        Function<String, FlagEnvironmentState> lookup = key -> {
            callCount[0]++;
            return "base-flag".equals(key) ? baseFlag : null;
        };
        var prereq1 = new FlagPrerequisite("base-flag", "true", true);
        var prereq2 = new FlagPrerequisite("base-flag", "true", true);
        var cache = new HashMap<String, String>();

        PrerequisiteChecker.checkAll(List.of(prereq1, prereq2), lookup, cache, new HashSet<>(), "parent-flag", engine, ctx);

        assertEquals(1, callCount[0], "base-flag should be looked up and evaluated only once, memoized for the second reference");
    }
}
