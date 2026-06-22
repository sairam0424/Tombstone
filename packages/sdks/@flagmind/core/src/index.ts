export { TombstoneClient } from './client.js';
export { EvaluationEngine } from './evaluation.js';
export { SSEStreamClient } from './streaming.js';
export { FlagCache } from './cache.js';

// Legacy OpenFeature adapter (v1 — kept for backward compatibility)
export { TombstoneProvider as TombstoneProviderLegacy } from './openfeature.js';
export type { ResolutionDetails as ResolutionDetailsLegacy, OpenFeatureEvaluationContext } from './openfeature.js';

// Full OpenFeature spec-compliant provider (v2 Phase 2.4)
export { TombstoneProvider, ProviderStatus, ErrorCode } from './provider.js';
export type { ResolutionDetails, OFEvaluationContext } from './provider.js';

export type {
  FlagType,
  FlagState,
  FlagEnvironmentState,
  TargetingRule,
  EvaluationContext,
  EvaluationResult,
  EvaluationReason,
  FlagSnapshot,
  FlagEvent,
  TombstoneClientConfig,
  RuleOperator,
} from './types.js';
