/**
 * Full wire-format reason union, shared with @tombstone/core's richer
 * evaluation model for forward compatibility. `evaluate()` (evaluation.ts)
 * now also produces PREREQUISITE_FAILED (see FlagPrerequisite/
 * FlagEnvironmentState.prerequisites below and evaluate()'s own doc
 * comment for the recursive checking algorithm, ported from
 * @tombstone/core's EvaluationEngine) and RULE_MATCH (see TargetingRule
 * below, ported the same way). TARGET_MATCH remains reserved: individual
 * `target_list` targeting has no real backend data model anywhere in this
 * project yet — @tombstone/core's own parseFlagEnvironmentState defaults
 * it to `[]` with a comment disclosing it as "a separate, larger, deferred
 * project" — so there is nothing real to port here either, unlike
 * targeting_rules (which flag-api has genuinely served since PR #245).
 */
export const EvaluationReason = {
  OFF: "OFF",
  FALLTHROUGH: "FALLTHROUGH",
  TARGET_MATCH: "TARGET_MATCH",
  RULE_MATCH: "RULE_MATCH",
  PREREQUISITE_FAILED: "PREREQUISITE_FAILED",
  ERROR: "ERROR",
} as const;

export type EvaluationReason =
  (typeof EvaluationReason)[keyof typeof EvaluationReason];

// GEO_COUNTRY/GEO_REGION resolve from context.geo, not the generic
// attribute path — mirrors @tombstone/core's GeoContext exactly.
export interface EvaluationContext {
  userId: string;
  orgId?: string;
  geo?: { country?: string; region?: string };
  attrs?: Record<string, string>;
}

export interface EvaluationResult<T = boolean> {
  value: T;
  reason: EvaluationReason;
  fromCache: boolean;
  flagKey: string;
  /**
   * Set on PREREQUISITE_FAILED (the specific prerequisite flagKey that
   * blocked evaluation) or RULE_MATCH (the id of the matched targeting
   * rule), matching @tombstone/core's EvaluationResult (found missing for
   * the prerequisite case by adversarial review of PR #244: without this,
   * a caller with multiple prerequisites on one flag has no way to tell
   * which one actually failed).
   */
  ruleId?: string;
}

/**
 * A single targeting rule attached to a flag's per-environment state —
 * ported from @tombstone/core's TargetingRule exactly (same field names,
 * same operator set, same priority semantics). REGEX is declared but
 * intentionally unimplemented here too, matching Core/Python/Java/Ruby/
 * .NET's shared parity gap (docs/SDK_CONTRACT.md) — a REGEX rule never
 * matches, by design, not by omission. Semver/date operators are likewise
 * not implemented (Core doesn't implement them either — TypeScript-wide
 * gap, not Edge-specific).
 */
export interface TargetingRule {
  id: string;
  ruleType: "USER" | "ORG" | "SEGMENT" | "CUSTOM";
  /** Dot-notation attribute path on EvaluationContext. Examples: "userId", "orgId", "geo.country" */
  attribute: string;
  operator: string;
  values: unknown[];
  variation: string;
  /** Lower = higher priority. Evaluated ascending (0 before 10). */
  priority: number;
}

export interface FlagPrerequisite {
  flagKey: string;
  requiredVariation: string;
  /**
   * true  → if this prerequisite fails, block the entire flag (PREREQUISITE_FAILED)
   * false → if this prerequisite fails, skip it and continue evaluation
   */
  gate: boolean;
}

export interface FlagEnvironmentState {
  flagKey: string;
  enabled: boolean;
  rolloutPct: number;
  safeDefault: string;
  environment: string;
  /**
   * Prerequisite flags that must pass before this flag is served. Optional
   * — absent/empty for flags with none. See evaluate()'s own doc comment
   * for the recursive checking algorithm.
   */
  prerequisites?: FlagPrerequisite[];
  /**
   * Targeting rules attached to this flag+environment. Optional — absent/
   * empty for flags with none. Evaluated in ascending priority order (see
   * evaluate()'s own doc comment).
   */
  targetingRules?: TargetingRule[];
}

export interface FlagSnapshot {
  environment: string;
  flags: FlagEnvironmentState[];
  hash: string;
  ts: number;
}
