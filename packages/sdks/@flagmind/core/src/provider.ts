/**
 * TombstoneProvider — full OpenFeature server-side spec compliance (Phase 2.4).
 *
 * Implements (all 3-0 adversarially verified requirements):
 *   1. Four typed resolve methods (Boolean, String, Number, Object)
 *   2. No-throw guarantee — errors return { value: defaultValue, reason: 'ERROR', ... }
 *   3. Five provider states: NOT_READY, READY, STALE, ERROR, FATAL
 *   4. Provider lifecycle: initialize() + onClose()
 *   5. Dynamic context per call (server-side)
 *   6. FATAL state short-circuits all future evaluations
 *
 * Usage (without @openfeature/server-sdk peer dep):
 *   const provider = new TombstoneProvider(client);
 *   await provider.initialize();
 *   const result = await provider.resolveBooleanValue('my-flag', false, { targetingKey: 'user-1' });
 *
 * Usage (with @openfeature/server-sdk):
 *   await OpenFeature.setProviderAndWait(provider);
 */

import { EventEmitter } from 'events';
import type { TombstoneClient } from './client.js';
import type { EvaluationContext } from './types.js';

// ─── OpenFeature type contracts (inline — no peer dep required) ───────────────
// These mirror @openfeature/server-sdk exactly so the class satisfies the
// Provider interface when the peer dep IS present (TypeScript structural typing).

/** Five provider states as defined by OpenFeature spec. */
export enum ProviderStatus {
  NOT_READY = 'NOT_READY',
  READY     = 'READY',
  STALE     = 'STALE',
  ERROR     = 'ERROR',
  FATAL     = 'FATAL',
}

export interface ResolutionDetails<T> {
  value: T;
  reason?: string;
  variant?: string;
  flagMetadata?: Record<string, unknown>;
  errorCode?: string;
  errorMessage?: string;
}

/** OpenFeature EvaluationContext — server-side (dynamic per call). */
export interface OFEvaluationContext {
  targetingKey?: string;
  [key: string]: unknown;
}

// ─── Internal helpers ─────────────────────────────────────────────────────────

const REASON_MAP: Record<string, string> = {
  OFF:                 'DISABLED',
  FALLTHROUGH:         'DEFAULT',
  TARGET_MATCH:        'TARGETING_MATCH',
  RULE_MATCH:          'TARGETING_MATCH',
  PREREQUISITE_FAILED: 'DEFAULT',
  ERROR:               'ERROR',
};

function mapReason(flagMindReason: string): string {
  return REASON_MAP[flagMindReason] ?? 'UNKNOWN';
}

function toEvaluationContext(ctx: OFEvaluationContext | undefined): EvaluationContext {
  const { targetingKey, ...rest } = ctx ?? {};
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

/** Error codes defined by OpenFeature spec. */
export const ErrorCode = {
  PROVIDER_NOT_READY:   'PROVIDER_NOT_READY',
  FLAG_NOT_FOUND:       'FLAG_NOT_FOUND',
  PARSE_ERROR:          'PARSE_ERROR',
  TYPE_MISMATCH:        'TYPE_MISMATCH',
  TARGETING_KEY_MISSING:'TARGETING_KEY_MISSING',
  INVALID_CONTEXT:      'INVALID_CONTEXT',
  PROVIDER_FATAL:       'PROVIDER_FATAL',
  GENERAL:              'GENERAL',
} as const;

// ─── TombstoneProvider ────────────────────────────────────────────────────────

/**
 * Full OpenFeature-spec compliant provider for Tombstone.
 *
 * Events emitted:
 *   'ready'         — provider reached READY state
 *   'providerError' — provider reached ERROR state (non-fatal)
 *                     NOTE: 'error' is intentionally NOT used — Node.js EventEmitter
 *                     re-throws 'error' events with no listeners which breaks the
 *                     no-throw guarantee required by OpenFeature spec.
 *   'stale'         — provider entered STALE state (SSE disconnect)
 *   'fatal'         — provider entered FATAL state (PROVIDER_FATAL error code)
 *   'close'         — provider shut down
 */
export class TombstoneProvider extends EventEmitter {
  // OpenFeature spec: readonly metadata with name field
  readonly metadata = { name: 'Tombstone' };

  private _status: ProviderStatus = ProviderStatus.NOT_READY;

  constructor(private readonly client: TombstoneClient) {
    super();
  }

  // ─── State ──────────────────────────────────────────────────────────────────

  /** Current provider status. OpenFeature spec requires this getter. */
  get status(): ProviderStatus {
    return this._status;
  }

  // ─── Lifecycle ───────────────────────────────────────────────────────────────

  /**
   * OpenFeature spec: initialize(). Called by the OpenFeature SDK after
   * setProvider(). Connects SSE, loads snapshot, sets status READY.
   */
  async initialize(context?: OFEvaluationContext): Promise<void> {
    // context is accepted per spec but Tombstone uses per-call context (server-side)
    void context;

    try {
      if (!this.client.isConnected()) {
        await this.client.connect();
      }
      this._status = ProviderStatus.READY;
      this.emit('ready');
    } catch (err: unknown) {
      this._status = ProviderStatus.ERROR;
      // Emit 'providerError' not 'error': Node.js EventEmitter re-throws 'error'
      // events when there are no listeners, which would violate the no-throw guarantee.
      this.emit('providerError', err);
      // Do not rethrow — provider is in ERROR state, evaluations return defaults
    }
  }

  /**
   * OpenFeature spec: onClose(). Called when the provider is removed or the
   * process is shutting down. Disconnects SSE, sets status NOT_READY.
   */
  async onClose(): Promise<void> {
    this.client.disconnect();
    this._status = ProviderStatus.NOT_READY;
    this.emit('close');
  }

  // ─── STALE / READY transitions (called by SSE layer) ─────────────────────────

  /**
   * Mark provider STALE when SSE disconnects. Evaluations continue using
   * cached data with reason STALE but do not throw.
   */
  markStale(): void {
    if (this._status !== ProviderStatus.FATAL) {
      this._status = ProviderStatus.STALE;
      this.emit('stale');
    }
  }

  /**
   * Mark provider READY when SSE reconnects after a STALE period.
   */
  markReady(): void {
    if (this._status === ProviderStatus.STALE || this._status === ProviderStatus.ERROR) {
      this._status = ProviderStatus.READY;
      this.emit('ready');
    }
  }

  // ─── FATAL state entry ────────────────────────────────────────────────────────

  /**
   * Transition to FATAL state. Called when the evaluator returns the
   * PROVIDER_FATAL error code. Short-circuits ALL subsequent evaluations.
   */
  private enterFatal(message?: string): void {
    this._status = ProviderStatus.FATAL;
    this.emit('fatal', message);
  }

  // ─── FATAL short-circuit helper ───────────────────────────────────────────────

  private isFatal(): boolean {
    return this._status === ProviderStatus.FATAL;
  }

  private fatalDetails<T>(defaultValue: T): ResolutionDetails<T> {
    return {
      value:        defaultValue,
      reason:       'ERROR',
      errorCode:    ErrorCode.PROVIDER_FATAL,
      errorMessage: 'Provider is in FATAL state — all evaluations short-circuited',
    };
  }

  // ─── Core evaluation (shared) ─────────────────────────────────────────────────

  private resolveValue<T>(
    flagKey: string,
    defaultValue: T,
    context: OFEvaluationContext | undefined,
    typeCheck: (v: unknown) => v is T,
  ): ResolutionDetails<T> {
    // FATAL short-circuit — never throws
    if (this.isFatal()) {
      return this.fatalDetails(defaultValue);
    }

    // Provider not yet initialized
    if (this._status === ProviderStatus.NOT_READY) {
      return {
        value:     defaultValue,
        reason:    'ERROR',
        errorCode: ErrorCode.PROVIDER_NOT_READY,
      };
    }

    try {
      const evalCtx = toEvaluationContext(context);
      const result = this.client.evaluate<T>(flagKey, evalCtx);

      // Check if the evaluator signalled a FATAL error code
      // (future: result may carry errorCode when intelligence layer fires PROVIDER_FATAL)
      const resultAsRecord = result as unknown as Record<string, unknown>;
      if (resultAsRecord['errorCode'] === ErrorCode.PROVIDER_FATAL) {
        this.enterFatal(resultAsRecord['errorMessage'] as string | undefined);
        return this.fatalDetails(defaultValue);
      }

      const resolvedValue = typeCheck(result.value) ? result.value : defaultValue;
      const details: ResolutionDetails<T> = {
        value:  resolvedValue,
        reason: mapReason(result.reason),
      };

      // Annotate with STALE reason when provider is stale
      if (this._status === ProviderStatus.STALE) {
        details.reason = 'STALE';
      }

      return details;
    } catch (err: unknown) {
      return {
        value:        defaultValue,
        reason:       'ERROR',
        errorCode:    ErrorCode.GENERAL,
        errorMessage: err instanceof Error ? err.message : String(err),
      };
    }
  }

  // ─── Four typed resolve methods (OpenFeature spec requirement) ────────────────

  /**
   * Resolve a boolean flag value.
   * No-throw guarantee: errors return { value: defaultValue, reason: 'ERROR', ... }
   */
  async resolveBooleanValue(
    flagKey: string,
    defaultValue: boolean,
    context?: OFEvaluationContext,
  ): Promise<ResolutionDetails<boolean>> {
    return this.resolveValue(flagKey, defaultValue, context, (v): v is boolean => typeof v === 'boolean');
  }

  /**
   * Resolve a string flag value.
   * No-throw guarantee.
   */
  async resolveStringValue(
    flagKey: string,
    defaultValue: string,
    context?: OFEvaluationContext,
  ): Promise<ResolutionDetails<string>> {
    return this.resolveValue(flagKey, defaultValue, context, (v): v is string => typeof v === 'string');
  }

  /**
   * Resolve a numeric flag value.
   * No-throw guarantee.
   */
  async resolveNumberValue(
    flagKey: string,
    defaultValue: number,
    context?: OFEvaluationContext,
  ): Promise<ResolutionDetails<number>> {
    return this.resolveValue(flagKey, defaultValue, context, (v): v is number => typeof v === 'number' && !isNaN(v));
  }

  /**
   * Resolve an object (JSON) flag value.
   * No-throw guarantee.
   */
  async resolveObjectValue<T extends object>(
    flagKey: string,
    defaultValue: T,
    context?: OFEvaluationContext,
  ): Promise<ResolutionDetails<T>> {
    return this.resolveValue(
      flagKey,
      defaultValue,
      context,
      (v): v is T => typeof v === 'object' && v !== null && !Array.isArray(v),
    );
  }
}
