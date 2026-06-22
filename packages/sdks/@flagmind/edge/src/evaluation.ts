import type { EvaluationContext, EvaluationResult, FlagEnvironmentState } from './types.js';
import { murmur3_32 } from './murmurhash3.js';

// MurmurHash3 unsigned 32-bit — matches all other Tombstone SDKs
function isInRollout(flagKey: string, userId: string, rolloutPct: number): boolean {
  if (rolloutPct >= 100) return true;
  if (rolloutPct <= 0) return false;
  const bucket = murmur3_32(flagKey + userId) % 100;
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
