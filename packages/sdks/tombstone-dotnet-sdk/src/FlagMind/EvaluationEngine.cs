using Murmur;
using System.Text;

namespace Tombstone;

public class EvaluationEngine
{
    private const uint FnvOffset = 2166136261;
    private const uint FnvPrime = 16777619;

    /// <summary>
    /// Full 5-step canonical evaluation pipeline. See docs/SDK_CONTRACT.md for the
    /// normative spec this implements. flagLookup resolves other flags in the same
    /// snapshot for prerequisite evaluation (step 2); omit it (or pass null) if the
    /// caller has no snapshot access — prerequisites will then always be treated as
    /// missing, and hard-gated prerequisites will produce PrerequisiteFailed.
    /// </summary>
    public EvaluationResult<T> Evaluate<T>(
        FlagEnvironmentState? flagState,
        EvaluationContext context,
        T defaultValue,
        string flagKey,
        Func<string, FlagEnvironmentState?>? flagLookup = null,
        Dictionary<string, string?>? prerequisiteCache = null,
        HashSet<string>? seenKeys = null)
    {
        flagLookup ??= _ => null;
        prerequisiteCache ??= new();
        seenKeys ??= new();

        // Step 1: Preliminary checks
        if (flagState is null)
            return new(defaultValue, EvaluationReason.Error, false, flagKey);

        if (!flagState.Enabled)
            return new((T)ParseSafeDefault(flagState.SafeDefault, defaultValue)!, EvaluationReason.Off, true, flagKey);

        // Step 2: Prerequisites
        if (flagState.Prerequisites.Count > 0)
        {
            var satisfied = PrerequisiteChecker.CheckAll(
                flagState.Prerequisites, flagLookup, prerequisiteCache, seenKeys, flagKey, this, context);
            if (!satisfied)
                return new(defaultValue, EvaluationReason.PrerequisiteFailed, true, flagKey);
        }

        // Step 3: Individual target list
        if (flagState.TargetList.Count > 0 && flagState.TargetList.Contains(context.UserId))
            return new((T)(object)true, EvaluationReason.TargetMatch, true, flagKey);

        // Step 4: Priority-sorted rule matching
        if (flagState.TargetingRules.Count > 0)
        {
            var ruleMatch = RuleMatcher.MatchRules(flagState.TargetingRules, context, flagKey);
            if (ruleMatch is not null)
                return new((T)(object)ruleMatch, EvaluationReason.RuleMatch, true, flagKey);
        }

        // Step 5: Fallthrough rollout
        if (flagState.RolloutPct >= 100)
            return new(CastEnabled(defaultValue), EvaluationReason.Fallthrough, true, flagKey);
        if (flagState.RolloutPct <= 0)
            return new(defaultValue, EvaluationReason.Fallthrough, true, flagKey);

        var inRollout = flagState.HashVersion == 2
            ? IsInRolloutFnv(flagKey, context.UserId, flagState.RolloutPct)
            : IsInRolloutMurmur3(flagKey, context.UserId, flagState.RolloutPct);

        return inRollout
            ? new(CastEnabled(defaultValue), EvaluationReason.Fallthrough, true, flagKey)
            : new(defaultValue, EvaluationReason.Fallthrough, true, flagKey);
    }

    // MurmurHash3 unsigned 32-bit — matches TypeScript/Python/Java/Ruby SDKs
    private static bool IsInRolloutMurmur3(string flagKey, string userId, int rolloutPct)
    {
        var hasher = MurmurHash.Create32(seed: 0, managed: true);
        var bytes = Encoding.UTF8.GetBytes(flagKey + userId);
        var hash = hasher.ComputeHash(bytes);
        uint bucket = BitConverter.ToUInt32(hash, 0) % 100;
        return bucket < (uint)rolloutPct;
    }

    // Canonical hashVersion=2: double-pass FNV-1a, UTF-8 byte iteration, 10,000-bucket
    // resolution. Ported from flagmind-python's evaluation.py:27-48 (byte iteration,
    // not TS's UTF-16 code-unit iteration — canonical choice per design spec Section 3).
    // C#'s uint arithmetic wraps on overflow identically to the masked (& 0xFFFFFFFF)
    // arithmetic used in the Java/Ruby/Python ports — no explicit masking needed here.
    private static uint Fnv1aRaw(string s)
    {
        uint h = FnvOffset;
        foreach (var b in Encoding.UTF8.GetBytes(s))
        {
            h ^= b;
            h *= FnvPrime;
        }
        return h;
    }

    private static bool IsInRolloutFnv(string flagKey, string userId, int rolloutPct)
    {
        var h1 = Fnv1aRaw(flagKey + userId);
        var h2 = Fnv1aRaw(h1.ToString());
        var bucket = (h2 % 10000) / 10000.0;
        return bucket < (rolloutPct / 100.0);
    }

    /// <summary>
    /// Canonical model: OFF-path parses SafeDefault into the target type (TS's
    /// approach), falling back to the caller's defaultValue on parse failure
    /// or type mismatch.
    /// </summary>
    private static object? ParseSafeDefault<T>(string safeDefault, T fallback)
    {
        // Written as if/return rather than a switch expression: a switch-expression
        // arm mixing a parsed `double` with the generic `fallback` (typed `T`) fails
        // to compile — C# cannot unify an unconstrained `T` with `double` inside a
        // single ternary/switch arm. Returning `object?` from separate branches
        // sidesteps that unification requirement entirely.
        if (fallback is bool)
            return safeDefault == "true";

        if (fallback is int or long or double or float)
        {
            var parsed = double.TryParse(
                safeDefault, System.Globalization.NumberStyles.Float,
                System.Globalization.CultureInfo.InvariantCulture, out var n);
            return parsed ? n : fallback;
        }

        if (fallback is string)
            return safeDefault;

        return fallback;
    }

    private static T CastEnabled<T>(T defaultValue) =>
        defaultValue is bool ? (T)(object)true : defaultValue;
}
