export type FlagType = "BOOLEAN" | "STRING" | "INTEGER" | "FLOAT" | "JSON";
export type FlagState = "DRAFT" | "ACTIVE" | "COMPLETE" | "ARCHIVED";
export type EvaluationReason =
  | "OFF"
  | "FALLTHROUGH"
  | "TARGET_MATCH"
  | "RULE_MATCH"
  | "PREREQUISITE_FAILED"
  | "ERROR";

/** Full operator set for targeting rule evaluation. */
export type RuleOperator =
  | "IN"
  | "NOT_IN"
  | "EQ"
  | "NEQ"
  | "LT"
  | "LTE"
  | "GT"
  | "GTE"
  | "CONTAINS"
  | "PREFIX"
  | "SUFFIX"
  | "REGEX"
  | "SEMVER_GTE"
  | "SEMVER_LTE"
  | "GEO_COUNTRY"
  | "GEO_REGION"
  | "DATE_BEFORE"
  | "DATE_AFTER";

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
  ruleType: "USER" | "ORG" | "SEGMENT" | "CUSTOM";
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

// GeoContext — geographic identifiers for GEO_COUNTRY/GEO_REGION operators
export interface GeoContext {
  country?: string;
  region?: string;
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

/**
 * EVAL-2: wire shape POSTed to {telemetryUrl}/api/v1/telemetry as a JSON
 * array. Field names and casing MUST match
 * services/evaluator/internal/telemetry/aggregator.go's TelemetryEvent
 * struct's json tags exactly (flag_key/environment/is_error/ts) — this is
 * a cross-language contract with no schema validation on either side
 * beyond Go's own json.Decode.
 */
export interface TelemetryEvent {
  flag_key: string;
  environment: string;
  is_error: boolean;
  /** RFC3339 (e.g. new Date().toISOString()) — Go's time.Time unmarshals
   * from this format; a raw epoch number would fail to decode server-side. */
  ts: string;
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
  /**
   * Debounce window (ms) for coalescing a burst of gateway "lag" events into a
   * single full-snapshot refetch. A slow client can receive many lag frames in
   * quick succession; they collapse into ONE refetch this many ms after the
   * last one. Default: 500.
   */
  lagRefetchDebounceMs?: number;
  /**
   * EVAL-2: fraction of evaluate() calls to record as telemetry, in [0,1].
   * Only takes effect when telemetryUrl is also set (see below) — this
   * field was declared long before any code read it; setting it alone
   * changed nothing. Default: 1.0 (every call, once telemetry is enabled).
   */
  telemetrySampleRate?: number;
  /**
   * EVAL-2: base URL of the evaluator service (POST {telemetryUrl}/api/v1/
   * telemetry) that consumes this SDK's evaluate() outcomes to drive the
   * circuit breaker + auto-rollback pillar. Deliberately opt-in, no
   * default guess (unlike apiUrl's localhost:8081 default) — every
   * existing caller that does not set this gets ZERO behavior change: no
   * timer started, no network calls, no buffering overhead.
   */
  telemetryUrl?: string;
  /**
   * EVAL-2: how often (ms) to flush buffered telemetry to telemetryUrl.
   * Default: 10000 (10s), matching the evaluator's own 10s aggregation
   * window (services/evaluator/internal/telemetry/aggregator.go) — no
   * value in flushing faster than the server aggregates.
   */
  telemetryFlushIntervalMs?: number;
}
