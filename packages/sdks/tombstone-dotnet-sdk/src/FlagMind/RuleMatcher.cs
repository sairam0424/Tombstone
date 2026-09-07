using System.Text.RegularExpressions;
using System.Globalization;
using Murmur;
using System.Text;

namespace Tombstone;

public static class RuleMatcher
{
    private static readonly HashSet<string> GeoAttributes = new() { "geo.country", "geo.region" };

    /// <summary>
    /// Canonical model: attribute resolution over the flat Attrs dictionary. This
    /// release's EvaluationContext.Attrs is Dictionary&lt;string,string&gt;, so
    /// multi-segment paths like "geo.country" resolve as literal flat keys — the
    /// wire format flattens nested JSON before populating Attrs, matching the
    /// same convention used by the Java/Ruby ports of this canonical model.
    /// </summary>
    public static object? ResolveAttribute(string attribute, EvaluationContext context)
    {
        if (attribute == "user_id") return context.UserId;
        if (attribute == "org_id") return context.OrgId;
        return context.AttrsOrEmpty.TryGetValue(attribute, out var value) ? value : null;
    }

    public static bool EvaluateCondition(PropertyCondition condition, EvaluationContext context)
    {
        var raw = ResolveAttribute(condition.Attribute, context);
        if (raw is null)
            throw new InconclusiveMatchException(
                $"Attribute '{condition.Attribute}' not present in evaluation context");

        var attrVal = raw.ToString() ?? "";
        var rawOp = condition.Operator.ToLowerInvariant();
        var op = NormalizeOperator(condition.Operator);
        var values = condition.Values;
        // isGeo is true whenever EITHER the attribute is a recognized geo
        // path OR the operator itself declares geo semantics (GEO_COUNTRY/
        // GEO_REGION) -- checking the attribute name ALONE would mean a rule
        // using a non-canonical attribute (e.g. "country" instead of
        // "geo.country") with a real GEO_COUNTRY operator silently falls
        // back to case-SENSITIVE matching, even though nothing (backend or
        // SDK) validates that operator=GEO_COUNTRY implies
        // attribute=="geo.country". Found and fixed identically in the
        // Java/Ruby SDKs' own EvaluateCondition (PRs #247/#248); checked
        // here proactively.
        var isGeo = GeoAttributes.Contains(condition.Attribute) ||
            rawOp is "geo_country" or "geo_region";

        bool result = op switch
        {
            "eq" or "in" => isGeo ? ContainsIgnoreCase(values, attrVal) : values.Contains(attrVal),
            // An EMPTY values list must never match "neq"/"nin":
            // !values.Contains(x) on an empty list is vacuously true (there
            // is nothing to find, so "not found" is trivially true), which
            // would make a rule with an empty/missing "values" list match
            // EVERY context unconditionally -- the opposite of "no
            // exclusions configured, so exclude nothing". Same bug class
            // found and fixed in the TypeScript/Java/Ruby SDKs' own NOT_IN/
            // "neq"/"nin" branches (PRs #246/#247/#248); fixed here
            // proactively per those findings' own explicit note that the
            // remaining SDKs likely share it.
            "neq" or "nin" => values.Count > 0 &&
                (isGeo ? !ContainsIgnoreCase(values, attrVal) : !values.Contains(attrVal)),
            "contains" => AnyContainsIgnoreCase(values, attrVal),
            "startswith" => AnyStartsWithIgnoreCase(values, attrVal),
            "endswith" => AnyEndsWithIgnoreCase(values, attrVal),
            "gt" or "gte" or "lt" or "lte" => EvaluateNumeric(op, attrVal, values, condition.Attribute),
            "semver_gt" or "semver_gte" or "semver_lt" or "semver_lte" or "semver_eq"
                => EvaluateSemver(op, attrVal, values, condition.Attribute),
            "date_before" or "date_after"
                => EvaluateDate(op, attrVal, values, condition.Attribute),
            // docs/SDK_CONTRACT.md:32 -- REGEX is declared (a real, distinct
            // operator value in flag-api's targeting_rules.operator CHECK
            // constraint) but deliberately NOT IMPLEMENTED in this release,
            // across all 5 SDKs (parity matrix: "No" for every language) --
            // matching TypeScript's own default:false behavior. Returning a
            // definite false (not throwing) matters specifically for
            // Negate=true: a thrown exception would skip the whole rule
            // regardless of Negate, while the contract's literal
            // "false, negated -> true" semantics require a definite result
            // here. Does NOT implement real regex matching, which stays
            // deliberately deferred ("Future work") for cross-SDK parity.
            "regex" => false,
            _ => throw new InconclusiveMatchException($"Unknown operator: '{op}'"),
        };

        return condition.Negate ? !result : result;
    }

    private static string NormalizeOperator(string operatorName)
    {
        var op = operatorName.ToLowerInvariant();
        return op switch
        {
            "not_in" => "nin",
            "prefix" => "startswith",
            "suffix" => "endswith",
            // flag-api's targeting_rules.operator CHECK constraint
            // (schema.sql) has GEO_COUNTRY/GEO_REGION as real, distinct
            // operator VALUES (not just an attribute-name convention) --
            // without this mapping, a targeting rule using either would hit
            // the switch's default branch and throw
            // InconclusiveMatchException on every evaluation, silently
            // never matching for any user. Mapped to "in" so it falls into
            // the existing "eq"/"in" branch, whose isGeo case-insensitive
            // comparison already implements the real geo-matching semantics
            // correctly. Found while wiring the real backend wire format
            // into this SDK for the first time -- the identical gap the
            // Java/Ruby SDKs' own NormalizeOperator needed (PRs #247/#248).
            "geo_country" or "geo_region" => "in",
            _ => op,
        };
    }

    private static bool ContainsIgnoreCase(List<string> values, string attrVal)
    {
        var upper = attrVal.ToUpperInvariant();
        return values.Any(v => v.ToUpperInvariant() == upper);
    }

    private static bool AnyContainsIgnoreCase(List<string> values, string attrVal)
    {
        var upperAttr = attrVal.ToUpperInvariant();
        return values.Any(v => upperAttr.Contains(v.ToUpperInvariant()));
    }

    private static bool AnyStartsWithIgnoreCase(List<string> values, string attrVal)
    {
        var upperAttr = attrVal.ToUpperInvariant();
        return values.Any(v => upperAttr.StartsWith(v.ToUpperInvariant(), StringComparison.Ordinal));
    }

    private static bool AnyEndsWithIgnoreCase(List<string> values, string attrVal)
    {
        var upperAttr = attrVal.ToUpperInvariant();
        return values.Any(v => upperAttr.EndsWith(v.ToUpperInvariant(), StringComparison.Ordinal));
    }

    // Uses invariant culture (not the current thread's culture) so decimal-separator
    // parsing is identical on every machine regardless of OS locale — matching
    // Java's Double.parseDouble and Ruby's Float(), which are always invariant.
    private static bool EvaluateNumeric(string op, string attrVal, List<string> values, string attribute)
    {
        var style = System.Globalization.NumberStyles.Float;
        var culture = System.Globalization.CultureInfo.InvariantCulture;
        if (values.Count == 0
            || !double.TryParse(attrVal, style, culture, out var nAttr)
            || !double.TryParse(values[0], style, culture, out var nVal))
            throw new InconclusiveMatchException($"Numeric cast failed for '{attribute}'");

        return op switch
        {
            "gt" => nAttr > nVal,
            "gte" => nAttr >= nVal,
            "lt" => nAttr < nVal,
            "lte" => nAttr <= nVal,
            _ => false,
        };
    }

    private static readonly Regex LeadingVOrBuildMetadata = new(@"(^v|\+.*$)", RegexOptions.Compiled);
    private static readonly Regex PureDigits = new(@"^\d+$", RegexOptions.Compiled);

    /// <summary>Ported byte-for-byte from flagmind-python's matching.py:27-39 (GrowthBook pattern).</summary>
    public static string PaddedVersion(string v)
    {
        v = LeadingVOrBuildMetadata.Replace(v, "");
        var parts = v.Split('-', '.');
        var padded = parts.Select(p => PureDigits.IsMatch(p) ? p.PadLeft(5, ' ') : p).ToList();
        if (padded.Count == 3) padded.Add("~");
        return string.Join(".", padded);
    }

    private static bool EvaluateSemver(string op, string attrVal, List<string> values, string attribute)
    {
        if (values.Count == 0)
            throw new InconclusiveMatchException($"semver operator requires at least one value for '{attribute}'");

        var a = PaddedVersion(attrVal);
        var b = PaddedVersion(values[0]);
        var cmp = string.CompareOrdinal(a, b);

        return op switch
        {
            "semver_gt" => cmp > 0,
            "semver_gte" => cmp >= 0,
            "semver_lt" => cmp < 0,
            "semver_lte" => cmp <= 0,
            "semver_eq" => cmp == 0,
            _ => false,
        };
    }

    private static bool EvaluateDate(string op, string attrVal, List<string> values, string attribute)
    {
        if (values.Count == 0
            || !DateTimeOffset.TryParse(NormalizeIso8601(attrVal), CultureInfo.InvariantCulture, DateTimeStyles.None, out var dtAttr)
            || !DateTimeOffset.TryParse(NormalizeIso8601(values[0]), CultureInfo.InvariantCulture, DateTimeStyles.None, out var dtVal))
            throw new InconclusiveMatchException($"Date parse failed for '{attribute}'");

        return op == "date_before" ? dtAttr < dtVal : dtAttr > dtVal;
    }

    private static string NormalizeIso8601(string s) => s.Replace("Z", "+00:00");

    /// <summary>
    /// Canonical model: priority-ascending sort (0 = highest), multi-condition AND
    /// per rule, per-rule rollout sub-bucketing (matched conditions but bucket
    /// outside this rule's own RolloutPct falls to the NEXT rule, not Step 5).
    /// </summary>
    public static string? MatchRules(List<TargetingRule> rules, EvaluationContext context, string flagKey)
    {
        var sorted = rules.OrderBy(r => r.Priority).ToList();

        foreach (var rule in sorted)
        {
            bool allMatch;
            try
            {
                allMatch = rule.Conditions.All(c => EvaluateCondition(c, context));
            }
            catch (InconclusiveMatchException)
            {
                continue; // rule inconclusive — try next rule
            }

            if (!allMatch) continue;

            var bucket = Murmur3Bucket(flagKey, context.UserId);
            if (bucket < rule.RolloutPct) return rule.Variation;

            // conditions matched but outside this rule's own rollout — try next rule
        }
        return null;
    }

    private static uint Murmur3Bucket(string flagKey, string userId)
    {
        var hasher = MurmurHash.Create32(seed: 0, managed: true);
        var bytes = Encoding.UTF8.GetBytes(flagKey + userId);
        var hash = hasher.ComputeHash(bytes);
        return BitConverter.ToUInt32(hash, 0) % 100;
    }
}
