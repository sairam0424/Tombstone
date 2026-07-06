# Tombstone — Activity Log

Append-only journal of finished work, so anyone (human or agent) can catch up fast.
Newest first. Append an entry above older entries whenever a bulk of work wraps (ideally right before
the commit that ships it). Keep entries SHORT: header line + What + Refs, nothing else.

**Entry grammar** (strict, one header line per entry):
```
## YYYY-MM-DD · Short title · #tag1 #tag2
What: 1-2 lines, outcome first.
Refs: [doc](path) (new|updated), repo PR/commit links.
```

**Tags** (reuse before inventing):
#analysis #product #infra #loop #harness #incident #governance #rollout #signal #research

**Retrieval recipes** (macOS; entry headers always start `## 20`):
```bash
# index of all entries (one line each)
grep '^## 20' LOG.md
# last 5 entries, full
tail -r LOG.md | awk '{print} /^## 20/{c++; if(c==5) exit}' | tail -r
# all entries about a topic
awk '/^## 20/{p=/#incident/} p' LOG.md
# entries from a month
awk '/^## 20/{p=/^## 2026-06/} p' LOG.md
```

---

<!-- entries below this line, newest first -->

## 2026-07-06 · v1.3.0 — Helm completion, Python SDK parity, Redoc explorer · #product #infra
What: Closed 3 documented v1.3.0 gaps: Helm chart v0.2.0 with Deployment templates for all 5 services (evaluator+HPA, intelligence, marketplace), Python SDK v0.2.0 with 5-step evaluation parity (prerequisites, targeting rules, full operator surface — no new deps), Redoc API explorer embedded in flag-api at /api/v1/docs (no CDN).
Refs: [CHANGELOG.md](CHANGELOG.md) (updated), [infra/helm/flagmind/](infra/helm/flagmind/) (updated), [packages/sdks/flagmind-python/](packages/sdks/flagmind-python/) (updated), [services/flag-api/internal/docs/](services/flag-api/internal/docs/) (new).


## 2026-07-05 · OSS launch assets generated for v1.2.1 · #product
What: Generated 6 launch assets covering all pending platforms (Show HN, Product Hunt, Twitter, awesome-go, StackShare, LinkedIn). Assets are ready-to-publish drafts; gate items (awesome-go needs A+ grade, Product Hunt needs warm-up, CNCF needs 300 stars) documented in DISCOVERABILITY_LOG.md.
Refs: [show-hn-v121.md](docs/submissions/show-hn-v121.md) (new), [product-hunt.md](docs/submissions/product-hunt.md) (new), [twitter-thread.md](docs/submissions/twitter-thread.md) (new), [awesome-go-pr.md](docs/submissions/awesome-go-pr.md) (new), [outreach-v121.md](docs/submissions/outreach-v121.md) (new), [linkedin-v121.md](docs/submissions/linkedin-v121.md) (new), [DISCOVERABILITY_LOG.md](docs/DISCOVERABILITY_LOG.md) (updated).

## 2026-07-05 · v1.2.1 — comprehensive documentation suite (12 new files) · #product #analysis
What: Added 12 documentation files covering evaluation model, ML intelligence, K8s deployment, SDK integration, API reference, Day 2 operations, Helm compatibility, and 5 operational runbooks. Based on 106-agent deep-research findings identifying critical adoption blockers.
Refs: [EVALUATION_MODEL.md](docs/EVALUATION_MODEL.md) (new), [INTELLIGENCE_MODEL.md](docs/INTELLIGENCE_MODEL.md) (new), [DAY2_OPERATIONS.md](docs/DAY2_OPERATIONS.md) (new), [DEPLOYMENT_KUBERNETES.md](docs/DEPLOYMENT_KUBERNETES.md) (new), [SDK_INTEGRATION_GUIDE.md](docs/SDK_INTEGRATION_GUIDE.md) (new), [API_REFERENCE.md](docs/API_REFERENCE.md) (new), [infra/helm/flagmind/COMPATIBILITY.md](infra/helm/flagmind/COMPATIBILITY.md) (new), [docs/runbooks/](docs/runbooks/) (5 new runbooks), README.md (updated), docs/README.md (updated).

## 2026-07-05 · v1.2.1 — 5 critical regression fixes from adversarial validation · #infra #analysis #product
What: 26-agent adversarial pre-release validation found 5 critical no-go issues (Slack kill-switch broken, four-eyes approval unreachable, fresh deploy crash, Datadog auto kill-switch silent failure, /readyz under rate-limiting). All fixed in PR #74, re-validated, re-promoted as v1.2.1.
Refs: PR #74 merged. release/v1.2.1 → PR to main.

## 2026-07-05 · post-release housekeeping + schema.sql baseline fix · #infra #harness
What: Synced develop to main HEAD after v1.2.0 release. Removed all resilience-initiative agent worktrees (13 freed). Added idempotency_keys table with actor-scoped unique index to schema.sql baseline so fresh installs don't need migration 010+012 separately.
Refs: [schema.sql](services/flag-api/internal/db/schema.sql) (updated).

## 2026-07-05 · v1.2.0 — resilience-patterns initiative complete · #infra #analysis #product
What: 10 resilience phases (#62–#71) + 4 audit-fix blockers (#72) shipped to develop and cut as v1.2.0. Covers retry/backoff/jitter, circuit breakers, distributed rate limiting, adaptive load shedding, idempotency keys, Redis Streams DLQ, scheduler FOR UPDATE SKIP LOCKED, webhook dedup, intelligence asyncio hardening, and snapshot reconciliation.
Refs: PRs #62-#72 merged to develop; release/v1.2.0 → PR to main.

## 2026-07-03 · Documentation upgrade — 3 new architecture docs + KB repair · #infra #harness #analysis
What: Added DATA_MODEL.md, DEPLOYMENT_ARCHITECTURE.md, SDK_CONTRACT.md. Repaired docs/README.md's stale index and missing kind:doc frontmatter on 9 files; fixed domains/README.md's flag-cleanup collector attribution bug.
Refs: [DATA_MODEL.md](DATA_MODEL.md) (new), [DEPLOYMENT_ARCHITECTURE.md](DEPLOYMENT_ARCHITECTURE.md) (new), [SDK_CONTRACT.md](SDK_CONTRACT.md) (new), docs/README.md (updated), domains/README.md (updated).

## 2026-06-24 · v2.1.0 shipped — all 10 phases complete · #infra #loop #harness
What: Redis Streams (Phase 4.1), Slack HTTP routes + governance loop, mTLS (Phase 6.1), Argos LLM rule generation (Phase 3.2) merged to main. All 32 items from the 10-phase beast/ultimate upgrade plan are fully implemented.
Refs: Pull requests #44 #45 #46 #47 #48 merged. Tombstone v2.1.0 on main.

## 2026-06-24 · loop-engineer harness activated — 4 domain loops wired · #harness #loop #ops
What: ship-change.js workflow, /pr skill, /new-loop skill, dev-local launcher, and 4 domain loops (flag-cleanup daily, incident-response event-driven, rollout-advisor weekdays, governance weekly) all deployed to main.
Refs: .claude/workflows/ship-change.js (new), scripts/loop-*.sh (4 new), .github/workflows/loop-*.yml (4 new), domains/*/README.md (4 new).

## 2026-06-23 · Bootstrap loop-engineer knowledge base substrate · #harness #loop #infra
What: Created signals/, docs/, domains/ scaffolding plus LOG.md and ARCHITECTURE.md for Loop-Engineer v2 integration.
Refs: [signals/README.md](signals/README.md) (new), [docs/README.md](docs/README.md) (new), [domains/README.md](domains/README.md) (new), [ARCHITECTURE.md](ARCHITECTURE.md) (new)

## 2026-06-23 · incident-response: test-flag · #loop #incident
What: Circuit trip documented. Error rate: 0. Correlated: none.
Refs: docs/incident-2026-06-23-test-flag.md (new), domains/incident-response/metrics/trips.jsonl (updated).
