package io.tombstone.evaluation;

import com.google.common.hash.Hashing;
import java.nio.charset.StandardCharsets;
import java.util.List;
import io.tombstone.types.*;

public class EvaluationEngine {

    public <T> EvaluationResult<T> evaluate(
        FlagEnvironmentState flagState,
        List<Object> rules,
        EvaluationContext context,
        T defaultValue,
        String flagKey
    ) {
        if (flagState == null) {
            return new EvaluationResult<>(defaultValue, EvaluationReason.ERROR, false, flagKey);
        }
        if (!flagState.enabled()) {
            return new EvaluationResult<>(defaultValue, EvaluationReason.OFF, true, flagKey);
        }
        if (flagState.rolloutPct() >= 100) {
            return new EvaluationResult<>(castEnabled(defaultValue), EvaluationReason.FALLTHROUGH, true, flagKey);
        }
        if (flagState.rolloutPct() <= 0) {
            return new EvaluationResult<>(defaultValue, EvaluationReason.FALLTHROUGH, true, flagKey);
        }
        if (isInRollout(flagKey, context.userId(), flagState.rolloutPct())) {
            return new EvaluationResult<>(castEnabled(defaultValue), EvaluationReason.FALLTHROUGH, true, flagKey);
        }
        return new EvaluationResult<>(defaultValue, EvaluationReason.FALLTHROUGH, true, flagKey);
    }

    // CRITICAL: Uses MurmurHash3 unsigned 32-bit to match TypeScript and Python SDKs.
    // TypeScript: murmurhash.v3(flagKey + userId) >>> 0 % 100
    // Python: mmh3.hash(flag_key + user_id, seed=0, signed=False) % 100
    private boolean isInRollout(String flagKey, String userId, int rolloutPct) {
        int hash = Hashing.murmur3_32_fixed()
            .hashString(flagKey + userId, StandardCharsets.UTF_8)
            .asInt();
        int bucket = Integer.remainderUnsigned(hash, 100);
        return bucket < rolloutPct;
    }

    @SuppressWarnings("unchecked")
    private <T> T castEnabled(T defaultValue) {
        if (defaultValue instanceof Boolean) return (T) Boolean.TRUE;
        return defaultValue;
    }
}
