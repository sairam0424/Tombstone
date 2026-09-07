/**
 * Full wire-format reason union, shared with @tombstone/core's richer
 * evaluation model for forward compatibility. `evaluate()` (evaluation.ts)
 * now also produces PREREQUISITE_FAILED (see FlagPrerequisite/
 * FlagEnvironmentState.prerequisites below and evaluate()'s own doc
 * comment for the recursive checking algorithm, ported from
 * @tombstone/core's EvaluationEngine). TARGET_MATCH/RULE_MATCH remain
 * reserved — this SDK still has no targetList/targetingRules fields, a
 * separate, out-of-scope gap.
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

export interface EvaluationContext {
  userId: string;
  orgId?: string;
  attrs?: Record<string, string>;
}

export interface EvaluationResult<T = boolean> {
  value: T;
  reason: EvaluationReason;
  fromCache: boolean;
  flagKey: string;
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
}

export interface FlagSnapshot {
  environment: string;
  flags: FlagEnvironmentState[];
  hash: string;
  ts: number;
}
