package io.tombstone.evaluation;

import io.tombstone.types.*;
import org.junit.jupiter.api.Test;
import java.util.*;
import static org.junit.jupiter.api.Assertions.*;

public class RuleMatcherTest {
    private EvaluationContext ctx(Map<String, String> attrs) {
        return new EvaluationContext("u1", "", attrs);
    }

    @Test void testResolveAttributeFlatKey() {
        var context = ctx(Map.of("plan", "pro"));
        assertEquals("pro", RuleMatcher.resolveAttribute("plan", context));
    }

    @Test void testResolveAttributeMissingReturnsNull() {
        var context = ctx(Map.of());
        assertNull(RuleMatcher.resolveAttribute("missing", context));
    }

    @Test void testEvaluateConditionEqMatch() {
        var condition = new PropertyCondition("plan", "eq", List.of("pro"), false);
        assertTrue(RuleMatcher.evaluateCondition(condition, ctx(Map.of("plan", "pro"))));
    }

    @Test void testEvaluateConditionEqNoMatch() {
        var condition = new PropertyCondition("plan", "eq", List.of("pro"), false);
        assertFalse(RuleMatcher.evaluateCondition(condition, ctx(Map.of("plan", "free"))));
    }

    @Test void testEvaluateConditionContainsCaseInsensitive() {
        var condition = new PropertyCondition("email", "contains", List.of("ACME"), false);
        assertTrue(RuleMatcher.evaluateCondition(condition, ctx(Map.of("email", "user@acme.com"))));
    }

    @Test void testEvaluateConditionNumericGt() {
        var condition = new PropertyCondition("age", "gt", List.of("18"), false);
        assertTrue(RuleMatcher.evaluateCondition(condition, ctx(Map.of("age", "21"))));
    }

    @Test void testEvaluateConditionNumericNonNumericThrows() {
        var condition = new PropertyCondition("age", "gt", List.of("18"), false);
        assertThrows(InconclusiveMatchException.class,
            () -> RuleMatcher.evaluateCondition(condition, ctx(Map.of("age", "not-a-number"))));
    }

    @Test void testEvaluateConditionMissingAttributeThrows() {
        var condition = new PropertyCondition("missing_attr", "eq", List.of("x"), false);
        assertThrows(InconclusiveMatchException.class,
            () -> RuleMatcher.evaluateCondition(condition, ctx(Map.of())));
    }

    @Test void testEvaluateConditionNegateInverts() {
        var condition = new PropertyCondition("plan", "eq", List.of("pro"), true);
        assertFalse(RuleMatcher.evaluateCondition(condition, ctx(Map.of("plan", "pro"))));
    }

    @Test void testEvaluateConditionGeoCaseInsensitive() {
        var condition = new PropertyCondition("geo.country", "in", List.of("US", "CA"), false);
        assertTrue(RuleMatcher.evaluateCondition(condition, ctx(Map.of("geo.country", "us"))));
    }

    @Test void testPaddedVersionOrdersNumericSegmentsCorrectly() {
        assertTrue(RuleMatcher.paddedVersion("1.9.0").compareTo(RuleMatcher.paddedVersion("1.10.0")) < 0);
    }

    @Test void testPaddedVersionPrereleaseSortsBelowRelease() {
        assertTrue(RuleMatcher.paddedVersion("1.0.0-beta").compareTo(RuleMatcher.paddedVersion("1.0.0")) < 0);
    }

    @Test void testPaddedVersionStripsVPrefixAndBuildMetadata() {
        assertEquals(RuleMatcher.paddedVersion("1.2.3"), RuleMatcher.paddedVersion("v1.2.3+build.5"));
    }

    @Test void testEvaluateConditionSemverGte() {
        var condition = new PropertyCondition("app_version", "semver_gte", List.of("1.9.0"), false);
        var context = ctx(Map.of("app_version", "1.10.0"));
        assertTrue(RuleMatcher.evaluateCondition(condition, context));
    }

    @Test void testEvaluateConditionSemverPrereleaseOrdering() {
        var condition = new PropertyCondition("app_version", "semver_gte", List.of("1.0.0"), false);
        var context = ctx(Map.of("app_version", "1.0.0-beta"));
        assertFalse(RuleMatcher.evaluateCondition(condition, context));
    }

    @Test void testEvaluateConditionDateBefore() {
        var condition = new PropertyCondition("signup_date", "date_before", List.of("2026-01-01T00:00:00Z"), false);
        var context = ctx(Map.of("signup_date", "2025-06-01T00:00:00Z"));
        assertTrue(RuleMatcher.evaluateCondition(condition, context));
    }

    @Test void testEvaluateConditionDateMalformedThrows() {
        var condition = new PropertyCondition("signup_date", "date_before", List.of("2026-01-01T00:00:00Z"), false);
        var context = ctx(Map.of("signup_date", "not-a-date"));
        assertThrows(InconclusiveMatchException.class,
            () -> RuleMatcher.evaluateCondition(condition, context));
    }
}
