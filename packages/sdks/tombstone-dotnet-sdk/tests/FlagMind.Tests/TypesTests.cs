namespace Tombstone.Tests;
using Xunit;

public class TypesTests
{
    [Fact] public void FlagEnvironmentState_ConstructsWithAllNewFields() {
        var prereq = new FlagPrerequisite("base-flag", "true", true);
        var condition = new PropertyCondition("plan", "eq", new List<string> { "pro" }, false);
        var rule = new TargetingRule("r1", new List<PropertyCondition> { condition }, 100.0, "matched", 0);

        var state = new FlagEnvironmentState(
            "id-1", "test-flag", "test", true, 50, "false", 0L,
            new List<FlagPrerequisite> { prereq },
            new List<TargetingRule> { rule },
            new List<string> { "user-1" },
            2
        );

        Assert.Equal(1, state.Prerequisites.Count);
        Assert.Equal("base-flag", state.Prerequisites[0].FlagKey);
        Assert.Equal(1, state.TargetingRules.Count);
        Assert.Equal("plan", state.TargetingRules[0].Conditions[0].Attribute);
        Assert.Equal(new List<string> { "user-1" }, state.TargetList);
        Assert.Equal(2, state.HashVersion);
    }

    [Fact] public void FlagEnvironmentState_DefaultsNewFieldsWhenOmitted() {
        // Existing 7-arg positional construction (matches EvaluationEngineTests.cs's Flag() helper)
        var state = new FlagEnvironmentState("id-1", "test-flag", "test", true, 50, "false", 0L);

        Assert.Equal(1, state.HashVersion);
        Assert.Empty(state.Prerequisites);
        Assert.Empty(state.TargetingRules);
        Assert.Empty(state.TargetList);
    }

    [Fact] public void FlagPrerequisite_ConstructsWithAllFields() {
        var prereq = new FlagPrerequisite("dep", "true", false);
        Assert.Equal("dep", prereq.FlagKey);
        Assert.Equal("true", prereq.RequiredVariation);
        Assert.False(prereq.Gate);
    }

    [Fact] public void TargetingRule_ConstructsWithAllFields() {
        var condition = new PropertyCondition("age", "gte", new List<string> { "18" }, false);
        var rule = new TargetingRule("r1", new List<PropertyCondition> { condition }, 50.0, "test", 0);
        Assert.Equal("r1", rule.Id);
        Assert.Equal(1, rule.Conditions.Count);
        Assert.Equal(50.0, rule.RolloutPct);
        Assert.Equal("test", rule.Variation);
        Assert.Equal(0, rule.Priority);
    }

    [Fact] public void PropertyCondition_ConstructsWithAllFields() {
        var cond = new PropertyCondition("plan", "eq", new List<string> { "pro", "enterprise" }, true);
        Assert.Equal("plan", cond.Attribute);
        Assert.Equal("eq", cond.Operator);
        Assert.Equal(new List<string> { "pro", "enterprise" }, cond.Values);
        Assert.True(cond.Negate);
    }
}
