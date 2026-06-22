/**
 * @tombstone/eval — Zero-dependency evaluation engine.
 * Runs in: Node.js, browser, Cloudflare Workers, Deno, Bun, WASM runtimes.
 * No murmurhash/eventsource deps — pure TypeScript algorithms.
 */

// ---------------------------------------------------------------------------
// Hash algorithms (inline, no external deps)
// ---------------------------------------------------------------------------

/**
 * Inline MurmurHash3 x86 32-bit — exact port of the `murmurhash` npm
 * package v3() method (seed=0, UTF-8 byte encoding).
 * Processes 4 bytes at a time using 16-bit multiplication to avoid
 * 32-bit overflow, matching the npm package's implementation precisely.
 * Produces identical bucket assignments to @tombstone/core.
 */
function murmur32(str: string): number {
  // Encode to UTF-8 bytes (same as TextEncoder in the npm package)
  const bytes: number[] = [];
  for (let i = 0; i < str.length; i++) {
    const code = str.charCodeAt(i);
    if (code < 0x80) {
      bytes.push(code);
    } else if (code < 0x800) {
      bytes.push(0xc0 | (code >> 6), 0x80 | (code & 0x3f));
    } else {
      bytes.push(
        0xe0 | (code >> 12),
        0x80 | ((code >> 6) & 0x3f),
        0x80 | (code & 0x3f),
      );
    }
  }

  const key = bytes;
  const len = key.length;
  const remainder = len & 3;
  const blen = len - remainder;
  const c1 = 0xcc9e2d51;
  const c2 = 0x1b873593;
  let h1 = 0; // seed = 0
  let i = 0;

  // Process 4-byte blocks
  while (i < blen) {
    let k1 =
      (key[i] & 0xff) |
      ((key[++i] & 0xff) << 8) |
      ((key[++i] & 0xff) << 16) |
      ((key[++i] & 0xff) << 24);
    ++i;

    k1 = (((k1 & 0xffff) * c1) + ((((k1 >>> 16) * c1) & 0xffff) << 16)) & 0xffffffff;
    k1 = (k1 << 15) | (k1 >>> 17);
    k1 = (((k1 & 0xffff) * c2) + ((((k1 >>> 16) * c2) & 0xffff) << 16)) & 0xffffffff;

    h1 ^= k1;
    h1 = (h1 << 13) | (h1 >>> 19);
    const h1b = (((h1 & 0xffff) * 5) + ((((h1 >>> 16) * 5) & 0xffff) << 16)) & 0xffffffff;
    h1 = (((h1b & 0xffff) + 0x6b64) + ((((h1b >>> 16) + 0xe654) & 0xffff) << 16));
  }

  // Tail bytes
  let k1 = 0;
  switch (remainder) {
    case 3: k1 ^= (key[i + 2] & 0xff) << 16;  // falls through
    // eslint-disable-next-line no-fallthrough
    case 2: k1 ^= (key[i + 1] & 0xff) << 8;   // falls through
    // eslint-disable-next-line no-fallthrough
    case 1:
      k1 ^= key[i] & 0xff;
      k1 = (((k1 & 0xffff) * c1) + ((((k1 >>> 16) * c1) & 0xffff) << 16)) & 0xffffffff;
      k1 = (k1 << 15) | (k1 >>> 17);
      k1 = (((k1 & 0xffff) * c2) + ((((k1 >>> 16) * c2) & 0xffff) << 16)) & 0xffffffff;
      h1 ^= k1;
  }

  h1 ^= len;
  h1 ^= h1 >>> 16;
  h1 = (((h1 & 0xffff) * 0x85ebca6b) + ((((h1 >>> 16) * 0x85ebca6b) & 0xffff) << 16)) & 0xffffffff;
  h1 ^= h1 >>> 13;
  h1 = (((h1 & 0xffff) * 0xc2b2ae35) + ((((h1 >>> 16) * 0xc2b2ae35) & 0xffff) << 16)) & 0xffffffff;
  h1 ^= h1 >>> 16;
  return h1 >>> 0;
}

/**
 * Inline FNV-32a (Fowler–Noll–Vo 1a).
 * Used as the building block for hashV2.
 */
function fnv32a(str: string): number {
  let h = 0x811c9dc5;
  for (let i = 0; i < str.length; i++) {
    h ^= str.charCodeAt(i) & 0xff;
    h = Math.imul(h, 0x01000193);
  }
  return h >>> 0;
}

/**
 * Hash v2: double-FNV32a — same algorithm as @tombstone/core.
 * Returns a float in [0, 1).
 */
function hashV2(seed: string, value: string): number {
  const a = fnv32a(seed + value);
  const b = fnv32a(value + seed);
  return (((a ^ b) >>> 0) / 0x100000000);
}

// ---------------------------------------------------------------------------
// Public types
// ---------------------------------------------------------------------------

export type EvaluationReason = 'OFF' | 'FALLTHROUGH' | 'RULE_MATCH' | 'ERROR';

export type RuleOperator =
  | 'IN' | 'NOT_IN' | 'EQ' | 'NEQ'
  | 'LT' | 'LTE' | 'GT' | 'GTE'
  | 'CONTAINS' | 'PREFIX' | 'SUFFIX';

export interface TargetingRule {
  attribute: string;
  operator: RuleOperator;
  values: unknown[];
  variation: string;
  priority: number;
}

export interface FlagState {
  flagKey: string;
  enabled: boolean;
  rolloutPct: number;
  safeDefault: string;
  hashVersion?: 1 | 2;
  targetingRules?: TargetingRule[];
}

export interface EvalContext {
  userId?: string;
  [key: string]: unknown;
}

export interface EvalResult {
  value: unknown;
  reason: EvaluationReason;
}

// ---------------------------------------------------------------------------
// Core evaluation helpers
// ---------------------------------------------------------------------------

/**
 * Returns true if the given userId falls within the rollout percentage for
 * the given flag key. Consistent (same inputs → same result) and stateless.
 */
export function isInRollout(
  flagKey: string,
  userId: string,
  rolloutPct: number,
  hashVersion: 1 | 2 = 1,
): boolean {
  if (rolloutPct >= 100) return true;
  if (rolloutPct <= 0) return false;

  if (hashVersion === 2) {
    return hashV2(flagKey, userId) < rolloutPct / 100;
  }

  // Default: MurmurHash3 v1 — same as @tombstone/core
  return (murmur32(flagKey + userId) % 100) < rolloutPct;
}

// ---------------------------------------------------------------------------
// Targeting rule evaluation
// ---------------------------------------------------------------------------

function evaluateOperator(operator: RuleOperator, value: string, ruleValues: unknown[]): boolean {
  const strValues = ruleValues.map(v => String(v));
  switch (operator) {
    case 'IN':       return strValues.includes(value);
    case 'NOT_IN':   return !strValues.includes(value);
    case 'EQ':       return value === strValues[0];
    case 'NEQ':      return value !== strValues[0];
    case 'CONTAINS': return strValues.some(v => value.includes(v));
    case 'PREFIX':   return strValues.some(v => value.startsWith(v));
    case 'SUFFIX':   return strValues.some(v => value.endsWith(v));
    case 'LT':       return parseFloat(value) < parseFloat(strValues[0] ?? '0');
    case 'LTE':      return parseFloat(value) <= parseFloat(strValues[0] ?? '0');
    case 'GT':       return parseFloat(value) > parseFloat(strValues[0] ?? '0');
    case 'GTE':      return parseFloat(value) >= parseFloat(strValues[0] ?? '0');
    default:         return false;
  }
}

function evaluateRule(rule: TargetingRule, context: EvalContext): boolean {
  const rawValue = context[rule.attribute];
  if (rawValue === undefined || rawValue === null) return false;
  return evaluateOperator(rule.operator as RuleOperator, String(rawValue), rule.values);
}

// ---------------------------------------------------------------------------
// Main evaluate() — 5-step pipeline (same as @tombstone/core)
// ---------------------------------------------------------------------------

/**
 * Evaluate a flag for the given context.
 *
 * Pipeline:
 *   1. Guard — flag must be defined
 *   2. OFF check — flag.enabled must be true
 *   3. Targeting rules (sorted by priority desc)
 *   4. Rollout hash check
 *   5. Default fallthrough
 */
export function evaluate(
  flag: FlagState,
  context: EvalContext,
  defaultValue: unknown,
): EvalResult {
  // Step 1: guard
  if (!flag) {
    return { value: defaultValue, reason: 'ERROR' };
  }

  // Step 2: OFF
  if (!flag.enabled) {
    return { value: defaultValue, reason: 'OFF' };
  }

  // Step 3: targeting rules
  const rules = flag.targetingRules ?? [];
  const sortedRules = [...rules].sort((a, b) => b.priority - a.priority);
  for (const rule of sortedRules) {
    if (evaluateRule(rule, context)) {
      return { value: rule.variation, reason: 'RULE_MATCH' };
    }
  }

  // Step 4: rollout
  const userId = typeof context.userId === 'string' ? context.userId : '';
  const hashVersion = flag.hashVersion ?? 1;
  if (isInRollout(flag.flagKey, userId, flag.rolloutPct, hashVersion)) {
    return { value: true, reason: 'FALLTHROUGH' };
  }

  // Step 5: default fallthrough
  return { value: defaultValue, reason: 'FALLTHROUGH' };
}
