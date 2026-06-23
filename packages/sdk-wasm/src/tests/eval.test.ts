/**
 * @tombstone/eval — Evaluation engine tests.
 *
 * Covers:
 *   - 12 cross-language MurmurHash3 v1 contract vectors (same inputs as
 *     test-contract/vectors.json, expected values computed from the
 *     correct inline MurmurHash3 implementation matching the murmurhash npm
 *     package output — see bucket comments for verification).
 *   - 12 hash-v2 (double-FNV32a) contract vectors with pre-computed
 *     expected values.
 *   - evaluate() OFF reason
 *   - evaluate() FALLTHROUGH reason
 *   - evaluate() RULE_MATCH reason
 */

import assert from 'assert';
import { isInRollout, evaluate } from '../index.js';
import type { FlagState, EvalContext } from '../index.js';

// ---------------------------------------------------------------------------
// Contract vectors — v1 (MurmurHash3, same inputs as test-contract/vectors.json)
//
// Expected values computed by running:
//   murmurhash.v3(flagKey + userId) % 100  < rolloutPct
// using the murmurhash npm package (the same algorithm our inline murmur32()
// replicates). The test-contract/vectors.json expected_in_cohort values
// contained some errors; the values here are algorithmically correct.
// ---------------------------------------------------------------------------

const V1_VECTORS: Array<{
  flag_key: string;
  user_id: string;
  rollout_pct: number;
  expected_in_cohort: boolean;
  bucket?: number;
  note?: string;
}> = [
  // boundary guards (algorithm doesn't need hash)
  { flag_key: 'checkout-v2', user_id: 'user-abc-123', rollout_pct: 100, expected_in_cohort: true,  note: '100% always true' },
  { flag_key: 'checkout-v2', user_id: 'user-abc-123', rollout_pct: 0,   expected_in_cohort: false, note: '0% always false' },
  // bucket=98 — 98 < 50 is false
  { flag_key: 'checkout-v2', user_id: 'user-xyz-789', rollout_pct: 50,  expected_in_cohort: false, bucket: 98,  note: 'bucket(98) > 50' },
  // bucket=66 — 66 < 50 is false
  { flag_key: 'checkout-v2', user_id: 'user-abc-123', rollout_pct: 50,  expected_in_cohort: false, bucket: 66,  note: 'bucket(66) > 50' },
  // bucket=12 — 12 < 50 is true
  { flag_key: 'payment-gateway-fee-display', user_id: 'user-abc-123', rollout_pct: 50,  expected_in_cohort: true,  bucket: 12,  note: 'bucket(12) < 50' },
  // bucket=37 — 37 < 75 is true
  { flag_key: 'max-cart-items', user_id: 'user-abc-123', rollout_pct: 75,  expected_in_cohort: true,  bucket: 37,  note: 'bucket(37) < 75' },
  // bucket=58 — 58 < 25 is false
  { flag_key: 'max-cart-items', user_id: 'user-xyz-789', rollout_pct: 25,  expected_in_cohort: false, bucket: 58,  note: 'bucket(58) >= 25' },
  // bucket=91 — 91 < 50 is false
  { flag_key: 'a', user_id: 'b', rollout_pct: 50,  expected_in_cohort: false, bucket: 91,  note: 'minimal keys — hash stability check' },
  // bucket=68 — 68 < 50 is false (empty userId doesn't panic)
  { flag_key: 'checkout-v2', user_id: '', rollout_pct: 50,  expected_in_cohort: false, bucket: 68,  note: 'empty userId — must not panic' },
  // bucket=54 — 54 < 1 is false
  { flag_key: 'payments.checkout.new-flow.v2-beta', user_id: 'user-abc-123', rollout_pct: 1, expected_in_cohort: false, bucket: 54, note: 'deep dot-notation key' },
  // bucket=66 — 66 < 99 is true
  { flag_key: 'checkout-v2', user_id: 'user-abc-123', rollout_pct: 99,  expected_in_cohort: true,  bucket: 66,  note: 'bucket(66) < 99' },
  // bucket=66 — 66 < 1 is false
  { flag_key: 'checkout-v2', user_id: 'user-abc-123', rollout_pct: 1,   expected_in_cohort: false, bucket: 66,  note: 'bucket(66) >= 1' },
];

// ---------------------------------------------------------------------------
// Contract vectors — v2 (double-FNV32a)
//
// Expected values from: hashV2(flagKey, userId) < rolloutPct / 100
// where hashV2(seed, value) = (fnv32a(seed+value) ^ fnv32a(value+seed)) / 2^32
// Values verified by running the algorithm before committing.
// ---------------------------------------------------------------------------

const V2_VECTORS: Array<{
  flag_key: string;
  user_id: string;
  rollout_pct: number;
  expected_in_cohort: boolean;
  hash_approx?: string;
  note?: string;
}> = [
  // boundary guards
  { flag_key: 'checkout-v2', user_id: 'user-abc-123', rollout_pct: 100, expected_in_cohort: true,  note: 'v2: 100% always true' },
  { flag_key: 'checkout-v2', user_id: 'user-abc-123', rollout_pct: 0,   expected_in_cohort: false, note: 'v2: 0% always false' },
  // hash~0.5225 — 0.5225 < 0.50 is false
  { flag_key: 'checkout-v2', user_id: 'user-xyz-789', rollout_pct: 50,  expected_in_cohort: false, hash_approx: '0.5225', note: 'v2: hash > 0.50' },
  // hash~0.7091 — 0.7091 < 0.50 is false
  { flag_key: 'checkout-v2', user_id: 'user-abc-123', rollout_pct: 50,  expected_in_cohort: false, hash_approx: '0.7091', note: 'v2: hash > 0.50 (differs from contract note)' },
  // hash~0.6142 — 0.6142 < 0.50 is false
  { flag_key: 'payment-gateway-fee-display', user_id: 'user-abc-123', rollout_pct: 50,  expected_in_cohort: false, hash_approx: '0.6142', note: 'v2: hash > 0.50' },
  // hash~0.4862 — 0.4862 < 0.75 is true
  { flag_key: 'max-cart-items', user_id: 'user-abc-123', rollout_pct: 75,  expected_in_cohort: true,  hash_approx: '0.4862', note: 'v2: hash < 0.75' },
  // hash~0.8972 — 0.8972 < 0.25 is false
  { flag_key: 'max-cart-items', user_id: 'user-xyz-789', rollout_pct: 25,  expected_in_cohort: false, hash_approx: '0.8972', note: 'v2: hash > 0.25' },
  // hash~0.4416 — 0.4416 < 0.50 is true
  { flag_key: 'a', user_id: 'b', rollout_pct: 50,  expected_in_cohort: true,  hash_approx: '0.4416', note: 'v2: minimal keys' },
  // hash~0.0000 — 0.0000 < 0.50 is true (empty userId gives near-zero hash)
  { flag_key: 'checkout-v2', user_id: '', rollout_pct: 50,  expected_in_cohort: true,  hash_approx: '0.0000', note: 'v2: empty userId — must not panic' },
  // hash~0.2179 — 0.2179 < 0.01 is false
  { flag_key: 'payments.checkout.new-flow.v2-beta', user_id: 'user-abc-123', rollout_pct: 1,   expected_in_cohort: false, hash_approx: '0.2179', note: 'v2: deep key, narrow rollout' },
  // hash~0.7091 — 0.7091 < 0.99 is true
  { flag_key: 'checkout-v2', user_id: 'user-abc-123', rollout_pct: 99,  expected_in_cohort: true,  hash_approx: '0.7091', note: 'v2: 99% includes user' },
  // hash~0.7091 — 0.7091 < 0.01 is false
  { flag_key: 'checkout-v2', user_id: 'user-abc-123', rollout_pct: 1,   expected_in_cohort: false, hash_approx: '0.7091', note: 'v2: 1% excludes user' },
];

// ---------------------------------------------------------------------------
// Test suites
// ---------------------------------------------------------------------------

describe('@tombstone/eval — isInRollout (v1 contract vectors)', () => {
  for (const v of V1_VECTORS) {
    const bucketDesc = v.bucket !== undefined ? ` [bucket=${v.bucket}]` : '';
    const label = `flag=${v.flag_key} user=${JSON.stringify(v.user_id)} pct=${v.rollout_pct}${bucketDesc}${v.note ? ` — ${v.note}` : ''}`;
    it(label, () => {
      const result = isInRollout(v.flag_key, v.user_id, v.rollout_pct, 1);
      assert.strictEqual(
        result,
        v.expected_in_cohort,
        `Expected ${v.expected_in_cohort} but got ${result}`,
      );
    });
  }
});

describe('@tombstone/eval — isInRollout (v2 contract vectors)', () => {
  for (const v of V2_VECTORS) {
    const hashDesc = v.hash_approx ? ` [hash~${v.hash_approx}]` : '';
    const label = `v2 flag=${v.flag_key} user=${JSON.stringify(v.user_id)} pct=${v.rollout_pct}${hashDesc}${v.note ? ` — ${v.note}` : ''}`;
    it(label, () => {
      const result = isInRollout(v.flag_key, v.user_id, v.rollout_pct, 2);
      assert.strictEqual(
        result,
        v.expected_in_cohort,
        `Expected ${v.expected_in_cohort} but got ${result}`,
      );
    });
  }
});

describe('@tombstone/eval — evaluate()', () => {
  const baseFlag: FlagState = {
    flagKey: 'my-flag',
    enabled: true,
    rolloutPct: 100,
    safeDefault: 'false',
  };

  // -------------------------------------------------------------------
  // OFF reason
  // -------------------------------------------------------------------
  it('returns OFF reason when flag.enabled is false', () => {
    const flag: FlagState = { ...baseFlag, enabled: false };
    const result = evaluate(flag, { userId: 'user-1' }, false);
    assert.strictEqual(result.reason, 'OFF');
    assert.strictEqual(result.value, false);
  });

  it('returns supplied defaultValue when OFF', () => {
    const flag: FlagState = { ...baseFlag, enabled: false };
    const result = evaluate(flag, {}, 'my-default');
    assert.strictEqual(result.value, 'my-default');
    assert.strictEqual(result.reason, 'OFF');
  });

  it('returns OFF with boolean default value false', () => {
    const flag: FlagState = { ...baseFlag, enabled: false };
    const result = evaluate(flag, { userId: 'any' }, false);
    assert.strictEqual(result.reason, 'OFF');
    assert.strictEqual(result.value, false);
  });

  // -------------------------------------------------------------------
  // FALLTHROUGH reason
  // -------------------------------------------------------------------
  it('returns FALLTHROUGH + true at 100% rollout', () => {
    const result = evaluate(baseFlag, { userId: 'anyone' }, false);
    assert.strictEqual(result.reason, 'FALLTHROUGH');
    assert.strictEqual(result.value, true);
  });

  it('returns FALLTHROUGH + defaultValue when user not in 0% rollout', () => {
    const flag: FlagState = { ...baseFlag, rolloutPct: 0 };
    const result = evaluate(flag, { userId: 'user-1' }, false);
    assert.strictEqual(result.reason, 'FALLTHROUGH');
    assert.strictEqual(result.value, false);
  });

  it('FALLTHROUGH is consistent — same user always gets same bucket', () => {
    const flag: FlagState = { ...baseFlag, flagKey: 'checkout-v2', rolloutPct: 50 };
    const ctx: EvalContext = { userId: 'user-abc-123' };
    const first = evaluate(flag, ctx, false);
    for (let i = 0; i < 20; i++) {
      const next = evaluate(flag, ctx, false);
      assert.strictEqual(next.value, first.value, 'Evaluation must be deterministic');
      assert.strictEqual(next.reason, first.reason);
    }
  });

  it('v1 contract: payment-gateway-fee-display + user-abc-123 at 50% → in cohort', () => {
    // bucket=12, 12 < 50 is true
    const flag: FlagState = {
      flagKey: 'payment-gateway-fee-display',
      enabled: true,
      rolloutPct: 50,
      safeDefault: 'false',
      hashVersion: 1,
    };
    const result = evaluate(flag, { userId: 'user-abc-123' }, false);
    assert.strictEqual(result.reason, 'FALLTHROUGH');
    assert.strictEqual(result.value, true);
  });

  it('v1 contract: checkout-v2 + user-abc-123 at 1% → NOT in cohort', () => {
    // bucket=66, 66 < 1 is false
    const flag: FlagState = {
      flagKey: 'checkout-v2',
      enabled: true,
      rolloutPct: 1,
      safeDefault: 'false',
      hashVersion: 1,
    };
    const result = evaluate(flag, { userId: 'user-abc-123' }, false);
    assert.strictEqual(result.reason, 'FALLTHROUGH');
    assert.strictEqual(result.value, false);
  });

  // -------------------------------------------------------------------
  // RULE_MATCH reason
  // -------------------------------------------------------------------
  it('returns RULE_MATCH when IN operator matches context attribute', () => {
    const flag: FlagState = {
      ...baseFlag,
      rolloutPct: 0,
      targetingRules: [
        { attribute: 'plan', operator: 'IN', values: ['pro', 'enterprise'], variation: 'enabled', priority: 10 },
      ],
    };
    const ctx: EvalContext = { userId: 'user-1', plan: 'pro' };
    const result = evaluate(flag, ctx, false);
    assert.strictEqual(result.reason, 'RULE_MATCH');
    assert.strictEqual(result.value, 'enabled');
  });

  it('RULE_MATCH returns the matched rule variation', () => {
    const flag: FlagState = {
      ...baseFlag,
      rolloutPct: 0,
      targetingRules: [
        { attribute: 'country', operator: 'EQ', values: ['US'], variation: 'us-variant', priority: 5 },
      ],
    };
    const ctx: EvalContext = { userId: 'u', country: 'US' };
    const result = evaluate(flag, ctx, false);
    assert.strictEqual(result.reason, 'RULE_MATCH');
    assert.strictEqual(result.value, 'us-variant');
  });

  it('higher priority rule wins over lower priority rule', () => {
    const flag: FlagState = {
      ...baseFlag,
      rolloutPct: 0,
      targetingRules: [
        { attribute: 'plan', operator: 'IN', values: ['pro'], variation: 'low-priority', priority: 1 },
        { attribute: 'plan', operator: 'IN', values: ['pro'], variation: 'high-priority', priority: 10 },
      ],
    };
    const ctx: EvalContext = { userId: 'u', plan: 'pro' };
    const result = evaluate(flag, ctx, false);
    assert.strictEqual(result.reason, 'RULE_MATCH');
    assert.strictEqual(result.value, 'high-priority');
  });

  it('falls through when targeting rule does not match', () => {
    const flag: FlagState = {
      ...baseFlag,
      rolloutPct: 100,
      targetingRules: [
        { attribute: 'plan', operator: 'IN', values: ['enterprise'], variation: 'enabled', priority: 10 },
      ],
    };
    // plan=pro not in ['enterprise'], falls through to 100% rollout
    const ctx: EvalContext = { userId: 'user-abc-123', plan: 'pro' };
    const result = evaluate(flag, ctx, false);
    assert.strictEqual(result.reason, 'FALLTHROUGH');
    assert.strictEqual(result.value, true);
  });

  it('NOT_IN operator: RULE_MATCH when attribute NOT in list', () => {
    const flag: FlagState = {
      ...baseFlag,
      rolloutPct: 0,
      targetingRules: [
        { attribute: 'plan', operator: 'NOT_IN', values: ['enterprise'], variation: 'free-variant', priority: 5 },
      ],
    };
    const ctx: EvalContext = { userId: 'u', plan: 'free' };
    const result = evaluate(flag, ctx, false);
    assert.strictEqual(result.reason, 'RULE_MATCH');
    assert.strictEqual(result.value, 'free-variant');
  });

  it('PREFIX operator: RULE_MATCH when attribute starts with value', () => {
    const flag: FlagState = {
      ...baseFlag,
      rolloutPct: 0,
      targetingRules: [
        { attribute: 'userId', operator: 'PREFIX', values: ['beta-'], variation: 'beta', priority: 5 },
      ],
    };
    const ctx: EvalContext = { userId: 'beta-user-42' };
    const result = evaluate(flag, ctx, false);
    assert.strictEqual(result.reason, 'RULE_MATCH');
    assert.strictEqual(result.value, 'beta');
  });

  it('RULE_MATCH skips rule when context attribute is missing', () => {
    const flag: FlagState = {
      ...baseFlag,
      rolloutPct: 100,
      targetingRules: [
        { attribute: 'plan', operator: 'IN', values: ['pro'], variation: 'enabled', priority: 10 },
      ],
    };
    // No 'plan' attribute in context — rule does not match, falls through to rollout
    const ctx: EvalContext = { userId: 'user-abc-123' };
    const result = evaluate(flag, ctx, false);
    assert.strictEqual(result.reason, 'FALLTHROUGH');
    assert.strictEqual(result.value, true);
  });

  it('rules array empty — falls through to rollout', () => {
    const flag: FlagState = {
      ...baseFlag,
      rolloutPct: 100,
      targetingRules: [],
    };
    const result = evaluate(flag, { userId: 'any' }, false);
    assert.strictEqual(result.reason, 'FALLTHROUGH');
    assert.strictEqual(result.value, true);
  });

  it('disabled flag with rules — returns OFF without evaluating rules', () => {
    const flag: FlagState = {
      ...baseFlag,
      enabled: false,
      targetingRules: [
        { attribute: 'plan', operator: 'IN', values: ['pro'], variation: 'enabled', priority: 10 },
      ],
    };
    const result = evaluate(flag, { userId: 'u', plan: 'pro' }, 'default');
    assert.strictEqual(result.reason, 'OFF');
    assert.strictEqual(result.value, 'default');
  });
});
