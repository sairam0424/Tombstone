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

/**
 * Alias used in the v2 task spec — identical to RuleOperator.
 * Both names are exported so callers can use either.
 */
export type OperatorType = RuleOperator;

export interface FlagEnvironmentState {
  flagId: string;
  flagKey: string;
  environment: string;
  enabled: boolean;
  rolloutPct: number;
  safeDefault: string;
  updatedAt: number;
  /** Targeting rules attached to this flag in this environment. Default: []. */
  targetingRules?: TargetingRule[];
}

export interface TargetingRule {
  id: string;
  /** Discriminator for the rule's subject. Informs attribute lookup strategy. */
  ruleType: 'USER' | 'ORG' | 'SEGMENT' | 'CUSTOM';
  /**
   * Dot-notation attribute path resolved against EvaluationContext.
   * Examples: "userId", "orgId", "geo.country", "device".
   */
  attribute: string;
  operator: OperatorType;
  values: unknown[];
  variation: string;
  /** Lower number = higher priority. Evaluated ascending (0 before 10). */
  priority: number;
}

/**
 * EvaluationContext — userId MUST be an opaque hash, never raw PII.
 *
 * Supports both flat and structured attributes:
 *   - Top-level: userId, orgId, device
 *   - Nested (dot-notation): geo.country, geo.region
 *   - Arbitrary extra keys for CUSTOM rules
 */
export interface EvaluationContext {
  userId?: string;
  orgId?: string;
  device?: string;
  geo?: { country?: string; region?: string };
  /** Legacy flat attribute bag — still supported for backward compatibility. */
  attrs?: Record<string, string>;
  /** Additional arbitrary attributes for CUSTOM rules. */
  [key: string]: unknown;
}

export interface EvaluationResult<T = boolean> {
  value: T;
  reason: EvaluationReason;
  fromCache: boolean;
  flagKey: string;
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
