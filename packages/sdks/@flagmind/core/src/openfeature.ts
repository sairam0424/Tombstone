import type { TombstoneClient } from './client.js';
import type { EvaluationContext } from './types.js';

// ─── OpenFeature interface definitions (inline — no peer dep required) ────────

export interface ResolutionDetails<T> {
  value: T;
  reason?: string;
  errorCode?: string;
  errorMessage?: string;
}

/** Minimal subset of the OpenFeature EvaluationContext contract. */
export interface OpenFeatureEvaluationContext {
  targetingKey?: string;
  [key: string]: unknown;
}

// ─── Reason mapping ───────────────────────────────────────────────────────────

const REASON_MAP: Record<string, string> = {
  OFF: 'DISABLED',
  FALLTHROUGH: 'DEFAULT',
  TARGET_MATCH: 'TARGETING_MATCH',
  RULE_MATCH: 'TARGETING_MATCH',
  PREREQUISITE_FAILED: 'DEFAULT',
  ERROR: 'ERROR',
};

function mapReason(flagMindReason: string): string {
  return REASON_MAP[flagMindReason] ?? 'UNKNOWN';
}

function buildEvaluationContext(
  context: OpenFeatureEvaluationContext | undefined
): EvaluationContext {
  const { targetingKey, ...rest } = context ?? {};
  const attrs: Record<string, string> = {};

  for (const [k, v] of Object.entries(rest)) {
    if (typeof v === 'string') {
      attrs[k] = v;
    } else if (v !== undefined && v !== null) {
      attrs[k] = String(v);
    }
  }

  return {
    userId: targetingKey ?? 'anonymous',
    ...(Object.keys(attrs).length > 0 ? { attrs } : {}),
  };
}

// ─── TombstoneProvider ─────────────────────────────────────────────────────────

/**
 * OpenFeature Provider for Tombstone.
 *
 * Usage:
 *   import { TombstoneClient } from '@tombstone/core';
 *   import { TombstoneProvider } from '@tombstone/core';
 *   import { OpenFeature } from '@openfeature/server-sdk';
 *
 *   const client = new TombstoneClient({ sdkKey: '...', environment: 'production', defaults: {} });
 *   const provider = new TombstoneProvider(client);
 *   await OpenFeature.setProviderAndWait(provider);
 */
export class TombstoneProvider {
  readonly metadata = { name: 'flagmind' };

  constructor(private readonly client: TombstoneClient) {}

  async initialize(): Promise<void> {
    if (!this.client.isConnected()) {
      await this.client.connect();
    }
  }

  async resolveBooleanEvaluation(
    flagKey: string,
    defaultValue: boolean,
    context?: OpenFeatureEvaluationContext
  ): Promise<ResolutionDetails<boolean>> {
    try {
      const ctx = buildEvaluationContext(context);
      const result = this.client.evaluate<boolean>(flagKey, ctx);
      return {
        value: typeof result.value === 'boolean' ? result.value : defaultValue,
        reason: mapReason(result.reason),
      };
    } catch (e: unknown) {
      return {
        value: defaultValue,
        reason: 'ERROR',
        errorCode: 'GENERAL',
        errorMessage: e instanceof Error ? e.message : String(e),
      };
    }
  }

  async resolveStringEvaluation(
    flagKey: string,
    defaultValue: string,
    context?: OpenFeatureEvaluationContext
  ): Promise<ResolutionDetails<string>> {
    try {
      const ctx = buildEvaluationContext(context);
      const result = this.client.evaluate<string>(flagKey, ctx);
      return {
        value: typeof result.value === 'string' ? result.value : defaultValue,
        reason: mapReason(result.reason),
      };
    } catch (e: unknown) {
      return {
        value: defaultValue,
        reason: 'ERROR',
        errorCode: 'GENERAL',
        errorMessage: e instanceof Error ? e.message : String(e),
      };
    }
  }

  async resolveNumberEvaluation(
    flagKey: string,
    defaultValue: number,
    context?: OpenFeatureEvaluationContext
  ): Promise<ResolutionDetails<number>> {
    try {
      const ctx = buildEvaluationContext(context);
      const result = this.client.evaluate<number>(flagKey, ctx);
      return {
        value: typeof result.value === 'number' ? result.value : defaultValue,
        reason: mapReason(result.reason),
      };
    } catch (e: unknown) {
      return {
        value: defaultValue,
        reason: 'ERROR',
        errorCode: 'GENERAL',
        errorMessage: e instanceof Error ? e.message : String(e),
      };
    }
  }

  async resolveObjectEvaluation<T extends object>(
    flagKey: string,
    defaultValue: T,
    context?: OpenFeatureEvaluationContext
  ): Promise<ResolutionDetails<T>> {
    try {
      const ctx = buildEvaluationContext(context);
      const result = this.client.evaluate<T>(flagKey, ctx);
      const isObject =
        typeof result.value === 'object' &&
        result.value !== null &&
        !Array.isArray(result.value);
      return {
        value: isObject ? result.value : defaultValue,
        reason: mapReason(result.reason),
      };
    } catch (e: unknown) {
      return {
        value: defaultValue,
        reason: 'ERROR',
        errorCode: 'GENERAL',
        errorMessage: e instanceof Error ? e.message : String(e),
      };
    }
  }
}
