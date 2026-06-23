import { strict as assert } from 'assert';
import { readFileSync } from 'fs';
import { join, dirname } from 'path';
import { fileURLToPath } from 'url';
import { EvaluationEngine } from '../evaluation.js';
import type { FlagEnvironmentState } from '../types.js';

const __dirname = dirname(fileURLToPath(import.meta.url));

// Load vectors relative to the test file location
const vectorsPath = join(__dirname, '../../../../test-contract/vectors.json');
const data = JSON.parse(readFileSync(vectorsPath, 'utf-8'));
const vectors = data.vectors ?? (Array.isArray(data) ? data : []);

const engine = new EvaluationEngine();

function makeFlag(flagKey: string, rolloutPct: number, hashVersion: 1 | 2 = 1): FlagEnvironmentState {
  return {
    flagId: 'contract-test',
    flagKey,
    environment: 'test',
    enabled: true,
    rolloutPct,
    safeDefault: 'false',
    updatedAt: 0,
    hashVersion,
  };
}

describe('Cross-SDK contract vectors', () => {
  for (const v of vectors) {
    const label = `${v.flag_key} / userId="${v.user_id}" / ${v.rollout_pct}% / v${v.hash_version ?? 1}`;
    it(label, () => {
      const flag = makeFlag(v.flag_key, v.rollout_pct, v.hash_version ?? 1);
      const cache = { get: (k: string) => k === v.flag_key ? flag : undefined };
      const ctx = { userId: v.user_id };
      const result = engine.evaluateWithDetail(v.flag_key, ctx, false, cache);
      const inCohort = result.value === true;
      assert.equal(
        inCohort,
        v.expected_in_cohort,
        `bucket mismatch: expected_in_cohort=${v.expected_in_cohort}, got=${inCohort} (reason=${result.reason})`,
      );
    });
  }
});
