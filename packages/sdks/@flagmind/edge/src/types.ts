/**
 * Full wire-format reason union, shared with @tombstone/core's richer
 * evaluation model for forward compatibility. This package's own
 * `evaluate()` (evaluation.ts) only ever returns OFF, FALLTHROUGH, or
 * ERROR today — TARGET_MATCH/RULE_MATCH/PREREQUISITE_FAILED are reserved
 * for when this SDK gains individual-target/rule/prerequisite support
 * (see SDK-2 follow-up); they are never actually produced by the current
 * evaluate() implementation, since FlagEnvironmentState below has no
 * targetList/targetingRules/prerequisites fields to evaluate in the
 * first place.
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

export interface FlagEnvironmentState {
  flagKey: string;
  enabled: boolean;
  rolloutPct: number;
  safeDefault: string;
  environment: string;
}

export interface FlagSnapshot {
  environment: string;
  flags: FlagEnvironmentState[];
  hash: string;
  ts: number;
}
