# @tombstone/core

Node.js SDK for Tombstone — in-process flag evaluation with SSE streaming.

**Version:** 2.0.1 | **Requires:** Node.js >= 18

## Installation

```bash
npm install @tombstone/core
```

## Quick Start

```typescript
import { TombstoneClient } from '@tombstone/core';

const client = new TombstoneClient({
  sdkKey: process.env.TOMBSTONE_SDK_KEY!,
  environment: 'production',
  apiUrl: 'https://flags.example.com',
  defaults: {
    checkout_v2: false,
    theme: 'default',
  },
});

// Connect: fetches snapshot, loads cache, opens SSE stream.
// Call once at startup — must complete before evaluate().
await client.connect();

// Synchronous evaluation — <0.5ms, never throws
const result = client.evaluate<boolean>('checkout_v2', { userId: 'u_abc123' });
console.log(result.value); // true | false

// Convenience boolean check
function showNewCheckout(userId: string): boolean {
  return client.evaluate<boolean>('checkout_v2', { userId }).value;
}
```

## TombstoneClient Config

```typescript
interface TombstoneClientConfig {
  sdkKey: string;           // SDK token issued from the Tombstone dashboard
  environment: string;      // 'production' | 'staging' | 'development' | custom
  apiUrl?: string;          // flag-api base URL (default: http://localhost:8081)
  gatewayUrl?: string;      // SSE gateway URL (default: http://localhost:8080)
  defaults: Record<string, unknown>; // safe defaults returned when flag is off/missing
  reconnectIntervalMs?: number;  // initial SSE reconnect delay (default: 1000)
  maxReconnectMs?: number;       // max SSE reconnect backoff (default: 30000)
  telemetrySampleRate?: number;  // evaluation telemetry sample rate 0–1 (default: 0.01)
}
```

## evaluate() and isEnabled()

```typescript
// Full result with reason and metadata
const result = client.evaluate<boolean>('feature_flag', {
  userId: 'u_abc123',
  orgId: 'org_acme',
  device: 'mobile',
  geo: { country: 'US', region: 'CA' },
  attrs: { plan: 'enterprise' },
});
// result.value   — resolved flag value
// result.reason  — 'OFF' | 'FALLTHROUGH' | 'RULE_MATCH' | 'TARGET_MATCH' | 'PREREQUISITE_FAILED' | 'ERROR'
// result.fromCache — always true for in-memory evaluation
// result.flagKey  — echoes the flag key
// result.ruleId   — set when reason === 'RULE_MATCH'

// Multi-variate flags
const theme = client.evaluate<string>('ui_theme', { userId: 'u_abc123' });
console.log(theme.value); // 'dark' | 'light' | 'default'
```

## v2 Features

### evaluateWithDetail()

`evaluateWithDetail()` is the primary low-level API. It accepts a `FlagLookup`
cache interface directly and returns the full `EvaluationResult<T>` including
reason, ruleId, and variationIndex.

`TombstoneClient.evaluate()` internally calls `evaluateWithDetail()` after
resolving the cache — the two produce identical results.

### 5-Step Evaluation Pipeline

Every flag evaluation runs through five sequential steps:

1. **Preliminary** — flag missing or disabled returns `OFF`
2. **Prerequisites** — gating prereqs must pass or evaluation returns `PREREQUISITE_FAILED`
3. **Individual targeting** — userId in explicit `targetList` returns `TARGET_MATCH`
4. **Rule matching** — first priority-sorted targeting rule match returns `RULE_MATCH`
5. **Fallthrough** — MurmurHash/FNV rollout bucket returns `FALLTHROUGH` or safe default

### EvaluationContext with Geo and Device

```typescript
import type { EvaluationContext } from '@tombstone/core';

const context: EvaluationContext = {
  userId: 'u_abc123',        // opaque hash — never raw PII
  orgId: 'org_acme',
  device: 'mobile',
  geo: {
    country: 'US',           // ISO 3166-1 alpha-2
    region: 'CA',            // ISO 3166-2 region code
  },
  attrs: {
    plan: 'enterprise',      // arbitrary string attributes for rule matching
    app_version: '3.14.0',
  },
};
```

`geo.country` and `geo.region` are used by the `GEO_COUNTRY` and `GEO_REGION`
rule operators. `attrs` is a flat string map for any other custom attributes.
`userId` **must** be an opaque hash — never pass raw PII.

### Hash v1 / v2 (hashVersion)

Tombstone supports two rollout hash algorithms. The algorithm used is stored
per-flag in the `hashVersion` field of `FlagEnvironmentState`.

| `hashVersion` | Algorithm | Input |
|---|---|---|
| `1` (default) | MurmurHash3 x86 32-bit | `flagKey + userId` |
| `2` | Double-FNV32a | `flagKey + userId` (both orderings XOR'd) |

Hash v2 provides better avalanche properties for flags with short or numeric
user IDs. The SDK selects the algorithm automatically based on the flag's stored
`hashVersion`. You do not need to configure this.

```typescript
// hashVersion is a server-side flag property — no client config needed
// Both versions produce consistent, stateless bucket assignments:
//   same (flagKey, userId, rolloutPct) → same result, every time
```

### Flag Prerequisites

A flag can declare prerequisite flags that must evaluate to a required variation
before the flag itself is served. If a gating prerequisite fails, the evaluation
reason is `PREREQUISITE_FAILED` and the safe default is returned.

```typescript
// Prerequisites are server-side flag configuration — no client code required.
// Example: 'checkout_v2' requires 'payments_v3' to evaluate to 'enabled'.
// If payments_v3 is OFF, checkout_v2 returns its safe default.

const result = client.evaluate('checkout_v2', { userId: 'u_abc123' });
if (result.reason === 'PREREQUISITE_FAILED') {
  // upstream flag is not ready
}
```

Prerequisites are evaluated recursively up to a depth of 5. Non-gating
prerequisites (`gate: false`) are informational only — they do not block
evaluation if they fail.

## OpenFeature Provider

`@tombstone/core` ships a native OpenFeature server-side provider. Install the
OpenFeature SDK as a peer dependency:

```bash
npm install @openfeature/server-sdk
```

```typescript
import { OpenFeature } from '@openfeature/server-sdk';
import { TombstoneProvider } from '@tombstone/core';
import { TombstoneClient } from '@tombstone/core';

const client = new TombstoneClient({
  sdkKey: process.env.TOMBSTONE_SDK_KEY!,
  environment: 'production',
  defaults: {},
});
await client.connect();

await OpenFeature.setProviderAndWait(new TombstoneProvider(client));

const featureClient = OpenFeature.getClient();
const enabled = await featureClient.getBooleanValue('checkout_v2', false, {
  targetingKey: 'u_abc123',
});
```

The provider maps OpenFeature's `targetingKey` to `EvaluationContext.userId`
automatically.

## TombstoneTestClient

Use `TombstoneTestClient` in tests — it requires no network connection and
gives fully deterministic, override-driven evaluation.

```typescript
import { TombstoneTestClient } from '@tombstone/core';

// Create an isolated client (all flags return their default)
const client = TombstoneTestClient.createIsolated();

// Override a flag value
client.override('checkout_v2', true);
expect(client.evaluate('checkout_v2', false)).toBe(true);

// Override multiple flags via factory
const client2 = TombstoneTestClient.withFlags({
  checkout_v2: true,
  ui_theme: 'dark',
});

// Force a userId into (or out of) the rollout cohort
client.assignToBucket('experiment_flag', 'u_abc123', true);
expect(client.isEnabled('experiment_flag', { userId: 'u_abc123' })).toBe(true);

// Clear a single override
client.clearOverride('checkout_v2');

// Clear all overrides
client.clearOverrides();
```

### TombstoneTestClient API

| Method | Description |
|---|---|
| `TombstoneTestClient.createIsolated()` | Returns a new client with no overrides |
| `TombstoneTestClient.withFlags(flags)` | Returns a new client pre-loaded with flag overrides |
| `client.override(flagKey, value)` | Set a fixed value for a flag; returns `this` for chaining |
| `client.clearOverride(flagKey)` | Remove override for one flag; returns `this` |
| `client.clearOverrides()` | Remove all overrides; returns `this` |
| `client.assignToBucket(flagKey, userId, inCohort?)` | Force a userId into/out of the rollout cohort |
| `client.evaluate(flagKey, defaultValue, context?)` | Return flag value (override → bucket → default) |
| `client.isEnabled(flagKey, context?)` | Boolean convenience method |

## Rate Limiting

SDK tokens are rate-limited to **1,000 requests per minute** per token. This
limit applies to HTTP calls made during `connect()` (snapshot fetch + rules
fetch). Streaming SSE connections and in-memory `evaluate()` calls do not count
against this limit.

If your service restarts frequently (e.g. serverless cold starts), cache the
snapshot externally or use the `@tombstone/eval` WASM package for zero-network
evaluation.

## Exports

The package exposes four entry points:

```typescript
import { TombstoneClient, TombstoneTestClient } from '@tombstone/core';
import { EvaluationEngine } from '@tombstone/core/evaluation';
import { SSEStreamClient } from '@tombstone/core/streaming';
import { /* admin types */ } from '@tombstone/core/admin';
```

## TypeScript

The package is ESM-only (`"type": "module"`). All types are exported from the
root entry point. Strict mode is enabled — `EvaluationResult<T>` is generic
over the flag value type.

## Changelog

### 2.0.1
- `evaluateWithDetail()` on `EvaluationEngine` — returns full `EvaluationResult<T>` including `ruleId` and `variationIndex`
- Hash v2 (double-FNV32a) via `hashVersion: 2` on flag state
- `EvaluationContext` extended with `geo`, `device`, and `attrs` fields
- Flag prerequisites with gating (`gate: true`) and non-gating (`gate: false`) modes
- `GEO_COUNTRY` and `GEO_REGION` rule operators
- `SEMVER_GTE`, `SEMVER_LTE`, `DATE_BEFORE`, `DATE_AFTER`, `REGEX` operators added to `RuleOperator`
- `TombstoneTestClient.withFlags()` and `assignToBucket()` factory/helpers
- OpenFeature provider (`TombstoneProvider`) ships in core package
- Rate limit documented: 1,000 req/min per SDK token
