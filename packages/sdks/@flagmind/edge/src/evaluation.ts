import type {
  EvaluationContext,
  EvaluationResult,
  FlagEnvironmentState,
  FlagPrerequisite,
  TargetingRule,
} from "./types.js";

/**
 * Lookup capability for resolving a prerequisite flag's own state during
 * recursive prerequisite checking. `EdgeFlagClient` builds this from the
 * same snapshot it's already loaded — there is no extra fetch per
 * prerequisite. Optional on `evaluate()` itself: a direct low-level caller
 * that passes no cache simply gets no prerequisite enforcement (permissive,
 * matching this function's pre-prerequisites behavior exactly).
 */
export interface FlagLookup {
  get(flagKey: string): FlagEnvironmentState | undefined;
}

// Mirrors @tombstone/core's EvaluationEngine.MAX_PREREQ_DEPTH — bounds a
// cyclic or very deep prerequisite chain so evaluate() can never recurse
// unboundedly. Once the cap is hit, prerequisite enforcement is simply
// skipped for that flag (permissive fallthrough), not treated as a failure.
const MAX_PREREQ_DEPTH = 5;

// Inline MurmurHash3 x86 32-bit (seed=0, UTF-8 byte encoding) — vendored,
// byte-for-byte identical port of @tombstone/eval's (packages/sdk-wasm)
// murmur32(), which itself is a verified exact port of the `murmurhash`
// npm package @tombstone/core uses. Deliberately vendored rather than
// imported: this package has zero runtime dependencies by design (must
// run in the Cloudflare Workers runtime, no Node APIs), and this repo has
// no publish/build convention yet for sharing code between SDK packages
// outside npm workspaces. Previously this hash was FNV-1a, which produces
// a DIFFERENT bucket than @tombstone/core/@tombstone/eval for the same
// flag+user — a real cross-SDK rollout inconsistency the old code's own
// comment disclosed but never fixed. Keep this in sync with
// packages/sdk-wasm/src/index.ts's murmur32 if either ever changes.
function murmur32(str: string): number {
  // Must combine UTF-16 surrogate pairs into a single >= U+10000 code
  // point BEFORE branching on byte width -- without this, each half of a
  // surrogate pair (0xD800-0xDFFF) falls into the 3-byte branch on its
  // own, producing a wrong byte sequence for any emoji/supplementary-
  // plane character (found by adversarial review of PR #207).
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

// This package only ever evaluates hashVersion 1 (MurmurHash3) — the
// KV-cached FlagEnvironmentState snapshot has no hashVersion field at
// all, a deliberate, disclosed limitation (see types.ts), not a bug.
function isInRollout(
  flagKey: string,
  userId: string,
  rolloutPct: number,
): boolean {
  if (rolloutPct >= 100) return true;
  if (rolloutPct <= 0) return false;
  const bucket = murmur32(flagKey + userId) % 100;
  return bucket < rolloutPct;
}

/**
 * Converts a flag's stored `safeDefault` string into the same type as the
 * caller's `defaultValue` — exact port of @tombstone/core's
 * `EvaluationEngine.parseSafeDefault` (also vendored into
 * @tombstone/eval). Used on the OFF path below instead of the caller's
 * `defaultValue` verbatim — the flag's own configured off-state value
 * takes precedence, matching Core exactly.
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

/**
 * Recursively checks `prereqs` against `cache`, mirroring
 * @tombstone/core's EvaluationEngine.checkPrerequisites exactly:
 * - A missing prerequisite flag state with `gate: true` fails closed
 *   (PREREQUISITE_FAILED) — nothing to evaluate, so it cannot pass.
 * - A missing prerequisite flag state with `gate: false` is skipped —
 *   nothing to gate on, so evaluation continues.
 * - A found prerequisite flag is evaluated through the FULL pipeline
 *   (recursively, so ITS OWN prerequisites/rollout apply too), and its
 *   stringified result is compared against `requiredVariation`. A mismatch
 *   with `gate: true` fails closed; a mismatch with `gate: false` is
 *   skipped.
 * Returns null when every prerequisite passed (or was non-gating), meaning
 * the caller should continue to its own rollout evaluation.
 */
function checkPrerequisites<T>(
  prereqs: FlagPrerequisite[],
  context: EvaluationContext,
  defaultValue: T,
  cache: FlagLookup,
  parentKey: string,
  depth: number,
): EvaluationResult<T> | null {
  for (const prereq of prereqs) {
    const prereqState = cache.get(prereq.flagKey);
    if (!prereqState) {
      if (prereq.gate) {
        return {
          value: defaultValue,
          reason: "PREREQUISITE_FAILED",
          fromCache: true,
          flagKey: parentKey,
          ruleId: prereq.flagKey,
        };
      }
      continue;
    }
    const prereqResult = evaluate<string>(
      prereqState,
      context,
      prereqState.safeDefault,
      prereq.flagKey,
      cache,
      depth + 1,
    );
    if (
      String(prereqResult.value) !== prereq.requiredVariation &&
      prereq.gate
    ) {
      return {
        value: defaultValue,
        reason: "PREREQUISITE_FAILED",
        fromCache: true,
        flagKey: parentKey,
        ruleId: prereq.flagKey,
      };
    }
  }
  return null;
}

// ─── Step 4: rule matching — exact port of @tombstone/core's matchesRule/
// resolveAttribute/applyOperator (evaluation.ts). Kept in sync deliberately:
// this SDK has zero runtime dependencies and can't import Core directly.

function matchesRule(rule: TargetingRule, context: EvaluationContext): boolean {
  // GEO operators resolve values from context.geo, not the generic attribute path.
  if (rule.operator === "GEO_COUNTRY") {
    const country = (context.geo?.country ?? "").toUpperCase();
    return (rule.values as string[])
      .map((v) => String(v).toUpperCase())
      .includes(country);
  }
  if (rule.operator === "GEO_REGION") {
    const region = (context.geo?.region ?? "").toUpperCase();
    return (rule.values as string[])
      .map((v) => String(v).toUpperCase())
      .includes(region);
  }

  const raw = resolveAttribute(rule.attribute, context);
  if (raw === undefined || raw === null) return false;
  return applyOperator(rule.operator, raw, rule.values);
}

function resolveAttribute(path: string, context: EvaluationContext): unknown {
  const segments = path.split(".");
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  let current: any = context;
  for (const seg of segments) {
    if (current == null || typeof current !== "object") {
      current = undefined;
      break;
    }
    current = (current as Record<string, unknown>)[seg];
  }
  if (current !== undefined) return current;
  if (segments.length === 1 && context.attrs !== undefined)
    return context.attrs[path];
  return undefined;
}

function applyOperator(
  operator: string,
  value: unknown,
  ruleValues: unknown[],
): boolean {
  const strValue = String(value);
  switch (operator) {
    case "IN":
      return ruleValues.some((v) => String(v) === strValue);
    // NOT_IN with an EMPTY ruleValues must NOT match — see @tombstone/core's
    // identical guard (found by adversarial review of PR #246): an empty/
    // missing "values" list must mean "excludes nothing", not "matches
    // everything" (Array.prototype.every is vacuously true on []).
    case "NOT_IN":
      return (
        ruleValues.length > 0 && ruleValues.every((v) => String(v) !== strValue)
      );
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
    // REGEX/SEMVER_GTE/SEMVER_LTE/DATE_BEFORE/DATE_AFTER intentionally fall
    // through to false — matches @tombstone/core's own applyOperator
    // exactly (a shared TypeScript-wide parity gap, not Edge-specific; see
    // TargetingRule's own doc comment).
    default:
      return false;
  }
}

export function evaluate<T = boolean>(
  flagState: FlagEnvironmentState | undefined,
  context: EvaluationContext,
  defaultValue: T,
  flagKey: string,
  cache?: FlagLookup,
  depth = 0,
): EvaluationResult<T> {
  if (!flagState) {
    return { value: defaultValue, reason: "ERROR", fromCache: false, flagKey };
  }
  if (!flagState.enabled) {
    return {
      value: parseSafeDefault(flagState.safeDefault, defaultValue) as T,
      reason: "OFF",
      fromCache: true,
      flagKey,
    };
  }
  // A non-finite depth (NaN, ±Infinity) would make `depth < MAX_PREREQ_DEPTH`
  // silently evaluate to false, disabling prerequisite enforcement with no
  // error — found by adversarial review of PR #244 probing evaluate()'s
  // now-public signature directly. depth is an internal recursion counter,
  // never derived from snapshot/network data, but a direct low-level
  // caller could still pass a bad value by mistake.
  const safeDepth = Number.isFinite(depth) ? depth : 0;
  const prereqs = flagState.prerequisites ?? [];
  if (prereqs.length > 0 && cache && safeDepth < MAX_PREREQ_DEPTH) {
    const blocked = checkPrerequisites<T>(
      prereqs,
      context,
      defaultValue,
      cache,
      flagKey,
      safeDepth,
    );
    if (blocked !== null) return blocked;
  }
  // Step 4: rule matching — ascending priority (0 = highest), mirroring
  // @tombstone/core's evaluateInternal exactly.
  const sortedRules = [...(flagState.targetingRules ?? [])].sort(
    (a, b) => a.priority - b.priority,
  );
  for (const rule of sortedRules) {
    if (matchesRule(rule, context)) {
      return {
        value: rule.variation as unknown as T,
        reason: "RULE_MATCH",
        fromCache: true,
        flagKey,
        ruleId: rule.id,
      };
    }
  }
  if (flagState.rolloutPct >= 100) {
    return {
      value: true as unknown as T,
      reason: "FALLTHROUGH",
      fromCache: true,
      flagKey,
    };
  }
  if (flagState.rolloutPct <= 0) {
    return {
      value: defaultValue,
      reason: "FALLTHROUGH",
      fromCache: true,
      flagKey,
    };
  }
  if (isInRollout(flagKey, context.userId, flagState.rolloutPct)) {
    return {
      value: true as unknown as T,
      reason: "FALLTHROUGH",
      fromCache: true,
      flagKey,
    };
  }
  return {
    value: defaultValue,
    reason: "FALLTHROUGH",
    fromCache: true,
    flagKey,
  };
}
