import murmurhash from 'murmurhash';
import type {
  FlagEnvironmentState,
  FlagPrerequisite,
  TargetingRule,
  EvaluationContext,
  EvaluationResult,
  EvaluationReason,
} from './types.js';

/**
 * FlagCache lookup interface.
 * EvaluationEngine accepts this narrow interface so it can be used standalone
 * (passing a Map directly in tests) or wired to the full FlagCache class.
 */
export interface FlagLookup {
  get(flagKey: string): FlagEnvironmentState | undefined;
}

/**
 * 5-Step Sequential Evaluation Pipeline
 * Based on LaunchDarkly's production-verified open-source evaluator.
 *
 * Step 1 — Preliminary      : flag missing or disabled → OFF
 * Step 2 — Prerequisites    : any prerequisite fails  → PREREQUISITE_FAILED
 * Step 3 — Individual target: userId in targetList    → TARGET_MATCH
 * Step 4 — Rule matching    : first priority rule hit → RULE_MATCH
 * Step 5 — Fallthrough      : rollout hash bucket     → FALLTHROUGH / OFF
 */
export class EvaluationEngine {
  /** Maximum prerequisite chain depth to prevent infinite loops. */
  private static readonly MAX_PREREQ_DEPTH = 5;

  // ─── Public API ────────────────────────────────────────────────────────────

  /**
   * Full evaluation with detailed trace. Returns an EvaluationResult that
   * includes the reason, optional ruleId (for RULE_MATCH), and variationIndex.
   */
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
   * Sugar wrapper — returns just the resolved value.
   * Kept for backward compatibility with the existing client.ts call-sites.
   *
   * Legacy overload signature (used by existing tests and TombstoneClient):
   *   evaluate(flagState, rules, context, defaultValue, flagKey)
   */
  evaluate<T>(
    flagStateOrKey: FlagEnvironmentState | undefined | string,
    rulesOrContext: TargetingRule[] | EvaluationContext,
    contextOrDefault: EvaluationContext | T,
    defaultValueOrFlagKey: T | string,
    flagKeyOrUndefined?: string,
    cacheArg?: FlagLookup,
  ): EvaluationResult<T> {
    // New signature: (flagKey, context, defaultValue, cache, rules?)
    if (typeof flagStateOrKey === 'string') {
      const flagKey = flagStateOrKey;
      const context = rulesOrContext as EvaluationContext;
      const defaultValue = contextOrDefault as T;
      const cache = defaultValueOrFlagKey as unknown as FlagLookup;
      const rules = (flagKeyOrUndefined as unknown as TargetingRule[]) ?? [];
      return this.evaluateWithDetail<T>(flagKey, context, defaultValue, cache, rules);
    }

    // Legacy signature: (flagState, rules, context, defaultValue, flagKey)
    const flagState = flagStateOrKey as FlagEnvironmentState | undefined;
    const rules = rulesOrContext as TargetingRule[];
    const context = contextOrDefault as EvaluationContext;
    const defaultValue = defaultValueOrFlagKey as T;
    const flagKey = flagKeyOrUndefined as string;

    // Build a minimal single-entry cache from the supplied flagState
    const singleCache: FlagLookup = {
      get: (key: string) => (key === flagKey ? flagState : undefined),
    };
    return this.evaluateInternal<T>(flagKey, context, defaultValue, singleCache, rules, 0);
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
    // ── Step 1: Preliminary ──────────────────────────────────────────────────
    const flagState = cache.get(flagKey);
    if (!flagState) {
      return this.result(defaultValue, 'ERROR', flagKey, { fromCache: false });
    }
    if (!flagState.enabled) {
      return this.result(this.parseSafeDefault<T>(flagState.safeDefault, defaultValue), 'OFF', flagKey);
    }

    // ── Step 2: Prerequisites ────────────────────────────────────────────────
    const prereqs = flagState.prerequisites ?? [];
    if (prereqs.length > 0 && depth < EvaluationEngine.MAX_PREREQ_DEPTH) {
      const prereqResult = this.checkPrerequisites<T>(prereqs, context, defaultValue, cache, flagKey, depth);
      if (prereqResult !== null) {
        return prereqResult;
      }
    }

    // ── Step 3: Individual targeting (explicit targetList) ───────────────────
    const targetList = flagState.targetList ?? [];
    if (targetList.length > 0 && targetList.includes(context.userId)) {
      return this.result(true as unknown as T, 'TARGET_MATCH', flagKey);
    }

    // ── Step 4: Rule matching ────────────────────────────────────────────────
    const sortedRules = [...rules].sort((a, b) => b.priority - a.priority);
    for (const rule of sortedRules) {
      if (this.evaluateRule(rule, context)) {
        return this.result(
          rule.variation as unknown as T,
          'RULE_MATCH',
          flagKey,
          { ruleId: rule.id },
        );
      }
    }

    // ── Step 5: Fallthrough rollout ──────────────────────────────────────────
    if (this.isInRollout(flagKey, context.userId, flagState.rolloutPct)) {
      return this.result(true as unknown as T, 'FALLTHROUGH', flagKey);
    }

    return this.result(defaultValue, 'FALLTHROUGH', flagKey);
  }

  // ─── Step 2 helpers ────────────────────────────────────────────────────────

  private checkPrerequisites<T>(
    prereqs: FlagPrerequisite[],
    context: EvaluationContext,
    defaultValue: T,
    cache: FlagLookup,
    parentFlagKey: string,
    depth: number,
  ): EvaluationResult<T> | null {
    for (const prereq of prereqs) {
      const prereqState = cache.get(prereq.flagKey);
      if (!prereqState) {
        // Prerequisite flag not in cache — treat as failed if gating
        if (prereq.gate) {
          return this.result(defaultValue, 'PREREQUISITE_FAILED', parentFlagKey, {
            ruleId: prereq.flagKey,
          });
        }
        continue;
      }

      // Evaluate the prerequisite inline (empty rules, recurse with depth+1)
      const prereqResult = this.evaluateInternal<string>(
        prereq.flagKey,
        context,
        prereqState.safeDefault,
        cache,
        [],
        depth + 1,
      );

      const resolvedVariation = String(prereqResult.value);
      const matches = resolvedVariation === prereq.requiredVariation;

      if (!matches && prereq.gate) {
        // Hard gate — block the entire feature
        return this.result(defaultValue, 'PREREQUISITE_FAILED', parentFlagKey, {
          ruleId: prereq.flagKey,
        });
      }
      // gate=false and no match → skip (continue to next prereq or next step)
    }
    return null; // all gating prerequisites passed
  }

  // ─── Rollout ───────────────────────────────────────────────────────────────

  /** Consistent assignment: same userId always gets same bucket for a given flag. */
  private isInRollout(flagKey: string, userId: string, rolloutPct: number): boolean {
    if (rolloutPct === 100) return true;
    if (rolloutPct === 0) return false;
    const hash = murmurhash.v3(flagKey + userId) >>> 0;
    return (hash % 100) < rolloutPct;
  }

  // ─── Rule evaluation ───────────────────────────────────────────────────────

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

  // ─── Result builder ────────────────────────────────────────────────────────

  private result<T>(
    value: T,
    reason: EvaluationReason,
    flagKey: string,
    extra?: { fromCache?: boolean; ruleId?: string; variationIndex?: number },
  ): EvaluationResult<T> {
    return {
      value,
      reason,
      fromCache: extra?.fromCache ?? true,
      flagKey,
      ...(extra?.ruleId !== undefined ? { ruleId: extra.ruleId } : {}),
      ...(extra?.variationIndex !== undefined ? { variationIndex: extra.variationIndex } : {}),
    };
  }

  // ─── Helpers ───────────────────────────────────────────────────────────────

  /**
   * Parse the safeDefault string into the expected type T.
   * Falls back to the supplied defaultValue if parsing fails.
   */
  private parseSafeDefault<T>(safeDefault: string, fallback: T): T {
    try {
      if (typeof fallback === 'boolean') {
        return (safeDefault === 'true') as unknown as T;
      }
      if (typeof fallback === 'number') {
        const n = Number(safeDefault);
        return (isNaN(n) ? fallback : n) as unknown as T;
      }
      if (typeof fallback === 'string') {
        return safeDefault as unknown as T;
      }
      return JSON.parse(safeDefault) as T;
    } catch {
      return fallback;
    }
  }
}
