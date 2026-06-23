import murmurhash from 'murmurhash';
import type {
  FlagEnvironmentState,
  FlagPrerequisite,
  TargetingRule,
  EvaluationContext,
  EvaluationResult,
  EvaluationReason,
} from './types.js';

export interface FlagLookup {
  get(flagKey: string): FlagEnvironmentState | undefined;
}

/**
 * 5-Step Sequential Evaluation Pipeline (LaunchDarkly-style, adversarially verified).
 *
 * Step 1 — Preliminary      : flag missing or disabled → OFF
 * Step 2 — Prerequisites    : any gating prerequisite fails → PREREQUISITE_FAILED
 * Step 3 — Individual target: userId in explicit targetList → TARGET_MATCH
 * Step 4 — Rule matching    : first priority-sorted rule match → RULE_MATCH
 * Step 5 — Fallthrough      : MurmurHash rollout bucket → FALLTHROUGH / OFF
 */
export class EvaluationEngine {
  private static readonly MAX_PREREQ_DEPTH = 5;

  // ─── Public API ────────────────────────────────────────────────────────────

  evaluateWithDetail<T>(
    flagKey: string,
    context: EvaluationContext,
    defaultValue: T,
    cache: FlagLookup,
    rules: TargetingRule[] = [],
  ): EvaluationResult<T> {
    return this.evaluateInternal<T>(flagKey, context, defaultValue, cache, rules, 0);
  }

  /**
   * Backward-compatible overload.
   * Legacy signature:  evaluate(flagState, rules, context, defaultValue, flagKey)
   * New signature:     evaluate(flagKey, context, defaultValue, cache, rules?)
   */
  evaluate<T>(
    flagStateOrKey: FlagEnvironmentState | undefined | string,
    rulesOrContext: TargetingRule[] | EvaluationContext,
    contextOrDefault: EvaluationContext | T,
    defaultValueOrCache: T | FlagLookup,
    flagKeyOrRules?: string | TargetingRule[],
  ): EvaluationResult<T> {
    if (typeof flagStateOrKey === 'string') {
      const flagKey    = flagStateOrKey;
      const context    = rulesOrContext as EvaluationContext;
      const defaultVal = contextOrDefault as T;
      const cache      = defaultValueOrCache as FlagLookup;
      const rules      = (Array.isArray(flagKeyOrRules) ? flagKeyOrRules : []) as TargetingRule[];
      return this.evaluateWithDetail<T>(flagKey, context, defaultVal, cache, rules);
    }

    // Legacy path
    const flagState  = flagStateOrKey as FlagEnvironmentState | undefined;
    const rules      = rulesOrContext as TargetingRule[];
    const context    = contextOrDefault as EvaluationContext;
    const defaultVal = defaultValueOrCache as T;
    const flagKey    = (typeof flagKeyOrRules === 'string' ? flagKeyOrRules : '') as string;

    const singleCache: FlagLookup = {
      get: (k: string) => (k === flagKey ? flagState : undefined),
    };
    return this.evaluateInternal<T>(flagKey, context, defaultVal, singleCache, rules, 0);
  }

  // ─── Internal pipeline ─────────────────────────────────────────────────────

  private evaluateInternal<T>(
    flagKey: string,
    context: EvaluationContext,
    defaultValue: T,
    cache: FlagLookup,
    rules: TargetingRule[],
    depth: number,
  ): EvaluationResult<T> {
    // Step 1: Preliminary
    const flagState = cache.get(flagKey);
    if (!flagState) {
      return this.result(defaultValue, 'ERROR', flagKey, { fromCache: false });
    }
    if (!flagState.enabled) {
      return this.result(this.parseSafeDefault<T>(flagState.safeDefault, defaultValue), 'OFF', flagKey);
    }

    // Step 2: Prerequisites
    const prereqs = flagState.prerequisites ?? [];
    if (prereqs.length > 0 && depth < EvaluationEngine.MAX_PREREQ_DEPTH) {
      const blocked = this.checkPrerequisites<T>(prereqs, context, defaultValue, cache, flagKey, depth);
      if (blocked !== null) return blocked;
    }

    // Step 3: Individual targeting
    const targetList = flagState.targetList ?? [];
    const userId = context.userId ?? '';
    if (targetList.length > 0 && targetList.includes(userId)) {
      return this.result(true as unknown as T, 'TARGET_MATCH', flagKey);
    }

    // Step 4: Rule matching — ascending priority (0 = highest)
    const sortedRules = [...(flagState.targetingRules ?? []), ...rules]
      .sort((a, b) => a.priority - b.priority);
    for (const rule of sortedRules) {
      if (this.matchesRule(rule, context)) {
        return this.result(rule.variation as unknown as T, 'RULE_MATCH', flagKey, { ruleId: rule.id });
      }
    }

    // Step 5: Fallthrough rollout
    if (this.isInRollout(flagKey, userId, flagState.rolloutPct, flagState.hashVersion ?? 1)) {
      return this.result(true as unknown as T, 'FALLTHROUGH', flagKey);
    }
    return this.result(defaultValue, 'FALLTHROUGH', flagKey);
  }

  // ─── Step 2: prerequisites ─────────────────────────────────────────────────

  private checkPrerequisites<T>(
    prereqs: FlagPrerequisite[],
    context: EvaluationContext,
    defaultValue: T,
    cache: FlagLookup,
    parentKey: string,
    depth: number,
  ): EvaluationResult<T> | null {
    for (const prereq of prereqs) {
      const prereqState = cache.get(prereq.flagKey);
      if (!prereqState && prereq.gate) {
        return this.result(defaultValue, 'PREREQUISITE_FAILED', parentKey, { ruleId: prereq.flagKey });
      }
      if (!prereqState) continue;

      const prereqResult = this.evaluateInternal<string>(
        prereq.flagKey, context, prereqState.safeDefault, cache, [], depth + 1,
      );
      if (String(prereqResult.value) !== prereq.requiredVariation && prereq.gate) {
        return this.result(defaultValue, 'PREREQUISITE_FAILED', parentKey, { ruleId: prereq.flagKey });
      }
    }
    return null;
  }

  // ─── Step 4: rule matching ─────────────────────────────────────────────────

  private matchesRule(rule: TargetingRule, context: EvaluationContext): boolean {
    const raw = this.resolveAttribute(rule.attribute, context);
    if (raw === undefined || raw === null) return false;
    return this.applyOperator(rule.operator, raw, rule.values);
  }

  private resolveAttribute(path: string, context: EvaluationContext): unknown {
    const segments = path.split('.');
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    let current: any = context;
    for (const seg of segments) {
      if (current == null || typeof current !== 'object') { current = undefined; break; }
      current = (current as Record<string, unknown>)[seg];
    }
    if (current !== undefined) return current;
    if (segments.length === 1 && context.attrs !== undefined) return context.attrs[path];
    return undefined;
  }

  private applyOperator(operator: string, value: unknown, ruleValues: unknown[]): boolean {
    const strValue = String(value);
    switch (operator) {
      case 'IN':       return ruleValues.some(v => String(v) === strValue);
      case 'NOT_IN':   return ruleValues.every(v => String(v) !== strValue);
      case 'EQ':       return strValue === String(ruleValues[0] ?? '');
      case 'NEQ':      return strValue !== String(ruleValues[0] ?? '');
      case 'CONTAINS': return typeof value === 'string' && ruleValues.some(v => value.includes(String(v)));
      case 'PREFIX':   return typeof value === 'string' && ruleValues.some(v => value.startsWith(String(v)));
      case 'SUFFIX':   return typeof value === 'string' && ruleValues.some(v => value.endsWith(String(v)));
      case 'LT': { const n = Number(value); return Number.isFinite(n) && n < Number(ruleValues[0] ?? 0); }
      case 'LTE': { const n = Number(value); return Number.isFinite(n) && n <= Number(ruleValues[0] ?? 0); }
      case 'GT': { const n = Number(value); return Number.isFinite(n) && n > Number(ruleValues[0] ?? 0); }
      case 'GTE': { const n = Number(value); return Number.isFinite(n) && n >= Number(ruleValues[0] ?? 0); }
      default: return false;
    }
  }

  // ─── Step 5: rollout ───────────────────────────────────────────────────────

  private isInRollout(flagKey: string, userId: string, rolloutPct: number, hashVersion: 1 | 2 = 1): boolean {
    if (rolloutPct >= 100) return true;
    if (rolloutPct <= 0) return false;
    if (hashVersion === 2) {
      const FNV_PRIME = 16777619, FNV_OFFSET = 2166136261;
      const fnv = (s: string) => {
        let h = FNV_OFFSET >>> 0;
        for (let i = 0; i < s.length; i++) { h ^= s.charCodeAt(i); h = Math.imul(h, FNV_PRIME) >>> 0; }
        return h >>> 0;
      };
      return (fnv(String(fnv(flagKey + userId))) % 10000) / 10000 < rolloutPct / 100;
    }
    return ((murmurhash.v3(flagKey + userId) >>> 0) % 100) < rolloutPct;
  }

  // ─── Helpers ───────────────────────────────────────────────────────────────

  private result<T>(
    value: T, reason: EvaluationReason, flagKey: string,
    extra?: { fromCache?: boolean; ruleId?: string; variationIndex?: number },
  ): EvaluationResult<T> {
    return {
      value, reason,
      fromCache: extra?.fromCache ?? true,
      flagKey,
      ...(extra?.ruleId !== undefined ? { ruleId: extra.ruleId } : {}),
      ...(extra?.variationIndex !== undefined ? { variationIndex: extra.variationIndex } : {}),
    };
  }

  private parseSafeDefault<T>(safeDefault: string, fallback: T): T {
    try {
      if (typeof fallback === 'boolean') return (safeDefault === 'true') as unknown as T;
      if (typeof fallback === 'number') { const n = Number(safeDefault); return (isNaN(n) ? fallback : n) as unknown as T; }
      if (typeof fallback === 'string') return safeDefault as unknown as T;
      return JSON.parse(safeDefault) as T;
    } catch { return fallback; }
  }
}
