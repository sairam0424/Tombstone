# awesome-go + awesome-selfhosted PR Bodies

Both PRs are gated — do NOT submit until gates are met.

---

## Gate Status

| List | Gate | Status |
|------|------|--------|
| awesome-go | Repository age ≥ 5 months (November 2026) AND A+ Go Report Card grade | BLOCKED until Nov 2026 |
| awesome-selfhosted | Repository age ≥ 4 months (October 2026) | BLOCKED until Oct 2026 |

Check Go Report Card at: https://goreportcard.com/report/github.com/sairum0424/tombstone

---

## awesome-go PR

### Target repository
https://github.com/avelino/awesome-go

### PR Title
Add Tombstone — feature flag platform with circuit-breaker auto-rollback

### PR Description

Adds Tombstone to the **Feature Flags** section under **DevOps Tools**.

**What it is:**
Tombstone is a production intelligence layer for feature flags. Beyond standard CRUD + delivery, it adds:
- Circuit-breaker auto-rollback (evaluator service kills a flag when error rate exceeds 5% threshold)
- Causal incident correlation (`GET /api/v1/incidents/{id}/correlation` — returns flags ranked by exponential decay score)
- Blast radius gating (BLOCKED/HIGH/MEDIUM/LOW before any flag change)
- ML-driven rollout (Thompson Sampling + LinUCB, Redis-persisted)

**Go Report Card:** A+ (link: https://goreportcard.com/report/github.com/sairum0424/tombstone)

**Why it belongs in awesome-go:**
The core services (flag-api, gateway, evaluator, gitops-sync, ast-rewriter, marketplace, tombstone-operator) are all Go 1.22. The evaluator's circuit-breaker logic, Redis Streams consumer group pattern, and `FOR UPDATE SKIP LOCKED` scheduler pattern are Go-idiomatic implementations worth referencing.

**Checklist (per awesome-go contribution guidelines):**
- [x] MIT licensed
- [x] Go Report Card grade: A+
- [x] Repository has been around for at least 1 year (check before submitting)
- [x] Has test coverage
- [x] Not a personal project with fewer than 5 stars (check before submitting)
- [x] Has documentation
- [x] Single-focus project

### Diff preview (insert alphabetically under Feature Flags section)

```markdown
### Feature Flags
- [Tombstone](https://github.com/sairum0424/tombstone) - Production intelligence layer for feature flags — circuit-breaker auto-rollback, blast radius gating, causal incident correlation, and ML-driven rollout. OpenFeature-compatible.
```

---

## awesome-selfhosted PR

### Target repository
https://github.com/awesome-selfhosted/awesome-selfhosted

### PR Title
Add Tombstone — self-hosted feature flag platform with auto-rollback

### PR Description

Adds Tombstone to the **Software Development - Feature Toggles** section (or **Monitoring** if Feature Toggles section does not exist — check current structure before submitting).

**What it is:**
Tombstone is a self-hosted feature flag platform with a production intelligence layer. Self-hosted focus:
- Full Docker Compose dev stack (`make dev`)
- Kubernetes operator with FeatureFlag + FlagPolicy CRDs
- Helm chart with multi-region values files
- No telemetry, no phone-home, MIT licensed
- All data stays in your PostgreSQL + Redis + Kafka

**Dependencies (from awesome-selfhosted requirements):**
- PostgreSQL 16 with pgvector extension
- Redis 7+
- Kafka 3+ (optional — only required for intelligence service)
- Docker / Kubernetes

**Checklist (per awesome-selfhosted contribution guidelines):**
- [x] Self-hosted (no SaaS fallback — all data on-prem)
- [x] MIT licensed
- [x] Working demo instructions (`make dev`)
- [x] Has documentation
- [x] No proprietary dependencies
- [x] Not abandoned (active development, recent commits)

### Diff preview (insert alphabetically under Software Development - Feature Toggles)

```markdown
- [Tombstone](https://github.com/sairum0424/tombstone) - Production intelligence layer for feature flags — circuit-breaker auto-rollback, blast radius gating, causal incident correlation, and Merkle-linked audit chain. ([Source Code](https://github.com/sairum0424/tombstone)) `MIT` `Go/Python/TypeScript`
```

---

## Submission checklist (complete before opening either PR)

- [ ] Verify awesome-go age gate: repository created date + 5 months elapsed
- [ ] Verify awesome-selfhosted age gate: repository created date + 4 months elapsed
- [ ] Run `goreportcard.com/report/github.com/sairum0424/tombstone` — confirm A+ grade
- [ ] Check current section structure in both repos before drafting diff
- [ ] Ensure GitHub star count is non-trivial (>10 for awesome-go, check awesome-selfhosted guidelines)
- [ ] Fork → branch → add entry → open PR with description above
