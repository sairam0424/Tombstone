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
  // Encode to UTF-8 bytes (same as TextEncoder in the npm package). Must
  // combine UTF-16 surrogate pairs into a single >= U+10000 code point
  // BEFORE branching on byte width -- without this, each half of a
  // surrogate pair (0xD800-0xDFFF) falls into the 3-byte branch on its
  // own, producing a completely different (wrong) byte sequence than the
  // real UTF-8 encoding for any emoji or other supplementary-plane
  // character, silently breaking cross-SDK parity for exactly those
  // inputs (found by adversarial review of PR #207).
  const bytes: number[] = [];
  for (let i = 0; i < str.length; i++) {
    let code = str.charCodeAt(i);
    if (code >= 0xd800 && code <= 0xdbff && i + 1 < str.length) {
      const next = str.charCodeAt(i + 1);
      if (next >= 0xdc00 && next <= 0xdfff) {
        code = (code - 0xd800) * 0x400 + (next - 0xdc00) + 0x10000;
        i++;
      }
    }
    if (code < 0x80) {
      bytes.push(code);
    } else if (code < 0x800) {
      bytes.push(0xc0 | (code >> 6), 0x80 | (code & 0x3f));
    } else if (code < 0x10000) {
      bytes.push(
        0xe0 | (code >> 12),
        0x80 | ((code >> 6) & 0x3f),
        0x80 | (code & 0x3f),
      );
    } else {
      bytes.push(
        0xf0 | (code >> 18),
        0x80 | ((code >> 12) & 0x3f),
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

    k1 =
      ((k1 & 0xffff) * c1 + ((((k1 >>> 16) * c1) & 0xffff) << 16)) & 0xffffffff;
    k1 = (k1 << 15) | (k1 >>> 17);
    k1 =
      ((k1 & 0xffff) * c2 + ((((k1 >>> 16) * c2) & 0xffff) << 16)) & 0xffffffff;

    h1 ^= k1;
    h1 = (h1 << 13) | (h1 >>> 19);
    const h1b =
      ((h1 & 0xffff) * 5 + ((((h1 >>> 16) * 5) & 0xffff) << 16)) & 0xffffffff;
    h1 = (h1b & 0xffff) + 0x6b64 + ((((h1b >>> 16) + 0xe654) & 0xffff) << 16);
  }

  // Tail bytes
  let k1 = 0;
  switch (remainder) {
    case 3:
      k1 ^= (key[i + 2] & 0xff) << 16; // falls through
    // eslint-disable-next-line no-fallthrough
    case 2:
      k1 ^= (key[i + 1] & 0xff) << 8; // falls through
    // eslint-disable-next-line no-fallthrough
    case 1:
      k1 ^= key[i] & 0xff;
      k1 =
        ((k1 & 0xffff) * c1 + ((((k1 >>> 16) * c1) & 0xffff) << 16)) &
        0xffffffff;
      k1 = (k1 << 15) | (k1 >>> 17);
      k1 =
        ((k1 & 0xffff) * c2 + ((((k1 >>> 16) * c2) & 0xffff) << 16)) &
        0xffffffff;
      h1 ^= k1;
  }

  h1 ^= len;
  h1 ^= h1 >>> 16;
  h1 =
    ((h1 & 0xffff) * 0x85ebca6b +
      ((((h1 >>> 16) * 0x85ebca6b) & 0xffff) << 16)) &
    0xffffffff;
  h1 ^= h1 >>> 13;
  h1 =
    ((h1 & 0xffff) * 0xc2b2ae35 +
      ((((h1 >>> 16) * 0xc2b2ae35) & 0xffff) << 16)) &
    0xffffffff;
  h1 ^= h1 >>> 16;
  return h1 >>> 0;
}

/**
 * Inline FNV-32a (Fowler–Noll–Vo 1a). Operates directly on UTF-16 code
 * units (no UTF-8 byte encoding, unlike murmur32 above) — matches
 * @tombstone/core's `fnv` closure in evaluation.ts's isInRollout exactly,
 * char code by char code, with no masking.
 */
function fnv32a(str: string): number {
  let h = 0x811c9dc5;
  for (let i = 0; i < str.length; i++) {
    h ^= str.charCodeAt(i);
    h = Math.imul(h, 0x01000193) >>> 0;
  }
  return h >>> 0;
}

/**
 * Hash v2: double-FNV32a, 10,000-bucket — exact port of @tombstone/core's
 * isInRollout hashVersion===2 branch (evaluation.ts): inner = fnv32a of
 * the concatenated flag key + user id; outer = fnv32a of the DECIMAL
 * STRING of inner (not the raw number); bucket = (outer % 10000) / 10000.
 * Returns a float in [0, 1).
 */
function hashV2(flagKey: string, userId: string): number {
  const inner = fnv32a(flagKey + userId);
  const outer = fnv32a(String(inner));
  return (outer % 10000) / 10000;
}

// ---------------------------------------------------------------------------
// Public types
// ---------------------------------------------------------------------------

export type EvaluationReason = "OFF" | "FALLTHROUGH" | "RULE_MATCH" | "ERROR";

export type RuleOperator =
  | "IN"
  | "NOT_IN"
  | "EQ"
  | "NEQ"
  | "LT"
  | "LTE"
  | "GT"
  | "GTE"
  | "CONTAINS"
  | "PREFIX"
  | "SUFFIX";

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
  return murmur32(flagKey + userId) % 100 < rolloutPct;
}

// ---------------------------------------------------------------------------
// Targeting rule evaluation
// ---------------------------------------------------------------------------

/**
 * Takes the RAW (not pre-stringified) attribute value, exactly matching
 * @tombstone/core's applyOperator: string ops stringify internally, but
 * LT/LTE/GT/GTE call Number() on the raw value directly (not its string
 * form -- Number(true)=1, but Number(String(true))=Number('true')=NaN),
 * and CONTAINS/PREFIX/SUFFIX require the raw value to already be a
 * string (a number or boolean can never match, matching Core's explicit
 * `typeof value === 'string'` guard) rather than matching against its
 * stringified form. Pre-stringifying here (a prior version of this file
 * did) silently changed rule-match results whenever a context attribute
 * was a boolean or number, not a string -- found by adversarial review
 * of PR #207.
 */
function evaluateOperator(
  operator: RuleOperator,
  value: unknown,
  ruleValues: unknown[],
): boolean {
  const strValue = String(value);
  switch (operator) {
    case "IN":
      return ruleValues.some((v) => String(v) === strValue);
    case "NOT_IN":
      return ruleValues.every((v) => String(v) !== strValue);
    case "EQ":
      return strValue === String(ruleValues[0] ?? "");
    case "NEQ":
      return strValue !== String(ruleValues[0] ?? "");
    case "CONTAINS":
      return (
        typeof value === "string" &&
        ruleValues.some((v) => value.includes(String(v)))
      );
    case "PREFIX":
      return (
        typeof value === "string" &&
        ruleValues.some((v) => value.startsWith(String(v)))
      );
    case "SUFFIX":
      return (
        typeof value === "string" &&
        ruleValues.some((v) => value.endsWith(String(v)))
      );
    case "LT": {
      const n = Number(value);
      return Number.isFinite(n) && n < Number(ruleValues[0] ?? 0);
    }
    case "LTE": {
      const n = Number(value);
      return Number.isFinite(n) && n <= Number(ruleValues[0] ?? 0);
    }
    case "GT": {
      const n = Number(value);
      return Number.isFinite(n) && n > Number(ruleValues[0] ?? 0);
    }
    case "GTE": {
      const n = Number(value);
      return Number.isFinite(n) && n >= Number(ruleValues[0] ?? 0);
    }
    default:
      return false;
  }
}

function evaluateRule(rule: TargetingRule, context: EvalContext): boolean {
  const rawValue = context[rule.attribute];
  if (rawValue === undefined || rawValue === null) return false;
  return evaluateOperator(rule.operator as RuleOperator, rawValue, rule.values);
}

/**
 * Converts a flag's stored `safeDefault` string into the same type as the
 * caller's `defaultValue`, exact port of @tombstone/core's
 * `EvaluationEngine.parseSafeDefault`. Used on the OFF path (Step 2 below)
 * instead of the caller's `defaultValue` — the flag's own configured
 * off-state value takes precedence, matching Core exactly.
 */
function parseSafeDefault(safeDefault: string, fallback: unknown): unknown {
  try {
    if (typeof fallback === "boolean") return safeDefault === "true";
    if (typeof fallback === "number") {
      const n = Number(safeDefault);
      return isNaN(n) ? fallback : n;
    }
    if (typeof fallback === "string") return safeDefault;
    return JSON.parse(safeDefault);
  } catch {
    return fallback;
  }
}

// ---------------------------------------------------------------------------
// Main evaluate() — matches @tombstone/core's steps 1, 4 (renumbered 3 here),
// and 5 of its 5-step pipeline.
//
// NOT YET IMPLEMENTED (disclosed gap, not a bug): Core's step 2
// (prerequisites) and step 3 (individual target list) require looking up
// OTHER flags via a cache/lookup abstraction this zero-dependency engine
// does not yet expose — see SDK-2 follow-up.
// ---------------------------------------------------------------------------

/**
 * Evaluate a flag for the given context.
 *
 * Pipeline:
 *   1. Guard — flag must be defined
 *   2. OFF check — flag.enabled must be true. Returns flag.safeDefault
 *      (converted to defaultValue's type), NOT the caller's defaultValue
 *      verbatim — matching @tombstone/core exactly.
 *   3. Targeting rules (sorted ascending by priority — 0 = highest,
 *      matching @tombstone/core; first match wins)
 *   4. Rollout hash check (MurmurHash3 v1 or double-FNV32a v2, both
 *      byte-for-byte identical to @tombstone/core)
 *   5. Default fallthrough
 */
export function evaluate(
  flag: FlagState,
  context: EvalContext,
  defaultValue: unknown,
): EvalResult {
  // Step 1: guard
  if (!flag) {
    return { value: defaultValue, reason: "ERROR" };
  }

  // Step 2: OFF
  if (!flag.enabled) {
    return {
      value: parseSafeDefault(flag.safeDefault, defaultValue),
      reason: "OFF",
    };
  }

  // Step 3: targeting rules — ascending priority (0 = highest)
  const rules = flag.targetingRules ?? [];
  const sortedRules = [...rules].sort((a, b) => a.priority - b.priority);
  for (const rule of sortedRules) {
    if (evaluateRule(rule, context)) {
      return { value: rule.variation, reason: "RULE_MATCH" };
    }
  }

  // Step 4: rollout
  const userId = typeof context.userId === "string" ? context.userId : "";
  const hashVersion = flag.hashVersion ?? 1;
  if (isInRollout(flag.flagKey, userId, flag.rolloutPct, hashVersion)) {
    return { value: true, reason: "FALLTHROUGH" };
  }

  // Step 5: default fallthrough
  return { value: defaultValue, reason: "FALLTHROUGH" };
}
