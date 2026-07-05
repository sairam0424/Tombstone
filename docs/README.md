---
kind: schema-readme
---

# docs/ — durable knowledge

One file per **doc**: something you learned, analyzed, or decided that you want to be findable
later. If a signal is raw evidence, a doc is the worked-through version: an analysis, a writeup,
a decision and its rationale, a how-it-works note.

This README is the schema. See `ARCHITECTURE.md` for the model.

## Frontmatter

```yaml
---
kind: doc
domain: []                  # which loop(s) this belongs to
status: draft | adopted | superseded   # optional; use when a doc can be acted on or replaced
links: []                   # related artifacts, [[slug]] or paths
---
```

Optionally add a `type:` field (e.g. `analysis`, `decision`, `learning`) if you find yourself
wanting to filter docs by shape — but don't force it. Most docs are just knowledge.

## Body

Main text = *what's true now*. Append an optional `## Timeline` for *what happened*
(revisions, supersessions, when a decision was revisited). Link liberally with `[[slug]]`.

## Naming

`<short-kebab-slug>.md` or `<TOPIC>-<YYYY-MM>.md` — whatever reads well and sorts sensibly.

## Existing Docs

| File | Description |
|------|-------------|
| `pr-flag-annotations.md` | PR annotation schema and conventions for feature flag changes |
| `EVALUATION_MODEL.md` | 5-step SDK evaluation pipeline, hash algorithms, flag state semantics, blast radius scoring, worked examples |
| `INTELLIGENCE_MODEL.md` | ML layer for non-ML engineers: 3-model anomaly ensemble, LinUCB bandit, CUPED, collision detection |
| `DAY2_OPERATIONS.md` | Capacity planning, upgrade procedure, backup/restore, monitoring thresholds, secret rotation |
| `DEPLOYMENT_KUBERNETES.md` | Helm single/multi-region deployment, tombstone-operator, manual manifests for incomplete chart |
| `SDK_INTEGRATION_GUIDE.md` | Per-language quickstarts, TombstoneTestClient patterns, OpenFeature provider, common gotchas |
| `API_REFERENCE.md` | REST endpoint table, MCP tools, SDK packages, rate limits, OpenAPI location |
| `runbooks/CIRCUIT_BREAKER.md` | CB states, trip thresholds, Redis key inspection, manual reset, troubleshooting |
| `runbooks/DLQ_REDIS_STREAMS.md` | PEL inspection, DLQ management API, poison message identification, replay |
| `runbooks/RATE_LIMITING.md` | SDK vs IP tiers, Lua bucket mechanics, Redis state inspection, capacity planning |
| `runbooks/AUTO_ROLLBACK.md` | Full rollback chain, telemetry flow, verifying via audit_log, re-enable procedure |
| `runbooks/SCHEDULED_CHANGES.md` | 30s poll loop, SKIP LOCKED, retry schedule, terminal FAILED, manual trigger |
