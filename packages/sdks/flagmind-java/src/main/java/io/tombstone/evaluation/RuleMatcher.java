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
            case "semver_gt", "semver_gte", "semver_lt", "semver_lte", "semver_eq" ->
                result = evaluateSemver(op, attrVal, values, condition.attribute());
            case "date_before", "date_after" ->
                result = evaluateDate(op, attrVal, values, condition.attribute());
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
