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
  | 'CONTAINS' | 'PREFIX' | 'SUFFIX'
  | 'REGEX'
  | 'SEMVER_GTE' | 'SEMVER_LTE'
  | 'GEO_COUNTRY' | 'GEO_REGION'
  | 'DATE_BEFORE' | 'DATE_AFTER';

export interface FlagEnvironmentState {
  flagId: string;
  flagKey: string;
  environment: string;
  enabled: boolean;
  rolloutPct: number;
  safeDefault: string;
  updatedAt: number;
}

export interface TargetingRule {
  id: string;
  attribute: string;
  operator: RuleOperator;
  values: string[];
  variation: string;
  priority: number;
}

// GeoContext — geographic identifiers for GEO_COUNTRY/GEO_REGION operators
export interface GeoContext {
  country?: string;
  region?: string;
}

// EvaluationContext — userId MUST be an opaque hash, never raw PII
export interface EvaluationContext {
  userId: string;
  orgId?: string;
  attrs?: Record<string, string>;
  geo?: GeoContext;
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
