namespace Tombstone.Tests;
using System.Text.Json;
using Xunit;

/// <summary>
/// Loads packages/sdks/test-contract/vectors.json and asserts the .NET SDK's
/// evaluation logic matches every vector. This is the executable definition
/// of "parity" for this SDK — see docs/SDK_CONTRACT.md.
/// </summary>
public class ContractVectorsTests
{
    private static readonly EvaluationEngine Engine = new();

    private static JsonElement LoadVectors()
    {
        var path = Path.Combine(AppContext.BaseDirectory, "..", "..", "..", "..", "..", "..", "test-contract", "vectors.json");
        var json = File.ReadAllText(path);
        return JsonDocument.Parse(json).RootElement;
    }

    /// <summary>
    /// Flattens nested JSON objects in a vector's "attrs" fixture into dot-notation
    /// flat keys (e.g. {"geo": {"country": "us"}} -> "geo.country" -> "us"), matching
    /// this SDK's EvaluationContext.Attrs Dictionary&lt;string,string&gt; model.
    /// vectors.json's nested structure represents the wire format each SDK's client
    /// adapts to its own internal attrs representation — .NET's is flat by design
    /// (see RuleMatcher.ResolveAttribute), so this harness must flatten before
    /// constructing EvaluationContext, exactly like a real .NET client would when
    /// deserializing an incoming evaluation-context payload. Without this, JsonElement's
    /// GetString() throws InvalidOperationException on the "geo" object-kind property.
    /// </summary>
    private static void FlattenAttrs(string prefix, JsonElement node, Dictionary<string, string> outAttrs)
    {
        foreach (var prop in node.EnumerateObject())
        {
            var key = prefix.Length == 0 ? prop.Name : $"{prefix}.{prop.Name}";
            if (prop.Value.ValueKind == JsonValueKind.Object)
                FlattenAttrs(key, prop.Value, outAttrs);
            else
                outAttrs[key] = prop.Value.ValueKind == JsonValueKind.Null ? "" : prop.Value.ToString();
        }
    }

    public static IEnumerable<object[]> HashVectorData()
    {
        var root = LoadVectors();
        foreach (var v in root.GetProperty("vectors").EnumerateArray())
        {
            yield return new object[]
            {
                v.GetProperty("flag_key").GetString()!,
                v.GetProperty("user_id").GetString()!,
                v.GetProperty("hash_version").GetInt32(),
                v.GetProperty("rollout_pct").GetInt32(),
                v.GetProperty("expected_in_cohort").GetBoolean(),
            };
        }
    }

    [Theory]
    [MemberData(nameof(HashVectorData))]
    public void HashVectorsMatch(string flagKey, string userId, int hashVersion, int rolloutPct, bool expected)
    {
        var flag = new FlagEnvironmentState(
            "id", flagKey, "test", true, rolloutPct, "false", 0L,
            new List<FlagPrerequisite>(), new List<TargetingRule>(), new List<string>(), hashVersion
        );
        var context = new EvaluationContext(userId, "", new());
        var result = Engine.Evaluate(flag, context, false, flagKey);

        Assert.Equal(expected, result.Value);
    }

    public static IEnumerable<object[]> PrerequisiteVectorData()
    {
        var root = LoadVectors();
        foreach (var v in root.GetProperty("prerequisite_vectors").EnumerateArray())
        {
            yield return new object[] { v.GetProperty("id").GetString()!, v.Clone() };
        }
    }

    [Theory]
    [MemberData(nameof(PrerequisiteVectorData))]
    public void PrerequisiteVectorsMatch(string id, JsonElement v)
    {
        var prereqNode = v.GetProperty("prerequisite");
        var prereq = new FlagPrerequisite(
            prereqNode.GetProperty("flag_key").GetString()!,
            prereqNode.GetProperty("required_variation").GetString()!,
            prereqNode.GetProperty("gate").GetBoolean()
        );
        var expectedSatisfied = v.GetProperty("expected_satisfied").GetBoolean();
        var allFlagsNode = v.GetProperty("all_flags");

        var seenKeys = new HashSet<string>();
        if (v.TryGetProperty("seen_keys", out var seenNode))
            foreach (var k in seenNode.EnumerateArray()) seenKeys.Add(k.GetString()!);

        // Lookup: each "all_flags" entry is {"enabled": bool, "variation": "true"|"false"}.
        // enabled=false resolves via the engine's Off branch; enabled=true with
        // rollout_pct=100 resolves via Fallthrough to true — together these reproduce
        // exactly the vector's declared "variation" string (see Java plan's equivalent
        // for the same reasoning — no non-boolean variation type exists this release).
        FlagEnvironmentState? Lookup(string key)
        {
            if (!allFlagsNode.TryGetProperty(key, out var fn)) return null;
            var enabled = fn.GetProperty("enabled").GetBoolean();
            var variation = fn.GetProperty("variation").GetString();
            var rolloutPct = variation == "true" ? 100 : 0;
            return new FlagEnvironmentState(
                "id", key, "test", enabled, rolloutPct, "false", 0L,
                new List<FlagPrerequisite>(), new List<TargetingRule>(), new List<string>(), 1
            );
        }

        var satisfied = PrerequisiteChecker.CheckAll(
            new List<FlagPrerequisite> { prereq }, Lookup, new(), seenKeys, "parent-flag", Engine,
            new EvaluationContext("u1", "", new())
        );

        Assert.Equal(expectedSatisfied, satisfied);
    }

    public static IEnumerable<object[]> RuleVectorData()
    {
        var root = LoadVectors();
        foreach (var v in root.GetProperty("rule_vectors").EnumerateArray())
        {
            yield return new object[] { v.GetProperty("id").GetString()!, v.Clone() };
        }
    }

    [Theory]
    [MemberData(nameof(RuleVectorData))]
    public void RuleVectorsMatch(string id, JsonElement v)
    {
        var rules = new List<TargetingRule>();
        foreach (var r in v.GetProperty("rules").EnumerateArray())
        {
            var conditions = new List<PropertyCondition>();
            foreach (var c in r.GetProperty("conditions").EnumerateArray())
            {
                var values = c.GetProperty("values").EnumerateArray().Select(x => x.GetString()!).ToList();
                conditions.Add(new PropertyCondition(
                    c.GetProperty("attribute").GetString()!, c.GetProperty("operator").GetString()!,
                    values, c.GetProperty("negate").GetBoolean()));
            }
            rules.Add(new TargetingRule(
                r.GetProperty("id").GetString()!, conditions, r.GetProperty("rollout_pct").GetDouble(),
                r.GetProperty("variation").GetString()!, r.GetProperty("priority").GetInt32()));
        }

        var attrs = new Dictionary<string, string>();
        FlattenAttrs("", v.GetProperty("attrs"), attrs);
        var userId = attrs.TryGetValue("user_id", out var uid) ? uid : "";

        var expectedNode = v.TryGetProperty("expected_result", out var en) && en.ValueKind != JsonValueKind.Null ? en : (JsonElement?)null;

        var context = new EvaluationContext(userId, "", attrs);
        var result = RuleMatcher.MatchRules(rules, context, "test-flag");

        if (expectedNode is null)
        {
            Assert.Null(result);
        }
        else
        {
            Assert.NotNull(result);
            Assert.Equal(expectedNode.Value.GetProperty("variation").GetString(), result);
        }
    }

    public static IEnumerable<object[]> MissingAttributeVectorData()
    {
        var root = LoadVectors();
        foreach (var v in root.GetProperty("missing_attribute_vectors").EnumerateArray())
        {
            yield return new object[] { v.GetProperty("id").GetString()!, v.Clone() };
        }
    }

    [Theory]
    [MemberData(nameof(MissingAttributeVectorData))]
    public void MissingAttributeVectorsMatch(string id, JsonElement v)
    {
        var expectedNode = v.TryGetProperty("expected_result", out var en) && en.ValueKind != JsonValueKind.Null ? en : (JsonElement?)null;

        var condition = new PropertyCondition("missing_attr", "eq", new List<string> { "x" }, false);
        var rule = new TargetingRule("r1", new List<PropertyCondition> { condition }, 100.0, "skipped", 0);
        var context = new EvaluationContext("u1", "", new());
        var result = RuleMatcher.MatchRules(new List<TargetingRule> { rule }, context, "test-flag");

        Assert.Equal(expectedNode is null, result is null);
    }
}
