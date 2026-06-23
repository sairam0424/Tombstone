import { strict as assert } from 'assert';
import { EvaluationEngine } from '../evaluation.js';
import type { FlagEnvironmentState, EvaluationContext } from '../types.js';

const engine = new EvaluationEngine();

const ctx: EvaluationContext = { userId: 'user-abc', orgId: 'org-1', attrs: { plan: 'pro' } };

const activeFlag = (pct: number, overrides: Partial<FlagEnvironmentState> = {}): FlagEnvironmentState => ({
  flagId: 'id-1', flagKey: 'test-flag', environment: 'test',
  enabled: true, rolloutPct: pct, safeDefault: 'false', updatedAt: 0,
  prerequisites: [],
  ...overrides,
});

// Minimal FlagLookup backed by a single-entry map
const singleCache = (flag: FlagEnvironmentState | undefined, flagKey = 'test-flag') => ({
  get: (key: string) => (key === flagKey ? flag : undefined),
});

// Multi-flag cache
const multiCache = (flags: Record<string, FlagEnvironmentState>) => ({
  get: (key: string) => flags[key],
});

// ─── Legacy signature (backward compat) ───────────────────────────────────────

describe('EvaluationEngine — legacy signature (flagState, rules, ctx, default, key)', () => {
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

  it('returns ERROR for unknown flag (undefined state)', () => {
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

  it('TARGET_MATCH: rule attribute=plan IN [pro,enterprise]', () => {
    const result = engine.evaluate(activeFlag(100), [
      { id: 'r1', ruleType: 'CUSTOM', attribute: 'plan', operator: 'IN', values: ['pro', 'enterprise'], variation: 'enabled', priority: 10 }
    ], ctx, false, 'test-flag');
    // plan=pro now hits Step 4 RULE_MATCH (not TARGET_MATCH — targetList is Step 3)
    assert.equal(result.reason, 'RULE_MATCH');
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
    assert.equal(result.reason, 'RULE_MATCH');
    assert.equal(result.value, 'us-variant');
  });

  it('TARGET_MATCH: EQ operator', () => {
    const result = engine.evaluate(activeFlag(100), [
      { id: 'r3', ruleType: 'USER', attribute: 'userId', operator: 'EQ', values: ['user-abc'], variation: 'user-variant', priority: 1 }
    ], ctx, false, 'test-flag');
    assert.equal(result.reason, 'RULE_MATCH');
    assert.equal(result.value, 'user-variant');
  });

  it('TARGET_MATCH: lower priority wins over higher priority number', () => {
    // Rule with priority 1 should beat priority 10
    const result = engine.evaluate(activeFlag(100), [
      { id: 'r-high', ruleType: 'CUSTOM', attribute: 'plan', operator: 'IN', values: ['pro'], variation: 'high-prio', priority: 1 },
      { id: 'r-low', ruleType: 'CUSTOM', attribute: 'plan', operator: 'IN', values: ['pro'], variation: 'low-prio', priority: 10 },
    ], ctx, false, 'test-flag');
    assert.equal(result.reason, 'RULE_MATCH');
    assert.equal(result.value, 'high-prio');
  });

  it('TARGET_MATCH: GTE operator on numeric attribute', () => {
    const numCtx: EvaluationContext = { userId: 'user-num', attrs: { score: '95' } };
    const result = engine.evaluate(activeFlag(100), [
      { id: 'r4', ruleType: 'CUSTOM', attribute: 'score', operator: 'GTE', values: [90], variation: 'premium', priority: 5 }
    ], numCtx, false, 'test-flag');
    assert.equal(result.reason, 'RULE_MATCH');
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

// ─── Step 1: Preliminary ──────────────────────────────────────────────────────

describe('Step 1 — Preliminary', () => {
  it('ERROR when flag not in cache', () => {
    const r = engine.evaluateWithDetail('missing', ctx, false, { get: () => undefined });
    assert.equal(r.reason, 'ERROR');
    assert.equal(r.fromCache, false);
  });

  it('OFF when flag is disabled, returns safeDefault', () => {
    const flag = activeFlag(100, { enabled: false, safeDefault: 'false' });
    const r = engine.evaluateWithDetail('test-flag', ctx, true, singleCache(flag));
    assert.equal(r.reason, 'OFF');
    assert.equal(r.value, false);
  });
});

// ─── Step 2: Prerequisites ────────────────────────────────────────────────────

describe('Step 2 — Prerequisites', () => {
  it('PREREQUISITE_FAILED when gating prereq is disabled', () => {
    const prereqFlag = activeFlag(100, { flagKey: 'prereq-flag', enabled: false, safeDefault: 'false' });
    const mainFlag = activeFlag(100, {
      flagKey: 'main-flag',
      prerequisites: [{ flagKey: 'prereq-flag', requiredVariation: 'true', gate: true }],
    });
    const cache = multiCache({ 'main-flag': mainFlag, 'prereq-flag': prereqFlag });
    const r = engine.evaluateWithDetail('main-flag', ctx, false, cache);
    assert.equal(r.reason, 'PREREQUISITE_FAILED');
    assert.equal(r.value, false);
  });

  it('PREREQUISITE_FAILED when prereq resolves to wrong variation', () => {
    const prereqFlag = activeFlag(100, { flagKey: 'prereq-flag', safeDefault: 'false' });
    const mainFlag = activeFlag(100, {
      flagKey: 'main-flag',
      prerequisites: [{ flagKey: 'prereq-flag', requiredVariation: 'enabled', gate: true }],
    });
    const cache = multiCache({ 'main-flag': mainFlag, 'prereq-flag': prereqFlag });
    const r = engine.evaluateWithDetail('main-flag', ctx, false, cache);
    assert.equal(r.reason, 'PREREQUISITE_FAILED');
  });

  it('continues when gating prereq passes (variation matches)', () => {
    // prereq-flag resolves to 'true' (100% rollout)
    const prereqFlag = activeFlag(100, { flagKey: 'prereq-flag', safeDefault: 'true' });
    const mainFlag = activeFlag(100, {
      flagKey: 'main-flag',
      prerequisites: [{ flagKey: 'prereq-flag', requiredVariation: 'true', gate: true }],
    });
    const cache = multiCache({ 'main-flag': mainFlag, 'prereq-flag': prereqFlag });
    const r = engine.evaluateWithDetail('main-flag', ctx, false, cache);
    // Should not be PREREQUISITE_FAILED — should reach fallthrough
    assert.notEqual(r.reason, 'PREREQUISITE_FAILED');
  });

  it('non-gating prereq failure skips rule but continues evaluation', () => {
    const prereqFlag = activeFlag(100, { flagKey: 'prereq-flag', enabled: false, safeDefault: 'false' });
    const mainFlag = activeFlag(100, {
      flagKey: 'main-flag',
      prerequisites: [{ flagKey: 'prereq-flag', requiredVariation: 'true', gate: false }],
    });
    const cache = multiCache({ 'main-flag': mainFlag, 'prereq-flag': prereqFlag });
    const r = engine.evaluateWithDetail('main-flag', ctx, false, cache);
    // gate=false means we skip — not PREREQUISITE_FAILED, should reach FALLTHROUGH
    assert.notEqual(r.reason, 'PREREQUISITE_FAILED');
    assert.equal(r.reason, 'FALLTHROUGH');
  });

  it('missing prereq flag with gate=true → PREREQUISITE_FAILED', () => {
    const mainFlag = activeFlag(100, {
      flagKey: 'main-flag',
      prerequisites: [{ flagKey: 'ghost-flag', requiredVariation: 'true', gate: true }],
    });
    const cache = multiCache({ 'main-flag': mainFlag });
    const r = engine.evaluateWithDetail('main-flag', ctx, false, cache);
    assert.equal(r.reason, 'PREREQUISITE_FAILED');
    assert.equal(r.ruleId, 'ghost-flag');
  });
});

// ─── Step 3: Individual targeting ────────────────────────────────────────────

describe('Step 3 — Individual targeting (targetList)', () => {
  it('TARGET_MATCH when userId is in targetList', () => {
    const flag = activeFlag(0, { targetList: ['user-abc', 'user-xyz'] });
    const r = engine.evaluateWithDetail('test-flag', ctx, false, singleCache(flag));
    assert.equal(r.reason, 'TARGET_MATCH');
    assert.equal(r.value, true);
  });

  it('no TARGET_MATCH when userId is NOT in targetList', () => {
    const flag = activeFlag(100, { targetList: ['other-user'] });
    const r = engine.evaluateWithDetail('test-flag', ctx, false, singleCache(flag));
    assert.notEqual(r.reason, 'TARGET_MATCH');
  });

  it('empty targetList skips Step 3', () => {
    const flag = activeFlag(100, { targetList: [] });
    const r = engine.evaluateWithDetail('test-flag', ctx, false, singleCache(flag));
    assert.equal(r.reason, 'FALLTHROUGH');
  });
});

// ─── Step 4: Rule matching ────────────────────────────────────────────────────

describe('Step 4 — Rule matching', () => {
  it('RULE_MATCH: IN operator, ruleId is set', () => {
    const flag = activeFlag(100);
    const r = engine.evaluateWithDetail('test-flag', ctx, false, singleCache(flag), [
      { id: 'rule-pro', ruleType: 'USER', attribute: 'plan', operator: 'IN', values: ['pro', 'enterprise'], variation: 'v2', priority: 10 },
    ]);
    assert.equal(r.reason, 'RULE_MATCH');
    assert.equal(r.value, 'v2');
    assert.equal(r.ruleId, 'rule-pro');
  });

  it('higher priority rule wins', () => {
    const flag = activeFlag(100);
    const r = engine.evaluateWithDetail('test-flag', ctx, false, singleCache(flag), [
      { id: 'low-prio', ruleType: 'USER', attribute: 'plan', operator: 'IN', values: ['pro'], variation: 'low-prio-var', priority: 99 },
      { id: 'high-prio', ruleType: 'USER', attribute: 'plan', operator: 'IN', values: ['pro'], variation: 'high-prio-var', priority: 1 },
    ]);
    assert.equal(r.reason, 'RULE_MATCH');
    assert.equal(r.value, 'high-prio-var');  // priority 1 wins over 99 (lower = higher priority)
    assert.equal(r.ruleId, 'high-prio');
  });

  it('RULE_MATCH: EQ operator', () => {
    const flag = activeFlag(100);
    const r = engine.evaluateWithDetail('test-flag', ctx, false, singleCache(flag), [
      { id: 'r-org', ruleType: 'USER', attribute: 'orgId', operator: 'EQ', values: ['org-1'], variation: 'org-enabled', priority: 5 },
    ]);
    assert.equal(r.reason, 'RULE_MATCH');
    assert.equal(r.value, 'org-enabled');
  });

  it('RULE_MATCH: PREFIX operator', () => {
    const ctxPrefix: EvaluationContext = { userId: 'user-abc', attrs: { email: 'admin@acme.com' } };
    const flag = activeFlag(100);
    const r = engine.evaluateWithDetail('test-flag', ctxPrefix, false, singleCache(flag), [
      { id: 'r-prefix', ruleType: 'USER', attribute: 'email', operator: 'PREFIX', values: ['admin@'], variation: 'admin-view', priority: 5 },
    ]);
    assert.equal(r.reason, 'RULE_MATCH');
    assert.equal(r.value, 'admin-view');
  });

  it('falls through when no rule matches', () => {
    const flag = activeFlag(100);
    const r = engine.evaluateWithDetail('test-flag', ctx, false, singleCache(flag), [
      { id: 'r1', ruleType: 'USER', attribute: 'plan', operator: 'EQ', values: ['enterprise'], variation: 'ent', priority: 5 },
    ]);
    assert.equal(r.reason, 'FALLTHROUGH');
  });
});

// ─── Step 5: Fallthrough rollout ──────────────────────────────────────────────

describe('Step 5 — Fallthrough rollout', () => {
  it('FALLTHROUGH at 100% → value=true', () => {
    const r = engine.evaluateWithDetail('test-flag', ctx, false, singleCache(activeFlag(100)));
    assert.equal(r.reason, 'FALLTHROUGH');
    assert.equal(r.value, true);
  });

  it('FALLTHROUGH at 0% → defaultValue', () => {
    const r = engine.evaluateWithDetail('test-flag', ctx, false, singleCache(activeFlag(0)));
    assert.equal(r.reason, 'FALLTHROUGH');
    assert.equal(r.value, false);
  });

  it('MurmurHash stickiness at 50%', () => {
    const flag = activeFlag(50);
    const results = Array.from({ length: 20 }, () =>
      engine.evaluateWithDetail('test-flag', ctx, false, singleCache(flag))
    );
    const allSame = results.every(r => r.value === results[0].value);
    assert.ok(allSame, 'consistent hashing: same user must always land in same bucket');
  });
});

// ─── Short-circuit ordering ───────────────────────────────────────────────────

describe('Short-circuit ordering', () => {
  it('Step 2 fires before Step 3 (prereq failure beats targetList match)', () => {
    const prereqFlag = activeFlag(100, { flagKey: 'prereq-flag', enabled: false });
    const mainFlag = activeFlag(100, {
      flagKey: 'main-flag',
      targetList: ['user-abc'],  // user IS in targetList
      prerequisites: [{ flagKey: 'prereq-flag', requiredVariation: 'true', gate: true }],
    });
    const cache = multiCache({ 'main-flag': mainFlag, 'prereq-flag': prereqFlag });
    const r = engine.evaluateWithDetail('main-flag', ctx, false, cache);
    // Even though userId is in targetList, prereq check runs first
    assert.equal(r.reason, 'PREREQUISITE_FAILED');
  });

  it('Step 3 fires before Step 4 (targetList beats rule match)', () => {
    const flag = activeFlag(0, {
      targetList: ['user-abc'],
    });
    const r = engine.evaluateWithDetail('test-flag', ctx, false, singleCache(flag), [
      { id: 'r1', ruleType: 'USER', attribute: 'plan', operator: 'IN', values: ['pro'], variation: 'rule-var', priority: 99 },
    ]);
    // userId hit in targetList (Step 3) before rule (Step 4)
    assert.equal(r.reason, 'TARGET_MATCH');
  });

  it('Step 4 fires before Step 5 (rule match beats rollout)', () => {
    const flag = activeFlag(0); // 0% rollout would normally return default
    const r = engine.evaluateWithDetail('test-flag', ctx, false, singleCache(flag), [
      { id: 'r1', ruleType: 'USER', attribute: 'plan', operator: 'IN', values: ['pro'], variation: 'matched', priority: 5 },
    ]);
    assert.equal(r.reason, 'RULE_MATCH');
    assert.equal(r.value, 'matched');
  });
});
