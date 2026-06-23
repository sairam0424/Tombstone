export type FlagType = 'BOOLEAN' | 'STRING' | 'INTEGER' | 'FLOAT' | 'JSON';
export type FlagState = 'DRAFT' | 'ACTIVE' | 'COMPLETE' | 'ARCHIVED';
export type EvaluationReason =
  | 'OFF'
  | 'FALLTHROUGH'
  | 'TARGET_MATCH'
  | 'RULE_MATCH'
  | 'PREREQUISITE_FAILED'
  | 'ERROR';

/** Full operator set for targeting rule evaluation. */
export type RuleOperator =
  | 'IN' | 'NOT_IN' | 'EQ' | 'NEQ'
  | 'LT' | 'LTE' | 'GT' | 'GTE'
  | 'CONTAINS' | 'PREFIX' | 'SUFFIX';

/** Alias — identical to RuleOperator. Both names exported for compatibility. */
export type OperatorType = RuleOperator;

export interface FlagPrerequisite {
  flagKey: string;
  requiredVariation: string;
  /**
   * true  → if prerequisite fails, block entire feature (PREREQUISITE_FAILED)
   * false → if prerequisite fails, skip current rule and continue evaluation
   */
  gate: boolean;
}

export interface FlagEnvironmentState {
  flagId: string;
  flagKey: string;
  environment: string;
  enabled: boolean;
  rolloutPct: number;
  safeDefault: string;
  updatedAt: number;
  hashVersion?: 1 | 2;
  /** Explicit list of userIds that always receive the flag's "on" variation. */
  targetList?: string[];
  /** Targeting rules attached to this flag. Default: []. */
  targetingRules?: TargetingRule[];
  /** Prerequisite flags that must pass before this flag is served. */
  prerequisites?: FlagPrerequisite[];
}

export interface TargetingRule {
  id: string;
  ruleType: 'USER' | 'ORG' | 'SEGMENT' | 'CUSTOM';
  /**
   * Dot-notation attribute path on EvaluationContext.
   * Examples: "userId", "orgId", "geo.country"
   */
  attribute: string;
  operator: OperatorType;
  values: unknown[];
  variation: string;
  /** Lower = higher priority. Evaluated ascending (0 before 10). */
  priority: number;
}

/**
 * EvaluationContext — userId MUST be an opaque hash, never raw PII.
 */
export interface EvaluationContext {
  userId?: string;
  orgId?: string;
  device?: string;
  geo?: { country?: string; region?: string };
  attrs?: Record<string, string>;
  [key: string]: unknown;
}

export interface EvaluationResult<T = boolean> {
  value: T;
  reason: EvaluationReason;
  fromCache: boolean;
  flagKey: string;
  /** Set when reason is RULE_MATCH — id of the matched targeting rule. */
  ruleId?: string;
  /** Index of the resolved variation within the flag's variation list. */
  variationIndex?: number;
}

export interface FlagSnapshot {
  environment: string;
  flags: FlagEnvironmentState[];
  hash: string;
  ts: number;
}

export interface FlagEvent {
  flagKey: string;
  enabled: boolean;
  rolloutPct: number;
  reason: string;
  ts: number;
  environment: string;
}

export interface TombstoneClientConfig {
  sdkKey: string;
  environment: string;
  gatewayUrl?: string;
  apiUrl?: string;
  defaults: Record<string, unknown>;
  reconnectIntervalMs?: number;
  maxReconnectMs?: number;
  telemetrySampleRate?: number;
}
