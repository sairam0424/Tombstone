namespace Tombstone.Tests;
using Xunit;

public class EvaluationEngineTests
{
    private readonly EvaluationEngine _engine = new();
    private static FlagEnvironmentState Flag(bool enabled, int pct) =>
        new("id1", "test-flag", "test", enabled, pct, "false", 0L);
    private static EvaluationContext Ctx(string userId = "user-abc-123") => EvaluationContext.Of(userId);

    [Fact] public void DisabledFlag_ReturnsOff() {
        var r = _engine.Evaluate(Flag(false, 100), Ctx(), false, "test-flag");
        Assert.Equal(EvaluationReason.Off, r.Reason);
        Assert.False(r.Value);
    }

    [Fact] public void FullRollout_ReturnsTrue() {
        var r = _engine.Evaluate(Flag(true, 100), Ctx(), false, "test-flag");
        Assert.Equal(EvaluationReason.Fallthrough, r.Reason);
        Assert.True(r.Value);
    }

    [Fact] public void ZeroRollout_ReturnsFalse() {
        var r = _engine.Evaluate(Flag(true, 0), Ctx(), false, "test-flag");
        Assert.False(r.Value);
    }

    [Fact] public void NullFlag_ReturnsError() {
        var r = _engine.Evaluate(null, Ctx(), false, "missing");
        Assert.Equal(EvaluationReason.Error, r.Reason);
    }

    [Theory]
    [InlineData("checkout-v2", "user-abc-123", 100, true)]
    [InlineData("checkout-v2", "user-abc-123", 0, false)]
    [InlineData("checkout-v2", "user-xyz-789", 50, false)]
    public void MurmurHash3_ParityWithTypeScript(string flagKey, string userId, int pct, bool expected) {
        var r = _engine.Evaluate(Flag(true, pct), EvaluationContext.Of(userId), false, flagKey);
        Assert.Equal(expected, r.Value);
    }

    [Fact] public void PrerequisiteHardGateBlocksEvaluation() {
        var baseFlag = Flag(false, 0);
        var prereq = new FlagPrerequisite("base-flag", "true", true);
        var parentFlag = new FlagEnvironmentState(
            "id-1", "parent-flag", "test", true, 100, "false", 0L,
            new List<FlagPrerequisite> { prereq }, new List<TargetingRule>(), new List<string>(), 1
        );
        Func<string, FlagEnvironmentState?> lookup = key => key == "base-flag" ? baseFlag : null;

        var result = _engine.Evaluate(parentFlag, Ctx(), false, "parent-flag", lookup, new(), new());

        Assert.Equal(EvaluationReason.PrerequisiteFailed, result.Reason);
        Assert.False(result.Value);
    }

    [Fact] public void TargetListMatchReturnsTrue() {
        var flag = new FlagEnvironmentState(
            "id-1", "test-flag", "test", true, 0, "false", 0L,
            new List<FlagPrerequisite>(), new List<TargetingRule>(), new List<string> { "user-abc-123" }, 1
        );

        var result = _engine.Evaluate(flag, Ctx(), false, "test-flag");

        Assert.Equal(EvaluationReason.TargetMatch, result.Reason);
        Assert.True(result.Value);
    }

    [Fact] public void RuleMatchReturnsRuleVariation() {
        var condition = new PropertyCondition("plan", "eq", new List<string> { "pro" }, false);
        var rule = new TargetingRule("r1", new List<PropertyCondition> { condition }, 100.0, "matched-variation", 0);
        var flag = new FlagEnvironmentState(
            "id-1", "test-flag", "test", true, 0, "false", 0L,
            new List<FlagPrerequisite>(), new List<TargetingRule> { rule }, new List<string>(), 1
        );
        var proContext = new EvaluationContext("u1", "", new() { ["plan"] = "pro" });

        var result = _engine.Evaluate(flag, proContext, "default-value", "test-flag");

        Assert.Equal(EvaluationReason.RuleMatch, result.Reason);
        Assert.Equal("matched-variation", result.Value);
    }

    [Fact] public void HashVersion2UsesFnv1a() {
        // Vector from test-contract/vectors.json: checkout-v2/user-abc-123, v2, expected_bucket=0.343.
        // rollout_pct=30 -> bucket 0.343 >= 0.30 -> NOT in cohort -> default returned.
        var flag = new FlagEnvironmentState(
            "id-1", "checkout-v2", "test", true, 30, "false", 0L,
            new List<FlagPrerequisite>(), new List<TargetingRule>(), new List<string>(), 2
        );
        var context = EvaluationContext.Of("user-abc-123");

        var result = _engine.Evaluate(flag, context, false, "checkout-v2");

        Assert.False(result.Value);
        Assert.Equal(EvaluationReason.Fallthrough, result.Reason);
    }
}
