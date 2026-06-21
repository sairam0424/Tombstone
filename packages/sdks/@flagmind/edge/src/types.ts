export const EvaluationReason = {
  OFF: 'OFF',
  FALLTHROUGH: 'FALLTHROUGH',
  TARGET_MATCH: 'TARGET_MATCH',
  RULE_MATCH: 'RULE_MATCH',
  PREREQUISITE_FAILED: 'PREREQUISITE_FAILED',
  ERROR: 'ERROR',
} as const;

export type EvaluationReason = typeof EvaluationReason[keyof typeof EvaluationReason];

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
