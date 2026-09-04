/**
 * @tombstone/eval — Evaluation engine tests.
 *
 * Covers:
 *   - The real cross-language rollout vectors from
 *     packages/sdks/test-contract/vectors.json (loaded directly, not
 *     hand-copied — a prior version of this file had self-derived v2
 *     expected values computed from this engine's OWN (buggy) hashV2,
 *     which is exactly why a real hashV2 algorithm bug went undetected
 *     here: the test data and the code under test shared the same bug).
 *   - evaluate() OFF / FALLTHROUGH / RULE_MATCH reasons.
 *   - safeDefault-on-OFF (flag.safeDefault wins over the caller's
 *     defaultValue, matching @tombstone/core exactly).
 *   - Ascending rule-priority ordering (0 = highest, matching
 *     @tombstone/core — this engine used to sort descending).
 */

import assert from "assert";
import { readFileSync } from "fs";
import { dirname, join } from "path";
import { fileURLToPath } from "url";
import { evaluate, isInRollout } from "../index.js";
import type { EvalContext, FlagState } from "../index.js";

const __dirname = dirname(fileURLToPath(import.meta.url));

// packages/sdk-wasm/dist/tests/ -> ../../../sdks/test-contract/vectors.json
// (same relative-path convention as @tombstone/core's contract.test.ts).
const vectorsPath = join(__dirname, "../../../sdks/test-contract/vectors.json");
const contractData = JSON.parse(readFileSync(vectorsPath, "utf-8"));

interface RolloutVector {
  flag_key: string;
  user_id: string;
  hash_version?: 1 | 2;
  rollout_pct: number;
  expected_in_cohort: boolean;
  note?: string;
}

const rolloutVectors: RolloutVector[] = contractData.vectors ?? [];

// ---------------------------------------------------------------------------
// Contract vectors — real vectors.json, both hashVersion 1 and 2
// ---------------------------------------------------------------------------

describe("@tombstone/eval — isInRollout (real cross-SDK contract vectors)", () => {
  assert.ok(
    rolloutVectors.length > 0,
    "contract vectors.json must not be empty",
  );

  for (const v of rolloutVectors) {
    const hashVersion = v.hash_version ?? 1;
    const label = `flag=${v.flag_key} user=${JSON.stringify(v.user_id)} pct=${v.rollout_pct} v${hashVersion}${v.note ? ` — ${v.note}` : ""}`;
    it(label, () => {
      const result = isInRollout(
        v.flag_key,
        v.user_id,
        v.rollout_pct,
        hashVersion,
      );
      assert.strictEqual(
        result,
        v.expected_in_cohort,
        `Expected ${v.expected_in_cohort} but got ${result}`,
      );
    });
  }
});

// ---------------------------------------------------------------------------
// isInRollout (v1) — supplementary-plane (emoji) inputs
//
// test-contract/vectors.json has zero vectors with characters outside the
// Basic Multilingual Plane, so the surrogate-pair bug this regression test
// guards against (found by adversarial review of PR #207 -- murmur32's
// hand-rolled UTF-8 encoder treated each half of a surrogate pair as its
// own 3-byte code point instead of combining them into one 4-byte one)
// was undetected by the contract-vector suite above. Expected buckets
// independently computed via the real `murmurhash` npm package
// (@tombstone/core's own dependency) -- not hand-calculated.
// ---------------------------------------------------------------------------

describe("@tombstone/eval — isInRollout (v1, supplementary-plane characters)", () => {
  it("emoji flagKey — bucket matches the real murmurhash npm package (85)", () => {
    // murmurhash.v3('flag-😀' + 'user-123') % 100 === 85
    const result = isInRollout("flag-😀", "user-123", 86, 1);
    assert.strictEqual(result, true, "bucket 85 < 86 must be in cohort");
    const excluded = isInRollout("flag-😀", "user-123", 85, 1);
    assert.strictEqual(excluded, false, "bucket 85 >= 85 must be excluded");
  });

  it("emoji userId — bucket matches the real murmurhash npm package (21)", () => {
    // murmurhash.v3('celebration-flag' + 'user-🎉-42') % 100 === 21
    const result = isInRollout("celebration-flag", "user-🎉-42", 22, 1);
    assert.strictEqual(result, true, "bucket 21 < 22 must be in cohort");
    const excluded = isInRollout("celebration-flag", "user-🎉-42", 21, 1);
    assert.strictEqual(excluded, false, "bucket 21 >= 21 must be excluded");
  });
});

// ---------------------------------------------------------------------------
// evaluate()
// ---------------------------------------------------------------------------

describe("@tombstone/eval — evaluate()", () => {
  const baseFlag: FlagState = {
    flagKey: "my-flag",
    enabled: true,
    rolloutPct: 100,
    safeDefault: "false",
  };

  // -------------------------------------------------------------------
  // OFF reason — uses flag.safeDefault, not the caller's defaultValue
  // -------------------------------------------------------------------
  it("returns OFF reason when flag.enabled is false", () => {
    const flag: FlagState = { ...baseFlag, enabled: false };
    const result = evaluate(flag, { userId: "user-1" }, false);
    assert.strictEqual(result.reason, "OFF");
    assert.strictEqual(result.value, false);
  });

  it("OFF returns flag.safeDefault converted to defaultValue's type, NOT the caller's defaultValue verbatim", () => {
    // safeDefault='true' but the caller passes false — the flag's own
    // configured off-state value must win, matching @tombstone/core.
    const flag: FlagState = {
      ...baseFlag,
      enabled: false,
      safeDefault: "true",
    };
    const result = evaluate(flag, { userId: "user-1" }, false);
    assert.strictEqual(result.reason, "OFF");
    assert.strictEqual(result.value, true);
  });

  it("OFF with a string defaultValue: safeDefault is used as-is (string identity)", () => {
    const flag: FlagState = {
      ...baseFlag,
      enabled: false,
      safeDefault: "archived",
    };
    const result = evaluate(flag, {}, "my-default");
    assert.strictEqual(result.value, "archived");
    assert.strictEqual(result.reason, "OFF");
  });

  it("OFF with a numeric defaultValue: safeDefault is parsed as a number", () => {
    const flag: FlagState = { ...baseFlag, enabled: false, safeDefault: "42" };
    const result = evaluate(flag, {}, 0);
    assert.strictEqual(result.value, 42);
    assert.strictEqual(result.reason, "OFF");
  });

  it("OFF: an unparseable numeric safeDefault falls back to defaultValue rather than NaN", () => {
    // NOTE: this scenario is definitionally identical between the fixed
    // and pre-fix `evaluate()` -- parseSafeDefault's own NaN-fallback
    // path returns the caller's defaultValue verbatim, same as the old
    // code's "ignore safeDefault entirely" behavior. This test verifies
    // the fallback path itself is correct (doesn't return NaN/throw), not
    // regression coverage for the OFF-ignores-safeDefault bug -- the
    // preceding test ("safeDefault is parsed as a number") is the one
    // that actually distinguishes fixed from unfixed behavior for numeric
    // defaults (found by adversarial review of PR #207).
    const flag: FlagState = {
      ...baseFlag,
      enabled: false,
      safeDefault: "not-a-number",
    };
    const result = evaluate(flag, {}, 7);
    assert.strictEqual(result.value, 7);
    assert.strictEqual(result.reason, "OFF");
  });

  it("returns OFF with boolean default value false", () => {
    const flag: FlagState = { ...baseFlag, enabled: false };
    const result = evaluate(flag, { userId: "any" }, false);
    assert.strictEqual(result.reason, "OFF");
    assert.strictEqual(result.value, false);
  });

  // -------------------------------------------------------------------
  // FALLTHROUGH reason
  // -------------------------------------------------------------------
  it("returns FALLTHROUGH + true at 100% rollout", () => {
    const result = evaluate(baseFlag, { userId: "anyone" }, false);
    assert.strictEqual(result.reason, "FALLTHROUGH");
    assert.strictEqual(result.value, true);
  });

  it("returns FALLTHROUGH + defaultValue when user not in 0% rollout", () => {
    const flag: FlagState = { ...baseFlag, rolloutPct: 0 };
    const result = evaluate(flag, { userId: "user-1" }, false);
    assert.strictEqual(result.reason, "FALLTHROUGH");
    assert.strictEqual(result.value, false);
  });

  it("FALLTHROUGH is consistent — same user always gets same bucket", () => {
    const flag: FlagState = {
      ...baseFlag,
      flagKey: "checkout-v2",
      rolloutPct: 50,
    };
    const ctx: EvalContext = { userId: "user-abc-123" };
    const first = evaluate(flag, ctx, false);
    for (let i = 0; i < 20; i++) {
      const next = evaluate(flag, ctx, false);
      assert.strictEqual(
        next.value,
        first.value,
        "Evaluation must be deterministic",
      );
      assert.strictEqual(next.reason, first.reason);
    }
  });

  it("v1 contract: payment-gateway-fee-display + user-abc-123 at 50% → in cohort", () => {
    const flag: FlagState = {
      flagKey: "payment-gateway-fee-display",
      enabled: true,
      rolloutPct: 50,
      safeDefault: "false",
      hashVersion: 1,
    };
    const result = evaluate(flag, { userId: "user-abc-123" }, false);
    assert.strictEqual(result.reason, "FALLTHROUGH");
    assert.strictEqual(result.value, true);
  });

  it("v1 contract: checkout-v2 + user-abc-123 at 1% → NOT in cohort", () => {
    const flag: FlagState = {
      flagKey: "checkout-v2",
      enabled: true,
      rolloutPct: 1,
      safeDefault: "false",
      hashVersion: 1,
    };
    const result = evaluate(flag, { userId: "user-abc-123" }, false);
    assert.strictEqual(result.reason, "FALLTHROUGH");
    assert.strictEqual(result.value, false);
  });

  // -------------------------------------------------------------------
  // RULE_MATCH reason
  // -------------------------------------------------------------------
  it("returns RULE_MATCH when IN operator matches context attribute", () => {
    const flag: FlagState = {
      ...baseFlag,
      rolloutPct: 0,
      targetingRules: [
        {
          attribute: "plan",
          operator: "IN",
          values: ["pro", "enterprise"],
          variation: "enabled",
          priority: 10,
        },
      ],
    };
    const ctx: EvalContext = { userId: "user-1", plan: "pro" };
    const result = evaluate(flag, ctx, false);
    assert.strictEqual(result.reason, "RULE_MATCH");
    assert.strictEqual(result.value, "enabled");
  });

  it("RULE_MATCH returns the matched rule variation", () => {
    const flag: FlagState = {
      ...baseFlag,
      rolloutPct: 0,
      targetingRules: [
        {
          attribute: "country",
          operator: "EQ",
          values: ["US"],
          variation: "us-variant",
          priority: 5,
        },
      ],
    };
    const ctx: EvalContext = { userId: "u", country: "US" };
    const result = evaluate(flag, ctx, false);
    assert.strictEqual(result.reason, "RULE_MATCH");
    assert.strictEqual(result.value, "us-variant");
  });

  it("lower priority NUMBER wins over higher priority number — 0 is highest, matching @tombstone/core", () => {
    // Regression test: this engine used to sort DESCENDING (higher number
    // wins), the opposite of @tombstone/core's documented "0 = highest"
    // convention — two rules that both match must resolve to the one
    // with the SMALLER priority number.
    const flag: FlagState = {
      ...baseFlag,
      rolloutPct: 0,
      targetingRules: [
        {
          attribute: "plan",
          operator: "IN",
          values: ["pro"],
          variation: "priority-10",
          priority: 10,
        },
        {
          attribute: "plan",
          operator: "IN",
          values: ["pro"],
          variation: "priority-1",
          priority: 1,
        },
        {
          attribute: "plan",
          operator: "IN",
          values: ["pro"],
          variation: "priority-0",
          priority: 0,
        },
      ],
    };
    const ctx: EvalContext = { userId: "u", plan: "pro" };
    const result = evaluate(flag, ctx, false);
    assert.strictEqual(result.reason, "RULE_MATCH");
    assert.strictEqual(result.value, "priority-0");
  });

  it("falls through when targeting rule does not match", () => {
    const flag: FlagState = {
      ...baseFlag,
      rolloutPct: 100,
      targetingRules: [
        {
          attribute: "plan",
          operator: "IN",
          values: ["enterprise"],
          variation: "enabled",
          priority: 10,
        },
      ],
    };
    // plan=pro not in ['enterprise'], falls through to 100% rollout
    const ctx: EvalContext = { userId: "user-abc-123", plan: "pro" };
    const result = evaluate(flag, ctx, false);
    assert.strictEqual(result.reason, "FALLTHROUGH");
    assert.strictEqual(result.value, true);
  });

  it("NOT_IN operator: RULE_MATCH when attribute NOT in list", () => {
    const flag: FlagState = {
      ...baseFlag,
      rolloutPct: 0,
      targetingRules: [
        {
          attribute: "plan",
          operator: "NOT_IN",
          values: ["enterprise"],
          variation: "free-variant",
          priority: 5,
        },
      ],
    };
    const ctx: EvalContext = { userId: "u", plan: "free" };
    const result = evaluate(flag, ctx, false);
    assert.strictEqual(result.reason, "RULE_MATCH");
    assert.strictEqual(result.value, "free-variant");
  });

  it("PREFIX operator: RULE_MATCH when attribute starts with value", () => {
    const flag: FlagState = {
      ...baseFlag,
      rolloutPct: 0,
      targetingRules: [
        {
          attribute: "userId",
          operator: "PREFIX",
          values: ["beta-"],
          variation: "beta",
          priority: 5,
        },
      ],
    };
    const ctx: EvalContext = { userId: "beta-user-42" };
    const result = evaluate(flag, ctx, false);
    assert.strictEqual(result.reason, "RULE_MATCH");
    assert.strictEqual(result.value, "beta");
  });

  it("GTE operator: numeric string comparison", () => {
    const flag: FlagState = {
      ...baseFlag,
      rolloutPct: 0,
      targetingRules: [
        {
          attribute: "age",
          operator: "GTE",
          values: ["18"],
          variation: "adult",
          priority: 0,
        },
      ],
    };
    const result = evaluate(flag, { userId: "u", age: "18" }, false);
    assert.strictEqual(result.reason, "RULE_MATCH");
    assert.strictEqual(result.value, "adult");
  });

  it("GT operator does not treat a non-numeric-looking string as its leading digits", () => {
    // Regression test: this engine used to parse comparison operands with
    // parseFloat, which happily parses "5px" as 5 — @tombstone/core uses
    // Number()+isFinite, which correctly treats "5px" as not-a-number and
    // never matches. A rule with a non-numeric attribute value must be
    // inconclusive (no match), not silently coerced.
    const flag: FlagState = {
      ...baseFlag,
      rolloutPct: 100,
      targetingRules: [
        {
          attribute: "width",
          operator: "GT",
          values: ["3"],
          variation: "wide",
          priority: 0,
        },
      ],
    };
    const result = evaluate(flag, { userId: "u", width: "5px" }, false);
    // No match -> falls through to the 100% rollout, not RULE_MATCH.
    assert.strictEqual(result.reason, "FALLTHROUGH");
    assert.strictEqual(result.value, true);
  });

  it("GT operator matches a RAW boolean attribute via Number(true)=1, matching @tombstone/core", () => {
    // Regression test: this engine used to pre-stringify the context
    // attribute via String(rawValue) before dispatch, so Number(value)
    // for LT/LTE/GT/GTE ran on the STRING "true"/"false" (NaN, never
    // matches) instead of the raw boolean (Number(true)=1, Number(false)=0,
    // both finite) that @tombstone/core's applyOperator receives directly.
    const flag: FlagState = {
      ...baseFlag,
      rolloutPct: 0,
      targetingRules: [
        {
          attribute: "isPremium",
          operator: "GT",
          values: [0],
          variation: "premium",
          priority: 0,
        },
      ],
    };
    const result = evaluate(flag, { userId: "u", isPremium: true }, false);
    assert.strictEqual(result.reason, "RULE_MATCH");
    assert.strictEqual(result.value, "premium");
  });

  it("CONTAINS operator never matches a RAW number/boolean attribute, matching @tombstone/core's typeof guard", () => {
    // Regression test: pre-stringifying the attribute meant a number like
    // 25 became the string "25", which CONTAINS/PREFIX/SUFFIX could then
    // substring-match against -- @tombstone/core's applyOperator requires
    // `typeof value === 'string'` for these operators and rejects a raw
    // number/boolean outright, regardless of what its stringified form
    // would look like.
    const flag: FlagState = {
      ...baseFlag,
      rolloutPct: 100,
      targetingRules: [
        {
          attribute: "age",
          operator: "CONTAINS",
          values: ["2"],
          variation: "should-never-match",
          priority: 0,
        },
      ],
    };
    const result = evaluate(flag, { userId: "u", age: 25 }, false);
    // No match (age is a number, not a string) -> falls through to rollout.
    assert.strictEqual(result.reason, "FALLTHROUGH");
    assert.strictEqual(result.value, true);
  });

  it("RULE_MATCH skips rule when context attribute is explicitly null", () => {
    // Distinct from the "missing" case below (key absent entirely) --
    // evaluateRule's guard is `rawValue === undefined || rawValue ===
    // null`; this exercises the null half specifically.
    const flag: FlagState = {
      ...baseFlag,
      rolloutPct: 100,
      targetingRules: [
        {
          attribute: "plan",
          operator: "IN",
          values: ["pro"],
          variation: "enabled",
          priority: 10,
        },
      ],
    };
    const ctx: EvalContext = { userId: "user-abc-123", plan: null };
    const result = evaluate(flag, ctx, false);
    assert.strictEqual(result.reason, "FALLTHROUGH");
    assert.strictEqual(result.value, true);
  });

  it("RULE_MATCH skips rule when context attribute is missing", () => {
    const flag: FlagState = {
      ...baseFlag,
      rolloutPct: 100,
      targetingRules: [
        {
          attribute: "plan",
          operator: "IN",
          values: ["pro"],
          variation: "enabled",
          priority: 10,
        },
      ],
    };
    // No 'plan' attribute in context — rule does not match, falls through to rollout
    const ctx: EvalContext = { userId: "user-abc-123" };
    const result = evaluate(flag, ctx, false);
    assert.strictEqual(result.reason, "FALLTHROUGH");
    assert.strictEqual(result.value, true);
  });

  it("rules array empty — falls through to rollout", () => {
    const flag: FlagState = {
      ...baseFlag,
      rolloutPct: 100,
      targetingRules: [],
    };
    const result = evaluate(flag, { userId: "any" }, false);
    assert.strictEqual(result.reason, "FALLTHROUGH");
    assert.strictEqual(result.value, true);
  });

  it("disabled flag with rules — returns OFF (safeDefault) without evaluating rules", () => {
    const flag: FlagState = {
      ...baseFlag,
      enabled: false,
      safeDefault: "true",
      targetingRules: [
        {
          attribute: "plan",
          operator: "IN",
          values: ["pro"],
          variation: "enabled",
          priority: 10,
        },
      ],
    };
    const result = evaluate(flag, { userId: "u", plan: "pro" }, false);
    assert.strictEqual(result.reason, "OFF");
    assert.strictEqual(result.value, true);
  });
});
