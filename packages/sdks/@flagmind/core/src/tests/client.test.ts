/**
 * TombstoneClient — EVAL-2 SDK telemetry tests.
 *
 * TombstoneClient itself had ZERO test coverage of any kind before this
 * file. Focused specifically on the new telemetry buffering/flush wiring
 * (evaluate() -> recordTelemetry() -> flushTelemetry() -> POST
 * {telemetryUrl}/api/v1/telemetry), not a general client test suite.
 *
 * Node has no global fetch mock installed by default in this test run, so
 * this file installs a minimal fake before constructing TombstoneClient —
 * matching streaming.test.ts's FakeEventSource convention for the same
 * reason (no real network calls in unit tests).
 *
 * This file installs its OWN EventSource stub (rather than relying on
 * streaming.test.ts having already set globalThis.EventSource as a side
 * effect of mocha's file-load order) -- found by adversarial review of
 * PR #218: running this file in isolation without streaming.test.ts
 * previously threw ReferenceError: EventSource is not defined from inside
 * connect().
 */
import { strict as assert } from "assert";
import { TombstoneClient } from "../client.js";
import type { FlagSnapshot, TombstoneClientConfig } from "../types.js";

type Listener = (e: { data?: string }) => void;

class FakeEventSource {
  listeners: Record<string, Listener[]> = {};
  onerror: (() => void) | null = null;
  closed = false;

  constructor(
    public url: string,
    public opts?: unknown,
  ) {}

  addEventListener(type: string, cb: Listener): void {
    (this.listeners[type] ??= []).push(cb);
  }

  close(): void {
    this.closed = true;
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

interface RecordedCall {
  url: string;
  method: string;
  headers: Record<string, string>;
  body?: string;
}

class FakeFetch {
  calls: RecordedCall[] = [];
  snapshotFlags: FlagSnapshot["flags"] = [];

  fn = async (
    url: string,
    init?: { method?: string; headers?: Record<string, string>; body?: string },
  ): Promise<{ ok: boolean; status: number; json: () => Promise<unknown> }> => {
    const method = init?.method ?? "GET";
    this.calls.push({
      url,
      method,
      headers: init?.headers ?? {},
      body: init?.body,
    });

    if (url.includes("/api/v1/environments/snapshot")) {
      const snapshot: FlagSnapshot = {
        environment: "production",
        flags: this.snapshotFlags,
        hash: "test-hash",
        ts: Date.now(),
      };
      return { ok: true, status: 200, json: async () => snapshot };
    }
    if (url.includes("/rules")) {
      // Best-effort per-flag rules fetch — no rules configured in these tests.
      return { ok: false, status: 404, json: async () => [] };
    }
    if (url.includes("/api/v1/telemetry")) {
      return { ok: true, status: 204, json: async () => undefined };
    }
    return { ok: false, status: 404, json: async () => undefined };
  };

  postCalls(): RecordedCall[] {
    return this.calls.filter(
      (c) => c.method === "POST" && c.url.includes("/telemetry"),
    );
  }
}

function baseConfig(
  overrides: Partial<TombstoneClientConfig> = {},
): TombstoneClientConfig {
  return {
    sdkKey: "test-key",
    environment: "production",
    apiUrl: "http://localhost:8081",
    defaults: {},
    ...overrides,
  };
}

describe("TombstoneClient — EVAL-2 telemetry", () => {
  let fakeFetch: FakeFetch;
  let originalFetch: typeof globalThis.fetch;

  beforeEach(() => {
    fakeFetch = new FakeFetch();
    originalFetch = globalThis.fetch;
    (globalThis as unknown as { fetch: unknown }).fetch = fakeFetch.fn;
  });

  afterEach(() => {
    (globalThis as unknown as { fetch: unknown }).fetch = originalFetch;
  });

  it("sends nothing when telemetryUrl is unset — zero behavior change", async () => {
    const client = new TombstoneClient(baseConfig());
    await client.connect();
    client.evaluate("any-flag", { userId: "u1" });
    await client.flush();

    assert.equal(fakeFetch.postCalls().length, 0);
    client.disconnect();
  });

  it("buffers and flushes a telemetry event with the exact wire shape the evaluator expects", async () => {
    fakeFetch.snapshotFlags = [
      {
        flagId: "1",
        flagKey: "known-flag",
        environment: "production",
        enabled: true,
        rolloutPct: 100,
        safeDefault: "false",
        updatedAt: Date.now(),
      },
    ];
    const client = new TombstoneClient(
      baseConfig({ telemetryUrl: "http://localhost:8082" }),
    );
    await client.connect();

    client.evaluate("known-flag", { userId: "u1" });
    await client.flush();

    const posts = fakeFetch.postCalls();
    assert.equal(posts.length, 1);
    assert.equal(posts[0].url, "http://localhost:8082/api/v1/telemetry");
    // Method AND headers -- a regression to GET, a missing Content-Type,
    // or a missing Authorization would previously have been invisible to
    // every test in this file (found by adversarial review of PR #218).
    assert.equal(posts[0].method, "POST");
    assert.equal(posts[0].headers["Content-Type"], "application/json");
    assert.equal(posts[0].headers.Authorization, "Bearer test-key");
    const batch = JSON.parse(posts[0].body ?? "[]") as Array<
      Record<string, unknown>
    >;
    assert.equal(batch.length, 1);
    // Field names/casing must match services/evaluator/internal/telemetry/
    // aggregator.go's TelemetryEvent json tags EXACTLY -- a cross-language
    // contract with no schema validation on either side.
    assert.equal(batch[0].flag_key, "known-flag");
    assert.equal(batch[0].environment, "production");
    assert.equal(batch[0].is_error, false);
    assert.equal(typeof batch[0].ts, "string");
    // Must be a real RFC3339 string Go's time.Time can unmarshal, not a raw
    // epoch number.
    assert.ok(!Number.isNaN(Date.parse(batch[0].ts as string)));

    client.disconnect();
  });

  it("marks is_error=true when the evaluation reason is ERROR", async () => {
    // No snapshot flags configured -- evaluating an unknown key returns
    // reason="ERROR" (evaluation.ts: `if (!flagState) return ...'ERROR'...`).
    const client = new TombstoneClient(
      baseConfig({ telemetryUrl: "http://localhost:8082" }),
    );
    await client.connect();

    const result = client.evaluate("never-configured-flag", { userId: "u1" });
    assert.equal(result.reason, "ERROR");
    await client.flush();

    const batch = JSON.parse(fakeFetch.postCalls()[0].body ?? "[]") as Array<
      Record<string, unknown>
    >;
    assert.equal(batch[0].is_error, true);

    client.disconnect();
  });

  it("buffer is cleared after a flush -- a second flush with no new evaluations sends nothing", async () => {
    const client = new TombstoneClient(
      baseConfig({ telemetryUrl: "http://localhost:8082" }),
    );
    await client.connect();

    client.evaluate("some-flag", { userId: "u1" });
    await client.flush();
    await client.flush();

    assert.equal(fakeFetch.postCalls().length, 1);
    client.disconnect();
  });

  it("telemetrySampleRate=0 drops every event deterministically", async () => {
    const client = new TombstoneClient(
      baseConfig({
        telemetryUrl: "http://localhost:8082",
        telemetrySampleRate: 0,
      }),
    );
    await client.connect();

    for (let i = 0; i < 20; i++)
      client.evaluate("some-flag", { userId: `u${i}` });
    await client.flush();

    assert.equal(fakeFetch.postCalls().length, 0);
    client.disconnect();
  });

  it("telemetrySampleRate=1 (default) keeps every event deterministically", async () => {
    const client = new TombstoneClient(
      baseConfig({
        telemetryUrl: "http://localhost:8082",
        telemetrySampleRate: 1,
      }),
    );
    await client.connect();

    for (let i = 0; i < 20; i++)
      client.evaluate("some-flag", { userId: `u${i}` });
    await client.flush();

    const batch = JSON.parse(
      fakeFetch.postCalls()[0].body ?? "[]",
    ) as unknown[];
    assert.equal(batch.length, 20);
    client.disconnect();
  });

  it("disconnect() triggers a final flush of whatever is still buffered", async () => {
    const client = new TombstoneClient(
      baseConfig({ telemetryUrl: "http://localhost:8082" }),
    );
    await client.connect();

    client.evaluate("some-flag", { userId: "u1" });
    client.disconnect();

    // The final flush inside disconnect() is fire-and-forget (not awaited
    // by disconnect() itself) -- give the microtask queue a tick to let it
    // actually run before asserting.
    await new Promise((resolve) => setTimeout(resolve, 0));

    assert.equal(fakeFetch.postCalls().length, 1);
  });

  it("a failed telemetry POST does not throw and silently drops the batch", async () => {
    (globalThis as unknown as { fetch: unknown }).fetch = async () => {
      throw new Error("network down");
    };
    const client = new TombstoneClient(
      baseConfig({ telemetryUrl: "http://localhost:8082" }),
    );
    // connect() itself swallows the snapshot fetch's own network error too.
    await client.connect();
    client.evaluate("some-flag", { userId: "u1" });

    await assert.doesNotReject(client.flush());
    client.disconnect();
  });

  it("the periodic interval timer itself flushes -- not just the manual flush() wrapper", async () => {
    /**
     * Every other test in this file drives flushTelemetry() via the manual
     * client.flush() wrapper or disconnect()'s explicit final flush -- none
     * of them let the setInterval-based periodic flush (this PR's own
     * stated purpose) actually fire on its own. Found by adversarial
     * review of PR #218, which proved this gap empirically by disabling
     * the interval callback's body and confirming the full suite still
     * passed. Uses a short REAL interval + waitFor polling (matching
     * streaming.test.ts's own debounce-timing test convention) since no
     * fake-timer library is a devDependency of this package.
     */
    const client = new TombstoneClient(
      baseConfig({
        telemetryUrl: "http://localhost:8082",
        telemetryFlushIntervalMs: 5,
      }),
    );
    await client.connect();

    client.evaluate("some-flag", { userId: "u1" });
    assert.equal(fakeFetch.postCalls().length, 0); // not flushed yet

    await waitFor(() => fakeFetch.postCalls().length === 1);

    client.disconnect();
  });

  it("caps the buffer at 1000 events and drops the OLDEST, not the newest", async () => {
    /**
     * The documented TELEMETRY_BUFFER_MAX=1000 drop-oldest cap was
     * previously claimed only in a source comment and never exercised by
     * any test -- the largest evaluate() loop anywhere else in this file
     * is 20 iterations. Found by adversarial review of PR #218, which
     * proved the gap empirically by disabling the cap in the compiled
     * output and confirming the full suite still passed. Pushes 1001
     * events with DISTINCT, ordered flag keys so the flushed batch's
     * actual contents can prove both halves of the claim: the buffer
     * stayed capped at exactly 1000, AND the survivor is the newest event
     * (flag-1000) while the oldest (flag-0) was the one dropped -- a
     * drop-newest bug would leave flag-0 present and flag-1000 missing
     * instead.
     */
    const client = new TombstoneClient(
      baseConfig({ telemetryUrl: "http://localhost:8082" }),
    );
    await client.connect();

    for (let i = 0; i <= 1000; i++) {
      client.evaluate(`flag-${i}`, { userId: "u1" });
    }
    await client.flush();

    const batch = JSON.parse(fakeFetch.postCalls()[0].body ?? "[]") as Array<
      Record<string, unknown>
    >;
    assert.equal(batch.length, 1000);
    assert.equal(
      batch.some((e) => e.flag_key === "flag-0"),
      false,
      "the oldest event (flag-0) should have been dropped once the buffer hit its cap",
    );
    assert.equal(
      batch.some((e) => e.flag_key === "flag-1000"),
      true,
      "the newest event (flag-1000) must survive the cap",
    );

    client.disconnect();
  });

  it("a second connect() call replaces, not leaks, the telemetry flush timer", async () => {
    /**
     * Regression test for a real HIGH-severity bug found by adversarial
     * review of PR #218, reproduced empirically against the compiled
     * output before this fix: connect() had no idempotency guard before
     * creating the telemetry setInterval. isConnected() stays false for
     * this method's ENTIRE fetchSnapshot() await, so two overlapping
     * connect() calls (e.g. two concurrent callers each guarding with
     * `if (!isConnected()) await connect()`, exactly what
     * TombstoneProvider.initialize()/openfeature.ts's provider both do)
     * both reached the timer-setup code, each creating their OWN
     * setInterval and overwriting the single telemetryFlushTimer field --
     * orphaning the first timer forever, since disconnect() can only ever
     * clear whichever one the field currently points at. This proves
     * disconnect() now stops ALL periodic flushing after a double
     * connect(), not just the most recently created timer: if the fix
     * regressed, the orphaned first timer would keep firing after
     * disconnect() and flush a THIRD time.
     */
    const client = new TombstoneClient(
      baseConfig({
        telemetryUrl: "http://localhost:8082",
        telemetryFlushIntervalMs: 5,
      }),
    );

    await client.connect();
    await client.connect(); // overlapping/repeated connect() -- must not leak the first timer

    client.evaluate("some-flag", { userId: "u1" });
    await waitFor(() => fakeFetch.postCalls().length >= 1);

    client.disconnect();
    const countAtDisconnect = fakeFetch.postCalls().length;

    // If an earlier timer were leaked, it would still be alive here and
    // could fire again during this wait, growing postCalls() past
    // countAtDisconnect. Waiting several multiples of the 5ms interval
    // gives a leaked timer ample opportunity to do so.
    await new Promise((resolve) => setTimeout(resolve, 40));

    assert.equal(
      fakeFetch.postCalls().length,
      countAtDisconnect,
      "no further telemetry POSTs should occur after disconnect(), even after a double connect()",
    );
  });
});
