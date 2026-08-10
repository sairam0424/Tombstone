package io.tombstone.evaluation;

import io.tombstone.types.EvaluationContext;
import io.tombstone.types.PropertyCondition;
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
