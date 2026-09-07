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
import type { TombstoneClientConfig } from "../types.js";

/**
 * A single flag entry exactly as flag-api's real snapshot endpoint sends it
 * on the wire (services/flag-api/internal/api/v1/environments.go's
 * FlagEnvironmentStateWithPrereqs/SnapshotPrerequisite structs) -- snake_case
 * throughout, NOT this SDK's own camelCase FlagEnvironmentState/
 * FlagPrerequisite types. Used to construct realistic fake snapshot
 * responses that exercise TombstoneClient's own wire-parsing code, instead
 * of handing it an already-typed object directly.
 */
interface RawWireFlag {
  flag_id: string;
  flag_key: string;
  environment: string;
  enabled: boolean;
  rollout_pct: number;
  safe_default: string;
  updated_at: number;
  prerequisites?: Array<{
    id?: string;
    flag_key: string;
    required_variation: string;
    gate?: boolean;
    priority?: number;
  }>;
  // flag-api's real snapshot response does not send these today (confirmed:
  // FlagEnvironmentStateWithPrereqs in environments.go has no target_list/
  // targeting_rules/hash_version fields) -- included here only to test
  // parseFlagEnvironmentState's defensive parsing of them for forward
  // compatibility, should a future backend change ever add them.
  target_list?: string[];
  targeting_rules?: Array<{
    id?: string;
    rule_type?: string;
    attribute: string;
    operator: string;
    values?: unknown[];
    variation?: string;
    priority?: number;
  }>;
  hash_version?: 1 | 2;
}

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

  // Test helper: dispatch a named SSE event to all registered listeners --
  // mirrors streaming.test.ts's identical FakeEventSource.emit.
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

interface RecordedCall {
  url: string;
  method: string;
  headers: Record<string, string>;
  body?: string;
}

class FakeFetch {
  calls: RecordedCall[] = [];
  snapshotFlags: RawWireFlag[] = [];
  // Overridable so tests can pin the snapshot's top-level ts to a known
  // value -- needed to deterministically exercise cache.ts's
  // applyPrerequisitesEvent staleness guard (ts comparisons against
  // Date.now() would be flaky/unobservable otherwise). Defaults to Date.now()
  // to match every pre-existing test in this file that doesn't care.
  snapshotTs: number | undefined;

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
      // Raw wire shape (snake_case) -- see RawWireFlag's doc comment. json()
      // returns a plain JS object here (no real network round-trip in this
      // test), which is exactly what JSON.parse(realHttpResponseBody) would
      // also produce: an object whose keys are the wire's own JSON tags, not
      // this SDK's camelCase field names.
      const snapshot = {
        environment: "production",
        flags: this.snapshotFlags,
        hash: "test-hash",
        ts: this.snapshotTs ?? Date.now(),
      };
      return { ok: true, status: 200, json: async () => snapshot };
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
        flag_id: "1",
        flag_key: "known-flag",
        environment: "production",
        enabled: true,
        rollout_pct: 100,
        safe_default: "false",
        updated_at: Math.floor(Date.now() / 1000),
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

describe("TombstoneClient — real-wire snapshot parsing", () => {
  /**
   * Regression suite for TWO bugs found while investigating SDK-4's
   * prerequisites-streaming follow-up. Every test above this describe block
   * used a fake fetch that returned an already-camelCase object directly, so
   * none of them ever exercised a real JSON round-trip; these use
   * RawWireFlag fixtures (snake_case, matching flag-api's actual JSON tags)
   * specifically to close that gap.
   *
   * Fix 1 (parseSnapshot/parseFlagEnvironmentState): fetchSnapshot() used to
   * do `(await resp.json()) as FlagSnapshot` -- a bare type assertion with no
   * snake_case-to-camelCase translation.
   *
   * Fix 2 (evaluateWithDetail + this.cache instead of the legacy single-flag
   * overload): evaluate()'s prerequisite checks could never resolve any flag
   * OTHER than the one being evaluated.
   *
   * IMPORTANT, found by adversarial review of this PR: only the SATISFIED
   * hard-gated prerequisite test below can actually distinguish Fix 2 from a
   * reverted state. For an UNMET hard-gated prerequisite, both the broken
   * legacy lookup (dependency not found -> `!prereqState && prereq.gate` ->
   * PREREQUISITE_FAILED) and the fixed lookup (dependency found but its real
   * value doesn't match required_variation -> PREREQUISITE_FAILED) converge
   * on the identical outcome -- there is no "unmet" scenario that diverges
   * between the two, since a missing dependency and a mismatched dependency
   * both hard-block. The two "unmet"/"gate omitted" tests below remain valid,
   * real regression tests for Fix 1's prerequisite-array/gate-default
   * parsing (confirmed failing in isolation when Fix 1 alone is reverted:
   * reason becomes ERROR instead of PREREQUISITE_FAILED) -- they just don't
   * additionally cover Fix 2, and are commented accordingly below.
   */
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

  it("evaluate() serves a flag's real state after connect() against a real snake_case snapshot", async () => {
    fakeFetch.snapshotFlags = [
      {
        flag_id: "1",
        flag_key: "known-flag",
        environment: "production",
        enabled: true,
        rollout_pct: 100,
        safe_default: "false",
        updated_at: Math.floor(Date.now() / 1000),
      },
    ];
    const client = new TombstoneClient(baseConfig());
    await client.connect();

    // Before the fix, loadSnapshot() keyed its cache Map by flag.flagKey,
    // which read as undefined for every entry against real snake_case JSON
    // -- flagKeys() would come back empty and evaluate() would fall through
    // to the caller's default regardless of the real 100% rollout.
    assert.deepEqual(client.flagKeys(), ["known-flag"]);
    const result = client.evaluate("known-flag", { userId: "u1" });
    assert.equal(result.value, true);
    assert.equal(result.reason, "FALLTHROUGH");

    client.disconnect();
  });

  it("a flag's real prerequisites parse correctly from a real snake_case snapshot", async () => {
    fakeFetch.snapshotFlags = [
      {
        flag_id: "1",
        flag_key: "parent-flag",
        environment: "production",
        enabled: true,
        rollout_pct: 100,
        safe_default: "false",
        updated_at: Math.floor(Date.now() / 1000),
      },
      {
        flag_id: "2",
        flag_key: "child-flag",
        environment: "production",
        enabled: true,
        rollout_pct: 100,
        safe_default: "false",
        updated_at: Math.floor(Date.now() / 1000),
        prerequisites: [
          {
            id: "prereq-1",
            flag_key: "parent-flag",
            required_variation: "true",
            gate: true,
            priority: 0,
          },
        ],
      },
    ];
    const client = new TombstoneClient(baseConfig());
    await client.connect();

    // A satisfied hard-gated prerequisite must not block evaluation. THIS is
    // the one test in this describe block that actually distinguishes Fix 2
    // from a reverted state: under the old legacy single-flag cache lookup,
    // "parent-flag" can never be resolved as a dependency (the lookup only
    // ever knows about "child-flag"), so this would incorrectly return
    // PREREQUISITE_FAILED even though parent-flag is real, enabled, and
    // satisfies required_variation -- confirmed empirically by adversarial
    // review of this PR (reverting only Fix 2 makes this test, and only this
    // test among the 4 below, fail).
    const result = client.evaluate("child-flag", { userId: "u1" });
    assert.equal(result.value, true);
    assert.notEqual(result.reason, "PREREQUISITE_FAILED");

    client.disconnect();
  });

  it('an unmet hard-gated prerequisite from a real snapshot blocks evaluation (Fix 1 only -- see this describe block\'s own doc comment for why no "unmet" scenario can distinguish Fix 2)', async () => {
    fakeFetch.snapshotFlags = [
      {
        flag_id: "1",
        flag_key: "parent-flag",
        environment: "production",
        enabled: false, // disabled -> evaluates to false
        rollout_pct: 0,
        safe_default: "false",
        updated_at: Math.floor(Date.now() / 1000),
      },
      {
        flag_id: "2",
        flag_key: "child-flag",
        environment: "production",
        enabled: true,
        rollout_pct: 100,
        safe_default: "false",
        updated_at: Math.floor(Date.now() / 1000),
        prerequisites: [
          {
            id: "prereq-1",
            flag_key: "parent-flag",
            required_variation: "true",
            gate: true,
          },
        ],
      },
    ];
    const client = new TombstoneClient(baseConfig());
    await client.connect();

    const result = client.evaluate("child-flag", { userId: "u1" });
    assert.equal(result.reason, "PREREQUISITE_FAILED");
    assert.equal(result.value, false);

    client.disconnect();
  });

  it("gate omitted on the wire defaults to true (hard-blocking), matching flag-api's own default (Fix 1 only, same reasoning as the unmet-prerequisite test above)", async () => {
    fakeFetch.snapshotFlags = [
      {
        flag_id: "1",
        flag_key: "parent-flag",
        environment: "production",
        enabled: false,
        rollout_pct: 0,
        safe_default: "false",
        updated_at: Math.floor(Date.now() / 1000),
      },
      {
        flag_id: "2",
        flag_key: "child-flag",
        environment: "production",
        enabled: true,
        rollout_pct: 100,
        safe_default: "false",
        updated_at: Math.floor(Date.now() / 1000),
        prerequisites: [
          { flag_key: "parent-flag", required_variation: "true" }, // no "gate" key
        ],
      },
    ];
    const client = new TombstoneClient(baseConfig());
    await client.connect();

    const result = client.evaluate("child-flag", { userId: "u1" });
    assert.equal(result.reason, "PREREQUISITE_FAILED");

    client.disconnect();
  });

  it("target_list/targeting_rules/hash_version parse correctly IF a future backend ever sends them (currently dead code -- flag-api's real snapshot response has none of these fields today)", async () => {
    fakeFetch.snapshotFlags = [
      {
        flag_id: "1",
        flag_key: "my-flag",
        environment: "production",
        enabled: true,
        rollout_pct: 0, // fallthrough alone would be false
        safe_default: "false",
        updated_at: Math.floor(Date.now() / 1000),
        target_list: ["u1"],
        targeting_rules: [
          {
            id: "r1",
            rule_type: "USER",
            attribute: "country",
            operator: "EQ",
            values: ["US"],
            variation: "special",
            priority: 0,
          },
        ],
        hash_version: 2,
      },
    ];
    const client = new TombstoneClient(baseConfig());
    await client.connect();

    // Step 3 (individual targeting) matches before rules/rollout are
    // consulted at all, proving target_list parsed into a real string[].
    const result = client.evaluate("my-flag", { userId: "u1" });
    assert.equal(result.reason, "TARGET_MATCH");
    assert.equal(result.value, true);

    client.disconnect();
  });
});

describe("TombstoneClient — live prerequisites_updated streaming (end-to-end)", () => {
  /**
   * End-to-end regression suite for the SDK-4 prerequisites-streaming
   * follow-up: a live "prerequisites_updated" SSE frame (services/flag-api/
   * internal/api/v1/prerequisites.go's PrerequisitesEvent, relayed verbatim
   * by the gateway) must actually change what evaluate() returns for the
   * affected flag, not just be parsed correctly in isolation (that's already
   * covered by streaming.test.ts's own "SSEStreamClient — prerequisites_updated
   * dispatch" suite, which stops at the callback boundary and never touches a
   * real TombstoneClient/FlagCache).
   *
   * Also proactively closes the exact gap PR #235's adversarial review found
   * in the Python SDK's equivalent test suite: a staleness test that only
   * uses a "clearly older" ts cannot distinguish a correct `<` comparison
   * (cache.ts's applyPrerequisitesEvent) from a buggy `<=` regression, since
   * both reject that input identically. The "ts equal to the cached value"
   * test below is the one that actually pins the `<` behavior down.
   */
  let fakeFetch: FakeFetch;
  let originalFetch: typeof globalThis.fetch;

  beforeEach(() => {
    fakeFetch = new FakeFetch();
    originalFetch = globalThis.fetch;
    (globalThis as unknown as { fetch: unknown }).fetch = fakeFetch.fn;
    // Re-assert THIS file's own EventSource stub. mocha requires every test
    // file before running any test, so whichever file loads LAST (e.g.
    // streaming.test.ts, alphabetically after this one) wins the module-level
    // `globalThis.EventSource = FakeEventSource` race for every test in every
    // file, not just its own -- both stub classes are structurally identical
    // (addEventListener/close/onerror/emit), so no prior test in this file
    // noticed, but instances would otherwise land in the OTHER file's
    // `FakeEventSource.instances` array, leaving this one permanently empty.
    (globalThis as unknown as { EventSource: unknown }).EventSource =
      FakeEventSource;
    FakeEventSource.instances = [];
  });

  afterEach(() => {
    (globalThis as unknown as { fetch: unknown }).fetch = originalFetch;
  });

  function snapshotWithParentAndChild(): void {
    fakeFetch.snapshotFlags = [
      {
        flag_id: "1",
        flag_key: "parent-flag",
        environment: "production",
        enabled: false, // disabled -> does NOT satisfy required_variation "true"
        rollout_pct: 0,
        safe_default: "false",
        updated_at: 1,
      },
      {
        flag_id: "2",
        flag_key: "child-flag",
        environment: "production",
        enabled: true,
        rollout_pct: 100,
        safe_default: "false",
        updated_at: 1,
        // No prerequisites in the snapshot itself -- the live event is what
        // introduces the gate, proving the update actually reaches the cache
        // rather than the snapshot's own (absent) prerequisites happening to
        // already produce the same outcome.
      },
    ];
  }

  it("a live prerequisites_updated event newer than the snapshot changes evaluate()'s outcome", async () => {
    fakeFetch.snapshotTs = 1000;
    snapshotWithParentAndChild();
    const client = new TombstoneClient(baseConfig());
    await client.connect();

    // Before the live event: child-flag has no prerequisites, so it evaluates
    // by rollout alone.
    const before = client.evaluate("child-flag", { userId: "u1" });
    assert.equal(before.value, true);
    assert.notEqual(before.reason, "PREREQUISITE_FAILED");

    FakeEventSource.instances[0].emit(
      "prerequisites_updated",
      JSON.stringify({
        flag_key: "child-flag",
        environment: "production",
        prerequisites: [
          { flag_key: "parent-flag", required_variation: "true", gate: true },
        ],
        ts: 2000, // newer than the snapshot's ts=1000
      }),
    );

    const after = client.evaluate("child-flag", { userId: "u1" });
    assert.equal(
      after.reason,
      "PREREQUISITE_FAILED",
      "the live event must actually be applied to the cache and change evaluate()'s outcome",
    );
    assert.equal(after.value, false);

    client.disconnect();
  });

  it("an event OLDER than the currently-cached ts is rejected -- evaluate() stays unaffected", async () => {
    fakeFetch.snapshotTs = 5000;
    snapshotWithParentAndChild();
    const client = new TombstoneClient(baseConfig());
    await client.connect();

    FakeEventSource.instances[0].emit(
      "prerequisites_updated",
      JSON.stringify({
        flag_key: "child-flag",
        environment: "production",
        prerequisites: [
          { flag_key: "parent-flag", required_variation: "true", gate: true },
        ],
        ts: 3000, // OLDER than the snapshot's ts=5000 -- must be rejected
      }),
    );

    const result = client.evaluate("child-flag", { userId: "u1" });
    assert.notEqual(
      result.reason,
      "PREREQUISITE_FAILED",
      "a stale out-of-order event must not overwrite the newer cached state",
    );
    assert.equal(result.value, true);

    client.disconnect();
  });

  it("an event whose ts EQUALS the currently-cached ts IS applied -- pins down `<`, not `<=`", async () => {
    /**
     * The precise boundary case PR #235's review flagged as missing from the
     * Python SDK's suite: cache.ts's applyPrerequisitesEvent rejects
     * `ts < existing.prerequisitesUpdatedAt`, which means an event whose ts
     * is EXACTLY EQUAL to the cached value must be treated as new-or-equal
     * and applied. Using a "clearly older" ts alone (the test above) cannot
     * distinguish this correct `<` from a buggy `<=` -- both reject a
     * strictly-older event identically. Only an equal-ts input tells the two
     * apart: `<=` would additionally (and wrongly) reject this one.
     */
    fakeFetch.snapshotTs = 5000;
    snapshotWithParentAndChild();
    const client = new TombstoneClient(baseConfig());
    await client.connect();

    FakeEventSource.instances[0].emit(
      "prerequisites_updated",
      JSON.stringify({
        flag_key: "child-flag",
        environment: "production",
        prerequisites: [
          { flag_key: "parent-flag", required_variation: "true", gate: true },
        ],
        ts: 5000, // EQUAL to the snapshot's own ts=5000
      }),
    );

    const result = client.evaluate("child-flag", { userId: "u1" });
    assert.equal(
      result.reason,
      "PREREQUISITE_FAILED",
      "an equal-ts event must be applied, not dropped as stale",
    );
    assert.equal(result.value, false);

    client.disconnect();
  });

  it("an update for a flag the client has never seen is a no-op (nothing to merge into)", async () => {
    fakeFetch.snapshotTs = 1000;
    snapshotWithParentAndChild();
    const client = new TombstoneClient(baseConfig());
    await client.connect();

    assert.doesNotThrow(() =>
      FakeEventSource.instances[0].emit(
        "prerequisites_updated",
        JSON.stringify({
          flag_key: "never-configured-flag",
          environment: "production",
          prerequisites: [
            { flag_key: "parent-flag", required_variation: "true" },
          ],
          ts: 9999,
        }),
      ),
    );

    // The unrelated, already-cached flag must be entirely unaffected.
    const result = client.evaluate("child-flag", { userId: "u1" });
    assert.notEqual(result.reason, "PREREQUISITE_FAILED");

    client.disconnect();
  });
});

describe("TombstoneClient — live targeting_rules_updated streaming (end-to-end)", () => {
  /**
   * Mirrors "TombstoneClient — live prerequisites_updated streaming
   * (end-to-end)" above exactly, for the targeting_rules follow-up (PR
   * #245's backend + this PR's SDK consumption). A live
   * "targeting_rules_updated" SSE frame must actually change what
   * evaluate() returns for the affected flag, not just be parsed correctly
   * in isolation (that's already covered by streaming.test.ts's own
   * "SSEStreamClient — targeting_rules_updated dispatch" suite).
   */
  let fakeFetch: FakeFetch;
  let originalFetch: typeof globalThis.fetch;

  beforeEach(() => {
    fakeFetch = new FakeFetch();
    originalFetch = globalThis.fetch;
    (globalThis as unknown as { fetch: unknown }).fetch = fakeFetch.fn;
    (globalThis as unknown as { EventSource: unknown }).EventSource =
      FakeEventSource;
    FakeEventSource.instances = [];
  });

  afterEach(() => {
    (globalThis as unknown as { fetch: unknown }).fetch = originalFetch;
  });

  function snapshotWithChildFlag(): void {
    fakeFetch.snapshotFlags = [
      {
        flag_id: "1",
        flag_key: "child-flag",
        environment: "production",
        enabled: true,
        rollout_pct: 0, // 0% -- without a matching rule, evaluate falls through to defaultValue
        safe_default: "false",
        updated_at: 1,
        // No targeting_rules in the snapshot itself -- the live event is
        // what introduces the rule, proving the update actually reaches
        // the cache rather than the snapshot's own (absent) rules
        // happening to already produce the same outcome.
      },
    ];
  }

  const rulePayload = {
    id: "r1",
    rule_type: "USER",
    attribute: "userId",
    operator: "EQ",
    values: ["u1"],
    variation: "special-variant",
    priority: 0,
  };

  it("a live targeting_rules_updated event newer than the snapshot changes evaluate()'s outcome", async () => {
    fakeFetch.snapshotTs = 1000;
    snapshotWithChildFlag();
    const client = new TombstoneClient(baseConfig());
    await client.connect();

    // Before the live event: child-flag has no targeting rules and 0%
    // rollout, so it falls through to the default value.
    const before = client.evaluate<string>("child-flag", { userId: "u1" });
    assert.notEqual(before.reason, "RULE_MATCH");

    FakeEventSource.instances[0].emit(
      "targeting_rules_updated",
      JSON.stringify({
        flag_key: "child-flag",
        environment: "production",
        targeting_rules: [rulePayload],
        ts: 2000, // newer than the snapshot's ts=1000
      }),
    );

    const after = client.evaluate<string>("child-flag", { userId: "u1" });
    assert.equal(
      after.reason,
      "RULE_MATCH",
      "the live event must actually be applied to the cache and change evaluate()'s outcome",
    );
    assert.equal(after.value, "special-variant");

    client.disconnect();
  });

  it("an event OLDER than the currently-cached ts is rejected -- evaluate() stays unaffected", async () => {
    fakeFetch.snapshotTs = 5000;
    snapshotWithChildFlag();
    const client = new TombstoneClient(baseConfig());
    await client.connect();

    FakeEventSource.instances[0].emit(
      "targeting_rules_updated",
      JSON.stringify({
        flag_key: "child-flag",
        environment: "production",
        targeting_rules: [rulePayload],
        ts: 3000, // OLDER than the snapshot's ts=5000 -- must be rejected
      }),
    );

    const result = client.evaluate<string>("child-flag", { userId: "u1" });
    assert.notEqual(
      result.reason,
      "RULE_MATCH",
      "a stale out-of-order event must not overwrite the newer cached state",
    );

    client.disconnect();
  });

  it("an event whose ts EQUALS the currently-cached ts IS applied -- pins down `<`, not `<=`", async () => {
    fakeFetch.snapshotTs = 5000;
    snapshotWithChildFlag();
    const client = new TombstoneClient(baseConfig());
    await client.connect();

    FakeEventSource.instances[0].emit(
      "targeting_rules_updated",
      JSON.stringify({
        flag_key: "child-flag",
        environment: "production",
        targeting_rules: [rulePayload],
        ts: 5000, // EQUAL to the snapshot's own ts=5000
      }),
    );

    const result = client.evaluate<string>("child-flag", { userId: "u1" });
    assert.equal(
      result.reason,
      "RULE_MATCH",
      "an equal-ts event must be applied, not dropped as stale",
    );
    assert.equal(result.value, "special-variant");

    client.disconnect();
  });

  it("an update for a flag the client has never seen is a no-op (nothing to merge into)", async () => {
    fakeFetch.snapshotTs = 1000;
    snapshotWithChildFlag();
    const client = new TombstoneClient(baseConfig());
    await client.connect();

    assert.doesNotThrow(() =>
      FakeEventSource.instances[0].emit(
        "targeting_rules_updated",
        JSON.stringify({
          flag_key: "never-configured-flag",
          environment: "production",
          targeting_rules: [rulePayload],
          ts: 9999,
        }),
      ),
    );

    // The unrelated, already-cached flag must be entirely unaffected.
    const result = client.evaluate<string>("child-flag", { userId: "u1" });
    assert.notEqual(result.reason, "RULE_MATCH");

    client.disconnect();
  });
});
