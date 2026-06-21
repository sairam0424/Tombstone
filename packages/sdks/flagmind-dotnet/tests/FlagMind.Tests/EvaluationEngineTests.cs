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
}
