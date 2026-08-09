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
