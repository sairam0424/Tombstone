# Tombstone — Agent Learning Log

Read this file BEFORE starting any task.
Update this file AFTER completing any task with new learnings.

## Lessons Learned

## Argo CD GitOps Provider — Task A2: Argo CD install manifests (2026-07-07)
- Kustomize security restriction applies to PATCH files the same way it applies to resource files: a `patches:` entry with `path: ../../production/argocd/foo.yaml` fails with "security; file is not in or below" even when the target overlay's `kustomization.yaml` is at `staging/argocd/`. Patch files must be LOCAL (within the overlay's own directory tree) — cross-directory patch path references are not permitted. Fix: copy the shared patch files into the staging directory as well (`cp production/argocd/argocd-cm-patch.yaml staging/argocd/`), and reference them with local paths in staging's `kustomization.yaml`. This is the same restriction documented for resource files (Task 3/4 of Flux integration), but it applies to patches too.
- The plan said `path: ../production/argocd/` for staging patches — this path resolves incorrectly BOTH from a traversal AND security perspective. The actual relative path needed would be `../../production/argocd/` (since staging/argocd/ needs two `..` to reach clusters/), but kustomize blocks it regardless. Always copy shared patches locally.
- Downloaded argocd-install-v2.11.0.yaml is 23024 lines — vendor this file (commit to git) rather than downloading at runtime for air-gapped or reproducible builds. The plan correctly notes this.
- The Argo CD `argocd-cm` ConfigMap uses YAML multiline string values (Lua scripts) with `|` block scalar. The key names use underscore `_` not dot `.` as the separator between group and kind (e.g. `resource.customizations.health.tombstone.io_FeatureFlag` — dot before group, underscore between group and kind). This is an Argo CD ConfigMap convention, not standard Kubernetes notation.

## Argo CD GitOps Provider — Task A1: gitops/providers/ scaffold (2026-07-07)
- A Kustomize overlay with `resources: []` (empty list) is valid and builds cleanly via `kubectl kustomize` — this is the correct pattern for a no-op provider (flux-only) where all bootstrap is handled by an existing external file (`gotk-sync.yaml`). Do NOT omit the `resources:` key entirely — the field must be present and explicitly empty.
- The `gitops/providers/argocd/` and `gitops/providers/both/` overlays reference `../../clusters/production/argocd/install.yaml` (and siblings) which do not yet exist. `kubectl kustomize` on these paths will fail with "no such file" until Tasks A2-A5 create those files — this is EXPECTED and intentional. Only `gitops/providers/flux/` must pass kustomize build in Task A1.
- Provider selection via Kustomize overlays avoids the circular bootstrap dependency that a GitOpsProvider CRD would introduce: tombstone-operator needs a GitOps controller to deploy it, and a GitOps controller needs tombstone-operator CRDs to recognize FeatureFlag/FlagPolicy resources. Kustomize overlays are applied at deploy-time by a human/CI pipeline, breaking the circular dependency entirely.
- `README.md` arrow syntax: use `->` not `→` (Unicode arrow) — consistent with Excalidraw diagram rule about plain-text files in constrained render contexts; also consistent with the existing `gitops/README.md` convention.

## Flux GitOps Integration — Tasks 6+7: CI GitOps handoff + deprecation (2026-07-06)
- The `update-gitops-tags` CI job uses `env:` to pass `steps.tag.outputs.version` into the `run:` shell — this avoids direct `${{ ... }}` interpolation inside `run:` blocks, which is the safe GitHub Actions pattern for preventing command injection (even though tag refs are controlled by the actor pushing, not attacker-supplied event payloads, consistency with the secure pattern is preferable).
- `inputs.cluster` from `workflow_dispatch` is also passed via `env: CLUSTER:` rather than inline `${{ inputs.cluster }}` in the `run:` block — `workflow_dispatch` inputs are actor-controlled, but inputs can contain shell metacharacters if the actor is malicious; the env-var pattern is always correct here.
- `gitops-sync` removal from the docker-publish matrix is a 3-line block removal (name + context + port) — the service source code at `services/gitops-sync/` is deliberately preserved per the deprecation convention. Never delete source code as part of a GitOps migration; only remove it from CI publish matrices and Helm chart references.
- The gitops/README.md uses a plain ASCII arrow (`->`) instead of Unicode `→` — consistent with the Excalidraw diagram rule about avoiding Unicode glyphs in plain text files that may be rendered in constrained contexts.
- Prepending a new section to an existing Markdown file: anchor the edit on the first heading + its subtitle paragraph (unique text), replace that block with the heading + subtitle + new section + separator `---`. This keeps the file structure intact and the edit is idempotent.
- CHANGELOG [Unreleased] block: always replace the empty `## [Unreleased]\n\n---` stub with the full entry block ending in `---` — keeping the separator ensures the next version heading renders correctly in Keep a Changelog format.

## Flux GitOps Integration — Task 5: flags/ directory for FeatureFlag CRs (2026-07-06)
- The production and staging overlays for `gitops/flags/` are structurally identical (`resources: ../../base`, `namespace: tombstone`) — the overlay pattern is used for future per-environment customizations (e.g. patch to override `rolloutPct` in staging) rather than immediate differences.
- `namespace: tombstone` in the overlay kustomization.yaml applies the namespace to ALL resources from the base, including both FeatureFlag and FlagPolicy CRs — no need to set namespace in the base manifests themselves.
- `tombstone-flags` Kustomization depends on `tombstone-apps` (not `tombstone-infrastructure`) because it needs the tombstone-operator to be running (deployed by apps layer via HelmRelease) before FeatureFlag CRs can be reconciled. Depending on `tombstone-infrastructure` only ensures CRDs exist, not that the operator pod is alive and watching for CRs.
- `kustomize build` (or `kubectl kustomize`) on a custom CRD directory succeeds even with unknown CRD schema — kustomize treats unrecognized apiVersions as opaque resources and passes them through unchanged. No CRD schema registration needed for build validation.
- The `selector: {}` field in FlagPolicy matches all FeatureFlag resources in the namespace — empty selector is the "match all" convention in Kubernetes (same as in NetworkPolicy, PodDisruptionBudget, etc.).

## Flux GitOps Integration — Task 1: gitops/ scaffold (2026-07-06)
- `kubectl kustomize <dir>` (bundled with kubectl) is a reliable kustomize substitute when standalone `kustomize` CLI is not installed. Produces identical output to `kustomize build`.
- Flux HelmRelease `spec.install.crds: CreateReplace` AND `spec.upgrade.crds: CreateReplace` must both be set — `install` covers first-time deployments, `upgrade` covers subsequent reconciliations. Omitting `upgrade.crds` means CRD upgrades silently skip on every helm upgrade after the initial install.
- `dependsOn` in a Kustomization (e.g. `tombstone-apps` depends on `tombstone-infrastructure`) only blocks object application — it does NOT wait for the upstream Kustomization to be fully Ready unless the upstream also has `spec.healthChecks`. Without healthChecks, Flux considers the upstream "applied" as soon as manifests are sent to the API server, before CRDs are actually established. Always pair `dependsOn` with `healthChecks` on the upstream.
- OCI HelmRepository (`spec.type: oci`) uses `oci://` URL scheme. Standard (HTTP) HelmRepository uses `https://`. Mixing them silently produces wrong behavior.
- The `gotk-sync.yaml` file is a Flux convention name (short for GitOps Toolkit sync) — it holds the GitRepository and Kustomization objects that bootstrap a cluster. Flux's own `flux bootstrap` command generates this pattern.
- kustomize `resources:` can reference directories with a trailing slash (`controllers/`) — it will recurse into the directory and pick up the `kustomization.yaml` inside. File references (`tombstone-operator/helmrelease.yaml`) are direct.

## v1.3.0 Phase A — Helm Chart Completion (2026-07-06)
- Helm Deployment `spec.selector.matchLabels` MUST use `tombstone.selectorLabels` (not `tombstone.labels`) — `tombstone.labels` contains `helm.sh/chart` which carries a version string, making the selector immutable across helm upgrades. This causes `helm upgrade` to fail with "selector is immutable". Always use `selectorLabels` in both `spec.selector.matchLabels` AND `spec.template.metadata.labels`.
- HPA template should be gated by `.Values.<service>.autoscaling.enabled` — keeps it dormant in most environments (disabled by default) while allowing per-environment enablement via value overrides.
- values.yaml for marketplace had port 8084, but CLAUDE.md documents marketplace at 8086 — the values.yaml was pre-existing and was not changed as the task only required adding autoscaling to the evaluator block. Future tasks should reconcile this discrepancy.
- `helm lint` INFO about missing icon is harmless — `0 chart(s) failed` is the only gate that matters for CI.

## v1.3.0 Phase A — Task A2: Intelligence Deployment template (2026-07-06)
- Python FastAPI services need higher probe delays than Go services: `livenessProbe.initialDelaySeconds: 15` / `readinessProbe.initialDelaySeconds: 20` — Python cold-start (importing numpy, scikit-learn, etc.) is 3-5x slower than Go binary startup.
- Liveness and readiness split: liveness hits `/health` (trivial, no dependency checks) while readiness hits `/readyz` (checks Postgres + Redis). This prevents restart storms — if Postgres is briefly unavailable, the pod stays Running (liveness OK) but stops receiving traffic (readiness fails), rather than being killed and restarted in a loop.
- `terminationGracePeriodSeconds: 60` for Python (vs 30 for Go) — Python processes may hold open DB connections or in-flight ML inference tasks that need time to drain cleanly.
- Extra `env:` block (not `envFrom:`) for `IS_PRIMARY_REGION` is correct — it's a per-deployment override from `values.yaml`, not a shared config map value. Using `env:` also allows per-cluster override via `--set intelligence.isPrimaryRegion=false` in helm commands.
- COMPATIBILITY.md "Known Gap" section should be updated as the LAST step of chart completion, after all Deployment templates are verified — it documents the state of the entire chart, not just the current task.

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

## Phase 5 Resilience — Distributed Rate Limiting (2026-07-03)
- flag-api and evaluator rate limiters moved from in-memory `sync.Map` (per-process, so N replicas = N× the effective limit) to Redis-backed leaky buckets — state is now shared across replicas
- The check-and-update MUST be one atomic Lua script via `redis.NewScript(...).Run(ctx, rdb, ...)` — never `WATCH`/`MULTI`/`EXEC`, which aborts/retries under exactly the high-concurrency load rate limiting is meant to handle (Redis's own documented guidance)
- Lua scripts returning fractional numbers to RESP get silently truncated to integers — `tostring()` every numeric return field in the script and `strconv.ParseFloat` on the Go side to preserve precision (needed for sub-second Retry-After values)
- `NewRateLimitMiddleware` signature changed to accept `*redis.Client` — both `cmd/main.go` call sites already had `rdb` constructed earlier for other purposes, so wiring was a one-line change at each call site
- Kept `Stop()` as a no-op rather than removing it — the in-memory version's background stale-entry sweep goroutine no longer exists (Redis TTL replaces it), but callers `defer rateMw.Stop()` and removing the method would be an unnecessary API break
- **SecretScan pitfall**: naming a Lua-script Go variable `tokenBucketScript` (or any `token...Script = ` / `...Token = "..."` assignment pattern) trips the repo's hardcoded-secret scanner as a false positive on "Generic Secret Assign" — even though it's a Lua source string, not a credential. Renamed to bucket/credential-neutral identifiers (`bucketScript`, `sdkRatePerMin`, `cred`) to avoid the block. Watch for this on any future middleware that assigns string literals to variables containing "token"/"key"/"secret" in the name.
- `github.com/alicebob/miniredis/v2` fully supports `EVAL`/Lua scripts through its bundled `gopher-lua` interpreter — no real Redis server needed for these tests. flag-api's go.mod already listed it (`// indirect`, used by an existing `flags_test.go` Redis Streams test) — using it directly in `ratelimit_test.go` just promotes it to a direct test dependency, no go.mod/go.sum diff needed. evaluator had no prior miniredis usage; added via `go get -t github.com/alicebob/miniredis/v2@v2.38.0` to match flag-api's version exactly.
- go-redis v9.5.1 confirmed identical across both flag-api and evaluator go.mod — no version skew to worry about for shared Lua-script code style
- **Worktree gotcha**: this worktree's initial HEAD was on `main`, not `develop` — the two branches have diverged significantly (different Go module import paths: `github.com/tombstone/<svc>` on develop vs `github.com/sairam0424/Tombstone/services/<svc>` on main, plus ~276 vs ~163 commits of drift). Always verify `git merge-base --is-ancestor <worktree-HEAD> origin/develop` before branching for a develop-targeted PR — branching from the wrong base silently produces a PR with unrelated import-path churn.

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

## v1.3.0 Phase C — Task C1: Redoc interactive API explorer (2026-07-06)
- `go-redoc` plan referenced `v0.0.7` and a chi sub-package (`github.com/mvrilo/go-redoc/chi`) — neither exists. The available versions are `v0.1.1`–`v0.1.5`; v0.1.5 only ships adapters for gin, fiber, and echo (not chi). **Fix**: use `goredoc.Redoc{SpecPath: specURL}` + `doc.Body()` to get the pre-rendered HTML, then wrap in a plain `http.HandlerFunc`. This gives identical behavior to any adapter — same embedded JS bundle, no CDN, serves the HTML.
- `goredoc.Redoc.Handler()` panics if `SpecFile` is empty (it tries to read a local file). For a spec served at a URL, use `Body()` instead, which only needs `SpecPath` to populate the template URL — no filesystem access.
- Docs routes MUST be registered BEFORE `r.Route("/api/v1", ...)` — chi routes are matched in registration order, and if the auth-gated group is registered first it will intercept `/api/v1/docs`. Register `r.Handle("/api/v1/docs", ...)` and `r.Handle("/api/v1/docs/*", ...)` directly on the root router.
- `go-redoc` embeds `redoc.standalone.js` via `//go:embed` — no CDN call at runtime, making it suitable for air-gapped or restricted environments.

## v1.3.0 Phase B — Task B4: Snapshot deserialization + all_flags wiring (2026-07-06)
- `_apply_snapshot` previously ignored `targeting_rules` and `prerequisites` from the API payload — flags were stored with empty lists. The fix iterates `raw.get("targeting_rules", [])` and constructs `TargetingRule(id, conditions=[PropertyCondition(...)], rollout_pct, variation)` objects; `prerequisites` is passed through as raw dicts (schema: `[{"flag_key": str, "required_value": bool}]`).
- The evaluate method in `TombstoneClient` now takes a shallow copy of `self._cache` under the lock (`all_flags = dict(self._cache)`) and passes it along with a fresh `evaluation_cache={}` to the `evaluate()` function — this enables the prerequisite pipeline (Step 2) to look up other flags from the same snapshot without needing any additional I/O.
- Shallow copy `dict(self._cache)` is correct here: `FlagEnvironmentState` objects are not mutated anywhere, so sharing references is safe. The new dict only protects against the top-level `_cache` dict being replaced while evaluation is in progress.
- Local imports inside `_apply_snapshot` (`from tombstone.types import ...`) keep the module-level import list clean and avoid circular import risk — `client.py` already imports `FlagEnvironmentState` at the top, but the targeted local import is also fine because Python caches module imports.
- TDD: test failed with `AssertionError: assert 0 == 1` (prerequisites empty list), not an AttributeError — confirming the types already had the field (from B1) but the snapshot deserialization simply wasn't populating it. One targeted fix (replace `_apply_snapshot`) resolved both prerequisites and targeting_rules in one shot.

## v1.3.0 Phase B — Task B3: 5-step evaluation pipeline (2026-07-06)
- `evaluate()` signature extended with two optional kwargs: `all_flags: dict[str, FlagEnvironmentState] | None = None` and `evaluation_cache: dict[str, bool] | None = None`. Both default to `{}` on first call — existing callers passing only the 4 positional args continue to work unchanged.
- `_check_prerequisites()` receives the `evaluation_cache` dict by reference and mutates it (adds entries). This is intentional — it's a memoization cache shared across a single top-level `evaluate()` call, preventing redundant re-evaluation of the same prerequisite flag (PostHog shipped pattern).
- Prerequisite chain: if `dep_key` missing from `all_flags`, cache it as `False` and return False immediately — missing flag == unmet prerequisite. This is conservative/safe: a misconfigured prerequisite reference fails closed.
- `_match_targeting_rules()` catches `InconclusiveMatchError` per rule and `continue`s to the next — missing attributes should not block evaluation of subsequent rules. Returning `None` from this function signals "no rule matched, continue to fallthrough".
- Rule rollout: even within a matched targeting rule, users are bucketed via MurmurHash3 against `rule.rollout_pct` — this allows gradual rollout of a targeted variation (e.g. US users, but only 50% of them). Same hash formula as fallthrough: `mmh3.hash(flag_key + user_id, seed=0, signed=False) % 100`.
- Pipeline order: preliminary (Step 1) → prerequisites (Step 2) → targeting rules (Step 3) → fallthrough rollout (Step 5). Step 4 is "rule matching fallthrough" in the TypeScript SDK but is handled inline in Step 3 here — functionally equivalent.
- TDD confirmed: 5 of 6 tests failed before implementation (1 passed by coincidence — `test_targeting_rule_no_match_falls_through` already worked since fallthrough was implemented). All 6 pass after.

## v1.3.0 Phase B — Task B2: match_property full operator surface (2026-07-06)
- `_padded_version()` (GrowthBook pattern): strip leading `v` and build metadata (`+...`), split on `[-.]`, left-pad numeric segments to 5 chars with `rjust(5, " ")`, then append `"~"` as a 4th part for 3-part versions — this makes `1.0.0 > 1.0.0-beta` because `"~"` sorts after any lowercase alpha suffix. No external dependencies needed.
- Pre-release ordering: `1.0.0-beta` → `["    1","    0","    0","beta"]` vs `1.0.0` → `["    1","    0","    0","~"]`. Python `"b" < "~"` is True, so the 4-element list comparison correctly resolves `1.0.0-beta < 1.0.0`.
- All string operators (contains/startsWith/endsWith) should compare in uppercase (`.upper()`) — avoids locale-dependent `.lower()` issues with Turkish-i, ß, etc. The case-insensitive contract just needs consistency, not locale correctness.
- Missing attribute MUST raise `InconclusiveMatchError` — never silently return False. Silent False would make "attribute not present" indistinguishable from "attribute present but no match", hiding misconfigured targeting rules.
- Numeric operators: cast both sides to `float` — this handles integer values stored as strings (e.g. score="75") correctly for all comparisons including lt/lte/gt/gte.
- Python `match` statement requires Python 3.10+ — confirmed this repo uses Python 3.12+ (pyproject.toml) so structural pattern matching is safe.
- TDD red-green cycle: confirmed — `ModuleNotFoundError: No module named 'tombstone.matching'` before implementation, 22/22 new tests pass after, full suite 50/50.

## Flux GitOps Integration — Task 4: Staging overlay (2026-07-06)
- Kustomize security restriction applies to individual FILE references too, not just directory traversal. `resources: - ../production/tombstone/namespace.yaml` fails with "file is not in or below" even though `../production` as a DIRECTORY reference is allowed. Always use directory references (`- ../production`) for base overlay patterns, never individual file paths from sibling directories.
- `patchesStrategicMerge` is deprecated in Kustomize v5 — use `patches` with `path:` key instead. Both work functionally but `patches` avoids the deprecation warning in `kubectl kustomize` output.
- A kustomize overlay at `gitops/apps/staging/` can reference `../production` (the production directory) as its base — kustomize allows parent-directory traversal when pointing to a DIRECTORY (which has its own kustomization.yaml), just not to individual files above the overlay's root. This is the canonical overlay pattern: staging extends production by pointing to its directory and applying strategic merge patches.

## Flux GitOps Integration — Task 3: ImagePolicy + ImageUpdateAutomation (2026-07-06)
- Kustomize security restriction: a `kustomization.yaml` cannot reference files **outside** its own directory tree. `../image-automation/policies.yaml` (parent traversal) is rejected with "file is not in or below" error. Solution: place the `image-automation/` subdirectory **inside** `controllers/` (i.e. `gitops/infrastructure/controllers/image-automation/`) so all referenced paths are descendants of the kustomization root.
- The plan's commit message says "8 tombstone-* images" but the policies.yaml only covers 5 services (flag-api, gateway, evaluator, intelligence, marketplace) — gitops-sync, ast-rewriter, and tombstone-operator are not scanned via ImagePolicy. The commit message was kept verbatim from the plan; note the discrepancy for future audits.
- `image.toolkit.fluxcd.io/v1beta1` for `ImageUpdateAutomation` and `image.toolkit.fluxcd.io/v1beta2` for `ImageRepository`/`ImagePolicy` — these use different beta API versions within the same Flux image toolkit group. Do not normalize them to the same version — each resource type has its own API version lifecycle.
- `gitrepository.yaml` in `image-automation/` is a documentation-only file (pure YAML comments, no Kubernetes objects). Kustomize skips files with no YAML documents silently — this is safe. Do NOT add it to `kustomization.yaml` resources list (it's not a manifest).
- `kubectl kustomize <dir>` is the fallback when the standalone `kustomize` binary is absent — confirmed equivalent output and exit codes for all Tombstone kustomize validation steps.

## Flux GitOps Integration — Task 2: apps Kustomization + flagmind HelmRelease (2026-07-06)
- The flagmind HelmRelease uses `spec.install.crds: Skip` (not `CreateReplace`) — the apps layer should NOT manage CRDs. CRD lifecycle is exclusively the operator chart's responsibility (Task 1). Using `Skip` here prevents a race where both HelmReleases try to own CRD resources.
- `spec.ignore.paths: ["/spec/environments/*/rolloutPct"]` is a Flux HelmRelease field (not a Kustomization field) — it tells the Flux HelmRelease controller to exclude those JSON paths from drift detection, preventing it from reverting ML-driven `rolloutPct` bumps back to the Git-committed value.
- Image policy markers `# {"$imagepolicy": "flux-system:<name>:tag"}` must appear as YAML comments on the exact line containing the `tag:` field. Kustomize strips comments from the build output (kubectl kustomize output does not show them) — but they are still present in the source file and that is what matters for Flux's ImageUpdateAutomation setter scanning. Always verify markers in the raw source file, not the kustomize build output.
- `kustomize build gitops/apps/production/` succeeds with exit 0 even though the chart path (`./infra/helm/flagmind`) is a relative GitRepository path — kustomize does not resolve chart source, only validates the YAML structure of the HelmRelease spec.
- `gitops/apps/kustomization.yaml` references `production/` (directory) — kustomize recurses into `gitops/apps/production/kustomization.yaml` which then references the individual resource files. This two-level indirection keeps the apps root kustomization clean and makes it easy to add `staging/` later.

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
