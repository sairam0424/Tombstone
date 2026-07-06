# Tombstone v1.3.0 Release Notes

**Released:** 2026-07-06
**Branch:** `release/v1.3.0` → `main`

---

## What's New

### Helm Chart v0.2.0 — Full K8s Coverage

The Helm chart (`infra/helm/flagmind/`) now deploys all 5 application services. Previously only `flag-api` and `gateway` had Deployment templates; `evaluator`, `intelligence`, and `marketplace` required manual `kubectl apply`.

**New templates:**
- `deployment-evaluator.yaml` — Go service, port 8082, liveness `/health`, readiness `/readyz`
- `deployment-intelligence.yaml` — Python FastAPI, port 8083, higher probe delays (20/15s), `IS_PRIMARY_REGION` env var, `terminationGracePeriodSeconds: 60`
- `deployment-marketplace.yaml` — Go service, port 8086
- `hpa-evaluator.yaml` — `autoscaling/v2` HPA, off by default (`evaluator.autoscaling.enabled: false`)

**Breaking change for existing deployments:** `secret.yaml` now requires values to be injected via Helm `--set-string` instead of using hardcoded placeholder base64 values:

```bash
helm upgrade tombstone ./infra/helm/flagmind \
  --set-string secrets.dbUrl="$DB_URL" \
  --set-string secrets.redisUrl="$REDIS_URL" \
  --set-string secrets.jwtToken="$JWT_SECRET" \
  --set-string secrets.apiToken="$FLAG_API_TOKEN"
```

**Also fixed:** `marketplace.service.port` corrected from `8084` → `8086` to match the service binary default.

---

### Python SDK v0.2.0 — Full 5-Step Evaluation Parity

`tombstone-sdk` on PyPI now implements the same 5-step evaluation pipeline as the TypeScript SDK.

**What was added:**
- **Step 2 — Prerequisites:** `_check_prerequisites()` evaluates dependent flags with a memoized `evaluation_cache` dict (PostHog production pattern — each dependency evaluated at most once per top-level call)
- **Step 3 — Targeting rules:** `match_property()` covers 19 operators across 5 categories with zero new runtime dependencies:
  - Equality: `eq`, `neq`, `in`, `nin`
  - String (case-insensitive, multi-value): `contains`, `startsWith`, `endsWith`
  - Numeric: `gt`, `gte`, `lt`, `lte`
  - Semver (zero-dep via `_padded_version()`): `semver_gt`, `semver_gte`, `semver_lt`, `semver_lte`, `semver_eq`
  - Date (ISO-8601): `date_before`, `date_after`
- **Two-tier exception pattern:** `InconclusiveMatchError` (continue to next rule) vs `RequiresServerEvaluation` (propagate immediately for API fallback)
- **Snapshot deserialization:** `_apply_snapshot` now deserializes `targeting_rules` and `prerequisites` from the API payload; malformed entries are logged and skipped without crashing the cache

**Upgrade:** `pip install tombstone-sdk==0.2.0`

---

### Redoc Interactive API Explorer

`GET http://localhost:8081/api/v1/docs` now serves an interactive Redoc explorer for the Tombstone REST API. The `redoc.standalone.js` bundle is embedded in the `flag-api` binary at compile time — no CDN dependency, works in air-gapped environments.

The JSON spec remains at `GET /api/v1/openapi.json`.

---

## Upgrade Guide

### From v1.2.x

1. **Helm users** — update `values.yaml` to inject secrets via `--set-string` (see above). The `secrets.*` stub block is now required in `values.yaml`.
2. **Python SDK users** — `pip install tombstone-sdk==0.2.0`. The `evaluate()` function signature has two new optional kwargs (`all_flags`, `evaluation_cache`) — existing callers with positional args are unaffected.
3. **flag-api** — no migration required. `/api/v1/docs` is additive.

---

## Known Limitations (v1.4.0 scope)

- Python SDK does not yet implement `hashVersion=2` (FNV-1a hash). Flags deployed with `hashVersion=2` on TypeScript will produce different rollout cohorts on Python.
- Python SDK does not yet implement explicit target list evaluation (`TARGET_MATCH` reason) or rule priority sorting.
- Operator case format (Python uses lowercase `eq`/`contains`, TypeScript types use uppercase `EQ`/`CONTAINS`) — wire format confirmation pending.
