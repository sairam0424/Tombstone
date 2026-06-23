import murmurhash from 'murmurhash';
import type {
  FlagEnvironmentState,
  TargetingRule,
  EvaluationContext,
  EvaluationResult,
} from './types.js';

// Pure-TS semver comparator — no external dependency.
// Returns positive if a > b, negative if a < b, zero if equal.
// Only handles MAJOR.MINOR.PATCH (pre-release/build metadata ignored).
function compareSemver(a: string, b: string): number {
  const pa = a.split('.').map(Number);
  const pb = b.split('.').map(Number);
  for (let i = 0; i < 3; i++) {
    const diff = (pa[i] || 0) - (pb[i] || 0);
    if (diff !== 0) return diff;
  }
  return 0;
}

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

    // MurmurHash-based percentage rollout — consistent assignment per userId
    const userId = context.userId ?? '';
    if (this.isInRollout(flagKey, userId, flagState.rolloutPct)) {
      return { value: true as unknown as T, reason: 'FALLTHROUGH', fromCache: true, flagKey };
    }

    return { value: defaultValue, reason: 'FALLTHROUGH', fromCache: true, flagKey };
  }

  // Consistent assignment: same userId always gets same result for a given flag
  private isInRollout(flagKey: string, userId: string, rolloutPct: number): boolean {
    if (rolloutPct === 100) return true;
    if (rolloutPct === 0) return false;
    const hash = murmurhash.v3(flagKey + userId) >>> 0;
    return (hash % 100) < rolloutPct;
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
    // GEO operators resolve values from context.geo, not the generic attribute path
    if (rule.operator === 'GEO_COUNTRY') {
      const country = (context.geo?.country ?? '').toUpperCase();
      return (rule.values as string[]).map(v => String(v).toUpperCase()).includes(country);
    }
    if (rule.operator === 'GEO_REGION') {
      const region = (context.geo?.region ?? '').toUpperCase();
      return (rule.values as string[]).map(v => String(v).toUpperCase()).includes(region);
    }

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

      case 'REGEX':
        try {
          return new RegExp(String(ruleValues[0])).test(strValue);
        } catch {
          return false;
        }

      case 'SEMVER_GTE':
        return compareSemver(strValue, String(ruleValues[0])) >= 0;

      case 'SEMVER_LTE':
        return compareSemver(strValue, String(ruleValues[0])) <= 0;

      case 'DATE_BEFORE':
        return new Date(strValue) < new Date(String(ruleValues[0]));

      case 'DATE_AFTER':
        return new Date(strValue) > new Date(String(ruleValues[0]));

      default:
        return false;
    }
  }
}
