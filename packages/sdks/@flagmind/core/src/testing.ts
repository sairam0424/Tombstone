/**
 * TombstoneTestClient — deterministic, override-driven flag evaluation for tests.
 *
 * Usage:
 *   const client = TombstoneTestClient.createIsolated();
 *   client.override('checkout_v2', true);
 *   expect(client.evaluate('checkout_v2', false)).toBe(true);
 *   client.clearOverrides();
 */
import type { EvaluationContext } from './types.js';

export class TombstoneTestClient {
  private overrides = new Map<string, unknown>();
  private bucketAssignments = new Map<string, Map<string, boolean>>(); // flagKey → userId → inCohort

  override(flagKey: string, value: unknown): this {
    this.overrides = new Map(this.overrides).set(flagKey, value); // immutable update
    return this;
  }

  clearOverride(flagKey: string): this {
    const next = new Map(this.overrides);
    next.delete(flagKey);
    this.overrides = next;
    return this;
  }

  clearOverrides(): this {
    this.overrides = new Map();
    return this;
  }

  /**
   * Force a specific userId to always be IN the rollout cohort for flagKey.
   * Useful for deterministic experiment assignment in tests.
   */
  assignToBucket(flagKey: string, userId: string, inCohort = true): this {
    const flagBuckets = new Map(this.bucketAssignments.get(flagKey) ?? []);
    flagBuckets.set(userId, inCohort);
    this.bucketAssignments = new Map(this.bucketAssignments).set(flagKey, flagBuckets);
    return this;
  }

  evaluate<T = unknown>(flagKey: string, defaultValue: T, context?: EvaluationContext): T {
    // 1. Check overrides first
    if (this.overrides.has(flagKey)) {
      return this.overrides.get(flagKey) as T;
    }
    // 2. Check deterministic bucket assignments
    if (context?.userId && this.bucketAssignments.has(flagKey)) {
      const assignment = this.bucketAssignments.get(flagKey)!.get(context.userId);
      if (assignment !== undefined) {
        return (assignment ? true : defaultValue) as T;
      }
    }
    return defaultValue;
  }

  isEnabled(flagKey: string, context?: EvaluationContext): boolean {
    return this.evaluate<unknown>(flagKey, false, context) === true;
  }

  /**
   * Returns a client with ALL flags returning safeDefault (maximal isolation).
   */
  static createIsolated(): TombstoneTestClient {
    return new TombstoneTestClient();
  }

  /**
   * Returns a client with specific flags pre-configured (convenience factory).
   */
  static withFlags(flags: Record<string, unknown>): TombstoneTestClient {
    const client = new TombstoneTestClient();
    for (const [key, val] of Object.entries(flags)) {
      client.override(key, val);
    }
    return client;
  }
}
