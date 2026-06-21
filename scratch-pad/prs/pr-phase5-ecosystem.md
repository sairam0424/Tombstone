# PR: feature/phase5-ecosystem → develop

## Summary
Phase 5A-5D: ecosystem expansion and enterprise readiness.

**Phase 5A** — Make the Graph Visible
- fix(sdk): MurmurHash3 standardization — Python was using MD5, causing non-deterministic rollout across languages
- Causal dependency graph UI: D3 force-directed visualization from audit_log co-occurrence data

**Phase 5B** — Make Cleanup Automated
- AST rewriter service (jscodeshift integration): scan + diff-preview for stale flag removal
- Terraform provider: tombstone_flag + tombstone_flag_environment resources

**Phase 5C** — Expand the Ecosystem
- Java 21, .NET 8, Ruby 3.3 server-side SDKs (all using MurmurHash3)
- Snowflake + BigQuery + Redshift warehouse connectors (zero-copy privacy)
- CUPED variance reduction + mSPRT always-valid inference + power calculator
- Cloudflare Workers Edge SDK

**Phase 5D** — Build the Moat
- VS Code extension: TombstoneCodeLensProvider (9 languages, backtick + dot-notation support)
- SOC 2 evidence export endpoints (CC6/CC7/CC8/CC9 mapping, HMAC-signed NDJSON)
- Marketplace: 7 first-party integrations with webhook dispatcher

## Test Plan
- [ ] Python SDK parity test: pytest packages/sdks/flagmind-python/tests/ passes
- [ ] Causal graph endpoint: POST http://localhost:8083/api/v1/dependency-graph returns nodes+edges
- [ ] AST rewriter scan: POST http://localhost:8085/api/v1/scan finds callsites
- [ ] Cross-language parity: all 12 vectors in test-contract/vectors.json verified

## Breaking Changes
- Python SDK hash function changed from MD5 to MurmurHash3 — rollout bucket assignments will change for existing users
