# Day 2 Operations Guide

Steady-state operations for teams running Tombstone in production: capacity planning, upgrades, backup/restore, monitoring, flag hygiene, and secret rotation.

---

## GitOps Operations

### Checking Flux Health

```bash
# List all Kustomization objects and their reconciliation status
flux get kustomizations

# Inspect the infrastructure Kustomization specifically
flux get kustomization tombstone-infrastructure -n flux-system

# Force a reconciliation (re-applies source + kustomization in one shot)
flux reconcile kustomization tombstone-infrastructure -n flux-system --with-source
```

### Checking Image Automation

```bash
# List all ImageRepository objects (flag-api, intelligence, etc.)
flux get imagerepository -n flux-system

# List all ImagePolicy objects and the latest resolved tag
flux get imagepolicy -n flux-system

# Force-poll the container registry for new tags
flux reconcile imagerepository tombstone-flag-api -n flux-system

# Force an ImageUpdateAutomation run (writes updated tags to git)
flux reconcile imageupdateautomation tombstone-image-updater -n flux-system
```

### Checking Argo CD Status (provider=argocd|both)

```bash
# List all Application objects and their sync/health state
kubectl get applications -n argocd
argocd app list

# Inspect a specific application (shows resource tree + conditions)
argocd app get tombstone-app

# Manually trigger a sync of the flags Application
argocd app sync tombstone-flags
```

### Checking Argo Rollouts (provider=both)

```bash
# List Rollout objects and their current canary step
kubectl get rollouts -n tombstone

# List AnalysisRun objects (blast-radius gate results)
kubectl get analysisrun -n tombstone

# Manually query the blast-radius evaluator (LOW/MEDIUM=promote, HIGH/BLOCKED=abort)
curl "http://evaluator:8082/api/v1/blast-radius?flag_key=checkout-v2"
```

### Switching GitOps Provider

```bash
# 1. Verify Flux infrastructure Kustomization is Ready before layering Argo CD on top
flux get kustomization tombstone-infrastructure -n flux-system

# 2. Apply the target overlay (flux | argocd | both)
kubectl apply -k gitops/providers/both/

# 3. Wait for the Argo CD server to become available
kubectl rollout status deployment/argocd-server -n argocd

# Note: argocd-bootstrap.yml MUST run AFTER flux-bootstrap.yml —
# the tombstone-operator CRDs must exist before Argo CD can apply FeatureFlag resources.
```

### GitOps Drift Alerts

**Flux side:** The `spec.ignore.paths` field in each Kustomization protects `rolloutPct` from being overwritten during reconciliation — ML-driven rollout mutations made by the evaluator are intentionally excluded from drift detection.

**Argo CD side:** Sync failures trigger a notification to the marketplace Slack integration:
```
POST marketplace.tombstone.svc:8086/api/v1/marketplace/slack/actions
```
Configure this in `gitops/providers/argocd/notifications-cm.yaml`. Both `ignoreDifferences` AND `RespectIgnoreDifferences: true` in the Application spec are required — `ignoreDifferences` alone does **not** prevent Argo CD from overwriting those fields during a sync.

---

## Capacity Planning

### Database Connections

Connection pools are set per service at startup:

| Service | `SetMaxOpenConns` | `SetMaxIdleConns` |
|---------|-----------------|-----------------|
| flag-api | 5 | 2 |
| evaluator | 3 | 2 |

With `N` replicas of each service, total Postgres connections = `N × conns_per_service`.

**Neon free tier** has a 20-connection limit. With default settings:
- 2× flag-api + 2× evaluator = `(2×5) + (2×3) = 16 connections` — fits Neon free tier
- 3× flag-api + 2× evaluator = `(3×5) + (2×3) = 21 connections` — exceeds Neon free tier

For production with Neon paid tiers or self-hosted Postgres, scale replicas freely. The connection pool settings (`SetMaxOpenConns`) should be tuned based on your Postgres `max_connections` setting.

### Redis Memory

Redis holds:
- Flag event streams: `tombstone:stream:{env}` — capped at ~10,000 events per stream × ~1KB/event ≈ **10MB per environment**
- Circuit breaker state: ~100 bytes per flag key (negligible)
- Rate limit buckets: ~50 bytes per active SDK token × number of active tokens
- LinUCB matrices: ~16KB per flag-environment pair (d=5, float64 matrices)

For 5,000 flags across 3 environments with LinUCB: `5000 × 3 × 16KB ≈ 240MB`. Plan accordingly.

### Intelligence Service

- **Daily retrain at 02:00 UTC**: Isolation Forest retraining across all tracked flags. CPU-intensive for ~30–60 seconds. No impact on request serving (runs in a background task).
- **Embedding model (BAAI/bge-m3)**: ~400MB RAM. This is why the intelligence service has a 1Gi memory limit in `infra/helm/flagmind/values.yaml`.
- **Recommendation**: run intelligence as a single replica (it's stateful via Redis). For HA, ensure Redis persistence (AOF or RDB) is enabled.

### Replica Count Guidelines

| Traffic Tier | flag-api | gateway | evaluator | intelligence |
|-------------|---------|---------|-----------|-------------|
| Dev / small (<50 req/s) | 1 | 1 | 1 | 1 |
| Medium (50-500 req/s) | 2 | 2 | 2 | 1 |
| Large (500+ req/s) | 3-5 | 3-5 | 2-3 | 1 |

Gateway replicas are stateless (SSE connections are load-balanced). Flag-api replicas share Postgres connection budget. Intelligence should stay at 1 or use Redis-backed state carefully.

---

## Upgrade Procedure

### Pre-Upgrade Checklist

- [ ] Confirm `develop` is synced to `main` (or your release branch is clean)
- [ ] Run `make test` locally or verify CI passes
- [ ] Back up Postgres: `pg_dump $DATABASE_URL > tombstone_backup_$(date +%Y%m%d).sql`
- [ ] Note current image tags (in case rollback is needed)
- [ ] Run `make migrate` against a staging database first

### Migration Application

```bash
# Apply all migrations in order (idempotent — safe to re-run)
make migrate
```

The `make migrate` target applies all `.sql` files in `services/flag-api/internal/db/migrations/` in sorted order. All migrations use `IF NOT EXISTS` and `ADD COLUMN IF NOT EXISTS` — safe to re-run.

**Migration sequence for v1.2.1** (migrations 010, 011, 012 are new since v1.0.0):
```
schema.sql (baseline) → 002 → 003 → 004 → 005 → 010 → 011 → 012
```

### Rolling Upgrade Order

Apply service-by-service in this order to maintain availability:

```
1. flag-api     ← schema migrations run here; no breaking API changes in v1.2.1
2. evaluator    ← depends on flag-api for kill-switch calls
3. gateway      ← depends on Redis Streams (now with DLQ); backwards compatible
4. intelligence ← standalone; safe to upgrade anytime
5. marketplace  ← integration layer; upgrade last to minimize webhook downtime
```

**Health gate between steps**: verify `/readyz` returns 200 before proceeding.

```bash
# Check readyz for each service
curl http://localhost:8081/readyz  # flag-api
curl http://localhost:8082/readyz  # evaluator
curl http://localhost:8080/readyz  # gateway
curl http://localhost:8083/readyz  # intelligence (Python: GET /health)
```

### Rollback

All v1.2.1 migrations are backwards-compatible:
- New columns have `DEFAULT` values or are `IF NOT EXISTS`
- Old service images work without the new columns (they simply won't use them)

To roll back: restore Postgres snapshot, deploy old image tags.

---

## Backup and Restore

### What Needs Backup

| Data store | Contents | Backup required? |
|-----------|----------|-----------------|
| PostgreSQL | Flags, environments, audit log, targeting rules, schedules | **Yes — primary source of truth** |
| Redis | Event streams, CB state, rate limit buckets, LinUCB matrices | No — all reconstructable |
| Redis Streams | Flag events (last 10k per stream) | No — ephemeral delivery buffer |

### Postgres Backup

```bash
# Manual backup
pg_dump $DATABASE_URL > tombstone_backup_$(date +%Y%m%d_%H%M%S).sql

# Restore
psql $DATABASE_URL < tombstone_backup_20260705_140000.sql
```

For production, use your provider's managed backup:
- **Neon**: automatic PITR (point-in-time recovery) on paid tiers
- **Self-hosted**: configure `pg_basebackup` or use WAL archiving for PITR

### Redis: Not Required

Redis data is entirely reconstructable:
- Circuit breaker state: resets to CLOSED (CLOSED is the zero state — no key = CLOSED)
- Rate limit buckets: fresh buckets refill from baseline within seconds
- LinUCB matrices: rebuild over ~50 observations (fast for active flags)
- Flag event streams: new events will populate naturally; no historical gap impact (SDKs fetch a full snapshot on reconnect)

If using Redis persistence for LinUCB continuity on high-value flags, enable RDB snapshots with `save 900 1` or AOF with `appendonly yes` in your Redis config.

---

## Monitoring and Alerting

### Key Endpoints Per Service

| Service | Health | Readiness | Metrics |
|---------|--------|-----------|---------|
| flag-api | `GET /health` | `GET /readyz` | OTel (OTLP_ENDPOINT) |
| gateway | `GET /health` | `GET /readyz` | `GET /api/v1/metrics` |
| evaluator | `GET /health` | `GET /readyz` | OTel |
| intelligence | `GET /health` | `GET /health` | — |
| marketplace | `GET /health` | `GET /readyz` | — |

### Recommended Alert Thresholds

| Alert | Warning | Critical | How to check |
|-------|---------|----------|-------------|
| Service readiness | `/readyz` fails | `/readyz` fails >30s | Periodic `curl /readyz` |
| DLQ depth | >5 messages | >20 messages | `redis-cli XLEN tombstone:stream:production:dlq` |
| Circuit breaker trips | 1 trip/hour | 3 trips/hour | Audit log `event_type = kill_switch_activated` |
| Evaluator p99 latency | >100ms | >200ms | OTel histogram |
| Governance health score | <0.85 | <0.70 | `scripts/loop-governance.sh` output |
| Postgres connection saturation | >80% of `max_connections` | >95% | `pg_stat_activity` |

### Governance Loop Health Score

The weekly governance loop (`scripts/loop-governance.sh`) outputs a `health_score` between 0.0 and 1.0. It covers:
- Change request approval lag (high lag = low score)
- Stale flag count (>30% stale = deduction)
- Audit coverage (all flag changes have audit entries)
- SOC2 evidence completeness

A score below 0.80 generates a Slack alert (requires `SLACK_BOT_TOKEN`).

---

## Flag Lifecycle Hygiene

### When to Archive a Flag

A flag is "ready to archive" when it has been at 100% rollout for 30+ days with no incidents. The `loop-flag-cleanup.sh` script (runs daily at 02:00 UTC) automatically detects these and writes signal files.

```bash
# Manual stale flag check
curl http://localhost:8083/api/v1/stale
```

### Archive vs Delete vs Tombstone

- **Archive**: sets `flag.state = ARCHIVED`, `flag_environments.enabled = false`. The flag key is preserved in the `flags` table but the DB trigger adds it to `flag_tombstones` on archive (preventing reuse). Use for normal cleanup.
- **Delete**: not available via API — all flag removals go through archive. This is intentional.
- **Tombstone** (Knight Capital pattern): the `flag_tombstones` table stores permanently reserved keys. The DB trigger `enforce_tombstone` blocks any future INSERT with the same key. This prevents the Knight Capital class of incidents where a retired flag key is accidentally reused.

### Pre-Migration Flag Freeze

Before a major Postgres migration, disable all scheduled changes:

```sql
-- Pause all pending scheduled changes temporarily
UPDATE scheduled_changes
SET status = 'PENDING', scheduled_for = scheduled_for + INTERVAL '1 hour'
WHERE status = 'PENDING' AND scheduled_for < NOW() + INTERVAL '30 minutes';
```

---

## Secret Rotation

### JWT_SECRET

Used by flag-api for JWT authentication of API calls.

**Impact**: All existing JWT tokens become invalid immediately on rotation. Active sessions see `401 Unauthorized` and must re-authenticate.

```bash
# Generate new secret
export NEW_JWT_SECRET=$(openssl rand -hex 32)

# Update environment variable in your deployment, then rolling-restart flag-api
# Users will need to log in again — no data loss
```

### FLAG_API_TOKEN

Used by evaluator, marketplace, and gitops-sync to authenticate against flag-api.

**Rotation order** (critical — rotate consumers before flag-api):
1. Update `FLAG_API_TOKEN` in evaluator environment → restart evaluator
2. Update `FLAG_API_TOKEN` in marketplace environment → restart marketplace
3. Update `FLAG_API_TOKEN` in gitops-sync environment → restart gitops-sync
4. Update `FLAG_API_TOKEN` in flag-api environment → restart flag-api

Reversing this order (updating flag-api first) will cause `403 Forbidden` errors in all consumers during the rotation window.

### SLACK_SIGNING_SECRET

Used only by the marketplace service for validating inbound Slack webhooks. Update in marketplace environment and restart. No impact on other services.

### mTLS Certificates

When `MTLS_ENABLED=true`, flag-api generates a TLS certificate on startup and writes it to the shared `/certs` volume. The certificate regenerates automatically on restart with the new `MTLS_CA_CERT` / `MTLS_CA_KEY` values. No downtime — the volume mount is shared by all clients.
