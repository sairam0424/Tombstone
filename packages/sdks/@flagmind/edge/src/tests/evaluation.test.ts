/**
 * @tomb-stone/edge — evaluation.ts tests.
 *
 * This package had ZERO tests of any kind before this file (SDK-2 gap).
 *
 * Covers:
 *   - Hash parity with @tombstone/core / @tombstone/eval: the real
 *     hashVersion=1 vectors from packages/sdks/test-contract/vectors.json
 *     (this package only ever evaluates v1 — it has no hashVersion field
 *     on FlagEnvironmentState). Before this fix, `evaluate()` used an
 *     FNV-1a hash that produced a DIFFERENT bucket than every other SDK
 *     for the same flag+user; these tests would have failed against the
 *     old implementation.
 *   - safeDefault-on-OFF (flag.safeDefault wins over the caller's
 *     defaultValue, matching @tombstone/core).
 *   - Rollout boundary conditions (>=100, <=0).
 */

import assert from "assert";
import { readFileSync } from "fs";
import { dirname, join } from "path";
import { fileURLToPath } from "url";
import { evaluate } from "../evaluation.js";
import type { EvaluationContext, FlagEnvironmentState } from "../types.js";

const __dirname = dirname(fileURLToPath(import.meta.url));

// packages/sdks/@flagmind/edge/dist/tests/ -> ../../../../test-contract/vectors.json
// (same relative-path convention as @tombstone/core's contract.test.ts,
// which lives at the same directory depth).
const vectorsPath = join(__dirname, "../../../../test-contract/vectors.json");
const contractData = JSON.parse(readFileSync(vectorsPath, "utf-8"));

interface RolloutVector {
  flag_key: string;
  user_id: string;
  hash_version?: 1 | 2;
  rollout_pct: number;
  expected_in_cohort: boolean;
  note?: string;
}

const v1Vectors: RolloutVector[] = (contractData.vectors ?? []).filter(
  (v: RolloutVector) => (v.hash_version ?? 1) === 1,
);

function makeFlag(flagKey: string, rolloutPct: number): FlagEnvironmentState {
  return {
    flagKey,
    enabled: true,
    rolloutPct,
    safeDefault: "false",
    environment: "test",
  };
}

describe("@tomb-stone/edge — evaluate() hash parity (real cross-SDK v1 contract vectors)", () => {
  assert.ok(v1Vectors.length > 0, "contract vectors.json must have v1 vectors");

  for (const v of v1Vectors) {
    const label = `flag=${v.flag_key} user=${JSON.stringify(v.user_id)} pct=${v.rollout_pct}${v.note ? ` — ${v.note}` : ""}`;
    it(label, () => {
      const flag = makeFlag(v.flag_key, v.rollout_pct);
      const ctx: EvaluationContext = { userId: v.user_id };
      const result = evaluate(flag, ctx, false, v.flag_key);
      const inCohort = result.value === true;
      assert.strictEqual(
        inCohort,
        v.expected_in_cohort,
        `Expected ${v.expected_in_cohort} but got ${inCohort} (reason=${result.reason})`,
      );
    });
  }
});

// test-contract/vectors.json has zero vectors with characters outside the
// Basic Multilingual Plane, so the surrogate-pair bug this regression test
// guards against (found by adversarial review of PR #207 -- the vendored
// murmur32's hand-rolled UTF-8 encoder treated each half of a surrogate
// pair as its own 3-byte code point instead of combining them into one
// 4-byte one) was undetected by the contract-vector suite above. Expected
// buckets independently computed via the real `murmurhash` npm package
// (@tombstone/core's own dependency) -- not hand-calculated.
describe("@tomb-stone/edge — evaluate() hash parity (supplementary-plane characters)", () => {
  it("emoji flagKey — bucket matches the real murmurhash npm package (85)", () => {
    // murmurhash.v3('flag-😀' + 'user-123') % 100 === 85
    const inCohort = makeFlag("flag-😀", 86);
    const result = evaluate<boolean>(
      inCohort,
      { userId: "user-123" },
      false,
      "flag-😀",
    );
    assert.strictEqual(result.value, true, "bucket 85 < 86 must be in cohort");

    const excludedFlag = makeFlag("flag-😀", 85);
    const excluded = evaluate<boolean>(
      excludedFlag,
      { userId: "user-123" },
      false,
      "flag-😀",
    );
    assert.strictEqual(
      excluded.value,
      false,
      "bucket 85 >= 85 must be excluded",
    );
  });

  it("emoji userId — bucket matches the real murmurhash npm package (21)", () => {
    // murmurhash.v3('celebration-flag' + 'user-🎉-42') % 100 === 21
    const inCohort = makeFlag("celebration-flag", 22);
    const result = evaluate<boolean>(
      inCohort,
      { userId: "user-🎉-42" },
      false,
      "celebration-flag",
    );
    assert.strictEqual(result.value, true, "bucket 21 < 22 must be in cohort");

    const excludedFlag = makeFlag("celebration-flag", 21);
    const excluded = evaluate<boolean>(
      excludedFlag,
      { userId: "user-🎉-42" },
      false,
      "celebration-flag",
    );
    assert.strictEqual(
      excluded.value,
      false,
      "bucket 21 >= 21 must be excluded",
    );
  });
});

describe("@tomb-stone/edge — evaluate()", () => {
  it("returns ERROR + defaultValue when flag is not found", () => {
    const result = evaluate(
      undefined,
      { userId: "u" },
      "fallback",
      "missing-flag",
    );
    assert.strictEqual(result.reason, "ERROR");
    assert.strictEqual(result.value, "fallback");
    assert.strictEqual(result.fromCache, false);
  });

  it("OFF returns flag.safeDefault converted to defaultValue's type, NOT the caller's defaultValue verbatim", () => {
    // safeDefault='true' but the caller passes false — matches @tombstone/core.
    const flag: FlagEnvironmentState = {
      flagKey: "my-flag",
      enabled: false,
      rolloutPct: 100,
      safeDefault: "true",
      environment: "test",
    };
    const result = evaluate<boolean>(flag, { userId: "u" }, false, "my-flag");
    assert.strictEqual(result.reason, "OFF");
    assert.strictEqual(result.value, true);
  });

  it("OFF with a string defaultValue: safeDefault is used as-is (string identity)", () => {
    const flag: FlagEnvironmentState = {
      flagKey: "my-flag",
      enabled: false,
      rolloutPct: 100,
      safeDefault: "archived",
      environment: "test",
    };
    const result = evaluate<string>(
      flag,
      { userId: "u" },
      "my-default",
      "my-flag",
    );
    assert.strictEqual(result.reason, "OFF");
    assert.strictEqual(result.value, "archived");
  });

  it("returns FALLTHROUGH + true at >=100% rollout without hashing", () => {
    const flag = makeFlag("my-flag", 100);
    const result = evaluate<boolean>(flag, { userId: "u" }, false, "my-flag");
    assert.strictEqual(result.reason, "FALLTHROUGH");
    assert.strictEqual(result.value, true);
  });

  it("returns FALLTHROUGH + defaultValue at <=0% rollout without hashing", () => {
    const flag = makeFlag("my-flag", 0);
    const result = evaluate<boolean>(flag, { userId: "u" }, false, "my-flag");
    assert.strictEqual(result.reason, "FALLTHROUGH");
    assert.strictEqual(result.value, false);
  });

  it("is deterministic — same user always gets the same bucket", () => {
    const flag = makeFlag("checkout-v2", 50);
    const ctx: EvaluationContext = { userId: "user-abc-123" };
    const first = evaluate<boolean>(flag, ctx, false, "checkout-v2");
    for (let i = 0; i < 20; i++) {
      const next = evaluate<boolean>(flag, ctx, false, "checkout-v2");
      assert.strictEqual(next.value, first.value);
      assert.strictEqual(next.reason, first.reason);
    }
  });
});
