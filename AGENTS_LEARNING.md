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

## Phase 8 — Streams DLQ (2026-07-04)
- `services/intelligence`'s `uv sync` still fails out of the box on the dead `pyod>=0.9.0` line (pulls `numba`/`llvmlite`, which reject Python 3.13). This is the SAME workaround already noted for the "intelligence-asyncio-hardening" branch: comment out the `pyod` line in `pyproject.toml`, `uv sync --all-packages`, run tests, then revert `pyproject.toml` (and do NOT commit the generated `uv.lock` — it wasn't tracked before and isn't part of this change). No new AGENTS_LEARNING entry was needed beyond this pointer since the prior phase already documents the root cause.
- Redis 7 (this platform's pinned version, `redis:7-alpine`) has NO `XREADGROUP...CLAIM` shortcut (that's Redis 8.4+). DLQ/reclaim logic MUST compose `XPENDING` (extended/range form, for idle-time + delivery-count) + `XCLAIM` + `XADD`/`XACK` manually. There is no native "move to DLQ" primitive — the dead-letter decision (XADD to `<stream>:dlq` + XACK off the primary PEL) is 100% application code.
- Critical XCLAIM subtlety: claiming a message via XCLAIM does NOT make it visible again through `XREADGROUP ... STREAMS key >` — the `>` cursor only ever returns never-before-delivered entries. A reclaim sweep that XCLAIMs a stale PEL entry must re-run the dispatch logic itself using the message body XCLAIM returns; it cannot rely on the normal read loop picking the claimed message back up.
- go-redis v9 API surface used: `Client.XPendingExt(ctx, &XPendingExtArgs{Stream, Group, Idle, Start, End, Count, Consumer})` → `[]XPendingExt{ID, Consumer, Idle, RetryCount}`; `Client.XClaim(ctx, &XClaimArgs{Stream, Group, Consumer, MinIdle, Messages})` → `[]XMessage`; both confirmed against the installed `github.com/redis/go-redis/v9 v9.5.1` module source (`go doc` + reading `stream_commands.go` directly) rather than assumed from memory.
- redis.asyncio (installed `redis==8.0.1` inside `services/intelligence/.venv`) equivalents: `Redis.xpending_range(name, groupname, min, max, count, consumername=None, idle=None)` returns `list[{"message_id", "consumer", "time_since_delivered", "times_delivered"}]` (dict keys, not attrs — different shape from go-redis's struct); `Redis.xclaim(name, groupname, consumername, min_idle_time, message_ids, ...)` returns `list[(id, fields_dict)]` tuples. Confirmed via `inspect.signature` + reading `redis/_parsers/helpers.py`'s `parse_xpending_range`/`parse_xclaim` on the actually-installed package — do not assume parity with go-redis's field names.
- DLQ stream-key convention MUST be byte-identical across languages sharing one Redis: `"<primary-stream-key>:dlq"` (i.e. `tombstone:stream:{environment}:dlq`), because gateway (Go) and intelligence (Python) run independent consumer groups against the SAME primary stream per environment and must file poison messages into ONE shared dead-letter queue, not two. `hub.DLQStreamKey()` (Go) and `RedisStreamsEventConsumer.dlq_stream_key()` (Python) are both trivial string concatenation for exactly this reason — keep them that way, don't let one side get clever.
- `maxDeliveryAttempts = 3` / `reclaimIdleThreshold = 30s` are intentionally identical constants in both the Go and Python implementations (`services/gateway/internal/hub/dlq.go` and `services/intelligence/app/kafka/consumer.py`) — a message that fails 3 times should hit the DLQ regardless of which language's consumer group happened to be processing it.
- DLQ replay is a deliberately MANUAL, human-triggered operation (`POST /internal/dlq/{environment}/replay` on the gateway) — NOT a timed auto-replayer like the ClickHouse writer's 60s DLQ replay (`services/intelligence/app/telemetry/clickhouse_writer.py`). Rationale: ClickHouse DLQ entries are typically transient warehouse blips where blind retry is correct; a flag-event message that already failed unmarshalling `maxDeliveryAttempts` times across the full idle-threshold window each time looks like a genuine schema/version mismatch, and auto-replaying it on a timer would just requeue the same failure forever.
- miniredis v2.38.0 (already an indirect dep via flag-api's `go.mod`) fully supports XADD/XGROUP/XREADGROUP/XACK/XPENDING/XCLAIM/XAUTOCLAIM — good enough to unit-test the whole DLQ path without a real Redis. Gotcha: `mr.FastForward()` only decrements key TTLs, it does NOT advance the clock XPENDING/XCLAIM use for idle-time math — use `mr.SetTime(t)` instead to make PEL entries look stale enough to reclaim in tests.
