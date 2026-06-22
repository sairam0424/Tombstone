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

    // Check targeting rules in priority order
    const sortedRules = [...rules].sort((a, b) => b.priority - a.priority);
    for (const rule of sortedRules) {
      if (this.evaluateRule(rule, context)) {
        return {
          value: rule.variation as unknown as T,
          reason: 'TARGET_MATCH',
          fromCache: true,
          flagKey,
        };
      }
    }

    // Percentage rollout — hash version determines bucketing algorithm
    if (this.isInRollout(flagKey, context.userId, flagState.rolloutPct, flagState.hashVersion ?? 1)) {
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

  private evaluateRule(rule: TargetingRule, context: EvaluationContext): boolean {
    let contextValue: string | undefined;
    if (rule.attribute === 'userId') {
      contextValue = context.userId;
    } else if (rule.attribute === 'orgId') {
      contextValue = context.orgId;
    } else {
      contextValue = context.attrs?.[rule.attribute];
    }
    if (contextValue === undefined) return false;
    return this.evaluateOperator(rule.operator, contextValue, rule.values);
  }

  private evaluateOperator(operator: string, value: string, ruleValues: string[]): boolean {
    switch (operator) {
      case 'IN':       return ruleValues.includes(value);
      case 'NOT_IN':   return !ruleValues.includes(value);
      case 'EQ':       return value === ruleValues[0];
      case 'NEQ':      return value !== ruleValues[0];
      case 'CONTAINS': return ruleValues.some(v => value.includes(v));
      case 'PREFIX':   return ruleValues.some(v => value.startsWith(v));
      case 'SUFFIX':   return ruleValues.some(v => value.endsWith(v));
      case 'LT':       return parseFloat(value) < parseFloat(ruleValues[0] ?? '0');
      case 'LTE':      return parseFloat(value) <= parseFloat(ruleValues[0] ?? '0');
      case 'GT':       return parseFloat(value) > parseFloat(ruleValues[0] ?? '0');
      case 'GTE':      return parseFloat(value) >= parseFloat(ruleValues[0] ?? '0');
      default:         return false;
    }
  }
}
