import { test, mock, beforeEach, afterEach } from "node:test";
import assert from "node:assert/strict";
import {
  handleBlastRadius,
  handleProposeChangeRequest,
  handleListChangeRequests,
} from "../tools/flags.js";

const API_URL = "http://localhost:8081";
const FIXTURE_BEARER = "fixture-bearer-value";

function jsonResponse(body: unknown, ok = true, status = 200): Response {
  return {
    ok,
    status,
    text: async () => JSON.stringify(body),
  } as unknown as Response;
}

let fetchMock: ReturnType<typeof mock.method>;

beforeEach(() => {
  fetchMock = mock.method(globalThis, "fetch");
});

afterEach(() => {
  fetchMock.mock.restore();
});

// handleBlastRadius previously sent this MCP tool's request to flag-api
// (which has no /api/v1/blast-radius route at all — that endpoint only
// exists on the evaluator service) with entirely wrong query parameter
// names (key/targetState instead of flag_key/environment/rollout_pct), so
// every call silently computed blast radius for an empty flag_key at the
// evaluator's own default 100% rollout, ignoring the flag actually asked
// about.
test("handleBlastRadius queries the evaluator service (not flag-api) with the flag's current rollout_pct, and returns the evaluator's result", async () => {
  const evaluatorResult = { result: { risk_score: "LOW" } };
  fetchMock.mock.mockImplementation(async (url: string) => {
    if (url.includes("/api/v1/environments/snapshot")) {
      return jsonResponse({
        flags: [{ flag_key: "payments.checkout", rollout_pct: 42 }],
      });
    }
    return jsonResponse(evaluatorResult);
  });

  const result = await handleBlastRadius(
    { key: "payments.checkout", targetState: false, environment: "production" },
    API_URL,
    FIXTURE_BEARER,
  );

  // Asserting only on fetch call shape (as an earlier version of this
  // suite did) would still pass if the handler returned the wrong thing
  // entirely — e.g. the snapshot instead of the blast-radius result.
  assert.deepEqual(result, evaluatorResult);

  const calls = fetchMock.mock.calls;
  assert.equal(
    calls.length,
    2,
    "expected a snapshot fetch then a blast-radius fetch",
  );

  const blastCall = calls.find((c) =>
    String(c.arguments[0]).includes("blast-radius"),
  );
  assert.ok(blastCall, "no request was made to the blast-radius endpoint");
  const blastUrl = new URL(String(blastCall!.arguments[0]));
  assert.equal(
    blastUrl.origin,
    "http://localhost:8082",
    "must target the evaluator service, not flag-api",
  );
  assert.equal(blastUrl.searchParams.get("flag_key"), "payments.checkout");
  assert.equal(blastUrl.searchParams.get("environment"), "production");
  assert.equal(blastUrl.searchParams.get("rollout_pct"), "42");
  assert.equal(
    blastUrl.searchParams.get("key"),
    null,
    "must not send the old, unrecognized 'key' param",
  );
  assert.equal(
    blastUrl.searchParams.get("targetState"),
    null,
    "must not send the old, unrecognized 'targetState' param",
  );
});

test("handleBlastRadius preserves a real 0% rollout_pct rather than falling back to 100 (?? not ||)", async () => {
  fetchMock.mock.mockImplementation(async (url: string) => {
    if (url.includes("/api/v1/environments/snapshot")) {
      return jsonResponse({
        flags: [{ flag_key: "payments.checkout", rollout_pct: 0 }],
      });
    }
    return jsonResponse({ result: { risk_score: "LOW" } });
  });

  await handleBlastRadius(
    { key: "payments.checkout", targetState: true, environment: "production" },
    API_URL,
    FIXTURE_BEARER,
  );

  const blastCall = fetchMock.mock.calls.find((c) =>
    String(c.arguments[0]).includes("blast-radius"),
  );
  const blastUrl = new URL(String(blastCall!.arguments[0]));
  assert.equal(
    blastUrl.searchParams.get("rollout_pct"),
    "0",
    "a real 0% rollout_pct must be sent as 0, not coerced to a truthy fallback",
  );
});

test("handleBlastRadius defaults environment to 'production' when omitted", async () => {
  fetchMock.mock.mockImplementation(async (url: string) => {
    if (url.includes("/api/v1/environments/snapshot")) {
      assert.equal(new URL(url).searchParams.get("environment"), "production");
      return jsonResponse({
        flags: [{ flag_key: "payments.checkout", rollout_pct: 10 }],
      });
    }
    return jsonResponse({ result: { risk_score: "LOW" } });
  });

  await handleBlastRadius(
    { key: "payments.checkout", targetState: true },
    API_URL,
    FIXTURE_BEARER,
  );

  const blastCall = fetchMock.mock.calls.find((c) =>
    String(c.arguments[0]).includes("blast-radius"),
  );
  const blastUrl = new URL(String(blastCall!.arguments[0]));
  assert.equal(blastUrl.searchParams.get("environment"), "production");
});

// A silent 100%-rollout fallback for a flag/environment not present in the
// snapshot (GetSnapshot returns 200 + empty flags array for BOTH a
// mistyped flag_key AND a nonexistent environment — there is no 404 to
// distinguish "not found" from "found, 0% rollout") sent a maximally-
// alarming, confidently WRONG query to the evaluator: zero telemetry for a
// key that was never evaluated downgrades Confidence to LOW, and
// traffic_pct_affected=100 + Confidence=LOW satisfies the evaluator's own
// BLOCKED gate — found by adversarial review of an earlier version of this
// fix. Must fail loudly instead.
test("handleBlastRadius throws a clear not-found error instead of silently defaulting to 100% rollout", async () => {
  fetchMock.mock.mockImplementation(async (url: string) => {
    if (url.includes("/api/v1/environments/snapshot")) {
      return jsonResponse({ flags: [] });
    }
    throw new Error("must not reach the evaluator for an unresolved flag");
  });

  await assert.rejects(
    () =>
      handleBlastRadius(
        { key: "unknown.flag", targetState: true, environment: "production" },
        API_URL,
        FIXTURE_BEARER,
      ),
    /not found/i,
  );

  assert.equal(
    fetchMock.mock.calls.length,
    1,
    "must not call the evaluator at all once the flag is confirmed missing",
  );
});

test("handleBlastRadius surfaces a snapshot-fetch failure distinctly, before ever reaching the evaluator", async () => {
  fetchMock.mock.mockImplementation(async (url: string) => {
    if (url.includes("/api/v1/environments/snapshot")) {
      return jsonResponse({ error: "unauthorized" }, false, 401);
    }
    throw new Error(
      "must not reach the evaluator if the snapshot fetch failed",
    );
  });

  await assert.rejects(
    () =>
      handleBlastRadius(
        {
          key: "payments.checkout",
          targetState: true,
          environment: "production",
        },
        API_URL,
        FIXTURE_BEARER,
      ),
    /fetching current rollout state failed/i,
  );
});

test("handleBlastRadius surfaces an evaluator-side failure distinctly from a snapshot failure", async () => {
  fetchMock.mock.mockImplementation(async (url: string) => {
    if (url.includes("/api/v1/environments/snapshot")) {
      return jsonResponse({
        flags: [{ flag_key: "payments.checkout", rollout_pct: 42 }],
      });
    }
    return jsonResponse({ error: "evaluator unavailable" }, false, 503);
  });

  await assert.rejects(
    () =>
      handleBlastRadius(
        {
          key: "payments.checkout",
          targetState: true,
          environment: "production",
        },
        API_URL,
        FIXTURE_BEARER,
      ),
    /evaluator computation failed/i,
  );
});

test("handleProposeChangeRequest posts the right body to /api/v1/change-requests and returns the response", async () => {
  const backendResponse = { id: "cr-1", status: "PENDING" };
  fetchMock.mock.mockImplementation(async () => jsonResponse(backendResponse));

  const result = await handleProposeChangeRequest(
    {
      flag_key: "payments.checkout",
      environment: "production",
      enabled: false,
      rollout_pct: 0,
    },
    API_URL,
    FIXTURE_BEARER,
  );

  assert.deepEqual(result, backendResponse);
  assert.equal(fetchMock.mock.calls.length, 1);
  const [url, opts] = fetchMock.mock.calls[0].arguments as [
    string,
    RequestInit,
  ];
  assert.equal(url, `${API_URL}/api/v1/change-requests`);
  assert.equal(opts.method, "POST");
  assert.deepEqual(JSON.parse(opts.body as string), {
    flag_key: "payments.checkout",
    environment: "production",
    enabled: false,
    rollout_pct: 0,
  });
});

test("handleProposeChangeRequest propagates a validation failure from the backend", async () => {
  fetchMock.mock.mockImplementation(async () =>
    jsonResponse(
      { message: "rollout_pct must be between 0 and 100" },
      false,
      400,
    ),
  );

  await assert.rejects(
    () =>
      handleProposeChangeRequest(
        {
          flag_key: "payments.checkout",
          environment: "production",
          enabled: false,
          rollout_pct: 0,
        },
        API_URL,
        FIXTURE_BEARER,
      ),
    /rollout_pct must be between 0 and 100/,
  );
});

test("handleListChangeRequests defaults to no explicit status filter, passes one through when given, and returns the response", async () => {
  const backendResponse = { requests: [] };
  fetchMock.mock.mockImplementation(async () => jsonResponse(backendResponse));

  const result = await handleListChangeRequests({}, API_URL, FIXTURE_BEARER);
  assert.deepEqual(result, backendResponse);
  assert.equal(
    fetchMock.mock.calls[0].arguments[0],
    `${API_URL}/api/v1/change-requests`,
  );

  await handleListChangeRequests(
    { status: "APPROVED" },
    API_URL,
    FIXTURE_BEARER,
  );
  assert.equal(
    fetchMock.mock.calls[1].arguments[0],
    `${API_URL}/api/v1/change-requests?status=APPROVED`,
  );
});

test("handleListChangeRequests propagates a backend failure", async () => {
  fetchMock.mock.mockImplementation(async () =>
    jsonResponse({ error: "query failed" }, false, 500),
  );

  await assert.rejects(() =>
    handleListChangeRequests({}, API_URL, FIXTURE_BEARER),
  );
});
