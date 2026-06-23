# @tombstone/eval

Zero-dependency evaluation engine for Tombstone.

Runs in: **Node.js**, **browser**, **Deno**, **Bun**, **Cloudflare Workers**, and any WASM-capable JS runtime.

**Version:** 0.1.0 | **Tests:** 41/41 passing | **Dependencies:** none

## Why @tombstone/eval?

`@tombstone/core` depends on `murmurhash` and `eventsource` — npm packages that
do not run in all JavaScript runtimes. `@tombstone/eval` eliminates both
dependencies by inlining:

- **MurmurHash3 x86 32-bit** — exact port of the `murmurhash` npm `v3()` method,
  producing identical bucket assignments to `@tombstone/core`
- **FNV-32a** — used as the building block for hash v2

The result is a single TypeScript file with no external imports that runs
everywhere a JS engine runs.

## Installation

```bash
npm install @tombstone/eval
```

## Usage

```typescript
import { evaluate, isInRollout } from '@tombstone/eval';
import type { FlagState, EvalContext, EvalResult } from '@tombstone/eval';

// isInRollout — consistent, stateless rollout check
const inCohort = isInRollout('my_flag', 'user_abc123', 50);
// true if the user falls within the 50% rollout for this flag

// evaluate — full 5-step pipeline
const flag: FlagState = {
  flagKey: 'checkout_v2',
  enabled: true,
  rolloutPct: 50,
  safeDefault: 'false',
  hashVersion: 1,
  targetingRules: [
    {
      attribute: 'plan',
      operator: 'IN',
      values: ['enterprise', 'pro'],
      variation: 'true',
      priority: 10,
    },
  ],
};

const context: EvalContext = {
  userId: 'user_abc123',
  plan: 'enterprise',
};

const result: EvalResult = evaluate(flag, context, false);
// result.value  — resolved flag value
// result.reason — 'OFF' | 'FALLTHROUGH' | 'RULE_MATCH' | 'ERROR'
```

## API

### `evaluate(flag, context, defaultValue)`

Runs the full 5-step evaluation pipeline on a single `FlagState`.

```typescript
function evaluate(
  flag: FlagState,
  context: EvalContext,
  defaultValue: unknown,
): EvalResult
```

Returns an `EvalResult` with `value` and `reason`. Never throws — on any
internal error returns `{ value: defaultValue, reason: 'ERROR' }`.

### `isInRollout(flagKey, userId, rolloutPct, hashVersion?)`

Returns `true` if the given `userId` falls within the rollout percentage for
the given `flagKey`. Consistent — same inputs always produce the same result.
Stateless — no external state required.

```typescript
function isInRollout(
  flagKey: string,
  userId: string,
  rolloutPct: number,
  hashVersion?: 1 | 2,   // default: 1
): boolean
```

## 5-Step Evaluation Pipeline

`evaluate()` implements the same pipeline as `@tombstone/core`:

1. **Guard** — flag must be defined (returns `ERROR` if missing)
2. **OFF check** — `flag.enabled` must be `true` (returns `OFF` if false)
3. **Targeting rules** — rules sorted by `priority` descending (higher number = higher priority in this engine); first match returns `RULE_MATCH`
4. **Rollout hash check** — `isInRollout()` with `flag.hashVersion`
5. **Default fallthrough** — returns `defaultValue` with reason `FALLTHROUGH`

## Hash v1 / v2

Both hash algorithms are inlined — no external packages.

| `hashVersion` | Algorithm | Notes |
|---|---|---|
| `1` (default) | MurmurHash3 x86 32-bit | Identical to `murmurhash` npm `v3()` |
| `2` | Double-FNV32a | Better avalanche for short/numeric user IDs |

```typescript
// Hash v1 (MurmurHash3)
isInRollout('my_flag', 'u123', 50, 1);

// Hash v2 (double-FNV32a)
isInRollout('my_flag', 'u123', 50, 2);
```

Both hash algorithms produce consistent bucket assignments that match
`@tombstone/core` for the same inputs.

## Types

```typescript
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
```

## Cloudflare Workers Example

```typescript
// worker.ts
import { evaluate } from '@tombstone/eval';

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    const userId = request.headers.get('X-User-Id') ?? '';

    // Flag state loaded from KV or pre-bundled at build time
    const flag = await env.FLAGS_KV.get('checkout_v2', 'json');

    const result = evaluate(flag, { userId }, false);

    return new Response(JSON.stringify({ enabled: result.value }), {
      headers: { 'Content-Type': 'application/json' },
    });
  },
};
```

No `murmurhash` or `eventsource` — both were incompatible with the Workers
runtime. `@tombstone/eval` has zero external dependencies.

## Deno / Bun Example

```typescript
// deno
import { evaluate, isInRollout } from 'npm:@tombstone/eval';

// bun
import { evaluate, isInRollout } from '@tombstone/eval';
```

## Relationship to @tombstone/core

| Feature | `@tombstone/eval` | `@tombstone/core` |
|---|---|---|
| Dependencies | None | `murmurhash`, `eventsource` |
| SSE streaming | No | Yes |
| Snapshot fetch | No | Yes |
| OpenFeature provider | No | Yes |
| Browser / Workers / Deno / Bun | Yes | Node.js only |
| Bundle size | ~4KB | ~25KB |
| Hash v1 (MurmurHash3) | Inlined | npm package |
| Hash v2 (FNV32a) | Inlined | Inlined |
| Targeting rules | Yes | Yes |
| 5-step pipeline | Yes | Yes |

Use `@tombstone/eval` when you need evaluation without a persistent SSE
connection — edge functions, browser bundles, serverless cold starts, or any
non-Node runtime. Use `@tombstone/core` for long-lived server processes that
benefit from real-time flag streaming.

## Tests

```bash
npm test
# 41 passing
```

Tests cover hash v1/v2 parity with `@tombstone/core`, all rule operators,
rollout edge cases (0%, 100%, boundary), disabled flags, and missing flags.
