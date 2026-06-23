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
      { id: 'r1', ruleType: 'CUSTOM', attribute: 'plan', operator: 'IN', values: ['pro', 'enterprise'], variation: 'enabled', priority: 10 }
    ], ctx, false, 'test-flag');
    assert.equal(result.reason, 'TARGET_MATCH');
    assert.equal(result.value, 'enabled');
  });

  it('falls through when targeting rule does not match', () => {
    const result = engine.evaluate(activeFlag(100), [
      { id: 'r1', ruleType: 'CUSTOM', attribute: 'plan', operator: 'IN', values: ['enterprise'], variation: 'enabled', priority: 10 }
    ], ctx, false, 'test-flag');
    // plan=pro not in ['enterprise'], falls through to rollout
    assert.equal(result.reason, 'FALLTHROUGH');
  });

  it('TARGET_MATCH: dot-notation attribute (geo.country)', () => {
    const geoCtx: EvaluationContext = { userId: 'user-geo', geo: { country: 'US' } };
    const result = engine.evaluate(activeFlag(100), [
      { id: 'r2', ruleType: 'CUSTOM', attribute: 'geo.country', operator: 'IN', values: ['US', 'CA'], variation: 'us-variant', priority: 5 }
    ], geoCtx, false, 'test-flag');
    assert.equal(result.reason, 'TARGET_MATCH');
    assert.equal(result.value, 'us-variant');
  });

  it('TARGET_MATCH: EQ operator', () => {
    const result = engine.evaluate(activeFlag(100), [
      { id: 'r3', ruleType: 'USER', attribute: 'userId', operator: 'EQ', values: ['user-abc'], variation: 'user-variant', priority: 1 }
    ], ctx, false, 'test-flag');
    assert.equal(result.reason, 'TARGET_MATCH');
    assert.equal(result.value, 'user-variant');
  });

  it('TARGET_MATCH: lower priority wins over higher priority number', () => {
    // Rule with priority 1 should beat priority 10
    const result = engine.evaluate(activeFlag(100), [
      { id: 'r-high', ruleType: 'CUSTOM', attribute: 'plan', operator: 'IN', values: ['pro'], variation: 'high-prio', priority: 1 },
      { id: 'r-low', ruleType: 'CUSTOM', attribute: 'plan', operator: 'IN', values: ['pro'], variation: 'low-prio', priority: 10 },
    ], ctx, false, 'test-flag');
    assert.equal(result.reason, 'TARGET_MATCH');
    assert.equal(result.value, 'high-prio');
  });

  it('TARGET_MATCH: GTE operator on numeric attribute', () => {
    const numCtx: EvaluationContext = { userId: 'user-num', attrs: { score: '95' } };
    const result = engine.evaluate(activeFlag(100), [
      { id: 'r4', ruleType: 'CUSTOM', attribute: 'score', operator: 'GTE', values: [90], variation: 'premium', priority: 5 }
    ], numCtx, false, 'test-flag');
    assert.equal(result.reason, 'TARGET_MATCH');
    assert.equal(result.value, 'premium');
  });

  it('returns false for missing attribute without throwing', () => {
    const emptyCtx: EvaluationContext = { userId: 'user-empty' };
    const result = engine.evaluate(activeFlag(100), [
      { id: 'r5', ruleType: 'CUSTOM', attribute: 'nonexistent.deep.key', operator: 'EQ', values: ['x'], variation: 'v', priority: 5 }
    ], emptyCtx, false, 'test-flag');
    // Attribute not found — falls through to rollout
    assert.equal(result.reason, 'FALLTHROUGH');
  });
});
