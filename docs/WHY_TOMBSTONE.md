# Why Tombstone

> **The thesis:** Every competitor asks "how do I deliver a flag value?" Tombstone asks "which of my 5,000 active flags is responsible for what's happening in production right now?"

Feature flags are no longer just a deployment tool. At scale — 5,000+ flags across 10+ services — they become a distributed state machine that nobody fully understands. Something breaks at 3am. Was it a flag? Which one? How do you roll it back without breaking something else?

Tombstone is the production intelligence layer that answers those questions automatically.

---

## The Core Problem Tombstone Solves

**Existing tools treat flags as configuration. Tombstone treats them as causal agents.**

When you have 50 flags, you can watch them manually. When you have 5,000 flags being changed by 40 engineers across 3 environments daily, you need:

1. **Automatic safety** — roll back bad flags before the incident page fires
2. **Causal attribution** — know which flag caused the incident from the alert itself
3. **Intelligent rollout** — don't manually babysit gradual rollouts for weeks
4. **Blast radius prediction** — before you change anything, know what breaks

None of this exists in open-source feature flag tooling today.

---

## What Tombstone Provides (Unique Capabilities)

### 1. Circuit-Breaker Auto-Rollback

**What it does:** When a flag causes errors in production, Tombstone automatically disables it — before your on-call engineer even opens the alert.

**Implementation:**
- 5% error rate over 100 requests in a 10-second window trips the breaker
- States: CLOSED → OPEN → HALF_OPEN (5-minute recovery window)
- Auto-rollback callback disables the flag and writes to the Merkle audit trail
- Circuit state stored per-flag in Redis; survives service restarts

**Why it matters:** Knight Capital lost $440M in 45 minutes because a bad deploy flag couldn't be rolled back fast enough. Tombstone's circuit breaker acts in seconds, not minutes.

**Who else has it (OSS):** Nobody. LaunchDarkly has manual kill switches; no OSS flag system has automated circuit-breaker rollback.

---

### 2. ML-Driven Rollout Recommendations

Two algorithms work in parallel to decide when (and how far) to advance a gradual rollout:

**Thompson Sampling (Beta posteriors):**
- Tracks successes/failures per flag-environment as a Beta(α, β) distribution
- Advances rollout when P(sampled success rate > threshold) ≥ 0.90, with ≥50 observations
- Rollout schedule: 1% → 5% → 10% → 25% → 50% → 75% → 100%
- Posteriors persisted to Redis with 90-day TTL; no cold-start on restart

**LinUCB Contextual Bandit:**
- Context vector: [rollout_pct, error_rate, request_count, hour_of_day, day_of_week]
- Maintains separate (A, b) matrices per flag-environment pair
- UCB score: `θᵀx + α·√(xᵀA⁻¹x)` — explores when uncertain, exploits when confident
- Matrices base64-encoded as numpy bytes in Redis; deterministically recoverable

**Why it matters:** Manual rollout management is the #1 source of "stuck at 25% forever" experiments. Thompson Sampling removes the decision entirely.

**Who else has it (OSS):** Nobody. GrowthBook has basic bayesian stats; no OSS tool has contextual bandit rollout.

---

### 3. 3-Model Ensemble Anomaly Detection

Inspired by ImDiffusion (VLDB 2024). Three models vote; 2/3 must agree:

| Model | Algorithm | Sensitivity |
|-------|-----------|-------------|
| Z-score | 2.5σ over a ~1.87h rolling window (672 x 10s samples) | Drift detection |
| Isolation Forest | scikit-learn, contamination=0.05, daily retraining | Non-linear anomalies |
| EWMA + adaptive threshold | Online Welford variance, α=0.1, 3σ adaptive | Recent trend shifts |

**Time-scale voting (ImDiffusion pattern):** Signals collected at 10s, 60s, and 5m granularities. Both model-level (2/3) AND granularity-level (2/3 scales agree) consensus required before flagging anomaly. This eliminates isolated-spike false positives.

**Anomaly score:** `0.4·Z + 0.3·ISO + 0.3·EWMA`

**Why it matters:** Single-model detectors fire on noise. The dual-gate consensus (model vote + time-scale vote) means alerts that fire are real.

**Who else has it (OSS):** Nobody. This is production-grade ML anomaly detection built specifically for flag telemetry.

---

### 4. Causal Dependency Graph + Incident Correlation

**The "What Changed?" query:** Given an incident timestamp, return the flags that changed in the preceding configurable window, ordered by blast radius. One API call, answered in milliseconds.

**Implementation:**
- Flag co-occurrences tracked via Redis sorted sets (O(log n) updates, not O(n²) materialization)
- Edge weight = co-occurrence count in evaluation traces
- Daily rebuild at 02:00 UTC; incremental updates on every evaluation event
- Blast radius tiers: BLOCKED (50%+ traffic + >5% historical error delta) / HIGH / MEDIUM / LOW

**Blast radius pre-check:** Before you change any flag, `GET /api/v1/blast-radius?flag_key=X&environment=Y&rollout_pct=Z` returns the tier. BLOCKED tier requires a 10-character minimum justification to proceed — deliberate friction to prevent accidental overrides.

**Incident correlation pipeline:** PagerDuty/OpsGenie webhook → `correlator.correlate()` → ranked list of causal flag candidates with timeline.

**Why it matters:** The first 10 minutes of a production incident are wasted on "was it a flag change?" Tombstone answers this before you even look at logs.

**Who else has it (OSS):** Nobody. No open-source flag system models flags as a dependency graph.

---

### 5. Merkle-Linked Audit Trail + Rekor Transparency

**Every flag mutation is cryptographically chained:**

```
sha256(id | event_type | actor | prev_state | new_state | timestamp)
```

Each entry carries `prev_hash` linking it to the prior entry. Deleting or modifying a historical entry breaks the chain — detectable instantly.

Audit entries are also submitted asynchronously to [Rekor](https://github.com/sigstore/rekor) (Sigstore's transparency log), an append-only public ledger. The UUID is stored per entry for out-of-band verification.

**OPA policy-as-code RBAC:** Rego policies in `services/flag-api/policies/` with `fsnotify` hot-reload. Policy changes take effect without restarting the service. Hard-coded Go fallback prevents lockout on parse error.

**Why it matters:** SOC2 Type II requires immutable audit trails. Merkle chaining + Rekor gives you cryptographic proof that nobody tampered with your audit log — something no other OSS flag system provides.

**Who else has it (OSS):** Nobody. Merkle-chained logs and Rekor integration are unique to Tombstone.

---

### 6. Tombstoning (Knight Capital Prevention Pattern)

**The problem:** Knight Capital's 2012 disaster ($440M loss in 45 minutes) was caused partly by a feature flag that was accidentally reactivated because its key was reused for a new, different feature.

**Tombstone's solution:** Once a flag is archived, its key is permanently reserved in `flag_tombstones`. Key reuse is blocked at both the **database constraint level** AND the **service layer**. The tombstone record stores the original flag's metadata and the reason for archival.

The feature is named after this pattern — every archived flag becomes a "tombstone," a permanent marker that says "this key was here, this is what it did, this is why it was retired."

**Why it matters:** This is the one failure mode that a kill switch can't save you from. Tombstoning is the only defense against accidental flag key reuse at scale.

**Who else has it (OSS):** Partial. Some tools support archival, but none enforce permanent key reservation at the DB constraint level.

---

### 7. Experimentation Engine (CUPED + mSPRT + Collision Detection)

**CUPED variance reduction (20–40% typical):**
- Removes pre-experiment covariate correlation: `Y_adjusted = Y - θ·(X - mean(X))`
- θ computed on pooled treatment + control data (per-variant computation biases the estimator)
- Outputs: adjusted means, 95% Welch CI, effect size, p-value, variance reduction %

**mSPRT sequential testing:**
- Mixture Sequential Probability Ratio Test with normal(0, τ²) prior
- Controls false discovery rate at any stopping point — no peeking penalty
- Enables stopping experiments early with statistical guarantees

**Collision detection:**
```
overlap = min(pct_a, pct_b) / max(pct_a, pct_b) × Jaccard(fingerprint_a, fingerprint_b)
```
- ≥0.9 → BLOCKED (auto-reject)
- ≥0.7 → WARNING (human review required)
- <0.7 → CLEAN

**Why it matters:** CUPED cuts experiment duration by 20–40%. mSPRT lets you stop early safely. Collision detection prevents two experiments from measuring each other's noise.

**Who else has it (OSS):** CUPED is in Statsig (cloud only) and partially in GrowthBook. mSPRT is nowhere in OSS. Collision detection is unique to Tombstone.

---

### 8. NLP Semantic Flag Search

**3-way fusion:**
1. **Dense embeddings** — BAAI/bge-m3 (1024-dim) via sentence-transformers
2. **BM25 lexical** — PostgreSQL full-text search
3. **Substring fallback** — `ILIKE`-based text match

Results ranked by Reciprocal Rank Fusion (RRF) across all three signals.

**Why it matters:** "Find the flag that controls the checkout flow" should work even if nobody named it `checkout_flow`. Semantic search over 5,000 flag descriptions is the only way to navigate at scale.

**Who else has it (OSS):** Nobody.

---

## Competitive Comparison

| Capability | Tombstone | Unleash | Flagsmith | Flipt | GrowthBook | LaunchDarkly |
|------------|-----------|---------|-----------|-------|------------|--------------|
| Flag CRUD + targeting rules | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| Real-time SSE streaming | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| GitOps YAML sync | ✅ | ❌ | ❌ | ✅ | ❌ | ✅ paid |
| OpenFeature SDK compliance | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| Approval workflows | ✅ | ✅ paid | ✅ paid | ❌ | ❌ | ✅ paid |
| **Circuit-breaker auto-rollback** | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ |
| **Blast radius pre-check** | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ |
| **Causal dependency graph** | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ |
| **Incident correlation ("What Changed?")** | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ |
| **Merkle-chained audit trail** | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ |
| **Rekor transparency log** | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ |
| **Tombstoning (key reuse prevention)** | ✅ | ❌ | partial | ❌ | ❌ | partial |
| **Thompson Sampling rollout** | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ |
| **LinUCB contextual bandit** | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ |
| **3-model ensemble anomaly detection** | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ |
| **CUPED variance reduction** | ✅ | ❌ | ❌ | ❌ | partial | ✅ paid |
| **mSPRT sequential testing** | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ |
| **Experiment collision detection** | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ |
| **NLP semantic flag search** | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ |
| OPA policy-as-code RBAC | ✅ | partial | ❌ | ❌ | ❌ | ✅ paid |
| Kubernetes operator + CRDs | ✅ | partial | ❌ | ✅ | ❌ | ✅ paid |
| AST dead-code scanner + rewriter | ✅ | ❌ | ❌ | ❌ | ❌ | ✅ paid |
| WASM zero-dependency eval engine | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ |
| Self-hosted, fully open-source | ✅ MIT | ✅ | ✅ | ✅ | ✅ | ❌ |
| Cloud managed option | planned v1.1 | ✅ | ✅ | ✅ | ✅ | ✅ |

**Legend:** ✅ = implemented · partial = limited implementation · ❌ = not available · paid = cloud/enterprise tier only

---

## Use Cases

### "We're doing 50+ deploys a day and something breaks every week at flag change time"

**Fit: High.**

The circuit-breaker auto-rollback and blast radius pre-check solve this directly. Before you change a flag, you know its tier (BLOCKED/HIGH/MEDIUM/LOW). After you change it, the circuit breaker monitors error rates and rolls back in seconds if the threshold is crossed.

The dependency graph tells you which flags you might be breaking by changing this one — before you change it.

---

### "We have 2,000+ flags and have no idea which ones are still used"

**Fit: High.**

The stale flag detection loop (daily) identifies flags at 100% rollout for 30+ days and flags with zero recent evaluation activity. The AST rewriter scans your codebase for dead flag references and generates automated PRs to remove them. The tombstoning mechanism ensures once you retire a flag key, it can never accidentally come back.

---

### "Our experimentation velocity is too slow — A/B tests run for 3 months"

**Fit: High.**

CUPED reduces experiment variance by 20–40%, meaning you need fewer observations to reach statistical significance. mSPRT lets you stop early without false discovery inflation. Thompson Sampling auto-advances rollout when the signal is clear. Collision detection catches experiments that would contaminate each other.

This combination can cut typical experiment duration by 50%+.

---

### "We need SOC2 Type II compliance audit trails for our feature flag system"

**Fit: High.**

The Merkle-chained audit trail with Rekor transparency provides cryptographic proof that no historical record was modified. OPA hot-reload RBAC enforces read/write/kill/admin permissions with a code-reviewable policy file. Break-glass tokens create a traceable emergency override mechanism.

Every flag change, approval, rejection, rollback, kill switch activation, and tombstone archival is in the immutable log.

---

### "We need to run feature flags at the CDN edge without origin round-trips"

**Fit: High.**

`@tombstone/edge` is a Cloudflare Workers SDK backed by KV snapshot storage. Evaluation happens at the edge — no origin round-trip for flag resolution. A Cron Trigger syncs the snapshot on a configurable schedule. The WASM engine (`@tombstone/eval`) is zero-dependency and works in any WASM runtime.

---

### "We want feature flags integrated into our incident management workflow"

**Fit: High.**

PagerDuty and OpsGenie webhooks flow into the incident correlation pipeline. When an alert fires, Tombstone immediately runs the "What Changed?" query and attaches the ranked list of causal flag candidates to the incident. The Slack integration sends the result directly to your incident channel.

---

### "We're a small team and just want simple feature toggles"

**Fit: Medium.**

Tombstone's `make dev` gets you a full flag system in 3 commands. For basic flag CRUD and rollout %, it works exactly like Unleash or Flagsmith. The advanced capabilities (ML, circuit breaker, dependency graph) are available but don't get in the way until you need them.

If you need a lightweight flag system with minimal ops overhead and no interest in the intelligence layer, Flipt or Unleash may be a simpler choice. Tombstone earns its complexity at scale.

---

## Scope and Boundaries

### What Tombstone is

- **Production intelligence layer** for teams managing 500+ active flags
- **Safety system** — circuit breaker, blast radius, auto-rollback
- **Intelligence system** — ML rollout, anomaly detection, incident correlation
- **Experimentation platform** — CUPED, mSPRT, collision detection
- **Compliance system** — Merkle audit, Rekor transparency, OPA RBAC
- **Self-hosted** — runs entirely on-premise; no data leaves your infrastructure

### What Tombstone is not

- A feature flag SaaS with a managed cloud (planned for v1.1)
- An A/B testing platform with visual editor (use GrowthBook for that alongside Tombstone)
- A session recording or analytics tool (use PostHog, Mixpanel, etc.)
- A deployment orchestrator (use ArgoCD, Flux, etc. for that)

### Current version scope (v1.0.0 self-hosted)

| In v1.0.0 | Planned (v1.1+) |
|-----------|-----------------|
| All 8 services via `make dev` | Managed cloud option |
| Circuit breaker + auto-rollback | Multi-region active-active |
| Blast radius + dependency graph | SOC2 Type II certification |
| Thompson Sampling + LinUCB | Kubernetes operator GA |
| 3-model anomaly ensemble | More warehouse connectors |
| CUPED + mSPRT + collision detection | Argos LLM rule generation (needs API key) |
| Merkle audit + Rekor transparency | Redis Streams full migration |
| OPA hot-reload RBAC | mTLS between internal services |
| Tombstoning + break-glass tokens | |
| NLP semantic search | |
| 6 integrations (Slack, Datadog, PagerDuty, OpsGenie, Jira, Linear) | |
| TypeScript/Python/Java/.NET/Ruby SDKs | |
| WASM zero-dependency eval engine | |

---

## Honest Caveats

**Tombstone is complex to run.** 8 services + PostgreSQL + Redis + Kafka is a non-trivial stack. `make dev` handles this for local development, but production deployment requires operational maturity.

**The ML layer needs data.** Thompson Sampling requires ≥50 observations before making recommendations. LinUCB needs several evaluation cycles to build useful matrices. Anomaly detection needs ≥50 observations per flag before Isolation Forest trains. If you're starting from zero evaluations, the intelligence layer starts giving useful signals after ~1–2 weeks of real traffic.

**The intelligence service is large.** The Docker image bundles BAAI/bge-m3 (~400MB) for NLP search. First build takes 3–5 minutes. Subsequent builds use the cached layer.

**Cloud deployment is v1.1.** v1.0.0 is self-hosted only. Northflank, Fly.io, and Kubernetes manifests exist in `infra/`, but managed cloud hosting is not yet available.

---

## The Name

The name comes from the tombstoning pattern — the practice of permanently archiving a flag key so it can never be accidentally reused. In production systems at scale, flag key reuse is one of the most dangerous forms of configuration drift. Every flag that Tombstone retires leaves a tombstone: a permanent marker recording what it was, what it did, and why it was ended.

It's also a reference to the Knight Capital disaster: the $440M, 45-minute loss caused partly by a feature flag that came back from the dead. Tombstone doesn't let that happen.
