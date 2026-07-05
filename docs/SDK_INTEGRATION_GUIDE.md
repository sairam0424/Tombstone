# SDK Integration Guide

This guide covers integrating Tombstone SDKs into your applications — from picking the right SDK to testing patterns and common gotchas.

---

## Which SDK to Use

| Runtime | SDK | npm / pip | When to use |
|---------|-----|-----------|-------------|
| Node.js / server-side TypeScript | `@tomb-stone/core` | npm | API routes, background jobs, server rendering |
| React (browser) | `@tomb-stone/react` | npm | Client-side React apps |
| Cloudflare Workers / Edge | `@tomb-stone/edge` | npm | Edge functions with Cloudflare KV |
| Browser (CDN, no bundler) | `@tomb-stone/browser` | npm | Plain HTML/JS, no Node.js dependencies |
| Python | `tombstone-sdk` | pip | Django, FastAPI, Flask backends |
| WASM / embedded | `@tomb-stone/eval` | npm | Zero-dependency evaluation engine |
| OpenFeature users | `@tomb-stone/core` (provider) | npm | When standardizing across multiple flag vendors |
| Java | `tombstone-java-sdk` | Maven | JVM services |
| Ruby | `tombstone-ruby` | RubyGems | Rails, Sinatra |
| .NET | `Tombstone.SDK` | NuGet | ASP.NET Core |

**Note**: Physical SDK directories are `@flagmind/*` in the monorepo. The published npm package names are `@tomb-stone/*`. Install via npm name, not directory name.

---

## TypeScript / Node.js Quickstart

```typescript
import { TombstoneClient } from '@tomb-stone/core';

// 1. Initialize (once, at application startup)
const client = new TombstoneClient({
  apiUrl: 'http://localhost:8081',      // flag-api URL
  sdkKey: process.env.FLAG_API_TOKEN,  // per-environment SDK key
  environment: 'production',
});

// 2. Connect: fetches snapshot, opens SSE stream for real-time updates
await client.connect();

// 3. Evaluate a boolean flag
const enabled = await client.isEnabled('checkout-v2', {
  userId: 'user-123',
  email: 'user@example.com',
  attrs: { plan: 'enterprise' },  // custom attributes for targeting rules
});

// 4. Get a multivariate variation
const variant = await client.getVariation('checkout-layout', 'control', {
  userId: 'user-123',
});
// Returns the variation key (e.g. "control", "treatment_a", "treatment_b")

// 5. Graceful shutdown (flushes telemetry, closes SSE connection)
await client.shutdown();
```

**`connect()` vs `initialize()`**: Use `connect()` — it fetches the full flag snapshot AND opens the SSE stream. The client serves evaluations from an in-process cache, so `isEnabled()` calls are synchronous after connect.

---

## React Quickstart

```tsx
import { TombstoneProvider, useFlag } from '@tomb-stone/react';

// 1. Wrap your app with TombstoneProvider
function App() {
  return (
    <TombstoneProvider
      apiUrl={process.env.NEXT_PUBLIC_FLAG_API_URL}
      sdkKey={process.env.NEXT_PUBLIC_SDK_KEY}
      environment="production"
    >
      <YourApp />
    </TombstoneProvider>
  );
}

// 2. Use useFlag() in any component
function CheckoutButton() {
  const isNewCheckout = useFlag('checkout-v2');  // boolean
  return isNewCheckout ? <NewCheckout /> : <LegacyCheckout />;
}

// 3. Feature gate pattern (reusable component)
function FeatureGate({ flagKey, children, fallback = null }) {
  const enabled = useFlag(flagKey);
  return enabled ? children : fallback;
}
```

The provider opens an SSE stream and re-renders components automatically when flag values change.

---

## Python Quickstart

```python
from tombstone import TombstoneClient

# 1. Initialize
client = TombstoneClient(
    api_url="http://localhost:8081",
    sdk_key=os.environ["FLAG_API_TOKEN"],
    environment="production",
)
client.initialize()  # fetches snapshot synchronously

# 2. Evaluate
enabled = client.is_enabled("checkout-v2", {
    "user_id": "user-123",
    "email": "user@example.com",
})

# 3. Variation
variant = client.get_variation("checkout-layout", "control", {"user_id": "user-123"})
```

**Python SDK differences from TypeScript**: The Python SDK implements a simplified 3-step pipeline (preliminary → targeting → rollout) without prerequisites or full rule-matching. For applications that use prerequisite flags or complex targeting rules, use the TypeScript SDK. See `docs/SDK_CONTRACT.md` for the exact feature parity matrix.

---

## Testing with TombstoneTestClient

`TombstoneTestClient` provides deterministic, no-server-required flag evaluation for unit tests. All methods are synchronous.

```typescript
import { TombstoneTestClient } from '@tomb-stone/core';

// 1. Isolated client — all flags return their defaultValue by default
const client = TombstoneTestClient.createIsolated();

// 2. Override specific flags
client.override('checkout-v2', true);
expect(client.evaluate('checkout-v2', false)).toBe(true);

// 3. Static factory — pre-configure flags
const client2 = TombstoneTestClient.withFlags({
  'checkout-v2': true,
  'new-pricing': false,
  'experiment-variant': 'treatment_a',
});

// 4. Deterministic bucket assignment for rollout testing
const client3 = TombstoneTestClient.createIsolated();
client3.assignToBucket('checkout-v2', 'user-123', true);   // user-123 is IN rollout
client3.assignToBucket('checkout-v2', 'user-456', false);  // user-456 is NOT in rollout

expect(client3.isEnabled('checkout-v2', { userId: 'user-123' })).toBe(true);
expect(client3.isEnabled('checkout-v2', { userId: 'user-456' })).toBe(false);

// 5. Clear overrides between tests
client.clearOverrides();
client.clearOverride('checkout-v2');  // clear single flag
```

**Why this matters**: `TombstoneTestClient` never makes network calls. Tests run without a running flag-api, are reproducible, and are fast. All methods are immutable (new Map on each update) — no state leaks between test cases.

---

## OpenFeature Provider

Use the `TombstoneProvider` when your organization standardizes on the OpenFeature SDK for vendor portability.

```typescript
import { OpenFeature } from '@openfeature/server-sdk';
import { TombstoneProvider } from '@tomb-stone/core';

// Initialize the provider
const provider = new TombstoneProvider({
  apiUrl: 'http://localhost:8081',
  sdkKey: process.env.FLAG_API_TOKEN,
  environment: 'production',
});

await OpenFeature.setProviderAndWait(provider);
const client = OpenFeature.getClient();

// Evaluate using the OpenFeature interface
const enabled = await client.getBooleanValue('checkout-v2', false, {
  targetingKey: 'user-123',
});

// Lifecycle — always call onClose during graceful shutdown
await OpenFeature.close();
```

**When to use native SDK vs OpenFeature provider**:
- Native SDK: gives access to Tombstone-specific features (blast radius, prerequisite details, `EvaluationResult.reason`)
- OpenFeature provider: use when you need to swap flag vendors without code changes, or when integrating with OpenFeature-aware frameworks

---

## Common Mistakes and Gotchas

### Package name vs directory name

```bash
# CORRECT: install by npm package name
npm install @tomb-stone/core @tomb-stone/react

# WRONG: @flagmind is the internal monorepo directory name, not the npm package
npm install @flagmind/core  # will fail in most environments
```

### SDK key is per-environment

Never share the same `sdkKey` across production and staging. Each environment has a separate key from `service_tokens` table.

### Python SDK feature parity

The Python SDK does not implement:
- Prerequisite flag evaluation (prerequisite flags are always treated as met)
- Complex targeting rules (only simple rollout percentage is evaluated)
- OpenFeature provider

For full feature parity on Python services, call the flag-api REST API directly or use the TypeScript SDK via a sidecar.

### Edge SDK requires Cloudflare KV binding

```typescript
// Edge SDK (Cloudflare Workers) requires KV namespace
import { TombstoneEdgeClient } from '@tomb-stone/edge';

const client = new TombstoneEdgeClient({
  apiUrl: env.FLAG_API_URL,
  sdkKey: env.FLAG_API_TOKEN,
  environment: 'production',
  kv: env.TOMBSTONE_KV,  // KV namespace binding — required
});
```

Without the KV binding, the edge SDK cannot cache flag snapshots and falls back to default values on every request.

### Don't poll — use SSE streaming

```typescript
// BAD: polling hits rate limits (1000 req/min SDK tier)
setInterval(async () => {
  const enabled = await fetchFlagFromAPI('checkout-v2');
}, 1000);

// GOOD: SSE stream updates the in-process cache automatically
await client.connect();  // opens SSE, updates cache in real time
const enabled = client.isEnabled('checkout-v2', context);  // cache hit, zero latency
```

---

## Debugging

### Verify SDK connection

```bash
# Check that the gateway SSE stream is active
curl -N http://localhost:8080/api/v1/stream?environment=production \
  -H "Authorization: Bearer $FLAG_API_TOKEN"
# Should stream: data: {"flag_key":"...","enabled":true,...}
```

### Verify flag state at flag-api

```bash
# Get current flag state
curl http://localhost:8081/api/v1/flags/checkout-v2?environment=production \
  -H "Authorization: Bearer $FLAG_API_TOKEN"

# Get full snapshot for an environment
curl "http://localhost:8081/api/v1/environments/snapshot?environment=production" \
  -H "Authorization: Bearer $FLAG_API_TOKEN"
```

### Check SDK cache contents (TypeScript)

```typescript
// FlagCache exposes cache metadata
console.log('Cached flags:', client.cache.keys());
console.log('Cache hash:', client.cache.getHash());
console.log('Cache size:', client.cache.size());
```
