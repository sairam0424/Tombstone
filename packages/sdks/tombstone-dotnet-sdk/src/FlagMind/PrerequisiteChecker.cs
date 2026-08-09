namespace Tombstone;

public static class PrerequisiteChecker
{
    /// <summary>
    /// Canonical model: string-compare mechanism against the dependency's
    /// stringified boolean outcome (forward-compatible with future
    /// multivariate prerequisites — see design spec Section 3). Cycle
    /// detection via explicit seen-set (Python's approach); memoization
    /// via cache dictionary keyed by dependency flag key (Python's approach).
    /// </summary>
    public static bool CheckAll(
        List<FlagPrerequisite> prerequisites,
        Func<string, FlagEnvironmentState?> flagLookup,
        Dictionary<string, string?> cache,
        HashSet<string> seen,
        string currentFlagKey,
        EvaluationEngine engine,
        EvaluationContext context)
    {
        var chainSeen = new HashSet<string>(seen) { currentFlagKey };

        foreach (var prereq in prerequisites)
        {
            var depKey = prereq.FlagKey;
            string? depVariation;

            if (cache.TryGetValue(depKey, out var cached))
            {
                depVariation = cached;
            }
            else if (chainSeen.Contains(depKey))
            {
                continue; // cycle detected — fail open, skip this one prerequisite
            }
            else
            {
                var depFlag = flagLookup(depKey);
                if (depFlag is null)
                {
                    depVariation = null;
                }
                else
                {
                    var depResult = engine.Evaluate(depFlag, context, false, depKey, flagLookup, cache, chainSeen);
                    // Lowercase to match Java/Ruby/Python's "true"/"false" string
                    // convention — C#'s bool.ToString() is PascalCase ("True"/"False")
                    // by default, which would silently break every prerequisite
                    // comparison against a RequiredVariation of "true".
                    depVariation = depResult.Value.ToString()?.ToLowerInvariant();
                }
                cache[depKey] = depVariation;
            }

            if (depVariation != prereq.RequiredVariation)
            {
                if (!prereq.Gate) continue; // soft — unmet but non-blocking
                return false; // hard gate — block entire parent flag
            }
        }
        return true;
    }
}
