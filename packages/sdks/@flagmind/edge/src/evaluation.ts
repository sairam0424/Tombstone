import type {
  EvaluationContext,
  EvaluationResult,
  FlagEnvironmentState,
} from "./types.js";

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

export function evaluate<T = boolean>(
  flagState: FlagEnvironmentState | undefined,
  context: EvaluationContext,
  defaultValue: T,
  flagKey: string,
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
