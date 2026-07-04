# Tombstone Glossary

Alphabetical reference for terms used throughout Tombstone's UI, APIs, and documentation.

---

**Anomaly Ensemble**
A 3-model voting system used by the intelligence service to detect when a flag's error rate or evaluation pattern has deviated abnormally. Three models vote independently (Z-score, Isolation Forest, EWMA with adaptive threshold) and signals collected at 10s, 60s, and 5m granularities also vote. Both gates require 2/3 agreement before an anomaly is declared, which eliminates false positives from isolated spikes. Requires ≥50 observations before the Isolation Forest component trains; earlier evaluations fall back to Z-score + EWMA only.

**Audit Log**
An append-only, Merkle-linked record of every flag change. Each entry captures the event type, actor, previous state, new state, and timestamp. The SHA-256 hash of each row covers all of those fields; `prev_hash` chains entries so any tampering is detectable. No UPDATE or DELETE is ever issued against the `audit_log` table. Available at `/audit` in the dashboard and via `GET /api/v1/audit`.

**Blast Radius**
The computed scope of impact if a flag causes or contributes to a production incident. Tombstone's evaluator service scores every active flag on four levels:

- `BLOCKED` — change is gated; not allowed to proceed without explicit override
- `HIGH` — flag touches critical path (payments, auth, data writes); extra approval recommended
- `MEDIUM` — moderate user or revenue exposure
- `LOW` — limited surface area; low risk to proceed

The blast radius score factors in rollout percentage, flag type, environment, and historical incident correlation. Visible at `/blast-radius` in the dashboard and via `GET /api/v1/blast-radius/:key`.

**Break-Glass Token**
A pre-authorized emergency SDK token created for on-call engineers that bypasses the normal four-eyes approval workflow. Scoped to specific environments and flag keys (or `*`), time-limited (typical expiry: 1–8 hours), and tied to an incident reference. Every action taken with a break-glass token is recorded in the audit log. Created at **Settings > SDK Tokens > Create Break-Glass Token**.

**Collision Detection**
A check that prevents two concurrent experiments from sharing enough user traffic to contaminate each other's causal signal. Tombstone computes a Jaccard overlap score between two experiments' targeting fingerprints and traffic percentages. Overlap ≥ 0.9 is auto-blocked; ≥ 0.7 requires human review; < 0.7 is clean. Run automatically when a new experiment is created or when rollout percentages are updated.

**CUPED (Controlled-experiment Using Pre-Experiment Data)**
A variance reduction technique used in the experimentation engine. By regressing out pre-experiment covariate correlation (θ = Cov(Y, X) / Var(X), computed on pooled treatment + control data), CUPED reduces measurement variance by 20–40%, which proportionally cuts the number of observations needed to reach statistical significance. Shorter required experiment duration translates directly to faster iteration. θ must be computed on pooled data — computing per-variant biases the estimator.

**Circuit Breaker**
An automatic safety mechanism in the evaluator service. When a flag's associated error rate exceeds 5% of the last 100 requests, the evaluator automatically rolls back the flag to its safe default and writes an incident signal. The circuit breaker prevents runaway rollouts from compounding a production incident. Inspired by the Knight Capital incident (2012), where a flag that should have been removed caused $440M in losses in 45 minutes. Circuit breaker state is visible on the flag detail page and at `GET /api/v1/evaluator/circuit/:key`.

**Change Request**

An approval record created when a team member requests a flag modification that requires four-eyes sign-off. Contains the proposed before/after state, the requestor, a description of expected impact, and a timestamp. Requires a second team member with the `approver` role to approve before the change is applied. Managed at `/approvals` in the dashboard.

**Environment**
A logical isolation boundary for flag evaluation. Tombstone ships with three default environments: `development`, `staging`, and `production`. A flag can have different rollout percentages, enable/disable states, and variations per environment. Environments are configured at **Settings > Environments**.

**Flag**
A named boolean or typed toggle that controls feature availability at runtime without a code deployment. Tombstone supports boolean flags (on/off), string flags (e.g., variant names), number flags (e.g., rate limits), and JSON flags (e.g., configuration objects). Flags are evaluated in a 5-step pipeline: targeting rules, prerequisites, rollout percentage, variations, and safe default fallback.

**Flag Key**
The unique, permanent identifier for a flag. Must be lowercase with hyphens (e.g., `checkout-v2`, `disable-payment-flow`). Once created, a flag key cannot be reused — even after the flag is archived. Reuse is blocked at both the database constraint and service layer to prevent the class of production bugs caused by key collision (where new code picks up a stale flag value from a key that was previously used for a different purpose). See also: Tombstoning.

**Kill Switch**
The pattern of disabling a flag to immediately deactivate a feature across all users. A kill switch flag is normally disabled (evaluates to `false`) and is enabled only in an emergency to turn a feature off. Setting rollout to 100% and toggling enabled propagates through the gateway's SSE stream to all connected SDKs within milliseconds. The circuit breaker can also trigger a kill switch automatically.

**LinUCB (Linear Upper Confidence Bound)**
A contextual bandit algorithm used by the intelligence service for adaptive rollout recommendations. Maintains separate exploration/exploitation matrices (A, b) per flag-environment pair. The UCB score `θᵀx + α·√(xᵀA⁻¹x)` balances exploiting known-good rollout strategies with exploring under uncertainty. The context vector encodes rollout %, error rate, request count, hour of day, and day of week. Matrices are base64-encoded and persisted to Redis with 90-day TTL for recovery across restarts. Works alongside Thompson Sampling — LinUCB handles context-aware decisions while Thompson Sampling handles aggregate Bayesian confidence.

**mSPRT (Mixture Sequential Probability Ratio Test)**
A sequential hypothesis testing method used in the experimentation engine that controls the false discovery rate (FDR) at any stopping point. Unlike fixed-horizon tests (where peeking inflates false positives), mSPRT allows analysts to stop an experiment early as soon as the evidence is strong enough, without statistical penalty. The test statistic uses a normal(0, τ²) mixture prior and the threshold is 1/α (not a p-value). Enables Tombstone to declare experiments complete days or weeks ahead of a fixed-horizon schedule when the signal is unambiguous.

**Prerequisites**
Flags that must evaluate to `true` before a given flag can evaluate to `true`. Implements the GrowthBook gate pattern: if flag B lists flag A as a prerequisite, and flag A is disabled, then flag B returns its safe default regardless of its own rollout settings. Useful for feature dependencies (e.g., "new checkout UI" requires "new cart service" to be enabled first). Configured on the flag detail page under **Prerequisites**.

**RRF (Reciprocal Rank Fusion)**
The score fusion method used in Tombstone's NLP flag search pipeline. Combines ranked results from three independent search signals — dense vector embeddings (BAAI/bge-m3, 1024-dim), BM25 lexical full-text (PostgreSQL tsvector), and substring ILIKE fallback — into a single ranked list. RRF score for a document = Σ 1/(k + rank_i) across all signals, where k=60 is a constant that prevents high-ranked results from dominating. This means a flag that ranks #1 in dense search but #20 in BM25 still scores well if BM25 is absent, making the search robust to vocabulary mismatch.

**Rollout Percentage**
The fraction of users (0–100%) who receive the flag's enabled value. Tombstone uses consistent hashing on the user ID plus the flag key, so the same user always gets the same bucket — the experience is sticky across requests. A rollout of 0% means no users see the flag as enabled; 100% means all users do. Used in canary releases to progressively expand exposure.

**Safe Default**
The value returned by the SDK when the flag-api is unreachable, the flag key does not exist, or evaluation fails for any reason. Configured per flag at creation time (default: `false` for boolean flags). The safe default ensures that a network partition or service outage degrades gracefully rather than breaking the application. Never leave the safe default as `true` for a flag that controls a safety-critical path.

**SDK Token**
The authentication key used by SDKs and direct API callers to identify themselves to flag-api. Each environment has its own SDK token. The default development token is `sdk-dev-token-change-in-prod` — rotate this before deploying to any shared environment. Tokens are managed at **Settings > SDK Tokens**. Break-glass tokens are a special subtype of SDK token with additional constraints and audit requirements.

**Thompson Sampling**
A Bayesian bandit algorithm used by the intelligence service for autonomous rollout recommendations. Tracks per-flag-environment Beta(α, β) posteriors updated with observed successes and failures. When P(sampled success rate > threshold) ≥ 0.90 with ≥50 observations, the service recommends advancing to the next rollout step (schedule: 1% → 5% → 10% → 25% → 50% → 75% → 100%). Posteriors are persisted to Redis with 90-day TTL so recommendations survive service restarts. Requires ≥50 observations before making a recommendation — during cold start, the service reports "insufficient data" rather than guessing. Works alongside LinUCB, which handles context-aware decisions.

**Tombstoning**
The permanent archival of a flag key after the flag has been fully cleaned up from the codebase. Tombstoning prevents any future flag from reusing the same key, eliminating the category of production incident where new code accidentally activates a stale rollout value (the Knight Capital pattern). A tombstoned key appears in the `flag_tombstones` table and at `/tombstones` in the dashboard. Tombstoning is a one-way operation — it cannot be reversed. The recommended cleanup sequence is: set rollout to 0%, remove all code references via ast-rewriter, then tombstone.

**Variation**
A named value for multivariate flags. Instead of a simple boolean, a flag with variations returns one of several named values (e.g., `blue`, `green`, `red` for a UI experiment; `v1`, `v2`, `v3` for an API version test). Each variation carries a weight that determines its share of traffic. Variations are configured on the flag detail page under **Variations** and are evaluated after rollout percentage in the 5-step evaluation pipeline.
