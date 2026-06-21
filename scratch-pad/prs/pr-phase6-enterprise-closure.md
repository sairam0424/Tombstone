# PR: feature/phase6-enterprise-closure → develop

## Summary
Phase 6A-6C: enterprise closure and critical gap fills.

**Phase 6A** — Close Critical Gaps
- Relay proxy: air-gapped + resilience backstop (3-tier fallback: upstream SSE → Redis → file)
- OpenFeature providers: TombstoneProvider for TypeScript + Python (vendor-neutral integration)
- jscodeshift subprocess: actual code rewriting, not just diff preview

**Phase 6B** — Enterprise Completeness
- SAML/OIDC SSO: OIDCIssuer callback, domain allowlist, JWT issuance (SSO_PROVIDER env var opt-in)
- Helm Charts: K8s deployments, ConfigMap, Secret, Ingress for flag-api + gateway
- SCIM 2.0: user provisioning + orphan flag detection on deprovisioning

**Phase 6C** — Intelligence Upgrade
- ClickHouse telemetry: MergeTree + materialized view (opt-in via CLICKHOUSE_HOST)
- AI ship/no-ship: Claude Haiku explanation (opt-in via ANTHROPIC_API_KEY)
- AutonomousRolloutToggle: Thompson Sampling enable/disable in FlagDetail UI

**Rename** — Tombstone (formerly FlagMind)
- All module paths, package names, env vars, class names, MCP tools renamed
- Tombstone references the Knight Capital $460M incident — the product origin story

## Test Plan
- [ ] go vet ./... passes on all 6 Go services
- [ ] Relay proxy tests: go test ./services/gateway/internal/relay/... (7/7 pass)
- [ ] SSO routes absent when SSO_PROVIDER unset
- [ ] helm template ./infra/helm/flagmind renders without errors (requires helm CLI)
- [ ] ClickHouse: only activated when CLICKHOUSE_HOST env var is set

## Breaking Changes
- Rename: all env vars changed FLAGMIND_* → TOMBSTONE_*, npm scope @flagmind/* → @tombstone/*, MCP tools tombstone_*
- Update any existing .env files, CI secrets, and client code
