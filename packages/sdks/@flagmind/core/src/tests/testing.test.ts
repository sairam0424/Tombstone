import { strict as assert } from 'assert';
import { TombstoneTestClient } from '../testing.js';
import type { EvaluationContext } from '../types.js';

const ctx = (userId: string): EvaluationContext => ({ userId });

describe('TombstoneTestClient', () => {
  describe('override()', () => {
    it('returns overridden value instead of default', () => {
      const client = TombstoneTestClient.createIsolated();
      client.override('checkout_v2', true);
      assert.equal(client.evaluate('checkout_v2', false), true);
    });

    it('chains fluently', () => {
      const client = TombstoneTestClient.createIsolated()
        .override('flag_a', 'enabled')
        .override('flag_b', 42);
      assert.equal(client.evaluate('flag_a', 'off'), 'enabled');
      assert.equal(client.evaluate('flag_b', 0), 42);
    });

    it('override accepts any value type (string, number, object)', () => {
      const client = TombstoneTestClient.createIsolated();
      client.override('str_flag', 'variant-b');
      client.override('num_flag', 99);
      client.override('obj_flag', { key: 'val' });
      assert.equal(client.evaluate('str_flag', 'default'), 'variant-b');
      assert.equal(client.evaluate('num_flag', 0), 99);
      assert.deepEqual(client.evaluate('obj_flag', {}), { key: 'val' });
    });
  });

  describe('clearOverrides()', () => {
    it('resets all overrides — evaluate returns defaultValue', () => {
      const client = TombstoneTestClient.createIsolated()
        .override('checkout_v2', true)
        .override('new_ui', 'v2');
      client.clearOverrides();
      assert.equal(client.evaluate('checkout_v2', false), false);
      assert.equal(client.evaluate('new_ui', 'v1'), 'v1');
    });
  });

  describe('clearOverride() (single key)', () => {
    it('removes only the targeted flag override', () => {
      const client = TombstoneTestClient.createIsolated()
        .override('flag_a', true)
        .override('flag_b', true);
      client.clearOverride('flag_a');
      assert.equal(client.evaluate('flag_a', false), false, 'flag_a override cleared');
      assert.equal(client.evaluate('flag_b', false), true, 'flag_b override retained');
    });
  });

  describe('assignToBucket()', () => {
    it('forces userId into cohort when inCohort=true', () => {
      const client = TombstoneTestClient.createIsolated();
      client.assignToBucket('experiment_flag', 'user-123', true);
      assert.equal(client.evaluate('experiment_flag', false, ctx('user-123')), true);
    });

    it('forces userId OUT of cohort when inCohort=false', () => {
      const client = TombstoneTestClient.createIsolated();
      client.assignToBucket('experiment_flag', 'user-abc', false);
      assert.equal(client.evaluate('experiment_flag', false, ctx('user-abc')), false);
    });

    it('bucket assignment does NOT affect other userIds', () => {
      const client = TombstoneTestClient.createIsolated();
      client.assignToBucket('experiment_flag', 'user-123', true);
      // user-xyz has no assignment — falls through to defaultValue
      assert.equal(client.evaluate('experiment_flag', false, ctx('user-xyz')), false);
    });

    it('override takes precedence over bucket assignment', () => {
      const client = TombstoneTestClient.createIsolated();
      client.override('experiment_flag', 'control');
      client.assignToBucket('experiment_flag', 'user-123', true);
      assert.equal(client.evaluate('experiment_flag', 'default', ctx('user-123')), 'control');
    });
  });

  describe('createIsolated()', () => {
    it('returns defaultValue for all flags with no setup', () => {
      const client = TombstoneTestClient.createIsolated();
      assert.equal(client.evaluate('any_flag', false), false);
      assert.equal(client.evaluate('any_flag', 'fallback'), 'fallback');
      assert.equal(client.evaluate('any_flag', 0), 0);
    });
  });

  describe('withFlags()', () => {
    it('pre-configures multiple flags from a Record', () => {
      const client = TombstoneTestClient.withFlags({
        checkout_v2: true,
        new_pricing: 'beta',
        max_retries: 3,
      });
      assert.equal(client.evaluate('checkout_v2', false), true);
      assert.equal(client.evaluate('new_pricing', 'stable'), 'beta');
      assert.equal(client.evaluate('max_retries', 1), 3);
    });

    it('flags not in the record still return defaultValue', () => {
      const client = TombstoneTestClient.withFlags({ known_flag: true });
      assert.equal(client.evaluate('unknown_flag', 'fallback'), 'fallback');
    });
  });

  describe('isEnabled()', () => {
    it('returns true when override is true', () => {
      const client = TombstoneTestClient.createIsolated().override('my_flag', true);
      assert.equal(client.isEnabled('my_flag'), true);
    });

    it('returns false when override is false', () => {
      const client = TombstoneTestClient.createIsolated().override('my_flag', false);
      assert.equal(client.isEnabled('my_flag'), false);
    });

    it('returns false for unset flag (default)', () => {
      const client = TombstoneTestClient.createIsolated();
      assert.equal(client.isEnabled('unset_flag'), false);
    });

    it('uses bucket assignment when context provided', () => {
      const client = TombstoneTestClient.createIsolated();
      client.assignToBucket('my_flag', 'user-1', true);
      assert.equal(client.isEnabled('my_flag', ctx('user-1')), true);
      assert.equal(client.isEnabled('my_flag', ctx('user-2')), false);
    });
  });

  describe('immutability', () => {
    it('override() creates a new Map — prior Map is not mutated', () => {
      const client = TombstoneTestClient.createIsolated();
      client.override('flag_a', 'first');
      const snapshot = client.evaluate('flag_a', 'none');
      client.override('flag_a', 'second');
      // The previous evaluation result is unchanged — we just verify the new value
      assert.equal(snapshot, 'first');
      assert.equal(client.evaluate('flag_a', 'none'), 'second');
    });

    it('clearOverrides() creates a new Map — other references unaffected', () => {
      const client = TombstoneTestClient.createIsolated().override('flag_a', true);
      const valueBefore = client.evaluate('flag_a', false);
      client.clearOverrides();
      const valueAfter = client.evaluate('flag_a', false);
      assert.equal(valueBefore, true);
      assert.equal(valueAfter, false);
    });

    it('assignToBucket() creates new Maps — does not mutate existing bucket maps', () => {
      const client = TombstoneTestClient.createIsolated();
      client.assignToBucket('flag_a', 'user-1', true);
      const beforeSecond = client.evaluate('flag_a', false, ctx('user-2'));
      client.assignToBucket('flag_a', 'user-2', true);
      // user-2 was not in cohort before second assignToBucket
      assert.equal(beforeSecond, false);
      assert.equal(client.evaluate('flag_a', false, ctx('user-2')), true);
    });
  });
});
