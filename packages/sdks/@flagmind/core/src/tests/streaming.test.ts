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
import { strict as assert } from "assert";
import { SSEStreamClient } from "../streaming.js";
import type {
  FlagEvent,
  PrerequisitesUpdateEvent,
  TargetingRulesUpdateEvent,
  TombstoneClientConfig,
} from "../types.js";

type Listener = (e: { data?: string }) => void;

class FakeEventSource {
  static instances: FakeEventSource[] = [];
  listeners: Record<string, Listener[]> = {};
  onerror: (() => void) | null = null;
  closed = false;

  constructor(
    public url: string,
    public opts?: unknown,
  ) {
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

(globalThis as unknown as { EventSource: unknown }).EventSource =
  FakeEventSource;

async function waitFor(
  predicate: () => boolean,
  timeoutMs = 500,
): Promise<void> {
  const start = Date.now();
  while (!predicate()) {
    if (Date.now() - start > timeoutMs) {
      throw new Error("waitFor: condition never became true");
    }
    await new Promise((resolve) => setTimeout(resolve, 2));
  }
}

const baseConfig: TombstoneClientConfig = {
  sdkKey: "test-key",
  environment: "production",
  gatewayUrl: "http://localhost:8080",
  defaults: {},
  reconnectIntervalMs: 1,
  maxReconnectMs: 5,
};

describe("SSEStreamClient — onReconnect callback", () => {
  beforeEach(() => {
    // Re-assert THIS file's own EventSource stub -- mocha requires every
    // test file before running any test, so whichever file's module-level
    // `globalThis.EventSource = FakeEventSource` assignment runs LAST wins
    // for every test in every file, not just its own. client.test.ts's own
    // end-to-end streaming suite depends on the exact same discipline for
    // the identical reason (see its beforeEach's own comment).
    (globalThis as unknown as { EventSource: unknown }).EventSource =
      FakeEventSource;
    FakeEventSource.instances = [];
  });

  it('does NOT fire onReconnect on the very first "connected" event', () => {
    let reconnectCount = 0;
    const client = new SSEStreamClient(
      baseConfig,
      (_e: FlagEvent) => {},
      () => {
        reconnectCount++;
      },
    );
    client.connect();

    const es = FakeEventSource.instances[0];
    es.emit("connected");

    assert.equal(
      reconnectCount,
      0,
      "initial connection must not be treated as a reconnect",
    );
    client.disconnect();
  });

  it('fires onReconnect on every SUBSEQUENT "connected" event (i.e. every reconnect)', async () => {
    let reconnectCount = 0;
    const client = new SSEStreamClient(
      baseConfig,
      (_e: FlagEvent) => {},
      () => {
        reconnectCount++;
      },
    );
    client.connect();

    // Initial connection — establishes hasConnectedOnce, no onReconnect yet.
    FakeEventSource.instances[0].emit("connected");
    assert.equal(reconnectCount, 0);

    // Simulate the connection dropping (network blip) — this schedules a
    // reconnect via the existing exponential-backoff timer.
    FakeEventSource.instances[0].onerror?.();

    // Wait for the reconnect timer to fire and open a new EventSource.
    await waitFor(() => FakeEventSource.instances.length === 2);

    // The gateway sends "connected" again once the new SSE connection is live.
    FakeEventSource.instances[1].emit("connected");

    assert.equal(
      reconnectCount,
      1,
      "first reconnect must fire onReconnect exactly once",
    );

    // A SECOND reconnect must also fire onReconnect — this is the "EVERY
    // reconnect" requirement, not just the first one after a STALE period.
    FakeEventSource.instances[1].onerror?.();
    await waitFor(() => FakeEventSource.instances.length === 3);
    FakeEventSource.instances[2].emit("connected");

    assert.equal(
      reconnectCount,
      2,
      "second reconnect must also fire onReconnect",
    );

    client.disconnect();
  });

  it("does not fire onReconnect after disconnect() has been called", () => {
    let reconnectCount = 0;
    const client = new SSEStreamClient(
      baseConfig,
      (_e: FlagEvent) => {},
      () => {
        reconnectCount++;
      },
    );
    client.connect();
    FakeEventSource.instances[0].emit("connected");
    client.disconnect();

    // A stray error after disconnect() must not schedule a reconnect.
    FakeEventSource.instances[0].onerror?.();
    assert.equal(
      FakeEventSource.instances.length,
      1,
      "disconnect() must not open a new connection",
    );
    assert.equal(reconnectCount, 0);
  });

  it("works with no onReconnect callback provided (optional parameter)", () => {
    // TombstoneClient always supplies one, but the parameter is optional at
    // the type level — must not throw if omitted.
    const client = new SSEStreamClient(baseConfig, (_e: FlagEvent) => {});
    client.connect();
    assert.doesNotThrow(() => FakeEventSource.instances[0].emit("connected"));
    client.disconnect();
  });
});

describe("SSEStreamClient — lag event triggers debounced snapshot refetch", () => {
  // The gateway writes an  event: lag  frame right before dropping a real
  // flag-update event for a client whose buffer is full. The lag handler must
  // recover the dropped update by re-running the SAME full-snapshot refetch
  // that reconnect uses — onReconnect — debounced so a burst collapses to one.
  // A short debounce window keeps the test fast (mirrors reconnectIntervalMs:1
  // in baseConfig) while still exercising the real setTimeout path.
  const lagConfig: TombstoneClientConfig = {
    ...baseConfig,
    lagRefetchDebounceMs: 10,
  };

  beforeEach(() => {
    // Re-assert THIS file's own EventSource stub -- mocha requires every
    // test file before running any test, so whichever file's module-level
    // `globalThis.EventSource = FakeEventSource` assignment runs LAST wins
    // for every test in every file, not just its own. client.test.ts's own
    // end-to-end streaming suite depends on the exact same discipline for
    // the identical reason (see its beforeEach's own comment).
    (globalThis as unknown as { EventSource: unknown }).EventSource =
      FakeEventSource;
    FakeEventSource.instances = [];
  });

  it("fires exactly ONE refetch for a single lag event", async () => {
    let refetchCount = 0;
    const client = new SSEStreamClient(
      lagConfig,
      (_e: FlagEvent) => {},
      () => {
        refetchCount++;
      },
    );
    client.connect();

    const es = FakeEventSource.instances[0];
    es.emit("connected"); // initial connect must NOT count as a refetch
    assert.equal(refetchCount, 0);

    es.emit("lag", '{"lag_ms":42}'); // gateway dropped an update — recover via refetch
    await waitFor(() => refetchCount === 1);
    assert.equal(
      refetchCount,
      1,
      "a single lag event must trigger exactly one refetch",
    );

    // Nothing more should fire once the debounce window has settled.
    await new Promise((resolve) => setTimeout(resolve, 40));
    assert.equal(refetchCount, 1);

    client.disconnect();
  });

  it("coalesces a BURST of lag events within the debounce window into ONE refetch", async () => {
    let refetchCount = 0;
    const client = new SSEStreamClient(
      lagConfig,
      (_e: FlagEvent) => {},
      () => {
        refetchCount++;
      },
    );
    client.connect();

    const es = FakeEventSource.instances[0];
    es.emit("connected");
    assert.equal(refetchCount, 0);

    // Five lag frames back-to-back (a buffer-full burst). Each one resets the
    // debounce timer, so only the last window survives to fire the refetch.
    for (let i = 0; i < 5; i++) {
      es.emit("lag", `{"lag_ms":${i}}`);
    }

    await waitFor(() => refetchCount === 1);
    // Let several more debounce windows elapse to prove no second refetch fires.
    await new Promise((resolve) => setTimeout(resolve, 40));
    assert.equal(
      refetchCount,
      1,
      "a burst of lag events must coalesce into exactly one refetch",
    );

    client.disconnect();
  });
});

describe("SSEStreamClient — prerequisites_updated dispatch", () => {
  // services/flag-api/internal/api/v1/prerequisites.go's PrerequisitesEvent
  // has a distinct payload shape from FlagEvent (flag_key/environment/
  // prerequisites/ts, no enabled/rollout_pct/reason at all). Proves it gets
  // its OWN listener/callback, not routed through onEvent (which would
  // coerce the missing FlagEvent keys into defaults for a flag that was
  // never actually disabled).
  beforeEach(() => {
    // Re-assert THIS file's own EventSource stub -- mocha requires every
    // test file before running any test, so whichever file's module-level
    // `globalThis.EventSource = FakeEventSource` assignment runs LAST wins
    // for every test in every file, not just its own. client.test.ts's own
    // end-to-end streaming suite depends on the exact same discipline for
    // the identical reason (see its beforeEach's own comment).
    (globalThis as unknown as { EventSource: unknown }).EventSource =
      FakeEventSource;
    FakeEventSource.instances = [];
  });

  it("parses a real prerequisites_updated frame and forwards it via its own callback, not onEvent", () => {
    let flagEventCalls = 0;
    let received: PrerequisitesUpdateEvent | undefined;
    const client = new SSEStreamClient(
      baseConfig,
      (_e: FlagEvent) => {
        flagEventCalls++;
      },
      undefined,
      (e: PrerequisitesUpdateEvent) => {
        received = e;
      },
    );
    client.connect();

    const es = FakeEventSource.instances[0];
    es.emit(
      "prerequisites_updated",
      JSON.stringify({
        flag_key: "child-flag",
        environment: "production",
        prerequisites: [
          { flag_key: "parent-flag", required_variation: "true", gate: true },
        ],
        ts: 1_700_000_000,
      }),
    );

    assert.equal(flagEventCalls, 0, "must not be routed through onEvent");
    assert.ok(received, "onPrerequisitesEvent must have been called");
    assert.equal(received?.flagKey, "child-flag");
    assert.equal(received?.environment, "production");
    assert.equal(received?.ts, 1_700_000_000);
    assert.deepEqual(received?.prerequisites, [
      { flagKey: "parent-flag", requiredVariation: "true", gate: true },
    ]);

    client.disconnect();
  });

  it("gate omitted on the wire defaults to true, matching flag-api's own AddPrerequisite default", () => {
    let received: PrerequisitesUpdateEvent | undefined;
    const client = new SSEStreamClient(
      baseConfig,
      (_e: FlagEvent) => {},
      undefined,
      (e: PrerequisitesUpdateEvent) => {
        received = e;
      },
    );
    client.connect();

    FakeEventSource.instances[0].emit(
      "prerequisites_updated",
      JSON.stringify({
        flag_key: "child-flag",
        environment: "production",
        prerequisites: [
          { flag_key: "parent-flag", required_variation: "true" },
        ],
        ts: 1,
      }),
    );

    assert.equal(received?.prerequisites[0].gate, true);
    client.disconnect();
  });

  it("works with no onPrerequisitesEvent callback provided (optional parameter)", () => {
    const client = new SSEStreamClient(baseConfig, (_e: FlagEvent) => {});
    client.connect();
    assert.doesNotThrow(() =>
      FakeEventSource.instances[0].emit(
        "prerequisites_updated",
        JSON.stringify({ flag_key: "child-flag", prerequisites: [] }),
      ),
    );
    client.disconnect();
  });
});

describe("SSEStreamClient — targeting_rules_updated dispatch", () => {
  // services/flag-api/internal/api/v1/targeting_rules.go's
  // TargetingRulesEvent has a distinct payload shape from FlagEvent
  // (flag_key/environment/targeting_rules/ts, no enabled/rollout_pct/
  // reason at all). Mirrors the prerequisites_updated suite above exactly.
  beforeEach(() => {
    (globalThis as unknown as { EventSource: unknown }).EventSource =
      FakeEventSource;
    FakeEventSource.instances = [];
  });

  it("parses a real targeting_rules_updated frame and forwards it via its own callback, not onEvent", () => {
    let flagEventCalls = 0;
    let received: TargetingRulesUpdateEvent | undefined;
    const client = new SSEStreamClient(
      baseConfig,
      (_e: FlagEvent) => {
        flagEventCalls++;
      },
      undefined,
      undefined,
      (e: TargetingRulesUpdateEvent) => {
        received = e;
      },
    );
    client.connect();

    const es = FakeEventSource.instances[0];
    es.emit(
      "targeting_rules_updated",
      JSON.stringify({
        flag_key: "child-flag",
        environment: "production",
        targeting_rules: [
          {
            id: "r1",
            rule_type: "USER",
            attribute: "email",
            operator: "CONTAINS",
            values: ["@acme.com"],
            variation: "true",
            priority: 0,
          },
        ],
        ts: 1_700_000_000,
      }),
    );

    assert.equal(flagEventCalls, 0, "must not be routed through onEvent");
    assert.ok(received, "onTargetingRulesEvent must have been called");
    assert.equal(received?.flagKey, "child-flag");
    assert.equal(received?.environment, "production");
    assert.equal(received?.ts, 1_700_000_000);
    assert.deepEqual(received?.targetingRules, [
      {
        id: "r1",
        ruleType: "USER",
        attribute: "email",
        operator: "CONTAINS",
        values: ["@acme.com"],
        variation: "true",
        priority: 0,
      },
    ]);

    client.disconnect();
  });

  it("works with no onTargetingRulesEvent callback provided (optional parameter)", () => {
    const client = new SSEStreamClient(baseConfig, (_e: FlagEvent) => {});
    client.connect();
    assert.doesNotThrow(() =>
      FakeEventSource.instances[0].emit(
        "targeting_rules_updated",
        JSON.stringify({ flag_key: "child-flag", targeting_rules: [] }),
      ),
    );
    client.disconnect();
  });

  it("an empty flag_key is silently ignored -- never forwards a malformed event", () => {
    let received: TargetingRulesUpdateEvent | undefined;
    const client = new SSEStreamClient(
      baseConfig,
      (_e: FlagEvent) => {},
      undefined,
      undefined,
      (e: TargetingRulesUpdateEvent) => {
        received = e;
      },
    );
    client.connect();

    FakeEventSource.instances[0].emit(
      "targeting_rules_updated",
      JSON.stringify({ environment: "production", targeting_rules: [] }),
    );

    assert.equal(received, undefined);
    client.disconnect();
  });
});
