# Tombstone — Agent Learning Log

Read this file BEFORE starting any task.
Update this file AFTER completing any task with new learnings.

## Lessons Learned

*(empty — populate as work progresses)*

## Common Pitfalls (pre-filled from architecture decisions)

- `go.work` multi-module: each service has its own `go.mod`. Cross-service imports are NOT allowed in production code (only in tests via `go.work`). Use the REST API or gRPC for cross-service calls.
- `GOWORK=off` is required in all Dockerfiles to ensure each service builds as a standalone module.
- The TypeScript SDK uses `"type": "module"` (ESM-only). All imports must use `.js` extensions even when referencing `.ts` source files.
- `audit_log.prev_hash` must always be computed as `sha256(previous_entry.id + previous_entry.created_at)` — never null except for the very first entry per flag.
- Redis channel naming: always `stream:{environment}:updates` — the broadcaster extracts `environment` by splitting on `:` (index 1).
- Flag tombstones are checked at TWO levels: DB constraint (authoritative) and service layer (human-readable error). Both are required.

## Phase 3 (2026-06-21)
- RBAC defaults to VIEWER (least privilege) when DB role lookup fails — intentional
- Break-glass tokens use "bgt_" prefix for easy identification in audit logs
- GitOps syncer never deletes flags — only creates/updates; archiving requires explicit API call
- Warehouse connector returns only aggregated statistics (never raw user rows) — zero-copy privacy guarantee
- BLOCKED blast radius requires 10-char minimum justification to prevent accidental one-click overrides

## Phase 4 (2026-06-21)
- MCP server uses StdioServerTransport — it communicates over stdin/stdout, not HTTP; no port needed
- Thompson Sampling requires min_observations=50 before recommending advancement — prevents premature rollouts on low-traffic flags
- Python SDK uses threading.Thread(daemon=True) for SSE listener — daemon threads die with the main process, no explicit cleanup needed
- CleanupPRGenerator falls back to template when ANTHROPIC_API_KEY is absent — always produces a valid PR spec
- Slack webhooks are fire-and-forget (return bool) — never block the main request path
- Datadog event API posts at https://api.datadoghq.com/api/v1/events — flag events appear as vertical markers on metric graphs

## Phase 5A MurmurHash3 Fix (2026-06-21)
- CRITICAL: ALL SDKs MUST use MurmurHash3 unsigned 32-bit seed=0 for rollout buckets
- Python was using MD5 — now fixed: mmh3.hash(flag_key + user_id, seed=0, signed=False) % 100
- TypeScript reference: murmurhash.v3(flagKey + userId) unsigned (>>> 0) then % 100
- Never use MD5/SHA for rollout assignment — they produce different buckets across languages
- All new SDKs (Java/Ruby/.NET) must pass test-contract/vectors.json parity vectors

## Phase 5B (2026-06-21)
- AST rewriter service uses ENV GOWORK=off (not inline flag) — this is correct because ENV applies to all subsequent RUN steps
- Terraform provider go.mod is standalone from go.work intentionally — Terraform providers cannot be in Go workspaces (breaks terraform init)
- Terraform resource "delete" maps to flag archive (tombstone) — not true data deletion by design
- GitOps syncer never deletes flags on reconcile; a git revert cannot wipe production configuration

## Phase 5C (2026-06-21)
- Java SDK uses Integer.remainderUnsigned(hash, 100) not hash % 100 — the % operator in Java gives signed results for negative ints, breaking bucket assignment
- .NET SDK uses Murmur.Create32(seed: 0, managed: true) and reads result as BitConverter.ToUInt32 — must be unsigned
- Ruby SDK uses MurmurHash3::V32.str_hash(flag_key + user_id, 0) — seed 0 is required for cross-language parity
- Edge SDK uses FNV-1a (not MurmurHash3) — acceptable deviation because Cloudflare Workers lack wasm-murmurhash; document this in the SDK README
- Snowflake and BigQuery connectors use asyncio.to_thread() — both vendor SDKs are synchronous-only
- CUPED theta must be computed on POOLED data (not per-variant) — computing per-variant biases the estimator
- mSPRT likelihood ratio threshold is 1/alpha (not the p-value) — these are fundamentally different inferential frameworks

## Phase 5D (2026-06-21)
- VS Code extension uses module:commonjs (not ESM) for vscode.* API compatibility
- CodeLens provider catches API errors and returns empty array — never throw from provideCodeLenses
- Marketplace registry is in-memory (state resets on restart) — use Redis for persistence in Phase 6
- Webhook dispatcher uses goroutines (fire-and-forget) — never block on webhook delivery
- SOC 2 ExportAuditLog uses streaming JSON lines (NDJSON) not a JSON array — auditors need line-by-line verification
- HMAC signature appended as final JSONL line for tamper detection
- marketplace go.work entry is required but dockerfile uses GOWORK=off

## Phase 6 (2026-06-22)
- Relay proxy reuses hub.Hub from gateway/internal/hub — no new fan-out logic needed
- OpenFeature providers define interfaces inline — avoids @openfeature/sdk dependency that may not be installed
- jscodeshift rewriter gracefully falls back to preview-only if jscodeshift binary not on PATH
- SCIM routes use separate SCIM_TOKEN auth separate from JWT — IdPs send server-to-server with own tokens
- SSO routes only registered when SSO_PROVIDER env var is set — no-op for local dev
- ClickHouse is opt-in via CLICKHOUSE_HOST env var — uses graceful degradation if not available
- AI experiment explanation uses Claude Haiku with max_tokens 200 — cost-effective for high-volume
- Autonomous rollout UI hides when flag is at 100% — nothing left to advance

## Phase 9 — intelligence asyncio hardening (2026-07-03)
- `services/intelligence` has TWO separate warehouse-connector class hierarchies that look
  like duplicates but aren't call-site-equivalent: standalone `app/warehouse/{bigquery,snowflake,databricks}.py`
  (used by `fetch_metric`/`test_connection`, part of the `WarehouseConnector` Protocol in `base.py`)
  vs. the `SnowflakeConnector`/`BigQueryConnector` classes INSIDE `app/warehouse/connector.py`
  (used by `query_experiment_metrics`, wired up via `get_connector()` and consumed by
  `app/experiments/routes.py`). Both sets independently wrapped blocking driver calls in
  bare `asyncio.to_thread` — both needed migrating to the new `run_warehouse_query` helper.
- `services/intelligence/app/warehouse/executor.py` (new): dedicated bounded `ThreadPoolExecutor`
  (max_workers=4) + `asyncio.wait_for` timeout (default 30s, overridable per-call via `timeout=` kwarg)
  isolates warehouse-driver blocking calls from Python's shared default executor, which is also used
  by `app/search/embedding_model.py` / `embedding_model_bedrock.py` for embedding inference.
  `asyncio.wait_for` does NOT kill the underlying thread on timeout — it only bounds how long the
  calling coroutine waits; the thread keeps running until it finishes naturally.
- `services/intelligence/pyproject.toml` has a base dependency on `pyod>=0.9.0` whose transitive dep
  `numba==0.53.1` fails to build on Python >=3.10 (this repo requires Python 3.12+, so `uv sync` is
  currently broken out of the box). Also: `pandas` (used directly by bigquery.py/snowflake.py/databricks.py)
  is NOT a base dependency — it only arrives transitively via the optional `bigquery`/`snowflake`/
  `databricks`/`all-warehouses` extras (e.g. `google-cloud-bigquery[pandas]`). To fully verify warehouse
  connector changes, sync with `uv sync --all-packages --extra all-warehouses` (after working around the
  pyod issue, e.g. by temporarily commenting it out — do not commit that removal as part of unrelated work).
- `asyncio.Lock` guarding shared in-memory state across FastAPI background tasks + HTTP handlers: store it
  on `app.state` (e.g. `app.state.background_job_lock = asyncio.Lock()`) in the lifespan startup, alongside
  other shared singletons, then thread it through as an explicit parameter to background task coroutines
  (avoid reaching into `app.state` from deep inside a task — pass the lock object directly).
- For an HTTP-triggered endpoint that shares state with a background job, prefer fail-fast
  (`if lock.locked(): return 409`) over blocking the request — there's a small unavoidable TOCTOU race
  between the `.locked()` check and actually acquiring the lock, but it's benign (worst case: an
  occasional missed 409, caller blocks briefly instead of fast-failing) — not worth over-engineering.
