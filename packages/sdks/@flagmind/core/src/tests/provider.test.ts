/**
 * TombstoneProvider — OpenFeature spec compliance tests (Phase 2.4).
 *
 * Covers:
 *   - Provider lifecycle (initialize / onClose)
 *   - Five provider states (NOT_READY, READY, STALE, ERROR, FATAL)
 *   - Four typed resolve methods (Boolean, String, Number, Object)
 *   - No-throw guarantee on all resolve methods
 *   - FATAL state short-circuit (all subsequent calls return default)
 *   - STALE state on SSE disconnect, READY on reconnect
 */

import { strict as assert } from 'assert';
import { TombstoneProvider, ProviderStatus, ErrorCode } from '../provider.js';
import type { OFEvaluationContext } from '../provider.js';
import type { TombstoneClient } from '../client.js';
import type { EvaluationResult } from '../types.js';

// ─── Minimal TombstoneClient stub ────────────────────────────────────────────

function makeClientStub(overrides: Partial<{
  connected: boolean;
  connectShouldThrow: boolean;
  evaluateResult: Partial<EvaluationResult<unknown>>;
}>): TombstoneClient {
  const state = {
    connected: overrides.connected ?? false,
    connectShouldThrow: overrides.connectShouldThrow ?? false,
    evaluateResult: overrides.evaluateResult ?? { value: true, reason: 'FALLTHROUGH', fromCache: true, flagKey: 'test' },
  };

  return {
    isConnected: () => state.connected,
    connect: async () => {
      if (state.connectShouldThrow) throw new Error('connection refused');
      state.connected = true;
    },
    disconnect: () => { state.connected = false; },
    evaluate: <T>(_flagKey: string, _ctx: unknown): EvaluationResult<T> => {
      return state.evaluateResult as EvaluationResult<T>;
    },
    flagKeys: () => [],
  } as unknown as TombstoneClient;
}

// ─── Lifecycle tests ──────────────────────────────────────────────────────────

describe('TombstoneProvider — lifecycle', () => {
  it('starts in NOT_READY state', () => {
    const client = makeClientStub({});
    const provider = new TombstoneProvider(client);
    assert.equal(provider.status, ProviderStatus.NOT_READY);
  });

  it('transitions to READY after initialize()', async () => {
    const client = makeClientStub({});
    const provider = new TombstoneProvider(client);
    await provider.initialize();
    assert.equal(provider.status, ProviderStatus.READY);
  });

  it('emits "ready" event on successful initialize()', async () => {
    const client = makeClientStub({});
    const provider = new TombstoneProvider(client);
    let emitted = false;
    provider.once('ready', () => { emitted = true; });
    await provider.initialize();
    assert.ok(emitted);
  });

  it('transitions to ERROR when connect() throws, does NOT rethrow', async () => {
    const client = makeClientStub({ connectShouldThrow: true });
    const provider = new TombstoneProvider(client);
    // Must not throw
    await provider.initialize();
    assert.equal(provider.status, ProviderStatus.ERROR);
  });

  it('emits "providerError" event when initialize() fails', async () => {
    const client = makeClientStub({ connectShouldThrow: true });
    const provider = new TombstoneProvider(client);
    let gotError = false;
    provider.once('providerError', () => { gotError = true; });
    await provider.initialize();
    assert.ok(gotError);
  });

  it('transitions to NOT_READY after onClose()', async () => {
    const client = makeClientStub({});
    const provider = new TombstoneProvider(client);
    await provider.initialize();
    await provider.onClose();
    assert.equal(provider.status, ProviderStatus.NOT_READY);
  });

  it('emits "close" event on onClose()', async () => {
    const client = makeClientStub({});
    const provider = new TombstoneProvider(client);
    await provider.initialize();
    let closed = false;
    provider.once('close', () => { closed = true; });
    await provider.onClose();
    assert.ok(closed);
  });

  it('skips connect() if client already connected', async () => {
    let connectCalled = 0;
    const client = {
      isConnected: () => true,
      connect: async () => { connectCalled++; },
      disconnect: () => {},
      evaluate: () => ({ value: false, reason: 'FALLTHROUGH', fromCache: true, flagKey: 'x' }),
      flagKeys: () => [],
    } as unknown as TombstoneClient;
    const provider = new TombstoneProvider(client);
    await provider.initialize();
    assert.equal(connectCalled, 0, 'connect() must not be called when already connected');
    assert.equal(provider.status, ProviderStatus.READY);
  });
});

// ─── STALE / READY transition tests ──────────────────────────────────────────

describe('TombstoneProvider — STALE / READY transitions', () => {
  it('enters STALE state via markStale()', async () => {
    const client = makeClientStub({});
    const provider = new TombstoneProvider(client);
    await provider.initialize();
    provider.markStale();
    assert.equal(provider.status, ProviderStatus.STALE);
  });

  it('emits "stale" event on markStale()', async () => {
    const client = makeClientStub({});
    const provider = new TombstoneProvider(client);
    await provider.initialize();
    let staleEmitted = false;
    provider.once('stale', () => { staleEmitted = true; });
    provider.markStale();
    assert.ok(staleEmitted);
  });

  it('recovers from STALE to READY via markReady()', async () => {
    const client = makeClientStub({});
    const provider = new TombstoneProvider(client);
    await provider.initialize();
    provider.markStale();
    provider.markReady();
    assert.equal(provider.status, ProviderStatus.READY);
  });

  it('does NOT leave FATAL state via markStale()', async () => {
    const client = makeClientStub({});
    const provider = new TombstoneProvider(client);
    await provider.initialize();
    // Simulate FATAL by triggering the internal path
    (provider as unknown as { enterFatal: (msg?: string) => void }).enterFatal?.('test');
    // Call private method via cast since enterFatal is private
    // Instead, trigger FATAL via a fake evaluate result
    provider.markStale(); // should be ignored in FATAL state
    assert.equal(provider.status, ProviderStatus.FATAL);
  });
});

// ─── FATAL short-circuit tests ────────────────────────────────────────────────

describe('TombstoneProvider — FATAL state', () => {
  async function buildFatalProvider(): Promise<TombstoneProvider> {
    const fatEvalResult: EvaluationResult<boolean> = {
      value: false,
      reason: 'ERROR',
      fromCache: false,
      flagKey: 'test',
      // @ts-ignore — injecting extra field to simulate PROVIDER_FATAL response
      errorCode: ErrorCode.PROVIDER_FATAL,
      errorMessage: 'circuit breaker open',
    };
    const client = makeClientStub({ evaluateResult: fatEvalResult });
    const provider = new TombstoneProvider(client);
    await provider.initialize();
    // Trigger the FATAL path via a resolve call
    await provider.resolveBooleanValue('any-flag', false, {});
    return provider;
  }

  it('enters FATAL when evaluator returns PROVIDER_FATAL error code', async () => {
    const provider = await buildFatalProvider();
    assert.equal(provider.status, ProviderStatus.FATAL);
  });

  it('emits "fatal" event when entering FATAL state', async () => {
    const fatEvalResult: EvaluationResult<boolean> = {
      value: false, reason: 'ERROR', fromCache: false, flagKey: 'test',
      // @ts-ignore
      errorCode: ErrorCode.PROVIDER_FATAL,
    };
    const client = makeClientStub({ evaluateResult: fatEvalResult });
    const provider = new TombstoneProvider(client);
    await provider.initialize();
    let fatalEmitted = false;
    provider.once('fatal', () => { fatalEmitted = true; });
    await provider.resolveBooleanValue('any-flag', false, {});
    assert.ok(fatalEmitted);
  });

  it('short-circuits all subsequent resolveBooleanValue calls', async () => {
    const provider = await buildFatalProvider();
    const result = await provider.resolveBooleanValue('my-flag', true, {});
    assert.equal(result.value, true, 'must return defaultValue');
    assert.equal(result.errorCode, ErrorCode.PROVIDER_FATAL);
    assert.equal(result.reason, 'ERROR');
  });

  it('short-circuits resolveStringValue after FATAL', async () => {
    const provider = await buildFatalProvider();
    const result = await provider.resolveStringValue('my-flag', 'fallback', {});
    assert.equal(result.value, 'fallback');
    assert.equal(result.errorCode, ErrorCode.PROVIDER_FATAL);
  });

  it('short-circuits resolveNumberValue after FATAL', async () => {
    const provider = await buildFatalProvider();
    const result = await provider.resolveNumberValue('my-flag', 42, {});
    assert.equal(result.value, 42);
    assert.equal(result.errorCode, ErrorCode.PROVIDER_FATAL);
  });

  it('short-circuits resolveObjectValue after FATAL', async () => {
    const provider = await buildFatalProvider();
    const defaultObj = { x: 1 };
    const result = await provider.resolveObjectValue('my-flag', defaultObj, {});
    assert.deepEqual(result.value, defaultObj);
    assert.equal(result.errorCode, ErrorCode.PROVIDER_FATAL);
  });
});

// ─── Typed resolve method tests ───────────────────────────────────────────────

describe('TombstoneProvider — typed resolve methods', () => {
  const ctx: OFEvaluationContext = { targetingKey: 'user-1', plan: 'pro' };

  it('resolveBooleanValue returns evaluated boolean', async () => {
    const client = makeClientStub({ evaluateResult: { value: true, reason: 'FALLTHROUGH', fromCache: true, flagKey: 'flag' } });
    const provider = new TombstoneProvider(client);
    await provider.initialize();
    const result = await provider.resolveBooleanValue('flag', false, ctx);
    assert.equal(result.value, true);
    assert.equal(result.reason, 'DEFAULT'); // FALLTHROUGH → DEFAULT
  });

  it('resolveBooleanValue returns defaultValue when value has wrong type', async () => {
    const client = makeClientStub({ evaluateResult: { value: 'not-a-bool', reason: 'FALLTHROUGH', fromCache: true, flagKey: 'flag' } });
    const provider = new TombstoneProvider(client);
    await provider.initialize();
    const result = await provider.resolveBooleanValue('flag', false, ctx);
    assert.equal(result.value, false, 'must fall back to default on type mismatch');
  });

  it('resolveStringValue returns evaluated string', async () => {
    const client = makeClientStub({ evaluateResult: { value: 'dark-mode', reason: 'TARGET_MATCH', fromCache: true, flagKey: 'flag' } });
    const provider = new TombstoneProvider(client);
    await provider.initialize();
    const result = await provider.resolveStringValue('flag', 'light-mode', ctx);
    assert.equal(result.value, 'dark-mode');
    assert.equal(result.reason, 'TARGETING_MATCH');
  });

  it('resolveStringValue returns defaultValue when value has wrong type', async () => {
    const client = makeClientStub({ evaluateResult: { value: 99, reason: 'FALLTHROUGH', fromCache: true, flagKey: 'flag' } });
    const provider = new TombstoneProvider(client);
    await provider.initialize();
    const result = await provider.resolveStringValue('flag', 'default-str', ctx);
    assert.equal(result.value, 'default-str');
  });

  it('resolveNumberValue returns evaluated number', async () => {
    const client = makeClientStub({ evaluateResult: { value: 3.14, reason: 'FALLTHROUGH', fromCache: true, flagKey: 'flag' } });
    const provider = new TombstoneProvider(client);
    await provider.initialize();
    const result = await provider.resolveNumberValue('flag', 0, ctx);
    assert.equal(result.value, 3.14);
  });

  it('resolveNumberValue returns defaultValue for NaN', async () => {
    const client = makeClientStub({ evaluateResult: { value: NaN, reason: 'FALLTHROUGH', fromCache: true, flagKey: 'flag' } });
    const provider = new TombstoneProvider(client);
    await provider.initialize();
    const result = await provider.resolveNumberValue('flag', 42, ctx);
    assert.equal(result.value, 42, 'NaN must fall back to default');
  });

  it('resolveObjectValue returns evaluated object', async () => {
    const obj = { rollout: 10, variant: 'v2' };
    const client = makeClientStub({ evaluateResult: { value: obj, reason: 'FALLTHROUGH', fromCache: true, flagKey: 'flag' } });
    const provider = new TombstoneProvider(client);
    await provider.initialize();
    const result = await provider.resolveObjectValue('flag', {}, ctx);
    assert.deepEqual(result.value, obj);
  });

  it('resolveObjectValue returns defaultValue for array (arrays not valid objects)', async () => {
    const client = makeClientStub({ evaluateResult: { value: [1, 2, 3], reason: 'FALLTHROUGH', fromCache: true, flagKey: 'flag' } });
    const provider = new TombstoneProvider(client);
    await provider.initialize();
    const def = { fallback: true };
    const result = await provider.resolveObjectValue('flag', def, ctx);
    assert.deepEqual(result.value, def, 'arrays must fall back to default');
  });

  it('resolveObjectValue returns defaultValue for null', async () => {
    const client = makeClientStub({ evaluateResult: { value: null, reason: 'FALLTHROUGH', fromCache: true, flagKey: 'flag' } });
    const provider = new TombstoneProvider(client);
    await provider.initialize();
    const def = { x: 0 };
    const result = await provider.resolveObjectValue('flag', def, ctx);
    assert.deepEqual(result.value, def);
  });
});

// ─── No-throw guarantee tests ─────────────────────────────────────────────────

describe('TombstoneProvider — no-throw guarantee', () => {
  it('resolveBooleanValue never throws even when evaluate() throws', async () => {
    const throwingClient = {
      isConnected: () => true,
      connect: async () => {},
      disconnect: () => {},
      evaluate: () => { throw new Error('unexpected internal error'); },
      flagKeys: () => [],
    } as unknown as TombstoneClient;
    const provider = new TombstoneProvider(throwingClient);
    await provider.initialize();
    // Must not throw
    const result = await provider.resolveBooleanValue('flag', false, {});
    assert.equal(result.value, false);
    assert.equal(result.reason, 'ERROR');
    assert.equal(result.errorCode, ErrorCode.GENERAL);
    assert.ok(typeof result.errorMessage === 'string');
  });

  it('resolveStringValue never throws', async () => {
    const throwingClient = {
      isConnected: () => true, connect: async () => {}, disconnect: () => {},
      evaluate: () => { throw new Error('boom'); },
      flagKeys: () => [],
    } as unknown as TombstoneClient;
    const provider = new TombstoneProvider(throwingClient);
    await provider.initialize();
    const result = await provider.resolveStringValue('flag', 'safe', {});
    assert.equal(result.value, 'safe');
    assert.equal(result.errorCode, ErrorCode.GENERAL);
  });

  it('resolveNumberValue never throws', async () => {
    const throwingClient = {
      isConnected: () => true, connect: async () => {}, disconnect: () => {},
      evaluate: () => { throw new Error('boom'); },
      flagKeys: () => [],
    } as unknown as TombstoneClient;
    const provider = new TombstoneProvider(throwingClient);
    await provider.initialize();
    const result = await provider.resolveNumberValue('flag', 99, {});
    assert.equal(result.value, 99);
  });

  it('resolveObjectValue never throws', async () => {
    const throwingClient = {
      isConnected: () => true, connect: async () => {}, disconnect: () => {},
      evaluate: () => { throw new Error('boom'); },
      flagKeys: () => [],
    } as unknown as TombstoneClient;
    const provider = new TombstoneProvider(throwingClient);
    await provider.initialize();
    const def = { safe: true };
    const result = await provider.resolveObjectValue('flag', def, {});
    assert.deepEqual(result.value, def);
  });

  it('returns PROVIDER_NOT_READY error when resolve called before initialize()', async () => {
    const client = makeClientStub({});
    const provider = new TombstoneProvider(client);
    // Do NOT call initialize()
    const result = await provider.resolveBooleanValue('flag', true, {});
    assert.equal(result.value, true);
    assert.equal(result.errorCode, ErrorCode.PROVIDER_NOT_READY);
  });
});
