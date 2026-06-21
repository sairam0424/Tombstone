# PR: feature/foundation-phase1-4 → develop

## Summary
Implements Tombstone Phase 1-4: the complete core production intelligence platform.

- **flag-api**: REST CRUD, append-only Merkle audit log, tombstoning, kill switch, RBAC, break-glass tokens, blast radius gate
- **gateway**: SSE streaming hub, Redis broadcaster, relay proxy foundations
- **evaluator**: Circuit breaker auto-rollback, blast radius scoring, telemetry ingest
- **intelligence**: Anomaly detection, incident correlation, NLP search, Thompson Sampling MAB, warehouse-native experiments, Slack/Datadog integrations
- **dashboard**: 9 views — FlagList, FlagDetail, IncidentTimeline, GovernanceDash, DependencyGraph, ApprovalQueue, BreakGlass, Experiments, Marketplace
- **SDKs**: @tombstone/core (TypeScript), @tombstone/react, Python, Java 21, .NET 8, Ruby 3.3, Cloudflare Edge
- **gitops-sync**: Flags-as-code YAML reconciler
- **MCP server**: 9 tools for Claude Code / Cursor integration
- **CI**: GitHub Actions (5 jobs), Docker Compose (12 services), e2e test suite (15 checks)

## Test Plan
- [ ] docker compose up --build succeeds
- [ ] make seed populates 3 sample flags
- [ ] TOMBSTONE_TEST_TOKEN=sdk-dev-token bash scripts/e2e-test.sh passes all 15 tests
- [ ] Dashboard loads at http://localhost:3000
- [ ] Kill switch activates within 5 seconds (SSE propagation)
- [ ] Circuit breaker test: POST /api/v1/telemetry with is_error=true x100

## Breaking Changes
None — initial implementation.
