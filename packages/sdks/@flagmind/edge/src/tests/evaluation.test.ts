import { evaluate } from '../evaluation.js';
import type { EvaluationContext, FlagEnvironmentState } from '../types.js';

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function makeFlag(enabled: boolean, rolloutPct: number): FlagEnvironmentState {
  return { enabled, rolloutPct };
}

function ctx(userId: string): EvaluationContext {
  return { userId };
}

// ---------------------------------------------------------------------------
// Cross-SDK parity vectors (from packages/sdks/test-contract/vectors.json)
// All 12 vectors must produce the expected_in_cohort result.
// ---------------------------------------------------------------------------

type Vector = {
  flag_key: string;
  user_id: string;
  rollout_pct: number;
  expected_in_cohort: boolean;
  note?: string;
};

const vectors: Vector[] = [
  { flag_key: "checkout-v2", user_id: "user-abc-123", rollout_pct: 100, expected_in_cohort: true, note: "100% always true" },
  { flag_key: "checkout-v2", user_id: "user-abc-123", rollout_pct: 0, expected_in_cohort: false, note: "0% always false" },
  { flag_key: "checkout-v2", user_id: "user-xyz-789", rollout_pct: 50, expected_in_cohort: false, note: "bucket > 50" },
  { flag_key: "checkout-v2", user_id: "user-abc-123", rollout_pct: 50, expected_in_cohort: true, note: "bucket < 50" },
  { flag_key: "payment-gateway-fee-display", user_id: "user-abc-123", rollout_pct: 50, expected_in_cohort: true },
  { flag_key: "max-cart-items", user_id: "user-abc-123", rollout_pct: 75, expected_in_cohort: true },
  { flag_key: "max-cart-items", user_id: "user-xyz-789", rollout_pct: 25, expected_in_cohort: false },
  { flag_key: "a", user_id: "b", rollout_pct: 50, expected_in_cohort: true, note: "minimal key and user_id — hash stability check" },
  { flag_key: "checkout-v2", user_id: "", rollout_pct: 50, expected_in_cohort: false, note: "empty user_id — must not panic, hash input is flag_key only" },
  { flag_key: "payments.checkout.new-flow.v2-beta", user_id: "user-abc-123", rollout_pct: 1, expected_in_cohort: false, note: "deep dot-notation key with long path" },
  { flag_key: "checkout-v2", user_id: "user-abc-123", rollout_pct: 99, expected_in_cohort: true, note: "bucket < 99 — confirms sub-100 boundary" },
  { flag_key: "checkout-v2", user_id: "user-abc-123", rollout_pct: 1, expected_in_cohort: false, note: "rollout_pct=1 — nearly everyone excluded" },
];

// ---------------------------------------------------------------------------
// Parity vector tests
// ---------------------------------------------------------------------------

describe('MurmurHash3 cross-SDK parity vectors', () => {
  for (const v of vectors) {
    const label = v.note
      ? `flag="${v.flag_key}" user="${v.user_id}" pct=${v.rollout_pct} — ${v.note}`
      : `flag="${v.flag_key}" user="${v.user_id}" pct=${v.rollout_pct}`;

    it(label, () => {
      const flag = makeFlag(true, v.rollout_pct);
      const result = evaluate(flag, ctx(v.user_id), false, v.flag_key);
      const inCohort = result.value === true;
      if (inCohort !== v.expected_in_cohort) {
        throw new Error(
          `Parity mismatch: expected in_cohort=${v.expected_in_cohort} but got ${inCohort} ` +
          `for flag="${v.flag_key}" user="${v.user_id}" pct=${v.rollout_pct}`
        );
      }
    });
  }
});

// ---------------------------------------------------------------------------
// Rollout boundary tests
// ---------------------------------------------------------------------------

describe('rollout boundary conditions', () => {
  it('100% rollout always returns true regardless of user', () => {
    const userIds = ['a', 'user-abc-123', 'user-xyz-789', '', 'anyone'];
    for (const userId of userIds) {
      const result = evaluate(makeFlag(true, 100), ctx(userId), false, 'checkout-v2');
      if (result.value !== true) {
        throw new Error(`100% rollout should always be true, got false for userId="${userId}"`);
      }
    }
  });

  it('0% rollout always returns false regardless of user', () => {
    const userIds = ['a', 'user-abc-123', 'user-xyz-789', '', 'anyone'];
    for (const userId of userIds) {
      const result = evaluate(makeFlag(true, 0), ctx(userId), false, 'checkout-v2');
      if (result.value !== false) {
        throw new Error(`0% rollout should always be false, got true for userId="${userId}"`);
      }
    }
  });
});

// ---------------------------------------------------------------------------
// Disabled flag and unknown flag tests
// ---------------------------------------------------------------------------

describe('disabled flag', () => {
  it('returns OFF reason with default value when flag is disabled', () => {
    const flag = makeFlag(false, 100);
    const result = evaluate(flag, ctx('user-abc-123'), false, 'checkout-v2');
    if (result.reason !== 'OFF') {
      throw new Error(`Expected reason="OFF", got "${result.reason}"`);
    }
    if (result.value !== false) {
      throw new Error(`Expected value=false (default), got ${result.value}`);
    }
  });

  it('disabled flag returns the provided default value, not true', () => {
    const flag = makeFlag(false, 100);
    const result = evaluate(flag, ctx('user-abc-123'), 'my-default', 'checkout-v2');
    if (result.value !== 'my-default') {
      throw new Error(`Expected default value "my-default", got "${result.value}"`);
    }
    if (result.reason !== 'OFF') {
      throw new Error(`Expected reason="OFF", got "${result.reason}"`);
    }
  });
});

describe('unknown flag (undefined state)', () => {
  it('returns ERROR reason with default value when flag state is undefined', () => {
    const result = evaluate(undefined, ctx('user-abc-123'), false, 'nonexistent-flag');
    if (result.reason !== 'ERROR') {
      throw new Error(`Expected reason="ERROR", got "${result.reason}"`);
    }
    if (result.value !== false) {
      throw new Error(`Expected value=false (default), got ${result.value}`);
    }
  });

  it('unknown flag returns the provided default value', () => {
    const result = evaluate(undefined, ctx('user-abc-123'), 'fallback', 'nonexistent-flag');
    if (result.value !== 'fallback') {
      throw new Error(`Expected default value "fallback", got "${result.value}"`);
    }
    if (result.reason !== 'ERROR') {
      throw new Error(`Expected reason="ERROR", got "${result.reason}"`);
    }
  });

  it('unknown flag sets flagKey on the result', () => {
    const result = evaluate(undefined, ctx('user-abc-123'), false, 'my-flag');
    if (result.flagKey !== 'my-flag') {
      throw new Error(`Expected flagKey="my-flag", got "${result.flagKey}"`);
    }
  });
});
