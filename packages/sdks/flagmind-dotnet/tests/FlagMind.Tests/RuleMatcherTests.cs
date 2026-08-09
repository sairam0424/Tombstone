namespace Tombstone.Tests;
using Xunit;

public class RuleMatcherTests
{
    private static EvaluationContext Ctx(Dictionary<string, string> attrs) =>
        new("u1", "", attrs);

    [Fact] public void ResolveAttribute_FlatKey() {
        var context = Ctx(new() { ["plan"] = "pro" });
        Assert.Equal("pro", RuleMatcher.ResolveAttribute("plan", context));
    }

    [Fact] public void ResolveAttribute_MissingReturnsNull() {
        var context = Ctx(new());
        Assert.Null(RuleMatcher.ResolveAttribute("missing", context));
    }

    [Fact] public void EvaluateCondition_EqMatch() {
        var condition = new PropertyCondition("plan", "eq", new List<string> { "pro" }, false);
        Assert.True(RuleMatcher.EvaluateCondition(condition, Ctx(new() { ["plan"] = "pro" })));
    }

    [Fact] public void EvaluateCondition_EqNoMatch() {
        var condition = new PropertyCondition("plan", "eq", new List<string> { "pro" }, false);
        Assert.False(RuleMatcher.EvaluateCondition(condition, Ctx(new() { ["plan"] = "free" })));
    }

    [Fact] public void EvaluateCondition_ContainsCaseInsensitive() {
        var condition = new PropertyCondition("email", "contains", new List<string> { "ACME" }, false);
        Assert.True(RuleMatcher.EvaluateCondition(condition, Ctx(new() { ["email"] = "user@acme.com" })));
    }

    [Fact] public void EvaluateCondition_NumericGt() {
        var condition = new PropertyCondition("age", "gt", new List<string> { "18" }, false);
        Assert.True(RuleMatcher.EvaluateCondition(condition, Ctx(new() { ["age"] = "21" })));
    }

    [Fact] public void EvaluateCondition_NumericNonNumericThrows() {
        var condition = new PropertyCondition("age", "gt", new List<string> { "18" }, false);
        Assert.Throws<InconclusiveMatchException>(() =>
            RuleMatcher.EvaluateCondition(condition, Ctx(new() { ["age"] = "not-a-number" })));
    }

    [Fact] public void EvaluateCondition_MissingAttributeThrows() {
        var condition = new PropertyCondition("missing_attr", "eq", new List<string> { "x" }, false);
        Assert.Throws<InconclusiveMatchException>(() =>
            RuleMatcher.EvaluateCondition(condition, Ctx(new())));
    }

    [Fact] public void EvaluateCondition_NegateInverts() {
        var condition = new PropertyCondition("plan", "eq", new List<string> { "pro" }, true);
        Assert.False(RuleMatcher.EvaluateCondition(condition, Ctx(new() { ["plan"] = "pro" })));
    }

    [Fact] public void EvaluateCondition_GeoCaseInsensitive() {
        var condition = new PropertyCondition("geo.country", "in", new List<string> { "US", "CA" }, false);
        Assert.True(RuleMatcher.EvaluateCondition(condition, Ctx(new() { ["geo.country"] = "us" })));
    }
}
