import type { EvaluationContext, EvaluationResult, FlagEnvironmentState } from './types.js';

// FNV-1a 32-bit hash — deterministic, Web API safe, no external deps.
// Note: For full MurmurHash3 parity with other SDKs, use wasm-murmurhash in production.
// FNV-1a produces different buckets than MurmurHash3; use this only for edge deployments
// where the slight inconsistency with server-side evaluation is acceptable.
function fnv1a32(str: string): number {
  let hash = 0x811c9dc5;
  for (let i = 0; i < str.length; i++) {
    hash ^= str.charCodeAt(i);
    hash = (hash * 0x01000193) >>> 0; // unsigned 32-bit
  }
  return hash >>> 0;
}

function isInRollout(flagKey: string, userId: string, rolloutPct: number): boolean {
  if (rolloutPct >= 100) return true;
  if (rolloutPct <= 0) return false;
  const bucket = fnv1a32(flagKey + userId) % 100;
  return bucket < rolloutPct;
}

export function evaluate<T = boolean>(
  flagState: FlagEnvironmentState | undefined,
  context: EvaluationContext,
  defaultValue: T,
  flagKey: string,
): EvaluationResult<T> {
  if (!flagState) {
    return { value: defaultValue, reason: 'ERROR', fromCache: false, flagKey };
  }
  if (!flagState.enabled) {
    return { value: defaultValue, reason: 'OFF', fromCache: true, flagKey };
  }
  if (flagState.rolloutPct >= 100) {
    return { value: true as unknown as T, reason: 'FALLTHROUGH', fromCache: true, flagKey };
  }
  if (flagState.rolloutPct <= 0) {
    return { value: defaultValue, reason: 'FALLTHROUGH', fromCache: true, flagKey };
  }
  if (isInRollout(flagKey, context.userId, flagState.rolloutPct)) {
    return { value: true as unknown as T, reason: 'FALLTHROUGH', fromCache: true, flagKey };
  }
  return { value: defaultValue, reason: 'FALLTHROUGH', fromCache: true, flagKey };
}
