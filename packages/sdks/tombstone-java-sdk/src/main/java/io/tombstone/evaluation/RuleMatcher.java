package io.tombstone.evaluation;

import com.google.common.hash.Hashing;
import io.tombstone.types.EvaluationContext;
import io.tombstone.types.PropertyCondition;
import io.tombstone.types.TargetingRule;
import java.nio.charset.StandardCharsets;
import java.time.OffsetDateTime;
import java.time.format.DateTimeParseException;
import java.util.*;
import java.util.Comparator;
import java.util.Locale;
import java.util.Optional;
import java.util.regex.Pattern;

public class RuleMatcher {

    private static final Set<String> GEO_ATTRIBUTES = Set.of("geo.country", "geo.region");
    private static final Pattern LEADING_V_OR_BUILD_METADATA = Pattern.compile("(^v|\\+.*$)");
    private static final Pattern PURE_DIGITS = Pattern.compile("^\\d+$");

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
        String rawOp = condition.operator().toLowerCase(Locale.ROOT);
        String op = normalizeOperator(condition.operator());
        List<String> values = condition.values();
        // isGeo is true whenever EITHER the attribute is a recognized geo
        // path OR the operator itself declares geo semantics (GEO_COUNTRY/
        // GEO_REGION) -- checking the attribute name ALONE meant a rule
        // using a non-canonical attribute (e.g. "country" instead of
        // "geo.country") with a real GEO_COUNTRY operator silently fell
        // back to case-SENSITIVE matching, even though nothing (backend
        // or SDK) validates that operator=GEO_COUNTRY implies
        // attribute=="geo.country" -- flag-api's AddTargetingRuleRequest.
        // validate() checks operator validity and non-empty attribute but
        // never checks the two are paired correctly. Found by adversarial
        // review of PR #247.
        boolean isGeo = GEO_ATTRIBUTES.contains(condition.attribute())
            || "geo_country".equals(rawOp) || "geo_region".equals(rawOp);

        boolean result;
        switch (op) {
            case "eq", "in" -> result = isGeo
                ? containsIgnoreCase(values, attrVal)
                : values.contains(attrVal);
            // An EMPTY values list must never match "neq"/"nin": !values.contains(x)
            // on an empty list is vacuously true (there is nothing to find, so
            // "not found" is trivially true), which would make a rule with an
            // empty/missing "values" list match EVERY context for EVERY
            // attribute -- the opposite of "no exclusions configured, so
            // exclude nothing". Same bug class found and fixed in the
            // TypeScript SDK's evaluation.ts (adversarial review of PR #246);
            // checked here proactively per that finding's own explicit note
            // that the other 4 SDKs' evaluation engines likely share it.
            case "neq", "nin" -> result = !values.isEmpty() && (isGeo
                ? !containsIgnoreCase(values, attrVal)
                : !values.contains(attrVal));
            case "contains" -> result = anyContainsIgnoreCase(values, attrVal);
            case "startswith" -> result = anyStartsWithIgnoreCase(values, attrVal);
            case "endswith" -> result = anyEndsWithIgnoreCase(values, attrVal);
            case "gt", "gte", "lt", "lte" -> result = evaluateNumeric(op, attrVal, values, condition.attribute());
            case "semver_gt", "semver_gte", "semver_lt", "semver_lte", "semver_eq" ->
                result = evaluateSemver(op, attrVal, values, condition.attribute());
            case "date_before", "date_after" ->
                result = evaluateDate(op, attrVal, values, condition.attribute());
            // docs/SDK_CONTRACT.md:32 -- REGEX is declared (a real, distinct
            // operator value in flag-api's targeting_rules.operator CHECK
            // constraint) but deliberately NOT IMPLEMENTED in this release,
            // across all 5 SDKs (parity matrix: "No" for every language) --
            // matching TypeScript's own default:false behavior. Returning
            // a definite false (not throwing) matters specifically for
            // negate=true: a thrown exception would skip the whole rule
            // regardless of negate, while the contract's literal
            // "false, negated -> true" semantics require a definite
            // result here. Does NOT implement real regex matching, which
            // remains deliberately deferred ("Future work") for cross-SDK
            // parity. Found missing by adversarial review of the .NET
            // SDK's PR #249, which discovered Java's own switch had no
            // "regex" case and fell through to the default throw below,
            // diverging from the documented contract -- the .NET/Ruby
            // SDKs already had this fix; this closes the identical gap
            // here.
            case "regex" -> result = false;
            default -> throw new InconclusiveMatchException("Unknown operator: '" + op + "'");
        }
        return condition.negate() ? !result : result;
    }

    private static String normalizeOperator(String operator) {
        String op = operator.toLowerCase(Locale.ROOT);
        return switch (op) {
            case "not_in" -> "nin";
            case "prefix" -> "startswith";
            case "suffix" -> "endswith";
            // flag-api's targeting_rules.operator CHECK constraint (schema.sql)
            // has GEO_COUNTRY/GEO_REGION as real, distinct operator VALUES
            // (not just an attribute-name convention) -- without this mapping,
            // a targeting rule using either would hit the switch's default
            // branch below and throw InconclusiveMatchException on every
            // evaluation, silently never matching for any user. Mapped to
            // "in" so it falls into the existing case "eq","in" branch, whose
            // isGeo case-insensitive comparison (via GEO_ATTRIBUTES) already
            // implements the real geo-matching semantics correctly -- found
            // while wiring the real backend wire format into this SDK for the
            // first time (targeting_rules had zero real snapshot data before
            // this change, so this gap was never previously reachable).
            case "geo_country", "geo_region" -> "in";
            default -> op;
        };
    }

    private static boolean containsIgnoreCase(List<String> values, String attrVal) {
        String upper = attrVal.toUpperCase(Locale.ROOT);
        return values.stream().anyMatch(v -> v.toUpperCase(Locale.ROOT).equals(upper));
    }

    private static boolean anyContainsIgnoreCase(List<String> values, String attrVal) {
        String upperAttr = attrVal.toUpperCase(Locale.ROOT);
        return values.stream().anyMatch(v -> upperAttr.contains(v.toUpperCase(Locale.ROOT)));
    }

    private static boolean anyStartsWithIgnoreCase(List<String> values, String attrVal) {
        String upperAttr = attrVal.toUpperCase(Locale.ROOT);
        return values.stream().anyMatch(v -> upperAttr.startsWith(v.toUpperCase(Locale.ROOT)));
    }

    private static boolean anyEndsWithIgnoreCase(List<String> values, String attrVal) {
        String upperAttr = attrVal.toUpperCase(Locale.ROOT);
        return values.stream().anyMatch(v -> upperAttr.endsWith(v.toUpperCase(Locale.ROOT)));
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
}
