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
