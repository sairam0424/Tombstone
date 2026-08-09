using System.Text.RegularExpressions;
using System.Globalization;

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
        var op = NormalizeOperator(condition.Operator);
        var values = condition.Values;
        var isGeo = GeoAttributes.Contains(condition.Attribute);

        bool result = op switch
        {
            "eq" or "in" => isGeo ? ContainsIgnoreCase(values, attrVal) : values.Contains(attrVal),
            "neq" or "nin" => isGeo ? !ContainsIgnoreCase(values, attrVal) : !values.Contains(attrVal),
            "contains" => AnyContainsIgnoreCase(values, attrVal),
            "startswith" => AnyStartsWithIgnoreCase(values, attrVal),
            "endswith" => AnyEndsWithIgnoreCase(values, attrVal),
            "gt" or "gte" or "lt" or "lte" => EvaluateNumeric(op, attrVal, values, condition.Attribute),
            "semver_gt" or "semver_gte" or "semver_lt" or "semver_lte" or "semver_eq"
                => EvaluateSemver(op, attrVal, values, condition.Attribute),
            "date_before" or "date_after"
                => EvaluateDate(op, attrVal, values, condition.Attribute),
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
}
