namespace Tombstone;

public enum EvaluationReason { Off, Fallthrough, TargetMatch, RuleMatch, PrerequisiteFailed, Error }

public record EvaluationContext(string UserId, string OrgId = "", Dictionary<string, string>? Attrs = null)
{
    public static EvaluationContext Of(string userId) => new(userId);
}

public record EvaluationResult<T>(T Value, EvaluationReason Reason, bool FromCache, string FlagKey);

public record FlagEnvironmentState(
    string FlagId, string FlagKey, string Environment,
    bool Enabled, int RolloutPct, string SafeDefault, long UpdatedAt
);
