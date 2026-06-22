export type FlagType = 'BOOLEAN' | 'STRING' | 'INTEGER' | 'FLOAT' | 'JSON';
export type FlagState = 'DRAFT' | 'ACTIVE' | 'COMPLETE' | 'ARCHIVED';
export type EvaluationReason =
  | 'OFF'
  | 'FALLTHROUGH'
  | 'TARGET_MATCH'
  | 'RULE_MATCH'
  | 'PREREQUISITE_FAILED'
  | 'ERROR';
export type RuleOperator =
  | 'IN' | 'NOT_IN' | 'EQ' | 'NEQ'
  | 'LT' | 'LTE' | 'GT' | 'GTE'
  | 'CONTAINS' | 'PREFIX' | 'SUFFIX';

export interface FlagPrerequisite {
  /** The flag key that must be evaluated before this flag. */
  flagKey: string;
  /** The variation value the prerequisite flag must resolve to. */
  requiredVariation: string;
  /**
   * When true: if the prerequisite fails the ENTIRE feature is blocked
   * (returns PREREQUISITE_FAILED with the safeDefault).
   * When false: only the current rule is skipped; evaluation continues
   * to the next step.
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
  /** Explicit list of userIds that always receive the flag's "on" variation. */
  targetList?: string[];
  /** Zero or more prerequisite flags that must pass before this flag is served. */
  prerequisites?: FlagPrerequisite[];
}

export interface TargetingRule {
  id: string;
  attribute: string;
  operator: RuleOperator;
  values: string[];
  variation: string;
  priority: number;
}

// EvaluationContext — userId MUST be an opaque hash, never raw PII
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
  /** Set when reason is RULE_MATCH — the id of the matched targeting rule. */
  ruleId?: string;
  /**
   * Index of the resolved variation within the flag's variation list.
   * Undefined for boolean flags where the variation is implicit.
   */
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
  /** Base URL for gateway service. Default: http://localhost:8080 */
  gatewayUrl?: string;
  /** Base URL for flag-api service. Default: http://localhost:8081 */
  apiUrl?: string;
  /** Mandatory defaults — returned when flag is not in cache or service is unreachable */
  defaults: Record<string, unknown>;
  /** Initial reconnect interval in ms. Doubles on each retry up to maxReconnectMs. */
  reconnectIntervalMs?: number;
  /** Maximum reconnect backoff in ms. Default: 30000 */
  maxReconnectMs?: number;
  /** Fraction of evaluations to emit as telemetry. 0.0–1.0. Default: 0.01 */
  telemetrySampleRate?: number;
}
