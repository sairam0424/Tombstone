namespace Tombstone;

public enum EvaluationReason { Off, Fallthrough, TargetMatch, RuleMatch, PrerequisiteFailed, Error }

public record EvaluationContext(string UserId, string OrgId = "", Dictionary<string, string>? Attrs = null)
{
    public static EvaluationContext Of(string userId) => new(userId);

    public Dictionary<string, string> AttrsOrEmpty => Attrs ?? new Dictionary<string, string>();
}

public record EvaluationResult<T>(T Value, EvaluationReason Reason, bool FromCache, string FlagKey);

public record FlagPrerequisite(string FlagKey, string RequiredVariation, bool Gate);

public record PropertyCondition(string Attribute, string Operator, List<string> Values, bool Negate = false);

public record TargetingRule(string Id, List<PropertyCondition> Conditions, double RolloutPct, string Variation, int Priority = 0);

public record FlagEnvironmentState(
    string FlagId, string FlagKey, string Environment,
    bool Enabled, int RolloutPct, string SafeDefault, long UpdatedAt,
    List<FlagPrerequisite>? Prerequisites = null,
    List<TargetingRule>? TargetingRules = null,
    List<string>? TargetList = null,
    int HashVersion = 1
)
{
    // Nullable constructor params, non-null properties — preserves the existing 7-arg
    // positional-construction call sites (e.g. EvaluationEngineTests.cs's Flag() helper)
    // while guaranteeing every consumer sees empty lists, never null.
    public List<FlagPrerequisite> Prerequisites { get; init; } = Prerequisites ?? new();
    public List<TargetingRule> TargetingRules { get; init; } = TargetingRules ?? new();
    public List<string> TargetList { get; init; } = TargetList ?? new();
}
