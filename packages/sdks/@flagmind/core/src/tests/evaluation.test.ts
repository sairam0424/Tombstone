import { strict as assert } from 'assert';
import { createRequire } from 'module';
import { EvaluationEngine } from '../evaluation.js';
import type { FlagEnvironmentState, EvaluationContext } from '../types.js';

const require = createRequire(import.meta.url);
// Load contract vectors relative to the repo root (packages/sdks/test-contract/vectors.json)
const vectors = require('../../../../test-contract/vectors.json') as {
  vectors: Array<{
    flag_key: string;
    user_id: string;
    hash_version?: 1 | 2;
    rollout_pct: number;
    expected_bucket?: number;
    expected_in_cohort: boolean;
    note?: string;
  }>;
};

const engine = new EvaluationEngine();

const ctx: EvaluationContext = { userId: 'user-abc', orgId: 'org-1', attrs: { plan: 'pro' } };

const activeFlag = (pct: number, hashVersion?: 1 | 2): FlagEnvironmentState => ({
  flagId: 'id-1', flagKey: 'test-flag', environment: 'test',
  enabled: true, rolloutPct: pct, safeDefault: 'false', updatedAt: 0,
  ...(hashVersion !== undefined ? { hashVersion } : {}),
});

describe('EvaluationEngine', () => {
  it('returns OFF when flag is disabled', () => {
    const flag: FlagEnvironmentState = { ...activeFlag(100), enabled: false };
    const result = engine.evaluate(flag, [], ctx, false, 'test-flag');
    assert.equal(result.reason, 'OFF');
    assert.equal(result.value, false);
  });

  it('returns FALLTHROUGH at 100% rollout', () => {
    const result = engine.evaluate(activeFlag(100), [], ctx, false, 'test-flag');
    assert.equal(result.reason, 'FALLTHROUGH');
    assert.equal(result.value, true);
  });

  it('returns default for unknown flag (undefined state)', () => {
    const result = engine.evaluate(undefined, [], ctx, false, 'missing-flag');
    assert.equal(result.reason, 'ERROR');
    assert.equal(result.value, false);
  });

  it('MurmurHash stickiness — same userId always gets same result', () => {
    const flag = activeFlag(50);
    const results = Array.from({ length: 20 }, () =>
      engine.evaluate(flag, [], ctx, false, 'test-flag')
    );
    const allSame = results.every(r => r.value === results[0].value);
    assert.ok(allSame, 'same user must always get same assignment');
  });

  it('returns 0% rollout as default (not in cohort)', () => {
    const result = engine.evaluate(activeFlag(0), [], ctx, false, 'test-flag');
    assert.equal(result.reason, 'FALLTHROUGH');
    assert.equal(result.value, false);
  });

  it('TARGET_MATCH: IN operator matches', () => {
    const result = engine.evaluate(activeFlag(100), [
      { id: 'r1', attribute: 'plan', operator: 'IN', values: ['pro', 'enterprise'], variation: 'enabled', priority: 10 }
    ], ctx, false, 'test-flag');
    assert.equal(result.reason, 'TARGET_MATCH');
    assert.equal(result.value, 'enabled');
  });

  it('falls through when targeting rule does not match', () => {
    const result = engine.evaluate(activeFlag(100), [
      { id: 'r1', attribute: 'plan', operator: 'IN', values: ['enterprise'], variation: 'enabled', priority: 10 }
    ], ctx, false, 'test-flag');
    // plan=pro not in ['enterprise'], falls through to rollout
    assert.equal(result.reason, 'FALLTHROUGH');
  });
});

// ---------------------------------------------------------------------------
// Hash v2 — double-FNV32a bucketing (parallel-experiment bias fix)
// ---------------------------------------------------------------------------

describe('EvaluationEngine — hash v2 double-FNV32a', () => {
  it('v2 flag at 100% rollout always returns true (boundary)', () => {
    const flag: FlagEnvironmentState = { ...activeFlag(100, 2), flagKey: 'checkout-v2' };
    const evalCtx: EvaluationContext = { userId: 'user-abc-123' };
    const result = engine.evaluate(flag, [], evalCtx, false, 'checkout-v2');
    assert.equal(result.value, true, '100% rollout must always be in cohort');
  });

  it('v2 flag at 0% rollout always returns false (boundary)', () => {
    const flag: FlagEnvironmentState = { ...activeFlag(0, 2), flagKey: 'checkout-v2' };
    const evalCtx: EvaluationContext = { userId: 'user-abc-123' };
    const result = engine.evaluate(flag, [], evalCtx, false, 'checkout-v2');
    assert.equal(result.value, false, '0% rollout must always be out of cohort');
  });

  it('v2 stickiness — same userId always gets same bucket', () => {
    const flag: FlagEnvironmentState = { ...activeFlag(50, 2), flagKey: 'checkout-v2' };
    const evalCtx: EvaluationContext = { userId: 'user-abc-123' };
    const results = Array.from({ length: 20 }, () =>
      engine.evaluate(flag, [], evalCtx, false, 'checkout-v2')
    );
    const allSame = results.every(r => r.value === results[0].value);
    assert.ok(allSame, 'v2: same user must always get same assignment');
  });

  it('v2 does not panic on empty userId', () => {
    const flag: FlagEnvironmentState = { ...activeFlag(50, 2), flagKey: 'checkout-v2' };
    const evalCtx: EvaluationContext = { userId: '' };
    assert.doesNotThrow(() => engine.evaluate(flag, [], evalCtx, false, 'checkout-v2'));
  });

  it('v2 handles dot-notation flag key without error', () => {
    const flag: FlagEnvironmentState = {
      ...activeFlag(2, 2),
      flagKey: 'payments.checkout.new-flow.v2-beta',
    };
    const evalCtx: EvaluationContext = { userId: 'user-abc-123' };
    assert.doesNotThrow(() =>
      engine.evaluate(flag, [], evalCtx, false, 'payments.checkout.new-flow.v2-beta')
    );
  });

  it('v1 and v2 produce independent bucket assignments for the same userId', () => {
    // user-xyz-789 with checkout-v2@v1 → bucket 0.98 (OUT at 50%)
    //               experiment.new-checkout@v2 → bucket 0.4766 (IN at 50%)
    // Demonstrates the two hash functions assign the same user differently across
    // parallel experiments, which is the core parallel-experiment bias fix.
    const v1Flag: FlagEnvironmentState = { ...activeFlag(50, 1), flagKey: 'checkout-v2' };
    const v2Flag: FlagEnvironmentState = { ...activeFlag(50, 2), flagKey: 'experiment.new-checkout' };
    const evalCtx: EvaluationContext = { userId: 'user-xyz-789' };
    const r1 = engine.evaluate(v1Flag, [], evalCtx, false, 'checkout-v2');
    const r2 = engine.evaluate(v2Flag, [], evalCtx, false, 'experiment.new-checkout');
    // v1: user-xyz-789/checkout-v2 → bucket 0.98 → OUT
    assert.equal(r1.value, false, 'v1: user-xyz-789 should be OUT of checkout-v2 at 50%');
    // v2: user-xyz-789/experiment.new-checkout → bucket 0.4766 → IN
    assert.equal(r2.value, true, 'v2: user-xyz-789 should be IN experiment.new-checkout at 50%');
    // Explicit boolean cast avoids the TS2367 strict narrowing false-vs-true comparison warning
    assert.ok(
      Boolean(r1.value) !== Boolean(r2.value),
      'parallel experiments assign same user differently (anti-bias check confirmed)',
    );
  });

  it('backward compat — flag without hashVersion uses v1 (MurmurHash3)', () => {
    // Flag with no hashVersion field — should behave identically to hashVersion:1
    const flagNoVersion: FlagEnvironmentState = {
      flagId: 'id-bc', flagKey: 'checkout-v2', environment: 'test',
      enabled: true, rolloutPct: 50, safeDefault: 'false', updatedAt: 0,
    };
    const flagV1: FlagEnvironmentState = { ...flagNoVersion, hashVersion: 1 };
    const evalCtx: EvaluationContext = { userId: 'user-abc-123' };
    const rNoVersion = engine.evaluate(flagNoVersion, [], evalCtx, false, 'checkout-v2');
    const rV1 = engine.evaluate(flagV1, [], evalCtx, false, 'checkout-v2');
    assert.equal(rNoVersion.value, rV1.value, 'omitting hashVersion must be identical to hashVersion:1');
  });

  // Contract vector tests — pinned bucket values for cross-language parity
  const v2Vectors = vectors.vectors.filter(v => (v.hash_version ?? 1) === 2);

  for (const vec of v2Vectors) {
    it(`contract vector: ${vec.flag_key} / userId="${vec.user_id}" / rollout=${vec.rollout_pct}% → ${vec.expected_in_cohort ? 'IN' : 'OUT'} (${vec.note ?? ''})`, () => {
      const flag: FlagEnvironmentState = {
        flagId: 'contract', flagKey: vec.flag_key, environment: 'test',
        enabled: true, rolloutPct: vec.rollout_pct, safeDefault: 'false',
        updatedAt: 0, hashVersion: 2,
      };
      const evalCtx: EvaluationContext = { userId: vec.user_id };
      const result = engine.evaluate(flag, [], evalCtx, false, vec.flag_key);
      const inCohort = result.value === true;
      assert.equal(
        inCohort,
        vec.expected_in_cohort,
        `bucket mismatch for ${vec.flag_key}/${vec.user_id} at ${vec.rollout_pct}%` +
          (vec.expected_bucket !== undefined ? ` (expected bucket≈${vec.expected_bucket})` : ''),
      );
    });
  }
});
