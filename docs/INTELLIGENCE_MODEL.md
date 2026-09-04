# Intelligence Model

This document explains Tombstone's ML-driven intelligence layer for platform/SRE engineers who are not data scientists. The goal: you should be able to trust auto-rollback decisions, understand why a rollout stalled, and debug unexpected anomaly alerts — without reading ML papers.

---

## What the Intelligence Service Does

The intelligence service (Python, port 8083) provides:

| Endpoint | Description |
|----------|-------------|
| `GET /api/v1/anomaly/{flag_key}` | Current anomaly detection status for a flag |
| `GET /api/v1/rollout/recommendations` | LinUCB rollout advancement recommendations |
| `GET /api/v1/experiments/collisions` | Experiment collision check |
| `GET /api/v1/stale` | Flags at 100% rollout for 30+ days |
| `POST /api/v1/intelligence/generate-rule?flag_key=` | Argos LLM rule generation (requires `ANTHROPIC_API_KEY`) |
| `GET /api/v1/search?q=` | Natural-language flag search |

The service is **advisory only** — it surfaces recommendations that humans and the circuit breaker act on. It does not flip flags directly except through the auto-rollback path (which goes through the evaluator, not the intelligence service).

---

## Anomaly Detection

### How It Works: 3-Model Ensemble

The `AnomalyEnsemble` class (`app/anomaly/ensemble.py`) runs three independent models on per-flag error rate streams. Inspired by ImDiffusion (VLDB 2024).

**Model 1 — Z-score (rolling window)**
- Compares the current 10-second observation against the rolling baseline of the same window
- Threshold: `|z| > 2.5` (2.5 standard deviations from the rolling mean)
- Requires minimum 10 observations in the window before flagging
- Good at: detecting sudden spikes against stable baselines

**Model 2 — Isolation Forest (batch)**
- scikit-learn `IsolationForest(n_estimators=100, contamination=0.05)`
- Trained on window_10s's rolling history (672 samples x 10s ≈ 1.87h, NOT
  7 days -- corrected by INT-4 after adversarial review found this doc
  contradicted the code's own, similarly-corrected comments). Retrained
  daily at 02:00 UTC.
- Requires minimum 50 observations before training. Until then, votes "normal".
- Good at: detecting structurally unusual patterns (not just spikes)

**Model 3 — EWMA with adaptive threshold**
- Exponentially weighted moving average with online variance (Welford-style)
- Decay factor `alpha=0.1` (weights recent observations more)
- Anomaly threshold: `|deviation| > 3.0 sigma` (tighter than static Z-score)
- Good at: detecting gradual drift that Z-score misses

### Voting Rules (2/3 + 2/3)

The ensemble uses a **dual-gate** vote:

1. **Model vote**: ≥2 of the 3 models must flag the observation as anomalous
2. **Time-scale vote**: ≥2 of the 3 time granularities (10s, 60s, 5m windows) must agree

**Final verdict: `anomaly = model_agreement AND granularity_agreement`**

This dual gate dramatically reduces false positives. A single-model spike on only the 10s window will not trigger an anomaly.

### What Gets Monitored

The anomaly detector receives error rate observations from the evaluator's SDK telemetry endpoint (`POST /api/v1/telemetry`). The signal is: `error_count / total_count` in each 10-second bucket.

### Querying Anomaly State

```bash
curl http://localhost:8083/api/v1/anomaly/checkout-v2
```

Response:
```json
{
  "anomaly": false,
  "score": 0.12,
  "model_votes": {"zscore": false, "isolation_forest": false, "ewma": true},
  "granularity_votes": {"10s": true, "60s": false, "5m": false},
  "obs_count": 342,
  "sufficient_data": true
}
```

`sufficient_data: false` means fewer than 50 observations — results are unreliable, don't act on them.

---

## LinUCB Rollout Advisor

### What Is LinUCB?

LinUCB is a **contextual bandit** algorithm. Unlike A/B tests (which split traffic randomly), LinUCB learns from context to make smarter rollout decisions. In plain terms: it learns that rolling out a payment flag at peak traffic on Fridays is riskier than on Tuesday mornings, and adjusts recommendations accordingly.

### Arms: What the Bandit Chooses Between

From `app/rollout/linucb.py`:

```
arm=0  control    → hold rollout at current percentage / recommend rollback
arm=1  treatment  → advance rollout (increase rollout_pct)
```

The bandit picks arm=1 (advance) when its UCB score is higher than arm=0 (hold).

### Context Features (d=5)

The 5-dimensional context vector `x`:

```python
[
    rollout_pct / 100.0,                    # current rollout percentage
    error_rate (clipped 0-1),               # current error rate
    min(request_count, 10_000) / 10_000.0,  # traffic volume
    hour / 23.0,                            # hour of day (0-23)
    day / 6.0,                              # day of week (0=Mon, 6=Sun)
]
```

This means the bandit treats a rollout at 2am with low traffic differently than the same rollout at 2pm with high traffic.

### Exploration vs Exploitation: alpha=1.0

The `alpha` parameter controls the exploration-exploitation tradeoff:
- **High alpha** → try more rollout percentages even when current data suggests staying
- **Low alpha** → trust accumulated experience more, stick with what's worked
- Default `alpha=1.0` is balanced. Do not change this without understanding the UCB formula.

### Redis Persistence

Matrices are stored as base64-encoded numpy bytes:
```
tombstone:linucb:{flag_key}:{env}:A:{arm_id}    → A matrix (d×d)
tombstone:linucb:{flag_key}:{env}:b:{arm_id}    → b vector (d)
tombstone:linucb:{flag_key}:{env}:meta:{arm_id} → n_obs, d
```

TTL: **90 days**. After 90 days with no activity, the bandit resets to a cold start (identity matrix). This is intentional — historical patterns become stale.

If Redis is cleared, the bandit restarts from scratch. It takes approximately 50 observations to leave the exploration-dominated phase.

### Querying Recommendations

```bash
curl http://localhost:8083/api/v1/rollout/recommendations
```

---

## CUPED Variance Reduction

CUPED (Controlled-experiment Using Pre-Experiment Data) reduces the variance in experiment metrics, meaning you need fewer users to detect a statistically significant result.

**Plain English**: Before your experiment starts, Tombstone looks at each user's pre-experiment error rate. It subtracts this baseline from the experiment metric. This removes the "noise" from pre-existing user differences, so the experiment result reflects only the flag's actual effect.

The variance reduction is typically 20-40%, meaning you can run experiments with 20-40% fewer users for the same statistical confidence level.

```
theta = Cov(Y, X) / Var(X)
Y_adjusted = Y - theta × (X - mean(X))
```

Where `Y` = experiment metric, `X` = pre-experiment covariate (same metric measured before the experiment).

---

## Experiment Collision Detection

Two running experiments "collide" when their user populations overlap enough to confound results.

From `app/experiments/collision.py`:

```
overlap >= 0.9  → blocked    (auto-reject; near-total overlap)
overlap >= 0.7  → warning    (human review required)
overlap <  0.7  → clean      (safe to proceed)
```

The overlap is estimated as:
```
base = min(rollout_a, rollout_b) / max(rollout_a, rollout_b)
overlap = base × targeting_rule_similarity
```

Example: two flags both targeting 100% rollout with identical rules → overlap = 1.0 → blocked.

---

## Human Override Semantics

When a human kills a flag that LinUCB was advancing:
1. The kill-switch is written to `flag_environments.enabled = false`
2. The evaluator's circuit breaker state is set to OPEN in Redis
3. LinUCB receives no new observations while the flag is off
4. When the flag is re-enabled, LinUCB resumes with its existing A/b matrices — it does not reset
5. The first observation after re-enable will update the bandit. If error rates are still high, it will recommend arm=0 (hold/rollback) quickly.

---

## Argos LLM Rule Generation

`POST /api/v1/intelligence/generate-rule?flag_key={key}` triggers a 3-agent LLM pipeline (requires `ANTHROPIC_API_KEY`). The pipeline:
1. Analyzes audit log history for the flag
2. Generates a targeting rule candidate that would have reduced incidents
3. Writes the rule as a signal file to `signals/rule-candidate-{flag}-{date}.md` with `status: pending-approval`

**Human approval is always required before the rule is applied.** The pipeline generates candidates only — it does not create targeting rules in the database.

---

## Troubleshooting

| Problem | What to check | How |
|---------|--------------|-----|
| Anomaly firing unexpectedly | Is `sufficient_data: true`? Model votes? | `GET /api/v1/anomaly/{key}` — check `obs_count` and `model_votes` |
| Rollout stalled (bandit not advancing) | Is arm=0 winning? Why? | Query recommendations; check if error_rate context feature is elevated |
| Collision warning on low-overlap flags | Rule similarity component | Check targeting rules — broad rules (no targeting) default to 0.5 similarity |
| Intelligence service restarted, bandit lost state | Redis was cleared | Bandit rebuilds over ~50 observations. Normal for first 5-10 minutes |
| Daily retrain not running | Service was down at 02:00 UTC | Check service logs for `"retrain_all"`; manually trigger is not exposed in v1.2.1 |
