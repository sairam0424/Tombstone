# Java SDK Parity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Bring `flagmind-java` from "steps 1+5 only" to the full 5-step canonical evaluation
pipeline (prerequisites, target list, priority-sorted rule matching with full operator
surface, both hash versions), verified against `packages/sdks/test-contract/vectors.json`
v1.2, and standardize its Maven `artifactId` naming.

**Architecture:** Extend the existing `record`-based types (`FlagEnvironmentState`,
`EvaluationContext`) with new fields for prerequisites/rules/target-list/hash-version.
Replace `EvaluationEngine.evaluate()`'s current 6-branch if-chain with the full pipeline,
adding two new package-private helper classes: `PrerequisiteChecker` (steps 2, with
memoization + cycle detection) and `RuleMatcher` (step 4, operator dispatch). A new
unchecked `InconclusiveMatchException` signals "cannot evaluate this condition" up to
`RuleMatcher`'s per-rule catch, which then continues to the next rule — mirroring Python's
`InconclusiveMatchError`/`continue` pattern from `evaluation.py:118-120`.

**Tech Stack:** Java 21, Gradle, Guava (already a dependency, used for MurmurHash3 — no new
external libraries needed for FNV-1a since it is trivial to implement by hand, matching the
Python reference's pure-arithmetic approach in `evaluation.py:27-48`), JUnit 5, Jackson
(already a dependency, used to deserialize `test-contract/vectors.json`).

## Global Constraints

- Canonical model per `docs/superpowers/specs/2026-08-04-v1.5.0-sdk-parity-and-dependency-viz-design.md`
  Section 3 — this is the ONLY source of truth for behavior. Do not consult TS or Python
  source for behavior not already cited in this plan; both diverge from the canonical model
  on several points (see spec Section 2a).
- Contract vectors: `packages/sdks/test-contract/vectors.json` MUST be at version `"1.2"` or
  higher before starting (Phase 1 of the overall v1.5.0 upgrade — confirm this file exists
  with `prerequisite_vectors`/`rule_vectors` keys before Task 1).
- No `variation`/value field is added to `FlagEnvironmentState` this release — prerequisite
  comparison uses the string-compare mechanism (forward-compatible with future multivariate
  support) but is only ever exercised against stringified boolean outcomes (`"true"`/`"false"`)
  in this release, since `enabled` (bool) is the only flag outcome type that exists.
- Regex targeting-rule operator stays declared-but-unimplemented — do NOT add regex support.
- String operators (`contains`/`startswith`/`endswith`) are case-**insensitive** (canonical
  choice) — this is a deliberate divergence from what a Java engineer might assume is "the
  default"; do not use `String.contains`/`startsWith`/`endsWith` directly without uppercasing
  both sides first.
- FNV-1a v2 hash MUST iterate over UTF-8 bytes, not Java `char` (UTF-16 code units) — use
  `s.getBytes(StandardCharsets.UTF_8)`, matching the canonical choice in spec Section 3.
- Maven `artifactId` changes from `flagmind-java` to `tombstone-java-sdk` — the Gradle
  `group = "io.tombstone"` (in `build.gradle:5`) is already correct and stays unchanged.
- Branch: `feat/java-sdk-parity-v1.5.0` off `origin/develop`.
- Run `cd packages/sdks/flagmind-java && gradle test` before every commit (this environment
  has no `gradle`/`gradlew` pre-installed — download Gradle 8.7 distribution directly if
  needed, per the pattern used successfully in the v1.4.3 release cycle).

---

## Phase 1 — Types

### Task 1: Add new fields to FlagEnvironmentState and supporting types

**Files:**
- Modify: `packages/sdks/flagmind-java/src/main/java/io/tombstone/types/FlagEnvironmentState.java`
- Create: `packages/sdks/flagmind-java/src/main/java/io/tombstone/types/FlagPrerequisite.java`
- Create: `packages/sdks/flagmind-java/src/main/java/io/tombstone/types/TargetingRule.java`
- Create: `packages/sdks/flagmind-java/src/main/java/io/tombstone/types/PropertyCondition.java`
- Test: `packages/sdks/flagmind-java/src/test/java/io/tombstone/types/FlagEnvironmentStateTest.java`

**Interfaces:**
- Consumes: nothing (pure type definitions).
- Produces: `FlagEnvironmentState(flagId, flagKey, environment, enabled, rolloutPct, safeDefault, updatedAt, prerequisites: List<FlagPrerequisite>, targetingRules: List<TargetingRule>, targetList: List<String>, hashVersion: int)` — 4 new fields appended after `updatedAt` to preserve positional-constructor call-site compatibility for the 7 existing fields (existing test file `EvaluationEngineTest.java:14` constructs positionally; this task's own new tests use the full 11-arg constructor). `FlagPrerequisite(flagKey: String, requiredVariation: String, gate: boolean)`. `TargetingRule(id: String, conditions: List<PropertyCondition>, rolloutPct: double, variation: String, priority: int)`. `PropertyCondition(attribute: String, operator: String, values: List<String>, negate: boolean)`.

- [ ] **Step 1: Write the failing test**

```java
// packages/sdks/flagmind-java/src/test/java/io/tombstone/types/FlagEnvironmentStateTest.java
package io.tombstone.types;

import org.junit.jupiter.api.Test;
import java.util.List;
import static org.junit.jupiter.api.Assertions.*;

public class FlagEnvironmentStateTest {
    @Test void testConstructWithAllFields() {
        var prereq = new FlagPrerequisite("base-flag", "true", true);
        var condition = new PropertyCondition("plan", "eq", List.of("pro"), false);
        var rule = new TargetingRule("r1", List.of(condition), 100.0, "matched", 0);

        var state = new FlagEnvironmentState(
            "id-1", "test-flag", "test", true, 50, "false", 0L,
            List.of(prereq), List.of(rule), List.of("user-1"), 2
        );

        assertEquals(1, state.prerequisites().size());
        assertEquals("base-flag", state.prerequisites().get(0).flagKey());
        assertEquals(1, state.targetingRules().size());
        assertEquals("plan", state.targetingRules().get(0).conditions().get(0).attribute());
        assertEquals(List.of("user-1"), state.targetList());
        assertEquals(2, state.hashVersion());
    }

    @Test void testDefaultHashVersionIsOneViaConvenienceFactory() {
        var state = FlagEnvironmentState.simple("id-1", "test-flag", "test", true, 50, "false", 0L);
        assertEquals(1, state.hashVersion());
        assertTrue(state.prerequisites().isEmpty());
        assertTrue(state.targetingRules().isEmpty());
        assertTrue(state.targetList().isEmpty());
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/sdks/flagmind-java && gradle test --tests "io.tombstone.types.FlagEnvironmentStateTest"`
Expected: FAIL — compile error, `FlagPrerequisite`/`TargetingRule`/`PropertyCondition` do not exist, and `FlagEnvironmentState`'s constructor doesn't accept 11 args.

- [ ] **Step 3: Create the new type records**

```java
// packages/sdks/flagmind-java/src/main/java/io/tombstone/types/FlagPrerequisite.java
package io.tombstone.types;

public record FlagPrerequisite(
    String flagKey,
    String requiredVariation,
    boolean gate
) {}
```

```java
// packages/sdks/flagmind-java/src/main/java/io/tombstone/types/PropertyCondition.java
package io.tombstone.types;

import java.util.List;

public record PropertyCondition(
    String attribute,
    String operator,
    List<String> values,
    boolean negate
) {}
```

```java
// packages/sdks/flagmind-java/src/main/java/io/tombstone/types/TargetingRule.java
package io.tombstone.types;

import java.util.List;

public record TargetingRule(
    String id,
    List<PropertyCondition> conditions,
    double rolloutPct,
    String variation,
    int priority
) {}
```

- [ ] **Step 4: Extend FlagEnvironmentState with new fields and a convenience factory**

```java
// packages/sdks/flagmind-java/src/main/java/io/tombstone/types/FlagEnvironmentState.java
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
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd packages/sdks/flagmind-java && gradle test --tests "io.tombstone.types.FlagEnvironmentStateTest"`
Expected: PASS (2 passed).

- [ ] **Step 6: Fix the existing EvaluationEngineTest call sites (positional constructor changed)**

The existing `EvaluationEngineTest.java:14` `flag(boolean enabled, int pct)` helper constructs
`FlagEnvironmentState` with 7 positional args — this now fails to compile since the record has
11 fields. Update it to use the new `simple()` factory:

```java
// packages/sdks/flagmind-java/src/test/java/io/tombstone/EvaluationEngineTest.java
// Change line 13-15 from:
//     private FlagEnvironmentState flag(boolean enabled, int pct) {
//         return new FlagEnvironmentState("id-1", "test-flag", "test", enabled, pct, "false", 0L);
//     }
// to:
    private FlagEnvironmentState flag(boolean enabled, int pct) {
        return FlagEnvironmentState.simple("id-1", "test-flag", "test", enabled, pct, "false", 0L);
    }
```

- [ ] **Step 7: Run the full existing test suite to confirm no regressions**

Run: `cd packages/sdks/flagmind-java && gradle test`
Expected: PASS (8 passed — 6 existing + 2 new).

- [ ] **Step 8: Commit**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone
git add packages/sdks/flagmind-java/src/main/java/io/tombstone/types/ packages/sdks/flagmind-java/src/test/java/io/tombstone/
git commit -m "feat(java-sdk): add prerequisite/rule/target-list/hash-version fields to FlagEnvironmentState"
```

---

### Task 2: Add InconclusiveMatchException and extend EvaluationReason usage

**Files:**
- Create: `packages/sdks/flagmind-java/src/main/java/io/tombstone/evaluation/InconclusiveMatchException.java`
- Test: `packages/sdks/flagmind-java/src/test/java/io/tombstone/evaluation/InconclusiveMatchExceptionTest.java`

**Interfaces:**
- Consumes: nothing.
- Produces: `InconclusiveMatchException extends RuntimeException` — thrown by `RuleMatcher` (Task 4) when a condition cannot be evaluated (missing attribute, unparseable numeric/date/semver value).

- [ ] **Step 1: Write the failing test**

```java
// packages/sdks/flagmind-java/src/test/java/io/tombstone/evaluation/InconclusiveMatchExceptionTest.java
package io.tombstone.evaluation;

import org.junit.jupiter.api.Test;
import static org.junit.jupiter.api.Assertions.*;

public class InconclusiveMatchExceptionTest {
    @Test void testIsRuntimeException() {
        var ex = new InconclusiveMatchException("attribute missing");
        assertTrue(ex instanceof RuntimeException);
        assertEquals("attribute missing", ex.getMessage());
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/sdks/flagmind-java && gradle test --tests "io.tombstone.evaluation.InconclusiveMatchExceptionTest"`
Expected: FAIL — class does not exist.

- [ ] **Step 3: Implement**

```java
// packages/sdks/flagmind-java/src/main/java/io/tombstone/evaluation/InconclusiveMatchException.java
package io.tombstone.evaluation;

/** Thrown when a targeting-rule condition cannot be evaluated locally
 *  (missing attribute, unparseable numeric/date/semver value). Caught
 *  per-rule by RuleMatcher, which treats it as "this rule did not
 *  match" and continues to the next priority-sorted rule. Unchecked —
 *  mirrors Python's InconclusiveMatchError, which is caught internally
 *  and never expected to propagate to SDK callers. */
public class InconclusiveMatchException extends RuntimeException {
    public InconclusiveMatchException(String message) {
        super(message);
    }
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd packages/sdks/flagmind-java && gradle test --tests "io.tombstone.evaluation.InconclusiveMatchExceptionTest"`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/sdks/flagmind-java/src/main/java/io/tombstone/evaluation/InconclusiveMatchException.java packages/sdks/flagmind-java/src/test/java/io/tombstone/evaluation/InconclusiveMatchExceptionTest.java
git commit -m "feat(java-sdk): add InconclusiveMatchException for unevaluatable rule conditions"
```

---

## Phase 2 — Rule Matching (Step 4)

### Task 3: RuleMatcher — attribute resolution and equality/string/numeric operators

**Files:**
- Create: `packages/sdks/flagmind-java/src/main/java/io/tombstone/evaluation/RuleMatcher.java`
- Test: `packages/sdks/flagmind-java/src/test/java/io/tombstone/evaluation/RuleMatcherTest.java`

**Interfaces:**
- Consumes: `PropertyCondition`, `TargetingRule`, `EvaluationContext` (Task 1 types + existing `EvaluationContext`).
- Produces: `RuleMatcher.resolveAttribute(String attribute, EvaluationContext context): Object` (dot-notation resolution, returns `null` if unresolvable), `RuleMatcher.evaluateCondition(PropertyCondition condition, EvaluationContext context): boolean` (throws `InconclusiveMatchException` on unresolvable/unparseable input), `RuleMatcher.matchRules(List<TargetingRule> rules, EvaluationContext context, String flagKey): Optional<String>` (returns matched variation, or empty if no rule matches; implements priority sort + per-rule rollout sub-bucketing).

`EvaluationContext.attrs()` is `Map<String, String>` (`EvaluationContext.java:8`) — dot-notation
resolution over a flat `Map<String,String>` means only single-segment attribute names resolve
via the map; multi-segment paths (e.g. `"geo.country"`) resolve via a nested-map convention
where the caller stores nested JSON-like structures. Since Java's existing `EvaluationContext`
has no nested-map support, this task adds dot-notation resolution over `attrs` treating dots as
literal key characters FIRST (flat lookup), falling back to a nested-path interpretation only if
a nested `Map<String,Object>` variant is later needed — for this release, GEO attributes are
sent by the caller as flat keys `"geo.country"`/`"geo.region"` directly in `attrs`, matching how
the wire format already flattens nested JSON before populating `Map<String,String>`. This keeps
`EvaluationContext`'s existing `Map<String,String>` type unchanged (no breaking API change for
existing SDK callers).

- [ ] **Step 1: Write the failing tests**

```java
// packages/sdks/flagmind-java/src/test/java/io/tombstone/evaluation/RuleMatcherTest.java
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
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd packages/sdks/flagmind-java && gradle test --tests "io.tombstone.evaluation.RuleMatcherTest"`
Expected: FAIL — `RuleMatcher` class does not exist.

- [ ] **Step 3: Implement RuleMatcher (attribute resolution + eq/string/numeric operators)**

```java
// packages/sdks/flagmind-java/src/main/java/io/tombstone/evaluation/RuleMatcher.java
package io.tombstone.evaluation;

import io.tombstone.types.*;
import java.util.*;

public class RuleMatcher {

    private static final Set<String> GEO_ATTRIBUTES = Set.of("geo.country", "geo.region");

    /** Canonical model: dot-notation attribute resolution over a flat attrs map
     *  (this release's EvaluationContext.attrs is Map<String,String>, so multi-
     *  segment paths like "geo.country" resolve as literal flat keys — the wire
     *  format flattens nested JSON before populating attrs). Returns null if
     *  the attribute is not present. */
    public static Object resolveAttribute(String attribute, EvaluationContext context) {
        if ("user_id".equals(attribute)) return context.userId();
        if ("org_id".equals(attribute)) return context.orgId();
        return context.attrs().get(attribute);
    }

    public static boolean evaluateCondition(PropertyCondition condition, EvaluationContext context) {
        Object raw = resolveAttribute(condition.attribute(), context);
        if (raw == null) {
            throw new InconclusiveMatchException(
                "Attribute '" + condition.attribute() + "' not present in evaluation context");
        }
        String attrVal = String.valueOf(raw);
        String op = normalizeOperator(condition.operator());
        List<String> values = condition.values();
        boolean isGeo = GEO_ATTRIBUTES.contains(condition.attribute());

        boolean result;
        switch (op) {
            case "eq", "in" -> result = isGeo
                ? containsIgnoreCase(values, attrVal)
                : values.contains(attrVal);
            case "neq", "nin" -> result = isGeo
                ? !containsIgnoreCase(values, attrVal)
                : !values.contains(attrVal);
            case "contains" -> result = anyContainsIgnoreCase(values, attrVal);
            case "startswith" -> result = anyStartsWithIgnoreCase(values, attrVal);
            case "endswith" -> result = anyEndsWithIgnoreCase(values, attrVal);
            case "gt", "gte", "lt", "lte" -> result = evaluateNumeric(op, attrVal, values, condition.attribute());
            default -> throw new InconclusiveMatchException("Unknown operator: '" + op + "'");
        }
        return condition.negate() ? !result : result;
    }

    private static String normalizeOperator(String operator) {
        String op = operator.toLowerCase();
        return switch (op) {
            case "not_in" -> "nin";
            case "prefix" -> "startswith";
            case "suffix" -> "endswith";
            default -> op;
        };
    }

    private static boolean containsIgnoreCase(List<String> values, String attrVal) {
        String upper = attrVal.toUpperCase();
        return values.stream().anyMatch(v -> v.toUpperCase().equals(upper));
    }

    private static boolean anyContainsIgnoreCase(List<String> values, String attrVal) {
        String upperAttr = attrVal.toUpperCase();
        return values.stream().anyMatch(v -> upperAttr.contains(v.toUpperCase()));
    }

    private static boolean anyStartsWithIgnoreCase(List<String> values, String attrVal) {
        String upperAttr = attrVal.toUpperCase();
        return values.stream().anyMatch(v -> upperAttr.startsWith(v.toUpperCase()));
    }

    private static boolean anyEndsWithIgnoreCase(List<String> values, String attrVal) {
        String upperAttr = attrVal.toUpperCase();
        return values.stream().anyMatch(v -> upperAttr.endsWith(v.toUpperCase()));
    }

    private static boolean evaluateNumeric(String op, String attrVal, List<String> values, String attribute) {
        double nAttr, nVal;
        try {
            nAttr = Double.parseDouble(attrVal);
            nVal = Double.parseDouble(values.get(0));
        } catch (NumberFormatException | IndexOutOfBoundsException e) {
            throw new InconclusiveMatchException(
                "Numeric cast failed for '" + attribute + "': " + e.getMessage());
        }
        return switch (op) {
            case "gt" -> nAttr > nVal;
            case "gte" -> nAttr >= nVal;
            case "lt" -> nAttr < nVal;
            case "lte" -> nAttr <= nVal;
            default -> false;
        };
    }
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd packages/sdks/flagmind-java && gradle test --tests "io.tombstone.evaluation.RuleMatcherTest"`
Expected: PASS (10 passed).

- [ ] **Step 5: Commit**

```bash
git add packages/sdks/flagmind-java/src/main/java/io/tombstone/evaluation/RuleMatcher.java packages/sdks/flagmind-java/src/test/java/io/tombstone/evaluation/RuleMatcherTest.java
git commit -m "feat(java-sdk): add RuleMatcher attribute resolution and eq/string/numeric operators"
```

---

### Task 4: RuleMatcher — semver and date operators

**Files:**
- Modify: `packages/sdks/flagmind-java/src/main/java/io/tombstone/evaluation/RuleMatcher.java`
- Modify: `packages/sdks/flagmind-java/src/test/java/io/tombstone/evaluation/RuleMatcherTest.java`

**Interfaces:**
- Consumes: `evaluateCondition` from Task 3 (adding new operator branches).
- Produces: `RuleMatcher.paddedVersion(String v): String` (package-private, used internally by `evaluateCondition`'s semver branch and directly tested).

- [ ] **Step 1: Write the failing tests**

```java
// Append to packages/sdks/flagmind-java/src/test/java/io/tombstone/evaluation/RuleMatcherTest.java

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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd packages/sdks/flagmind-java && gradle test --tests "io.tombstone.evaluation.RuleMatcherTest"`
Expected: FAIL — `paddedVersion` doesn't exist, semver/date operators throw `InconclusiveMatchException` unconditionally (fall into the `default` branch).

- [ ] **Step 3: Add semver padding and date/semver operator branches**

```java
// In packages/sdks/flagmind-java/src/main/java/io/tombstone/evaluation/RuleMatcher.java
// Add these imports at the top:
import java.time.OffsetDateTime;
import java.time.format.DateTimeParseException;
import java.util.regex.Pattern;

// Add this constant near GEO_ATTRIBUTES:
    private static final Pattern LEADING_V_OR_BUILD_METADATA = Pattern.compile("(^v|\\+.*$)");
    private static final Pattern PURE_DIGITS = Pattern.compile("^\\d+$");

// Add these branches to the switch in evaluateCondition, before "default":
            case "semver_gt", "semver_gte", "semver_lt", "semver_lte", "semver_eq" ->
                result = evaluateSemver(op, attrVal, values, condition.attribute());
            case "date_before", "date_after" ->
                result = evaluateDate(op, attrVal, values, condition.attribute());

// Add these methods after evaluateNumeric:

    /** Ported byte-for-byte from flagmind-python's matching.py:27-39 (GrowthBook pattern). */
    static String paddedVersion(String v) {
        v = LEADING_V_OR_BUILD_METADATA.matcher(v).replaceAll("");
        String[] parts = v.split("[-.]");
        var padded = new ArrayList<String>();
        for (String p : parts) {
            padded.add(PURE_DIGITS.matcher(p).matches() ? String.format("%5s", p) : p);
        }
        if (padded.size() == 3) {
            padded.add("~");
        }
        return String.join(".", padded);
    }

    private static boolean evaluateSemver(String op, String attrVal, List<String> values, String attribute) {
        if (values.isEmpty()) {
            throw new InconclusiveMatchException(
                "semver operator requires at least one value for '" + attribute + "'");
        }
        String a = paddedVersion(attrVal);
        String b = paddedVersion(values.get(0));
        int cmp = a.compareTo(b);
        return switch (op) {
            case "semver_gt" -> cmp > 0;
            case "semver_gte" -> cmp >= 0;
            case "semver_lt" -> cmp < 0;
            case "semver_lte" -> cmp <= 0;
            case "semver_eq" -> cmp == 0;
            default -> false;
        };
    }

    private static boolean evaluateDate(String op, String attrVal, List<String> values, String attribute) {
        OffsetDateTime dtAttr, dtVal;
        try {
            dtAttr = OffsetDateTime.parse(normalizeIso8601(attrVal));
            dtVal = OffsetDateTime.parse(normalizeIso8601(values.get(0)));
        } catch (DateTimeParseException | IndexOutOfBoundsException e) {
            throw new InconclusiveMatchException(
                "Date parse failed for '" + attribute + "': " + e.getMessage());
        }
        return "date_before".equals(op) ? dtAttr.isBefore(dtVal) : dtAttr.isAfter(dtVal);
    }

    private static String normalizeIso8601(String s) {
        return s.replace("Z", "+00:00");
    }
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd packages/sdks/flagmind-java && gradle test --tests "io.tombstone.evaluation.RuleMatcherTest"`
Expected: PASS (17 passed).

- [ ] **Step 5: Commit**

```bash
git add packages/sdks/flagmind-java/src/main/java/io/tombstone/evaluation/RuleMatcher.java packages/sdks/flagmind-java/src/test/java/io/tombstone/evaluation/RuleMatcherTest.java
git commit -m "feat(java-sdk): add semver and date operators to RuleMatcher"
```

---

### Task 5: RuleMatcher — priority sort, multi-condition AND, per-rule rollout, matchRules entrypoint

**Files:**
- Modify: `packages/sdks/flagmind-java/src/main/java/io/tombstone/evaluation/RuleMatcher.java`
- Modify: `packages/sdks/flagmind-java/src/test/java/io/tombstone/evaluation/RuleMatcherTest.java`

**Interfaces:**
- Consumes: `evaluateCondition` from Tasks 3-4, MurmurHash3 hashing logic (extracted from `EvaluationEngine.isInRollout` in Task 6, but for this task inline the same Guava call directly in `RuleMatcher` since `matchRules` needs it independently of the engine's Step 5 fallthrough).
- Produces: `RuleMatcher.matchRules(List<TargetingRule> rules, EvaluationContext context, String flagKey): Optional<String>`.

- [ ] **Step 1: Write the failing tests**

```java
// Append to packages/sdks/flagmind-java/src/test/java/io/tombstone/evaluation/RuleMatcherTest.java

    @Test void testMatchRulesFirstPriorityWins() {
        var cond = new PropertyCondition("plan", "eq", List.of("pro"), false);
        var r1 = new TargetingRule("r1", List.of(cond), 100.0, "variant-a", 0);
        var r2 = new TargetingRule("r2", List.of(cond), 100.0, "variant-b", 1);
        var result = RuleMatcher.matchRules(List.of(r2, r1), ctx(Map.of("plan", "pro")), "test-flag");
        assertEquals(Optional.of("variant-a"), result);
    }

    @Test void testMatchRulesMultiConditionAndBothMatch() {
        var c1 = new PropertyCondition("plan", "eq", List.of("pro"), false);
        var c2 = new PropertyCondition("region", "eq", List.of("us"), false);
        var rule = new TargetingRule("r1", List.of(c1, c2), 100.0, "match", 0);
        var result = RuleMatcher.matchRules(List.of(rule), ctx(Map.of("plan", "pro", "region", "us")), "test-flag");
        assertEquals(Optional.of("match"), result);
    }

    @Test void testMatchRulesMultiConditionAndOneFails() {
        var c1 = new PropertyCondition("plan", "eq", List.of("pro"), false);
        var c2 = new PropertyCondition("region", "eq", List.of("us"), false);
        var rule = new TargetingRule("r1", List.of(c1, c2), 100.0, "match", 0);
        var result = RuleMatcher.matchRules(List.of(rule), ctx(Map.of("plan", "pro", "region", "eu")), "test-flag");
        assertEquals(Optional.empty(), result);
    }

    @Test void testMatchRulesNoMatchFallsThrough() {
        var cond = new PropertyCondition("plan", "eq", List.of("enterprise"), false);
        var rule = new TargetingRule("r1", List.of(cond), 100.0, "match", 0);
        var result = RuleMatcher.matchRules(List.of(rule), ctx(Map.of("plan", "free")), "test-flag");
        assertEquals(Optional.empty(), result);
    }

    @Test void testMatchRulesInconclusiveConditionSkipsToNextRule() {
        var missingCond = new PropertyCondition("missing_attr", "eq", List.of("x"), false);
        var proCond = new PropertyCondition("plan", "eq", List.of("pro"), false);
        var r1 = new TargetingRule("r1", List.of(missingCond), 100.0, "skipped", 0);
        var r2 = new TargetingRule("r2", List.of(proCond), 100.0, "fallback-match", 1);
        var result = RuleMatcher.matchRules(List.of(r1, r2), ctx(Map.of("plan", "pro")), "test-flag");
        assertEquals(Optional.of("fallback-match"), result);
    }

    @Test void testMatchRulesPerRuleRolloutSubBucketingFallsToNextRule() {
        var cond = new PropertyCondition("plan", "eq", List.of("pro"), false);
        var r1 = new TargetingRule("r1", List.of(cond), 0.0, "never", 0);
        var r2 = new TargetingRule("r2", List.of(cond), 100.0, "fallback", 1);
        var result = RuleMatcher.matchRules(List.of(r1, r2), ctx(Map.of("plan", "pro")), "test-flag");
        assertEquals(Optional.of("fallback"), result);
    }
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd packages/sdks/flagmind-java && gradle test --tests "io.tombstone.evaluation.RuleMatcherTest"`
Expected: FAIL — `matchRules` does not exist.

- [ ] **Step 3: Implement matchRules**

```java
// In packages/sdks/flagmind-java/src/main/java/io/tombstone/evaluation/RuleMatcher.java
// Add these imports at the top:
import com.google.common.hash.Hashing;
import java.nio.charset.StandardCharsets;

// Add this method (public entrypoint for Step 4):

    /** Canonical model: priority-ascending sort (0 = highest), multi-condition AND
     *  per rule, per-rule rollout sub-bucketing (matched conditions but bucket
     *  outside this rule's own rolloutPct falls to the NEXT rule, not Step 5). */
    public static Optional<String> matchRules(List<TargetingRule> rules, EvaluationContext context, String flagKey) {
        var sorted = rules.stream()
            .sorted(Comparator.comparingInt(TargetingRule::priority))
            .toList();

        for (TargetingRule rule : sorted) {
            boolean allMatch;
            try {
                allMatch = rule.conditions().stream().allMatch(c -> evaluateCondition(c, context));
            } catch (InconclusiveMatchException e) {
                continue; // rule inconclusive — try next rule
            }
            if (!allMatch) {
                continue;
            }
            int bucket = murmur3Bucket(flagKey, context.userId());
            if (bucket < rule.rolloutPct()) {
                return Optional.of(rule.variation());
            }
            // conditions matched but outside this rule's own rollout — try next rule
        }
        return Optional.empty();
    }

    private static int murmur3Bucket(String flagKey, String userId) {
        int hash = Hashing.murmur3_32_fixed()
            .hashString(flagKey + userId, StandardCharsets.UTF_8)
            .asInt();
        return Integer.remainderUnsigned(hash, 100);
    }
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd packages/sdks/flagmind-java && gradle test --tests "io.tombstone.evaluation.RuleMatcherTest"`
Expected: PASS (23 passed).

- [ ] **Step 5: Commit**

```bash
git add packages/sdks/flagmind-java/src/main/java/io/tombstone/evaluation/RuleMatcher.java packages/sdks/flagmind-java/src/test/java/io/tombstone/evaluation/RuleMatcherTest.java
git commit -m "feat(java-sdk): add matchRules with priority sort and per-rule rollout sub-bucketing"
```

---

## Phase 3 — Prerequisites (Step 2) and Target List (Step 3)

### Task 6: PrerequisiteChecker with cycle detection and memoization

**Files:**
- Create: `packages/sdks/flagmind-java/src/main/java/io/tombstone/evaluation/PrerequisiteChecker.java`
- Test: `packages/sdks/flagmind-java/src/test/java/io/tombstone/evaluation/PrerequisiteCheckerTest.java`

**Interfaces:**
- Consumes: `FlagPrerequisite` (Task 1), a lookup function for other flags in the same snapshot.
- Produces: `PrerequisiteChecker.checkAll(List<FlagPrerequisite> prerequisites, Function<String, FlagEnvironmentState> flagLookup, Map<String, String> cache, Set<String> seen, String currentFlagKey, EvaluationEngine engine, EvaluationContext context): boolean`. Takes the `EvaluationEngine` itself as a parameter to enable recursive evaluation of dependency flags (mirrors Python's `evaluation.py:89-94`, which calls its own module-level `evaluate()` recursively) — this is a forward reference resolved when `EvaluationEngine` gains its `evaluate` overload in Task 7; until then this class only depends on the `EvaluationEngine` type signature, not its implementation.

- [ ] **Step 1: Write the failing tests**

```java
// packages/sdks/flagmind-java/src/test/java/io/tombstone/evaluation/PrerequisiteCheckerTest.java
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd packages/sdks/flagmind-java && gradle test --tests "io.tombstone.evaluation.PrerequisiteCheckerTest"`
Expected: FAIL — `PrerequisiteChecker` does not exist.

- [ ] **Step 3: Implement PrerequisiteChecker**

```java
// packages/sdks/flagmind-java/src/main/java/io/tombstone/evaluation/PrerequisiteChecker.java
package io.tombstone.evaluation;

import io.tombstone.types.*;
import java.util.*;
import java.util.function.Function;

public class PrerequisiteChecker {

    /** Canonical model: string-compare mechanism against the dependency's
     *  stringified boolean outcome (forward-compatible with future
     *  multivariate prerequisites — see design spec Section 3). Cycle
     *  detection via explicit seen-set (Python's approach); memoization
     *  via cache dict keyed by dependency flag key (Python's approach). */
    public static boolean checkAll(
        List<FlagPrerequisite> prerequisites,
        Function<String, FlagEnvironmentState> flagLookup,
        Map<String, String> cache,
        Set<String> seen,
        String currentFlagKey,
        EvaluationEngine engine,
        EvaluationContext context
    ) {
        var chainSeen = new HashSet<>(seen);
        chainSeen.add(currentFlagKey);

        for (FlagPrerequisite prereq : prerequisites) {
            String depKey = prereq.flagKey();
            String depVariation;

            if (cache.containsKey(depKey)) {
                depVariation = cache.get(depKey);
            } else if (chainSeen.contains(depKey)) {
                continue; // cycle detected — fail open, skip this one prerequisite
            } else {
                FlagEnvironmentState depFlag = flagLookup.apply(depKey);
                if (depFlag == null) {
                    depVariation = null;
                } else {
                    var depResult = engine.evaluate(depFlag, context, false, depKey, flagLookup, cache, chainSeen);
                    depVariation = String.valueOf(depResult.value());
                }
                cache.put(depKey, depVariation);
            }

            if (!Objects.equals(depVariation, prereq.requiredVariation())) {
                if (!prereq.gate()) {
                    continue; // soft — unmet but non-blocking
                }
                return false; // hard gate — block entire parent flag
            }
        }
        return true;
    }
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd packages/sdks/flagmind-java && gradle test --tests "io.tombstone.evaluation.PrerequisiteCheckerTest"`
Expected: FAIL — `engine.evaluate(...)` with 7 args doesn't exist yet on `EvaluationEngine`. This is
expected at this point in the plan; Task 7 adds the new `evaluate` overload PrerequisiteChecker
depends on. Do not attempt to make this pass yet — proceed to Task 7, then return and re-run.

- [ ] **Step 5: Commit (test file only, main class awaiting Task 7's engine overload)**

```bash
git add packages/sdks/flagmind-java/src/main/java/io/tombstone/evaluation/PrerequisiteChecker.java packages/sdks/flagmind-java/src/test/java/io/tombstone/evaluation/PrerequisiteCheckerTest.java
git commit -m "feat(java-sdk): add PrerequisiteChecker with cycle detection and memoization (tests pending EvaluationEngine.evaluate overload)"
```

---

## Phase 4 — Full Pipeline Integration

### Task 7: Rewrite EvaluationEngine.evaluate to the full 5-step pipeline

**Files:**
- Modify: `packages/sdks/flagmind-java/src/main/java/io/tombstone/evaluation/EvaluationEngine.java`
- Modify: `packages/sdks/flagmind-java/src/test/java/io/tombstone/EvaluationEngineTest.java`

**Interfaces:**
- Consumes: `PrerequisiteChecker.checkAll` (Task 6), `RuleMatcher.matchRules` (Task 5), `FlagEnvironmentState`'s new fields (Task 1).
- Produces: `EvaluationEngine.evaluate(FlagEnvironmentState flagState, EvaluationContext context, T defaultValue, String flagKey, Function<String, FlagEnvironmentState> flagLookup, Map<String, String> prerequisiteCache, Set<String> seenKeys): EvaluationResult<T>` — the new canonical signature. The OLD signature `evaluate(FlagEnvironmentState, List<Object>, EvaluationContext, T, String)` is REMOVED entirely (the `List<Object> rules` parameter was already dead code per the design spec's audit finding — no production call site used it meaningfully) and replaced with two overloads: the 7-arg full form above, and a convenience 4-arg overload `evaluate(FlagEnvironmentState, EvaluationContext, T, String)` for callers with no prerequisites/target-list to resolve (defaults `flagLookup` to `key -> null`, fresh empty cache/seen-set per call).

- [ ] **Step 1: Update the existing test file's call sites to the new signature**

The existing `EvaluationEngineTest.java` calls `engine.evaluate(flag(...), Collections.emptyList(), ctx, false, "test-flag")` — 5 args with a dead `rules` list. Update every call site to drop that parameter using the new 4-arg convenience overload:

```java
// packages/sdks/flagmind-java/src/test/java/io/tombstone/EvaluationEngineTest.java
// Replace every occurrence of:
//     engine.evaluate(flag(...), Collections.emptyList(), ctx, false, "test-flag")
// with:
//     engine.evaluate(flag(...), ctx, false, "test-flag")
// (5 call sites: testDisabledReturnsOff, test100PctReturnsTrue, test0PctReturnsFalse,
//  testStickinessConsistency's loop body, testMurmurHash3ParityWithTypeScript's loop body.
//  testNullFlagReturnsError similarly drops Collections.emptyList().)
// Also remove the now-unused `import java.util.Collections;` line.
```

- [ ] **Step 2: Run the existing test file to verify it fails (new signature doesn't exist yet)**

Run: `cd packages/sdks/flagmind-java && gradle test --tests "io.tombstone.EvaluationEngineTest"`
Expected: FAIL — compile error, no `evaluate(FlagEnvironmentState, EvaluationContext, T, String)` overload exists yet.

- [ ] **Step 3: Write new integration tests for the full pipeline**

```java
// Append to packages/sdks/flagmind-java/src/test/java/io/tombstone/EvaluationEngineTest.java

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
```

- [ ] **Step 4: Rewrite EvaluationEngine**

```java
// packages/sdks/flagmind-java/src/main/java/io/tombstone/evaluation/EvaluationEngine.java
package io.tombstone.evaluation;

import com.google.common.hash.Hashing;
import java.nio.charset.StandardCharsets;
import java.util.*;
import java.util.function.Function;
import io.tombstone.types.*;

public class EvaluationEngine {

    private static final int MAX_PREREQ_DEPTH_UNUSED = 0; // no depth ceiling in canonical model — cycle detection via seen-set instead

    /** Convenience overload for callers with no prerequisites/target-list to resolve. */
    public <T> EvaluationResult<T> evaluate(
        FlagEnvironmentState flagState, EvaluationContext context, T defaultValue, String flagKey
    ) {
        return evaluate(flagState, context, defaultValue, flagKey, key -> null, new HashMap<>(), new HashSet<>());
    }

    /** Full 5-step canonical evaluation pipeline. See docs/SDK_CONTRACT.md for the
     *  normative spec this implements. flagLookup resolves other flags in the same
     *  snapshot for prerequisite evaluation (steps 2); pass key -> null if the
     *  caller has no snapshot access (prerequisites will then always be treated
     *  as missing, and hard-gated prerequisites will PREREQUISITE_FAILED). */
    @SuppressWarnings("unchecked")
    public <T> EvaluationResult<T> evaluate(
        FlagEnvironmentState flagState,
        EvaluationContext context,
        T defaultValue,
        String flagKey,
        Function<String, FlagEnvironmentState> flagLookup,
        Map<String, String> prerequisiteCache,
        Set<String> seenKeys
    ) {
        // Step 1: Preliminary checks
        if (flagState == null) {
            return new EvaluationResult<>(defaultValue, EvaluationReason.ERROR, false, flagKey);
        }
        if (!flagState.enabled()) {
            return new EvaluationResult<>((T) parseSafeDefault(flagState.safeDefault(), defaultValue), EvaluationReason.OFF, true, flagKey);
        }

        // Step 2: Prerequisites
        if (!flagState.prerequisites().isEmpty()) {
            boolean satisfied = PrerequisiteChecker.checkAll(
                flagState.prerequisites(), flagLookup, prerequisiteCache, seenKeys, flagKey, this, context);
            if (!satisfied) {
                return new EvaluationResult<>(defaultValue, EvaluationReason.PREREQUISITE_FAILED, true, flagKey);
            }
        }

        // Step 3: Individual target list
        if (!flagState.targetList().isEmpty() && flagState.targetList().contains(context.userId())) {
            return new EvaluationResult<>((T) Boolean.TRUE, EvaluationReason.TARGET_MATCH, true, flagKey);
        }

        // Step 4: Priority-sorted rule matching
        if (!flagState.targetingRules().isEmpty()) {
            var ruleMatch = RuleMatcher.matchRules(flagState.targetingRules(), context, flagKey);
            if (ruleMatch.isPresent()) {
                return new EvaluationResult<>((T) ruleMatch.get(), EvaluationReason.RULE_MATCH, true, flagKey);
            }
        }

        // Step 5: Fallthrough rollout
        if (flagState.rolloutPct() >= 100) {
            return new EvaluationResult<>(castEnabled(defaultValue), EvaluationReason.FALLTHROUGH, true, flagKey);
        }
        if (flagState.rolloutPct() <= 0) {
            return new EvaluationResult<>(defaultValue, EvaluationReason.FALLTHROUGH, true, flagKey);
        }
        boolean inRollout = flagState.hashVersion() == 2
            ? isInRolloutFnv(flagKey, context.userId(), flagState.rolloutPct())
            : isInRolloutMurmur3(flagKey, context.userId(), flagState.rolloutPct());
        if (inRollout) {
            return new EvaluationResult<>(castEnabled(defaultValue), EvaluationReason.FALLTHROUGH, true, flagKey);
        }
        return new EvaluationResult<>(defaultValue, EvaluationReason.FALLTHROUGH, true, flagKey);
    }

    // CRITICAL: Uses MurmurHash3 unsigned 32-bit to match TypeScript and Python SDKs.
    // TypeScript: murmurhash.v3(flagKey + userId) >>> 0 % 100
    // Python: mmh3.hash(flag_key + user_id, seed=0, signed=False) % 100
    private boolean isInRolloutMurmur3(String flagKey, String userId, int rolloutPct) {
        int hash = Hashing.murmur3_32_fixed()
            .hashString(flagKey + userId, StandardCharsets.UTF_8)
            .asInt();
        int bucket = Integer.remainderUnsigned(hash, 100);
        return bucket < rolloutPct;
    }

    // Canonical hashVersion=2: double-pass FNV-1a, UTF-8 byte iteration, 10,000-bucket
    // resolution. Ported from flagmind-python's evaluation.py:27-48 (byte iteration,
    // not TS's UTF-16 code-unit iteration — canonical choice per design spec Section 3).
    private static final long FNV_OFFSET = 2166136261L;
    private static final long FNV_PRIME = 16777619L;

    private static long fnv1aRaw(String s) {
        long h = FNV_OFFSET;
        for (byte b : s.getBytes(StandardCharsets.UTF_8)) {
            h ^= (b & 0xFF);
            h = (h * FNV_PRIME) & 0xFFFFFFFFL;
        }
        return h & 0xFFFFFFFFL;
    }

    private boolean isInRolloutFnv(String flagKey, String userId, int rolloutPct) {
        long h1 = fnv1aRaw(flagKey + userId);
        long h2 = fnv1aRaw(String.valueOf(h1));
        double bucket = (h2 % 10000) / 10000.0;
        return bucket < (rolloutPct / 100.0);
    }

    /** Canonical model: OFF-path parses safeDefault into the target type (TS's
     *  approach), falling back to the caller's defaultValue on parse failure
     *  or type mismatch. */
    @SuppressWarnings("unchecked")
    private <T> Object parseSafeDefault(String safeDefault, T fallback) {
        if (fallback instanceof Boolean) {
            return "true".equals(safeDefault);
        }
        if (fallback instanceof Number) {
            try {
                return Double.parseDouble(safeDefault);
            } catch (NumberFormatException e) {
                return fallback;
            }
        }
        if (fallback instanceof String) {
            return safeDefault;
        }
        return fallback;
    }

    @SuppressWarnings("unchecked")
    private <T> T castEnabled(T defaultValue) {
        if (defaultValue instanceof Boolean) return (T) Boolean.TRUE;
        return defaultValue;
    }
}
```

- [ ] **Step 5: Run all EvaluationEngine tests to verify they pass**

Run: `cd packages/sdks/flagmind-java && gradle test --tests "io.tombstone.EvaluationEngineTest"`
Expected: PASS (10 passed — 6 existing updated + 4 new).

- [ ] **Step 6: Return to Task 6's PrerequisiteCheckerTest and confirm it now passes**

Run: `cd packages/sdks/flagmind-java && gradle test --tests "io.tombstone.evaluation.PrerequisiteCheckerTest"`
Expected: PASS (7 passed) — the `engine.evaluate(depFlag, context, false, depKey, flagLookup, cache, chainSeen)` call
in `PrerequisiteChecker.checkAll` now resolves against the 7-arg overload added in this task.

- [ ] **Step 7: Run the full test suite**

Run: `cd packages/sdks/flagmind-java && gradle test`
Expected: PASS (all tests across all files — types (2) + InconclusiveMatchException (1) +
RuleMatcher (23) + PrerequisiteChecker (7) + EvaluationEngine (10) = 43 passed).

- [ ] **Step 8: Commit**

```bash
git add packages/sdks/flagmind-java/src/main/java/io/tombstone/evaluation/EvaluationEngine.java packages/sdks/flagmind-java/src/test/java/io/tombstone/EvaluationEngineTest.java
git commit -m "feat(java-sdk): implement full 5-step canonical evaluation pipeline in EvaluationEngine"
```

---

## Phase 5 — Contract Vector Verification

### Task 8: Vector-harness test loading test-contract/vectors.json

**Files:**
- Create: `packages/sdks/flagmind-java/src/test/java/io/tombstone/ContractVectorsTest.java`

**Interfaces:**
- Consumes: `EvaluationEngine.evaluate` (Task 7), `RuleMatcher.matchRules` (Task 5), `PrerequisiteChecker.checkAll` (Task 6), Jackson (`com.fasterxml.jackson.databind.ObjectMapper`, already a dependency per `build.gradle:14`).
- Produces: nothing consumed elsewhere — this is the terminal verification task for Java parity.

- [ ] **Step 1: Write the vector-harness test**

```java
// packages/sdks/flagmind-java/src/test/java/io/tombstone/ContractVectorsTest.java
package io.tombstone;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import io.tombstone.evaluation.EvaluationEngine;
import io.tombstone.evaluation.PrerequisiteChecker;
import io.tombstone.evaluation.RuleMatcher;
import io.tombstone.types.*;
import org.junit.jupiter.api.DynamicTest;
import org.junit.jupiter.api.TestFactory;

import java.io.File;
import java.util.*;
import java.util.function.Function;
import java.util.stream.Stream;

import static org.junit.jupiter.api.Assertions.*;

/** Loads packages/sdks/test-contract/vectors.json and asserts the Java SDK's
 *  evaluation logic matches every vector. This is the executable definition
 *  of "parity" for this SDK — see docs/SDK_CONTRACT.md. */
public class ContractVectorsTest {

    private static final EvaluationEngine ENGINE = new EvaluationEngine();
    private static JsonNode vectors;

    private static JsonNode loadVectors() throws Exception {
        if (vectors == null) {
            var mapper = new ObjectMapper();
            var file = new File("../test-contract/vectors.json");
            vectors = mapper.readTree(file);
        }
        return vectors;
    }

    @TestFactory
    Stream<DynamicTest> hashVectors() throws Exception {
        var root = loadVectors();
        var list = new ArrayList<DynamicTest>();
        for (JsonNode v : root.get("vectors")) {
            String flagKey = v.get("flag_key").asText();
            String userId = v.get("user_id").asText();
            int hashVersion = v.get("hash_version").asInt();
            int rolloutPct = v.get("rollout_pct").asInt();
            boolean expected = v.get("expected_in_cohort").asBoolean();

            list.add(DynamicTest.dynamicTest(
                flagKey + "/" + userId + "/v" + hashVersion + "/" + rolloutPct + "%",
                () -> {
                    var flag = new FlagEnvironmentState(
                        "id", flagKey, "test", true, rolloutPct, "false", 0L,
                        List.of(), List.of(), List.of(), hashVersion
                    );
                    var context = new EvaluationContext(userId, "", Map.of());
                    var result = ENGINE.evaluate(flag, context, false, flagKey);
                    assertEquals(expected, (Boolean) result.value(),
                        "hash vector mismatch for " + flagKey + "/" + userId);
                }
            ));
        }
        return list.stream();
    }

    @TestFactory
    Stream<DynamicTest> prerequisiteVectors() throws Exception {
        var root = loadVectors();
        var list = new ArrayList<DynamicTest>();
        for (JsonNode v : root.get("prerequisite_vectors")) {
            String id = v.get("id").asText();
            var prereqNode = v.get("prerequisite");
            var prereq = new FlagPrerequisite(
                prereqNode.get("flag_key").asText(),
                prereqNode.get("required_variation").asText(),
                prereqNode.get("gate").asBoolean()
            );
            boolean expectedSatisfied = v.get("expected_satisfied").asBoolean();

            var allFlagsNode = v.get("all_flags");

            Set<String> seenKeys = new HashSet<>();
            if (v.has("seen_keys")) {
                v.get("seen_keys").forEach(k -> seenKeys.add(k.asText()));
            }

            // Lookup function: each "all_flags" entry is {"enabled": bool, "variation": "true"|"false"}.
            // enabled=false always resolves via the engine's OFF branch regardless of rolloutPct;
            // enabled=true with rolloutPct=100 always resolves via the FALLTHROUGH branch to true —
            // together these two shapes are sufficient to make the dependency evaluate to exactly
            // the vector's declared "variation" string, since this release has no non-boolean
            // variation type (see design spec Section 3's prerequisite-comparison note).
            Function<String, FlagEnvironmentState> lookup = key -> {
                if (!allFlagsNode.has(key)) return null;
                var fn = allFlagsNode.get(key);
                boolean enabled = fn.get("enabled").asBoolean();
                String variation = fn.get("variation").asText();
                int rolloutPct = "true".equals(variation) ? 100 : 0;
                return new FlagEnvironmentState(
                    "id", key, "test", enabled, rolloutPct, "false", 0L,
                    List.of(), List.of(), List.of(), 1
                );
            };

            list.add(DynamicTest.dynamicTest(id, () -> {
                boolean satisfied = PrerequisiteChecker.checkAll(
                    List.of(prereq), lookup, new HashMap<>(), seenKeys, "parent-flag", ENGINE,
                    new EvaluationContext("u1", "", Map.of())
                );
                assertEquals(expectedSatisfied, satisfied, "prerequisite vector mismatch for " + id);
            }));
        }
        return list.stream();
    }

    @TestFactory
    Stream<DynamicTest> ruleVectors() throws Exception {
        var root = loadVectors();
        var list = new ArrayList<DynamicTest>();
        for (JsonNode v : root.get("rule_vectors")) {
            String id = v.get("id").asText();
            var rulesNode = v.get("rules");
            List<TargetingRule> rules = new ArrayList<>();
            for (JsonNode r : rulesNode) {
                List<PropertyCondition> conditions = new ArrayList<>();
                for (JsonNode c : r.get("conditions")) {
                    List<String> values = new ArrayList<>();
                    c.get("values").forEach(val -> values.add(val.asText()));
                    conditions.add(new PropertyCondition(
                        c.get("attribute").asText(), c.get("operator").asText(), values, c.get("negate").asBoolean()));
                }
                rules.add(new TargetingRule(
                    r.get("id").asText(), conditions, r.get("rollout_pct").asDouble(),
                    r.get("variation").asText(), r.get("priority").asInt()));
            }

            Map<String, String> attrs = new HashMap<>();
            var attrsNode = v.get("attrs");
            attrsNode.fields().forEachRemaining(e -> attrs.put(e.getKey(), e.getValue().asText()));
            String userId = attrs.getOrDefault("user_id", "");

            var expectedNode = v.get("expected_result");

            list.add(DynamicTest.dynamicTest(id, () -> {
                var context = new EvaluationContext(userId, "", attrs);
                var result = RuleMatcher.matchRules(rules, context, "test-flag");
                if (expectedNode == null || expectedNode.isNull()) {
                    assertTrue(result.isEmpty(), "expected no rule match for " + id);
                } else {
                    assertTrue(result.isPresent(), "expected a rule match for " + id);
                    assertEquals(expectedNode.get("variation").asText(), result.get());
                }
            }));
        }
        return list.stream();
    }

    @TestFactory
    Stream<DynamicTest> missingAttributeVectors() throws Exception {
        var root = loadVectors();
        var list = new ArrayList<DynamicTest>();
        // Same structure as rule_vectors — reuses the missing_attribute_vectors array,
        // which has a single rule with a missing-attribute condition and no fallback rule.
        for (JsonNode v : root.get("missing_attribute_vectors")) {
            String id = v.get("id").asText();
            var expectedNode = v.get("expected_result");
            list.add(DynamicTest.dynamicTest(id, () -> {
                var condition = new PropertyCondition("missing_attr", "eq", List.of("x"), false);
                var rule = new TargetingRule("r1", List.of(condition), 100.0, "skipped", 0);
                var context = new EvaluationContext("u1", "", Map.of());
                var result = RuleMatcher.matchRules(List.of(rule), context, "test-flag");
                assertTrue((expectedNode == null || expectedNode.isNull()) == result.isEmpty(),
                    "missing-attribute vector mismatch for " + id);
            }));
        }
        return list.stream();
    }
}
```

- [ ] **Step 2: Run the contract vector tests**

Run: `cd packages/sdks/flagmind-java && gradle test --tests "io.tombstone.ContractVectorsTest"`
Expected: PASS — all dynamic tests generated from `vectors.json` (24 hash + 7 prerequisite + 14 rule + 1 missing-attribute = 46 dynamic tests) pass.

- [ ] **Step 3: If any vector fails, diagnose before adjusting anything**

If a hash vector fails: re-check `isInRolloutMurmur3`/`isInRolloutFnv` byte-for-byte against
Section 3 of the design spec — do NOT adjust the vector, the vector is ground truth (Phase 1 of
the overall upgrade already verified it against a hand-tested oracle). If a rule/prerequisite
vector fails: re-check `RuleMatcher`/`PrerequisiteChecker` against `docs/SDK_CONTRACT.md`'s
Canonical Model table — the bug is almost certainly in this SDK's Java code, not the vector.

- [ ] **Step 4: Run the full Java test suite one final time**

Run: `cd packages/sdks/flagmind-java && gradle test`
Expected: PASS (43 unit tests + 46 dynamic contract-vector tests = 89 total).

- [ ] **Step 5: Commit**

```bash
git add packages/sdks/flagmind-java/src/test/java/io/tombstone/ContractVectorsTest.java
git commit -m "test(java-sdk): add contract-vector harness verifying parity against test-contract/vectors.json"
```

---

## Phase 6 — Naming Cleanup

### Task 9: Standardize Maven artifactId

**Files:**
- Modify: `packages/sdks/flagmind-java/build.gradle`

**Interfaces:**
- Consumes: nothing.
- Produces: nothing consumed by code — this changes only the published Maven coordinate.

- [ ] **Step 1: Change the Maven artifactId**

```groovy
// packages/sdks/flagmind-java/build.gradle
// Change line 31 from:
//     artifactId = 'flagmind-java'
// to:
    artifactId = 'tombstone-java-sdk'
```

Note: the Gradle `group = "io.tombstone"` (line 5) already matches the product name and is
unchanged. The directory `packages/sdks/flagmind-java/` is NOT renamed in this task — a
directory rename affects every other task's file paths in this plan; if desired, do it as a
separate follow-up PR after this plan's Phase 1-5 work is merged, to avoid churn mid-implementation.

- [ ] **Step 2: Verify the build still configures correctly**

Run: `cd packages/sdks/flagmind-java && gradle build -x test`
Expected: `BUILD SUCCESSFUL` — publishing configuration parses correctly with the new artifactId.

- [ ] **Step 3: Commit**

```bash
git add packages/sdks/flagmind-java/build.gradle
git commit -m "chore(java-sdk): rename Maven artifactId flagmind-java -> tombstone-java-sdk"
```

---

## Phase 7 — PR

### Task 10: Open PR to develop

**Files:** none (GitHub operation only)

- [ ] **Step 1: Run the full test suite one final time before pushing**

Run: `cd packages/sdks/flagmind-java && gradle test`
Expected: PASS (89 total tests).

- [ ] **Step 2: Push the branch**

```bash
git push -u origin feat/java-sdk-parity-v1.5.0
```

- [ ] **Step 3: Open the PR**

```bash
gh pr create --base develop --title "feat(java-sdk): bring flagmind-java to full 5-step evaluation parity" --body "$(cat <<'EOF'
## Summary
- Implements steps 2-4 of the canonical evaluation pipeline (prerequisites with cycle detection + memoization, target list, priority-sorted rule matching with full operator surface including semver/date/geo, per-rule rollout sub-bucketing) plus hashVersion=2 (FNV-1a).
- Removes the dead `List<Object> rules` parameter from `EvaluationEngine.evaluate` (was never read).
- Standardizes Maven artifactId to `tombstone-java-sdk` (was `flagmind-java`, inconsistent with the `io.tombstone` Gradle group).
- Verified against `test-contract/vectors.json` v1.2 (46 dynamic contract-vector tests, all passing).

Phase 2 of the v1.5.0 upgrade. See docs/superpowers/specs/2026-08-04-v1.5.0-sdk-parity-and-dependency-viz-design.md.

## Test plan
- [x] 43 unit tests across types, RuleMatcher, PrerequisiteChecker, EvaluationEngine
- [x] 46 dynamic contract-vector tests loading test-contract/vectors.json
- [x] Existing 6 pre-upgrade tests updated to new signature, still passing (no regression)
EOF
)"
```

- [ ] **Step 4: Report the PR URL to the user and stop — do not merge**

Per this repo's established workflow, PR merges are done by the user, not automatically.
