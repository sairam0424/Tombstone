import murmurhash from 'murmurhash';
import type {
  FlagEnvironmentState,
  TargetingRule,
  EvaluationContext,
  EvaluationResult,
  EvaluationReason,
} from './types.js';

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

    // MurmurHash-based percentage rollout
    if (this.isInRollout(flagKey, context.userId, flagState.rolloutPct)) {
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
