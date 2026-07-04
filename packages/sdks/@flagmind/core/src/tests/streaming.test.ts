/**
 * SSEStreamClient — reconnect-triggers-refetch tests.
 *
 * Verifies the Phase 3 change: EVERY SSE reconnect (not only ones that went
 * through a STALE provider state) invokes the onReconnect callback, which
 * TombstoneClient wires to a fresh full-snapshot refetch. This guards
 * against the dual-write gap — a missed Redis publish while briefly
 * disconnected is repaired by re-syncing from flag-api's snapshot endpoint
 * on reconnect.
 *
 * Node has no global EventSource, so this file installs a minimal fake
 * before constructing SSEStreamClient — it implements just enough of the
 * interface streaming.ts actually uses (addEventListener, onerror, close).
 */
import { strict as assert } from 'assert';
import { SSEStreamClient } from '../streaming.js';
import type { FlagEvent, TombstoneClientConfig } from '../types.js';

type Listener = (e: { data?: string }) => void;

class FakeEventSource {
  static instances: FakeEventSource[] = [];
  listeners: Record<string, Listener[]> = {};
  onerror: (() => void) | null = null;
  closed = false;

  constructor(public url: string, public opts?: unknown) {
    FakeEventSource.instances.push(this);
  }

  addEventListener(type: string, cb: Listener): void {
    (this.listeners[type] ??= []).push(cb);
  }

  close(): void {
    this.closed = true;
  }

  // Test helper: dispatch a named SSE event to all registered listeners.
  emit(type: string, data?: string): void {
    for (const cb of this.listeners[type] ?? []) cb({ data });
  }
}

(globalThis as unknown as { EventSource: unknown }).EventSource = FakeEventSource;

async function waitFor(predicate: () => boolean, timeoutMs = 500): Promise<void> {
  const start = Date.now();
  while (!predicate()) {
    if (Date.now() - start > timeoutMs) {
      throw new Error('waitFor: condition never became true');
    }
    await new Promise(resolve => setTimeout(resolve, 2));
  }
}

const baseConfig: TombstoneClientConfig = {
  sdkKey: 'test-key',
  environment: 'production',
  gatewayUrl: 'http://localhost:8080',
  defaults: {},
  reconnectIntervalMs: 1,
  maxReconnectMs: 5,
};

describe('SSEStreamClient — onReconnect callback', () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
  });

  it('does NOT fire onReconnect on the very first "connected" event', () => {
    let reconnectCount = 0;
    const client = new SSEStreamClient(
      baseConfig,
      (_e: FlagEvent) => {},
      () => { reconnectCount++; },
    );
    client.connect();

    const es = FakeEventSource.instances[0];
    es.emit('connected');

    assert.equal(reconnectCount, 0, 'initial connection must not be treated as a reconnect');
    client.disconnect();
  });

  it('fires onReconnect on every SUBSEQUENT "connected" event (i.e. every reconnect)', async () => {
    let reconnectCount = 0;
    const client = new SSEStreamClient(
      baseConfig,
      (_e: FlagEvent) => {},
      () => { reconnectCount++; },
    );
    client.connect();

    // Initial connection — establishes hasConnectedOnce, no onReconnect yet.
    FakeEventSource.instances[0].emit('connected');
    assert.equal(reconnectCount, 0);

    // Simulate the connection dropping (network blip) — this schedules a
    // reconnect via the existing exponential-backoff timer.
    FakeEventSource.instances[0].onerror?.();

    // Wait for the reconnect timer to fire and open a new EventSource.
    await waitFor(() => FakeEventSource.instances.length === 2);

    // The gateway sends "connected" again once the new SSE connection is live.
    FakeEventSource.instances[1].emit('connected');

    assert.equal(reconnectCount, 1, 'first reconnect must fire onReconnect exactly once');

    // A SECOND reconnect must also fire onReconnect — this is the "EVERY
    // reconnect" requirement, not just the first one after a STALE period.
    FakeEventSource.instances[1].onerror?.();
    await waitFor(() => FakeEventSource.instances.length === 3);
    FakeEventSource.instances[2].emit('connected');

    assert.equal(reconnectCount, 2, 'second reconnect must also fire onReconnect');

    client.disconnect();
  });

  it('does not fire onReconnect after disconnect() has been called', () => {
    let reconnectCount = 0;
    const client = new SSEStreamClient(
      baseConfig,
      (_e: FlagEvent) => {},
      () => { reconnectCount++; },
    );
    client.connect();
    FakeEventSource.instances[0].emit('connected');
    client.disconnect();

    // A stray error after disconnect() must not schedule a reconnect.
    FakeEventSource.instances[0].onerror?.();
    assert.equal(FakeEventSource.instances.length, 1, 'disconnect() must not open a new connection');
    assert.equal(reconnectCount, 0);
  });

  it('works with no onReconnect callback provided (optional parameter)', () => {
    // TombstoneClient always supplies one, but the parameter is optional at
    // the type level — must not throw if omitted.
    const client = new SSEStreamClient(baseConfig, (_e: FlagEvent) => {});
    client.connect();
    assert.doesNotThrow(() => FakeEventSource.instances[0].emit('connected'));
    client.disconnect();
  });
});
