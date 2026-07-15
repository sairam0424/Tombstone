package io.tombstone.types;
import java.util.Map;
import java.util.HashMap;

public record EvaluationContext(
    String userId,
    String orgId,
    Map<String, String> attrs
) {
    public static EvaluationContext of(String userId) {
        return new EvaluationContext(userId, "", new HashMap<>());
    }
    public static EvaluationContext of(String userId, String orgId) {
        return new EvaluationContext(userId, orgId, new HashMap<>());
    }
}
