import murmurhash from 'murmurhash';
import type {
  FlagEnvironmentState,
  TargetingRule,
  EvaluationContext,
  EvaluationResult,
} from './types.js';

// ---------------------------------------------------------------------------
// Hash v2: double-FNV32a with 10,000-unit weight
// Fixes parallel-experiment bias present in v1's MurmurHash3 / 100-bucket scheme.
// Reference: GrowthBook hash v2 — 2-1 adversarially verified.
// ---------------------------------------------------------------------------

const FNV_PRIME = 16777619;
const FNV_OFFSET = 2166136261;

/** FNV-32a over a UTF-16 string (char-by-char, unsigned 32-bit). */
function fnv32a(str: string): number {
  let hash = FNV_OFFSET >>> 0;
  for (let i = 0; i < str.length; i++) {
    hash ^= str.charCodeAt(i);
    hash = Math.imul(hash, FNV_PRIME) >>> 0; // keep unsigned 32-bit
  }
  return hash >>> 0;
}

/**
 * Hash v2: double-FNV32a — applies FNV-32a twice (outer over decimal string of
 * inner result) then maps to [0, 1) with 10,000-unit precision.
 * @param seed  - typically the flag key
 * @param value - typically the user id
 */
function hashV2(seed: string, value: string): number {
  const inner = fnv32a(seed + value);
  const outer = fnv32a(String(inner));
  return (outer % 10000) / 10000;
}

/**
 * Hash v1 (legacy): MurmurHash3 unsigned 32-bit with 100-bucket modulus.
 * Kept unchanged — all existing flags that omit hashVersion use this path.
 */
function hashV1(flagKey: string, userId: string): number {
  const h = murmurhash.v3(flagKey + userId) >>> 0;
  return (h % 100) / 100;
}

// ---------------------------------------------------------------------------

export class EvaluationEngine {
  evaluate<T>(
    flagState: FlagEnvironmentState | undefined,
    rules: TargetingRule[],
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

    // Check targeting rules in priority order — lower number = higher priority.
    // Sort ascending so priority 0 is evaluated before priority 10.
    const sortedRules = [...rules].sort((a, b) => a.priority - b.priority);
    for (const rule of sortedRules) {
      if (this.matchesRule(rule, context)) {
        return {
          value: rule.variation as unknown as T,
          reason: 'TARGET_MATCH',
          fromCache: true,
          flagKey,
        };
      }
    }

    // Percentage rollout — hash version determines bucketing algorithm
    const userId = context.userId ?? '';
    if (this.isInRollout(flagKey, userId, flagState.rolloutPct, flagState.hashVersion ?? 1)) {
      return { value: true as unknown as T, reason: 'FALLTHROUGH', fromCache: true, flagKey };
    }

    return { value: defaultValue, reason: 'FALLTHROUGH', fromCache: true, flagKey };
  }

  /**
   * Consistent assignment: same userId always gets the same result for a given flag.
   *
   * hashVersion 1 (default, backward compat): MurmurHash3 % 100 < rolloutPct
   * hashVersion 2 (new experiments):          double-FNV32a, 10,000-bucket, compare to rolloutPct/100
   */
  private isInRollout(
    flagKey: string,
    userId: string,
    rolloutPct: number,
    hashVersion: 1 | 2 = 1,
  ): boolean {
    if (rolloutPct === 100) return true;
    if (rolloutPct === 0) return false;

    if (hashVersion === 2) {
      return hashV2(flagKey, userId) < rolloutPct / 100;
    }

    return hashV1(flagKey, userId) * 100 < rolloutPct;
  }

  /**
   * Evaluate whether a targeting rule matches the provided context.
   *
   * Attribute resolution (in order):
   *   1. Dot-notation path (e.g. "geo.country") walked on the context object.
   *   2. Legacy attrs bag (context.attrs["key"]) for backward compatibility.
   *
   * Returns false — never throws — if the attribute is absent or the type
   * is incompatible with the operator.
   */
  private matchesRule(rule: TargetingRule, context: EvaluationContext): boolean {
    const raw = this.resolveAttribute(rule.attribute, context);
    if (raw === undefined || raw === null) return false;
    return this.applyOperator(rule.operator, raw, rule.values);
  }

  /**
   * Walk a dot-notation path on the context object.
   * Falls back to context.attrs[key] for single-segment keys that are not
   * found as top-level properties.
   */
  private resolveAttribute(path: string, context: EvaluationContext): unknown {
    // Walk dot-notation path
    const segments = path.split('.');
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    let current: any = context;
    for (const seg of segments) {
      if (current === undefined || current === null || typeof current !== 'object') {
        current = undefined;
        break;
      }
      current = (current as Record<string, unknown>)[seg];
    }

    if (current !== undefined) return current;

    // Fallback: legacy attrs bag (single-segment keys only)
    if (segments.length === 1 && context.attrs !== undefined) {
      return context.attrs[path];
    }

    return undefined;
  }

  /**
   * Apply an operator against the resolved context value and the rule's
   * value list. Returns false on any type mismatch — never throws.
   */
  private applyOperator(operator: string, value: unknown, ruleValues: unknown[]): boolean {
    const strValue = String(value);

    switch (operator) {
      case 'IN':
        return ruleValues.some(v => String(v) === strValue);

      case 'NOT_IN':
        return ruleValues.every(v => String(v) !== strValue);

      case 'EQ':
        return strValue === String(ruleValues[0] ?? '');

      case 'NEQ':
        return strValue !== String(ruleValues[0] ?? '');

      case 'LT': {
        const n = Number(value);
        if (!Number.isFinite(n)) return false;
        return n < Number(ruleValues[0] ?? 0);
      }

      case 'LTE': {
        const n = Number(value);
        if (!Number.isFinite(n)) return false;
        return n <= Number(ruleValues[0] ?? 0);
      }

      case 'GT': {
        const n = Number(value);
        if (!Number.isFinite(n)) return false;
        return n > Number(ruleValues[0] ?? 0);
      }

      case 'GTE': {
        const n = Number(value);
        if (!Number.isFinite(n)) return false;
        return n >= Number(ruleValues[0] ?? 0);
      }

      case 'CONTAINS':
        if (typeof value !== 'string') return false;
        return ruleValues.some(v => value.includes(String(v)));

      case 'PREFIX':
        if (typeof value !== 'string') return false;
        return ruleValues.some(v => value.startsWith(String(v)));

      case 'SUFFIX':
        if (typeof value !== 'string') return false;
        return ruleValues.some(v => value.endsWith(String(v)));

      default:
        return false;
    }
  }
}
