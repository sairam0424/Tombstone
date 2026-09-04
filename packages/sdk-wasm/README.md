# @tombstone/eval

Zero-dependency evaluation engine for Tombstone.

Runs in: **Node.js**, **browser**, **Deno**, **Bun**, **Cloudflare Workers**, and any WASM-capable JS runtime.

**Version:** 0.1.0 | **Tests:** 46/46 passing | **Dependencies:** none

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

## Evaluation Pipeline

`evaluate()` implements 3 of `@tombstone/core`'s 5 pipeline steps — Core's
step 2 (prerequisites) and step 3 (individual target list) are **not yet
implemented** here, since they require looking up other flags via a
cache/lookup abstraction this zero-dependency engine doesn't yet expose
(a disclosed gap, not a bug — see the comparison table below):

1. **Guard** — flag must be defined (returns `ERROR` if missing)
2. **OFF check** — `flag.enabled` must be `true` (returns `OFF` with
   `flag.safeDefault`, converted to `defaultValue`'s type — NOT the
   caller's `defaultValue` verbatim, matching `@tombstone/core` exactly)
3. **Targeting rules** — rules sorted by `priority` ascending (`0` =
   highest priority, matching `@tombstone/core`); first match returns
   `RULE_MATCH`
4. **Rollout hash check** — `isInRollout()` with `flag.hashVersion`
5. **Default fallthrough** — returns `defaultValue` with reason `FALLTHROUGH`

## Hash v1 / v2

Both hash algorithms are inlined — no external packages.

| `hashVersion` | Algorithm | Notes |
|---|---|---|
| `1` (default) | MurmurHash3 x86 32-bit | Identical to `murmurhash` npm `v3()` |
| `2` | Double-FNV32a, 10,000-bucket | `inner = fnv32a(flagKey+userId)`, `outer = fnv32a(String(inner))`, `bucket = (outer % 10000) / 10000` — same algorithm as `@tombstone/core`'s `isInRollout` hashVersion===2 branch |

```typescript
// Hash v1 (MurmurHash3)
isInRollout('my_flag', 'u123', 50, 1);

// Hash v2 (double-FNV32a)
isInRollout('my_flag', 'u123', 50, 2);
```

Both hash algorithms produce bucket assignments that match `@tombstone/core`
for the same inputs, byte-for-byte, verified against the real cross-SDK
contract vectors in `packages/sdks/test-contract/vectors.json` (not a
hand-copied fixture).

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
| Targeting rules (single-condition, IN/EQ/CONTAINS/etc.) | Yes | Yes |
| Prerequisites (Core's step 2) | **No** — not yet implemented | Yes |
| Individual target list (Core's step 3) | **No** — not yet implemented | Yes |
| Pipeline steps implemented | 3 of 5 | 5 of 5 |

Use `@tombstone/eval` when you need evaluation without a persistent SSE
connection — edge functions, browser bundles, serverless cold starts, or any
non-Node runtime, **and your flags don't rely on prerequisites or individual
user targeting** (not yet supported here — see the pipeline gap above). Use
`@tombstone/core` for long-lived server processes, or any flag that needs the
full 5-step pipeline.

## Tests

```bash
npm test
# 46 passing
```

Tests run against the real cross-SDK contract vectors in
`packages/sdks/test-contract/vectors.json` (not a hand-copied fixture), plus
all rule operators, rollout edge cases (0%, 100%, boundary), safeDefault-on-OFF,
rule-priority ordering, disabled flags, and missing flags.
