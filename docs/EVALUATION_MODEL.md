# Evaluation Model

This document describes how Tombstone evaluates a feature flag — from SDK call to final value — including the complete 5-step pipeline, hash bucketing, flag state semantics, blast radius scoring, and worked examples.

---

## Request Flow

```
SDK.isEnabled("checkout-v2", context)
   │
   ├─ [1] FlagCache.get("checkout-v2")         ← in-process memory Map<string, FlagEnvironmentState>
   │        hit → run evaluation pipeline locally
   │        miss → HTTP GET /api/v1/flags/{key}/snapshot (flag-api)
   │
   ├─ [2] SSE stream from gateway (:8080)       ← keeps cache warm in real time
   │        gateway receives Redis Streams event → pushes FlagEvent to connected SDKs
   │        SDK calls cache.applyEvent(event) → immutable update (new Map, never mutate)
   │
   └─ [3] EvaluationEngine.evaluateWithDetail()
            runs 5-step pipeline entirely in-process (no network call)
```

**Key architecture insight:** evaluation is always local. The gateway and flag-api are used to *populate* the cache, not to evaluate. This means zero latency on the hot path.

---

## The 5-Step Evaluation Pipeline

All logic lives in `packages/sdks/@flagmind/core/src/evaluation.ts` — `EvaluationEngine.evaluateInternal()`.

### Step 1 — Preliminary

```
cache.get(flagKey) → undefined?   → { value: defaultValue, reason: "ERROR" }
flagState.enabled === false?      → { value: parseSafeDefault(), reason: "OFF" }
```

- `ERROR` means the flag does not exist in the SDK's cache.
- `OFF` means the flag exists but is disabled for this environment. Returns `safeDefault` (the value configured as the safe fallback), not the caller's `defaultValue`.

### Step 2 — Prerequisites (max depth 5)

```
for each prereq in flagState.prerequisites:
  evaluate prereq flag recursively (depth + 1)
  if prereq result ≠ prereq.requiredVariation AND prereq.gate === true:
    → { value: defaultValue, reason: "PREREQUISITE_FAILED", ruleId: prereq.flagKey }
  if prereq.gate === false:
    mismatch is ignored — continue to next step
```

- `gate: true` (default): the entire feature is blocked if the prerequisite is not met. Use for hard dependencies ("new checkout requires payment-v2 to be enabled").
- `gate: false`: only skips the current targeting rule if unmet; evaluation continues to fallthrough. Use for soft feature augmentation.
- Circular dependency protection: `detectCycle()` in `PrerequisiteHandler` uses DFS up to 5 hops at write time. The SDK also enforces `MAX_PREREQ_DEPTH = 5` at evaluation time.

### Step 3 — Individual Targeting

```
if userId in flagState.targetList:
  → { value: true, reason: "TARGET_MATCH" }
```

`targetList` is an explicit allow-list of `userId` strings. This is the highest-priority targeting mechanism — it bypasses rules and rollout entirely.

### Step 4 — Rule Matching

Rules are sorted ascending by `priority` (0 = highest priority), then evaluated in order. First match wins.

Supported operators (from `applyOperator()`):
`IN`, `NOT_IN`, `EQ`, `NEQ`, `CONTAINS`, `PREFIX`, `SUFFIX`, `LT`, `LTE`, `GT`, `GTE`, `GEO_COUNTRY`, `GEO_REGION`

GEO operators resolve from `context.geo.country` / `context.geo.region`. All other operators resolve attributes via dot-path from the context object (e.g. `"org.plan"` resolves `context.org.plan`), falling back to `context.attrs[path]`.

```
for rule in sortedRules:
  if matchesRule(rule, context):
    → { value: rule.variation, reason: "RULE_MATCH", ruleId: rule.id }
```

### Step 5 — Fallthrough Rollout

```
isInRollout(flagKey, userId, rolloutPct, hashVersion)
  → true:  { value: true, reason: "FALLTHROUGH" }
  → false: { value: defaultValue, reason: "FALLTHROUGH" }
```

---

## Hash Algorithms

Two hash versions are supported. The version is stored per flag as `hashVersion`.

### Version 1 — MurmurHash3 (default)

```typescript
// seed: 0, output mod 100 → bucket 0–99
(murmurhash.v3(flagKey + userId) >>> 0) % 100 < rolloutPct
```

- Bucket space: 100 (1% resolution)
- Sticky: same `flagKey + userId` always maps to same bucket

### Version 2 — Double-FNV32a

```typescript
const FNV_PRIME = 16777619, FNV_OFFSET = 2166136261;
const fnv = (s: string) => { /* FNV-1a */ };
(fnv(String(fnv(flagKey + userId))) % 10000) / 10000 < rolloutPct / 100
```

- Bucket space: 10,000 (0.01% resolution) — use for precision rollouts
- Double-hash removes correlation between adjacent keys

---

## Flag States and SDK Return Values

| State | `enabled` in cache | SDK receives | Reason |
|-------|-------------------|--------------|--------|
| `DRAFT` | `false` | `safeDefault` | `OFF` |
| `ACTIVE` (rollout 0%) | `true` | `defaultValue` | `FALLTHROUGH` |
| `ACTIVE` (rollout 50%) | `true` | `true` or `defaultValue` (hash-based) | `FALLTHROUGH` |
| `ARCHIVED` | `false` | `safeDefault` | `OFF` |
| Kill switch activated | `false` | `safeDefault` | `OFF` |
| Tombstoned (key reused attempt) | blocked at DB | N/A — INSERT rejected |
| Flag missing from cache | — | `defaultValue` (caller's) | `ERROR` |

**Safe default vs caller default:**
- `safeDefault`: stored on the flag, configured by the flag owner. Returned when the flag is `OFF`.
- `defaultValue`: passed by the calling code. Returned when flag is missing (`ERROR`).

---

## Blast Radius Scoring

Computed by `services/evaluator/internal/blast/blast_radius.go` via `GET /api/v1/blast-radius?flag_key=...&environment=...&rollout_pct=...`.

```go
func (c *Calculator) scoreRisk(r *BlastRadiusResult) RiskScore {
    if r.TrafficPctAffected >= 50 && r.HistoricalErrorDelta > 0.05 {
        return RiskBlocked   // Requires typed justification to proceed
    }
    if r.TrafficPctAffected >= 25 || r.DependentFlagsCount > 5 {
        return RiskHigh
    }
    if r.TrafficPctAffected >= 10 || r.DependentFlagsCount > 2 {
        return RiskMedium
    }
    return RiskLow
}
```

| Score | Condition | Action |
|-------|-----------|--------|
| `BLOCKED` | ≥50% traffic AND historical error delta >5% | Requires typed justification. Change request blocked until approved. |
| `HIGH` | ≥25% traffic OR >5 dependent flags (co-changed in 30 days) | Warning shown in dashboard. Change requests auto-created. |
| `MEDIUM` | ≥10% traffic OR >2 dependent flags | Yellow indicator. |
| `LOW` | Below all thresholds | Green. Proceed normally. |

---

## Multivariate Variations

Weights sum to **10,000** (1 unit = 0.01%). Stored in `flag_variations` table.

```sql
weight INT NOT NULL CHECK (weight >= 0 AND weight <= 10000)
```

Example: `{ control: 5000, treatment_a: 3000, treatment_b: 2000 }` = 50% / 30% / 20%.

The hash bucket (0–9999 in v2) determines which variation a user receives. The SDK reads `variations` from `FlagEnvironmentState` and selects by cumulative weight.

---

## Worked Examples

### 1. User in targeting rule → `RULE_MATCH`
```
flag: { enabled: true, rolloutPct: 0 }
rule: { attribute: "org.plan", operator: "EQ", values: ["enterprise"], variation: "true", priority: 0 }
context: { userId: "u-1", org: { plan: "enterprise" } }

Step 1: flag enabled ✓
Step 2: no prerequisites
Step 3: userId not in targetList
Step 4: rule matches (org.plan == "enterprise") → value: "true", reason: "RULE_MATCH"
```

### 2. User at rollout boundary (v1 hash)
```
flag: { enabled: true, rolloutPct: 30, hashVersion: 1 }
context: { userId: "user-42" }

murmurhash.v3("checkout-v2user-42") % 100 = 27  → 27 < 30 → IN rollout
→ value: true, reason: "FALLTHROUGH"

murmurhash.v3("checkout-v2user-99") % 100 = 73  → 73 >= 30 → NOT in rollout
→ value: false (defaultValue), reason: "FALLTHROUGH"
```

### 3. Prerequisite not met (gate=true) → `PREREQUISITE_FAILED`
```
flag "checkout-v2": prerequisites: [{ flagKey: "payment-v2", requiredVariation: "true", gate: true }]
flag "payment-v2": { enabled: false }

Step 2: payment-v2 evaluates → OFF (safeDefault "false")
        "false" ≠ requiredVariation "true" AND gate: true
→ value: defaultValue (caller's), reason: "PREREQUISITE_FAILED", ruleId: "payment-v2"
```

### 4. Flag is tombstoned
```
Tombstoned flag key "old-checkout-v1" is in flag_tombstones table.
DB trigger enforce_tombstone blocks any INSERT INTO flags WHERE key = "old-checkout-v1".
SDK: cache.get("old-checkout-v1") → undefined → reason: "ERROR", value: defaultValue
```

### 5. Circuit breaker open → serve last known good
```
evaluator detects error rate > 5% over 100 requests in 10s window
circuit breaker transitions CLOSED → OPEN
OnTrip callback fires: calls flag-api kill-switch endpoint
flag.enabled flips to false
gateway pushes FlagEvent to all SDKs → cache.applyEvent() → enabled: false

SDK next call: flagState.enabled === false → value: safeDefault, reason: "OFF"
```
This is the auto-rollback behavior: the circuit breaker's OnTrip callback is what triggers the kill switch, not a separate process.

---

## Cache Immutability Guarantee

`FlagCache` enforces immutability on every update. `applyEvent()` and `setTargetingRules()` both create a `new Map(this.memory)` before setting the updated entry. This means:

- Concurrent reads always see a consistent snapshot
- No partial updates are visible during a flag change
- Safe to read from multiple evaluation goroutines/threads without locking
