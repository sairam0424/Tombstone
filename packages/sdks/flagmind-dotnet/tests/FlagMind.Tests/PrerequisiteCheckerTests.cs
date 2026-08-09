namespace Tombstone.Tests;
using Xunit;

public class PrerequisiteCheckerTests
{
    private readonly EvaluationEngine _engine = new();
    private readonly EvaluationContext _ctx = new("u1", "", new());

    [Fact] public void HardGateUnmetBlocks() {
        var baseFlag = new FlagEnvironmentState("id-2", "base-flag", "test", false, 0, "false", 0L);
        Func<string, FlagEnvironmentState?> lookup = key => key == "base-flag" ? baseFlag : null;
        var prereq = new FlagPrerequisite("base-flag", "true", true);

        var satisfied = PrerequisiteChecker.CheckAll(
            new List<FlagPrerequisite> { prereq }, lookup, new(), new(), "parent-flag", _engine, _ctx);

        Assert.False(satisfied);
    }

    [Fact] public void HardGateMetPasses() {
        var baseFlag = new FlagEnvironmentState("id-2", "base-flag", "test", true, 100, "false", 0L);
        Func<string, FlagEnvironmentState?> lookup = key => key == "base-flag" ? baseFlag : null;
        var prereq = new FlagPrerequisite("base-flag", "true", true);

        var satisfied = PrerequisiteChecker.CheckAll(
            new List<FlagPrerequisite> { prereq }, lookup, new(), new(), "parent-flag", _engine, _ctx);

        Assert.True(satisfied);
    }

    [Fact] public void SoftGateUnmetStillPasses() {
        var baseFlag = new FlagEnvironmentState("id-2", "base-flag", "test", false, 0, "false", 0L);
        Func<string, FlagEnvironmentState?> lookup = key => key == "base-flag" ? baseFlag : null;
        var prereq = new FlagPrerequisite("base-flag", "true", false);

        var satisfied = PrerequisiteChecker.CheckAll(
            new List<FlagPrerequisite> { prereq }, lookup, new(), new(), "parent-flag", _engine, _ctx);

        Assert.True(satisfied);
    }

    [Fact] public void CycleDetectedFailsOpen() {
        Func<string, FlagEnvironmentState?> lookup = _ => null; // unreachable — cycle short-circuits before lookup
        var prereq = new FlagPrerequisite("self-ref", "true", true);
        var seen = new HashSet<string> { "self-ref" };

        var satisfied = PrerequisiteChecker.CheckAll(
            new List<FlagPrerequisite> { prereq }, lookup, new(), seen, "self-ref", _engine, _ctx);

        Assert.True(satisfied);
    }

    [Fact] public void MissingPrerequisiteFlagWithHardGateBlocks() {
        Func<string, FlagEnvironmentState?> lookup = _ => null;
        var prereq = new FlagPrerequisite("nonexistent", "true", true);

        var satisfied = PrerequisiteChecker.CheckAll(
            new List<FlagPrerequisite> { prereq }, lookup, new(), new(), "parent-flag", _engine, _ctx);

        Assert.False(satisfied);
    }

    [Fact] public void MissingPrerequisiteFlagWithSoftGatePasses() {
        Func<string, FlagEnvironmentState?> lookup = _ => null;
        var prereq = new FlagPrerequisite("nonexistent", "true", false);

        var satisfied = PrerequisiteChecker.CheckAll(
            new List<FlagPrerequisite> { prereq }, lookup, new(), new(), "parent-flag", _engine, _ctx);

        Assert.True(satisfied);
    }

    [Fact] public void MemoizationPreventsRedundantEvaluation() {
        var callCount = 0;
        var baseFlag = new FlagEnvironmentState("id-2", "base-flag", "test", true, 100, "false", 0L);
        Func<string, FlagEnvironmentState?> lookup = key => {
            if (key == "base-flag") callCount++;
            return key == "base-flag" ? baseFlag : null;
        };
        var prereq1 = new FlagPrerequisite("base-flag", "true", true);
        var prereq2 = new FlagPrerequisite("base-flag", "true", true);
        var cache = new Dictionary<string, string?>();

        PrerequisiteChecker.CheckAll(
            new List<FlagPrerequisite> { prereq1, prereq2 }, lookup, cache, new(), "parent-flag", _engine, _ctx);

        Assert.Equal(1, callCount);
    }
}
