import { strict as assert } from 'assert';
import { EvaluationEngine } from '../evaluation.js';
import type { FlagEnvironmentState, EvaluationContext } from '../types.js';

const engine = new EvaluationEngine();

const ctx: EvaluationContext = { userId: 'user-abc', orgId: 'org-1', attrs: { plan: 'pro' } };

const activeFlag = (pct: number): FlagEnvironmentState => ({
  flagId: 'id-1', flagKey: 'test-flag', environment: 'test',
  enabled: true, rolloutPct: pct, safeDefault: 'false', updatedAt: 0,
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
