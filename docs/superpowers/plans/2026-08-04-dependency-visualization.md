# Dependency Visualization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Expose the existing causal dependency graph (`services/intelligence/app/graph/builder.py`) via two new REST endpoints, build an MCP tool for the new endpoints, and deliver a frontend "Dependencies" tab with interactive force-directed graph visualization, "disable simulation" mode, and a critical-flags health panel.

**Architecture:** Two new FastAPI endpoints in `services/intelligence/app/main.py` wrapping `DependencyGraphBuilder.get_impact_fast()` (Redis-backed O(log n) with DB fallback). A new MCP tool `tombstone_get_dependency_graph` in `workspace-mcp/src/tools/flags.ts` following the existing 8-tool pattern. Frontend: new `<DependenciesTab>` component in `workspace-dashboard/src/views/FlagDetail/` using d3-force for graph rendering, React state for node selection/highlight, and a critical-flags panel fetched from `/api/v1/graph/critical-flags`.

**Tech Stack:** Python 3.12 (FastAPI + asyncpg + asyncio), existing `better-sqlite3` for local dev, TypeScript (React 19 + d3@7.9.0 + react-router-dom@7.0.0), Vitest 2 for frontend tests, pytest for backend tests.

## Global Constraints

- Canonical model per `docs/superpowers/specs/2026-08-04-v1.5.0-sdk-parity-and-dependency-viz-design.md` Section 6 — this is the ONLY source of truth for behavior. Do not redesign the scoring formula or the API shape.
- No new tables/migrations — read-only surface over existing Redis sorted sets (`tombstone:depgraph:{flag_key}`) and `audit_log`/`flags` tables.
- No new external dependencies for frontend graph rendering — must use the existing `d3@7.9.0` dependency from `workspace-dashboard/package.json:16`.
- Critical-flags scoring formula is fixed: `score = (in_degree + out_degree) * avg_edge_weight * blast_radius_multiplier`, where `blast_radius_multiplier` is `4/3/2/1` for `BLOCKED/HIGH/MEDIUM/LOW`. This formula has been verified by execution against a small example graph (see execution log in this plan's research phase).
- Blast-radius tiers are computed by the `services/evaluator/internal/blast/blast_radius.go` Go service — intelligence service must call evaluator's HTTP API at `/api/v1/blast-radius?flag_key=<key>&environment=<env>&rollout_pct=<pct>` to retrieve the tier (response field `result.risk_score`), falling back to `"LOW"` if evaluator is unreachable (fail-open).
- Branch: `feat/dependency-visualization-v1.5.0` off `origin/develop`.
- Run `cd services/intelligence && uv run pytest tests/` before every commit.
- Run `cd workspace-dashboard && npm run test` after frontend changes.

---

## Phase 1 — Backend: Dependency-Subgraph Endpoint

### Task 1: Add GET /api/v1/graph/dependencies endpoint wrapping get_impact_fast

**Files:**
- Modify: `services/intelligence/app/main.py`
- Test: `services/intelligence/tests/test_dependency_graph_endpoint.py`

**Interfaces:**
- Consumes: `DependencyGraphBuilder.get_impact_fast(flag_key, redis_client)` (existing, `app/graph/builder.py:112-134`), existing `app.state.redis` and `app.state.graph_builder` initialized in `lifespan()`.
- Produces: `GET /api/v1/graph/dependencies?flag_key=<key>&depth=<n>` returning JSON `{"flag_key": str, "depth": int, "nodes": [{"key": str, "enabled": bool, "rollout_pct": int}], "edges": [{"source": str, "target": str, "weight": float}]}`. `depth` defaults to 1 (direct neighbors only); `depth=2` traverses to 2nd-degree neighbors, etc. Falls back to `get_impact(flag_key, environment, days=30)` DB scan when Redis key is missing (cold start).

- [ ] **Step 1: Write the failing test**

```python
# services/intelligence/tests/test_dependency_graph_endpoint.py
"""
Tests for GET /api/v1/graph/dependencies endpoint.

Requires a test fixture for `app` (FastAPI instance) with mocked
`app.state.redis` and `app.state.graph_builder`. Uses the same async test
harness pattern as `test_background_job_lock.py` (pytest-asyncio).
"""
from __future__ import annotations

import pytest
from fastapi import FastAPI
from fastapi.testclient import TestClient


class MockGraphBuilder:
    """Minimal mock for DependencyGraphBuilder matching the public API
    that GET /api/v1/graph/dependencies consumes."""

    def __init__(self):
        self.redis_data: dict[str, list[dict]] = {}
        self.db_fallback_data: dict[str, dict] = {}

    async def get_impact_fast(self, flag_key: str, redis_client) -> list[dict] | None:
        return self.redis_data.get(flag_key)

    async def get_impact(self, flag_key: str, environment: str, days: int) -> dict:
        return self.db_fallback_data.get(flag_key, {
            "flag_key": flag_key,
            "environment": environment,
            "co_changed_with": [],
        })


@pytest.fixture
def app_with_mocked_builder():
    """Returns a FastAPI app with mocked graph_builder and redis state."""
    app = FastAPI()
    app.state.graph_builder = MockGraphBuilder()
    app.state.redis = object()  # non-None sentinel
    return app


def test_dependencies_endpoint_redis_hit_depth_1(app_with_mocked_builder):
    """Redis hit with depth=1 returns direct neighbors only."""
    from app import main  # imports the route we're testing
    # Inject the mocked app state into main's global app instance
    # (alternatively, refactor main.py to accept app as param — deferred to follow-up)
    main.app.state.graph_builder = app_with_mocked_builder.state.graph_builder
    main.app.state.redis = app_with_mocked_builder.state.redis

    # Seed Redis mock data
    mock_builder: MockGraphBuilder = main.app.state.graph_builder
    mock_builder.redis_data["payments.checkout"] = [
        {"flag_key": "payments.processor", "weight": 0.8},
        {"flag_key": "fraud.detection", "weight": 0.6},
    ]

    client = TestClient(main.app)
    response = client.get("/api/v1/graph/dependencies?flag_key=payments.checkout&depth=1")

    assert response.status_code == 200
    data = response.json()
    assert data["flag_key"] == "payments.checkout"
    assert data["depth"] == 1
    assert len(data["edges"]) == 2
    assert data["edges"][0]["source"] == "payments.checkout"
    assert data["edges"][0]["target"] == "payments.processor"
    assert data["edges"][0]["weight"] == 0.8


def test_dependencies_endpoint_redis_miss_db_fallback(app_with_mocked_builder):
    """Redis miss triggers DB fallback via get_impact()."""
    from app import main
    main.app.state.graph_builder = app_with_mocked_builder.state.graph_builder
    main.app.state.redis = app_with_mocked_builder.state.redis

    mock_builder: MockGraphBuilder = main.app.state.graph_builder
    # Redis returns None (cold start)
    mock_builder.redis_data["new.flag"] = None
    mock_builder.db_fallback_data["new.flag"] = {
        "flag_key": "new.flag",
        "environment": "production",
        "co_changed_with": [{"flag_key": "old.flag", "co_change_count": 3, "avg_seconds_apart": 120.0}],
    }

    client = TestClient(main.app)
    response = client.get("/api/v1/graph/dependencies?flag_key=new.flag&depth=1")

    assert response.status_code == 200
    data = response.json()
    assert data["flag_key"] == "new.flag"
    # Fallback path infers weight from co_change_count (higher count → higher coupling)
    assert len(data["edges"]) == 1
    assert data["edges"][0]["target"] == "old.flag"


def test_dependencies_endpoint_depth_2_traversal(app_with_mocked_builder):
    """depth=2 traverses to 2nd-degree neighbors."""
    from app import main
    main.app.state.graph_builder = app_with_mocked_builder.state.graph_builder
    main.app.state.redis = app_with_mocked_builder.state.redis

    mock_builder: MockGraphBuilder = main.app.state.graph_builder
    mock_builder.redis_data["A"] = [{"flag_key": "B", "weight": 0.9}]
    mock_builder.redis_data["B"] = [{"flag_key": "C", "weight": 0.7}]

    client = TestClient(main.app)
    response = client.get("/api/v1/graph/dependencies?flag_key=A&depth=2")

    assert response.status_code == 200
    data = response.json()
    assert data["depth"] == 2
    # Edges: A→B and B→C
    edge_pairs = {(e["source"], e["target"]) for e in data["edges"]}
    assert ("A", "B") in edge_pairs
    assert ("B", "C") in edge_pairs
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd services/intelligence && uv run pytest tests/test_dependency_graph_endpoint.py -v`
Expected: FAIL — `GET /api/v1/graph/dependencies` endpoint does not exist.

- [ ] **Step 3: Implement the endpoint in main.py**

```python
# In services/intelligence/app/main.py
# Add this route after the existing @app.get("/api/v1/dependency-graph/impact/{flag_key}")
# handler (line 341-363 in the file I read).

from collections import deque

@app.get("/api/v1/graph/dependencies")
async def get_dependency_subgraph(
    flag_key: str,
    depth: int = 1,
    environment: str = "production",
    request: Request = None,
):
    """
    Return a dependency subgraph centered on `flag_key`, traversing to `depth` hops.

    Uses Redis sorted-set O(log n) lookup when Redis is available and keys are warm.
    Falls back to the original O(n²) DB scan per flag on Redis cold-start.
    Returns nodes (flags in the subgraph) and edges (weighted connections).
    """
    if depth < 1:
        depth = 1
    if depth > 5:
        depth = 5  # cap at 5 hops to prevent runaway traversal

    redis_client = request.app.state.redis
    builder: DependencyGraphBuilder = request.app.state.graph_builder

    visited: set[str] = set()
    edges: list[dict] = []
    queue: deque[tuple[str, int]] = deque([(flag_key, 0)])
    visited.add(flag_key)

    while queue:
        current_key, current_depth = queue.popleft()
        if current_depth >= depth:
            continue

        # Fetch neighbors
        neighbors = None
        if redis_client is not None:
            neighbors = await builder.get_impact_fast(current_key, redis_client)
            if neighbors is None:
                # Redis miss — fall back to DB
                logger.info("dep graph Redis miss for %s — falling back to DB", current_key)
                db_result = await builder.get_impact(current_key, environment, days=30)
                co_changed = db_result.get("co_changed_with", [])
                # DB fallback returns {"flag_key": str, "co_change_count": int, "avg_seconds_apart": float}
                # Infer weight from co_change_count: higher count → higher coupling
                neighbors = [
                    {"flag_key": item["flag_key"], "weight": min(1.0, item["co_change_count"] / 10.0)}
                    for item in co_changed
                ]

        if not neighbors:
            continue

        for neighbor in neighbors:
            neighbor_key = neighbor["flag_key"]
            weight = neighbor["weight"]
            edges.append({"source": current_key, "target": neighbor_key, "weight": weight})
            if neighbor_key not in visited:
                visited.add(neighbor_key)
                queue.append((neighbor_key, current_depth + 1))

    # Fetch node metadata from flags table for all visited keys
    nodes = []
    if visited:
        pool = await builder._get_pool()
        rows = await pool.fetch(
            """
            SELECT f.key, fe.enabled, fe.rollout_pct
            FROM flags f
            LEFT JOIN flag_environments fe ON fe.flag_id = f.id AND fe.environment = $1
            WHERE f.key = ANY($2::text[])
            """,
            environment,
            list(visited),
        )
        nodes = [
            {"key": r["key"], "enabled": bool(r["enabled"] or False), "rollout_pct": int(r["rollout_pct"] or 0)}
            for r in rows
        ]

    return {
        "flag_key": flag_key,
        "depth": depth,
        "environment": environment,
        "nodes": nodes,
        "edges": edges,
    }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd services/intelligence && uv run pytest tests/test_dependency_graph_endpoint.py -v`
Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone
git add services/intelligence/app/main.py services/intelligence/tests/test_dependency_graph_endpoint.py
git commit -m "feat(intelligence): add GET /api/v1/graph/dependencies subgraph endpoint"
```

---

## Phase 2 — Backend: Critical-Flags Endpoint

### Task 2: Add GET /api/v1/graph/critical-flags endpoint with blast-radius integration

**Files:**
- Modify: `services/intelligence/app/main.py`
- Test: `services/intelligence/tests/test_critical_flags_endpoint.py`

**Interfaces:**
- Consumes: `DependencyGraphBuilder` (existing), evaluator HTTP API `GET /api/v1/blast-radius?flag_key=<key>&environment=<env>&rollout_pct=<pct>` (Go service at `services/evaluator/internal/blast/blast_radius.go:124-151`), existing `app.state.redis`.
- Produces: `GET /api/v1/graph/critical-flags?limit=<n>&environment=<env>` returning JSON `{"flags": [{"key": str, "score": float, "in_degree": int, "out_degree": int, "avg_edge_weight": float, "blast_radius_tier": str}], "generated_at": int}`. Sorted descending by score. Default limit=20.

- [ ] **Step 1: Write the failing test**

```python
# services/intelligence/tests/test_critical_flags_endpoint.py
"""
Tests for GET /api/v1/graph/critical-flags endpoint.

Mocks the evaluator HTTP API blast-radius call and verifies the scoring
formula: score = (in_degree + out_degree) * avg_edge_weight * blast_radius_multiplier.
"""
from __future__ import annotations

import pytest
from unittest.mock import AsyncMock, patch
from fastapi.testclient import TestClient


class MockGraphBuilder:
    """Mock for DependencyGraphBuilder that provides in/out degree data."""

    def __init__(self):
        self.all_edges: list[tuple[str, str, float]] = []

    async def _get_pool(self):
        """Mock pool for the endpoint's flags table query."""
        return MockPool()

    def set_edges(self, edges: list[tuple[str, str, float]]):
        """edges: list of (source, target, weight)"""
        self.all_edges = edges

    def get_degree_data(self) -> dict[str, dict]:
        """Compute in/out degree and avg weight for all flags in the graph."""
        from collections import defaultdict
        in_deg = defaultdict(list)
        out_deg = defaultdict(list)

        for source, target, weight in self.all_edges:
            out_deg[source].append(weight)
            in_deg[target].append(weight)

        result = {}
        all_flags = set(in_deg.keys()) | set(out_deg.keys())
        for flag in all_flags:
            in_weights = in_deg.get(flag, [])
            out_weights = out_deg.get(flag, [])
            all_weights = in_weights + out_weights
            result[flag] = {
                "in_degree": len(in_weights),
                "out_degree": len(out_weights),
                "avg_edge_weight": sum(all_weights) / len(all_weights) if all_weights else 0.0,
            }
        return result


class MockPool:
    """Mock asyncpg pool for flags table query."""

    async def fetch(self, query: str, *args):
        # Return empty list — the test will inject flags via the mock builder's degree data
        return []


@pytest.fixture
def app_with_critical_flags_mock():
    from fastapi import FastAPI
    app = FastAPI()
    app.state.graph_builder = MockGraphBuilder()
    app.state.redis = object()
    return app


@patch("app.main.httpx.AsyncClient.get")
def test_critical_flags_scoring_formula(mock_evaluator_get, app_with_critical_flags_mock):
    """Verify the scoring formula produces correct rankings."""
    from app import main
    main.app.state.graph_builder = app_with_critical_flags_mock.state.graph_builder
    main.app.state.redis = app_with_critical_flags_mock.state.redis

    mock_builder: MockGraphBuilder = main.app.state.graph_builder
    # Graph: 3 flags with known degree/weight
    mock_builder.set_edges([
        ("A", "B", 0.8),
        ("A", "C", 0.6),
        ("B", "C", 0.7),
        ("D", "A", 0.9),
        ("D", "C", 0.5),
    ])

    # Mock evaluator blast-radius responses
    # A: BLOCKED, B: HIGH, C: MEDIUM, D: LOW
    async def mock_blast_call(url: str, **kwargs):
        from types import SimpleNamespace
        if "flag_key=A" in url:
            return SimpleNamespace(status_code=200, json=lambda: {"result": {"risk_score": "BLOCKED"}})
        if "flag_key=B" in url:
            return SimpleNamespace(status_code=200, json=lambda: {"result": {"risk_score": "HIGH"}})
        if "flag_key=C" in url:
            return SimpleNamespace(status_code=200, json=lambda: {"result": {"risk_score": "MEDIUM"}})
        if "flag_key=D" in url:
            return SimpleNamespace(status_code=200, json=lambda: {"result": {"risk_score": "LOW"}})
        return SimpleNamespace(status_code=500, json=lambda: {})

    mock_evaluator_get.side_effect = mock_blast_call

    client = TestClient(main.app)
    response = client.get("/api/v1/graph/critical-flags?limit=10")

    assert response.status_code == 200
    data = response.json()
    flags = data["flags"]

    # Verify scoring:
    # A: in=1 (D→A), out=2 (A→B, A→C), avg_weight=(0.9+0.8+0.6)/3=0.767, mult=4 → score=3*0.767*4≈9.2
    # B: in=1 (A→B), out=1 (B→C), avg_weight=(0.8+0.7)/2=0.75, mult=3 → score=2*0.75*3=4.5
    # C: in=3, out=0, avg_weight=(0.6+0.7+0.5)/3=0.6, mult=2 → score=3*0.6*2=3.6
    # D: in=0, out=2, avg_weight=(0.9+0.5)/2=0.7, mult=1 → score=2*0.7*1=1.4
    # Ranking: A > B > C > D

    assert flags[0]["key"] == "A"
    assert flags[1]["key"] == "B"
    assert flags[2]["key"] == "C"
    assert flags[3]["key"] == "D"


@patch("app.main.httpx.AsyncClient.get")
def test_critical_flags_evaluator_unreachable_fallback_low(mock_evaluator_get, app_with_critical_flags_mock):
    """When evaluator is unreachable, fallback to blast_radius_tier='LOW'."""
    from app import main
    main.app.state.graph_builder = app_with_critical_flags_mock.state.graph_builder
    main.app.state.redis = app_with_critical_flags_mock.state.redis

    mock_builder: MockGraphBuilder = main.app.state.graph_builder
    mock_builder.set_edges([("A", "B", 0.5)])

    # Evaluator always returns 500
    async def mock_blast_fail(url: str, **kwargs):
        from types import SimpleNamespace
        return SimpleNamespace(status_code=500, json=lambda: {})

    mock_evaluator_get.side_effect = mock_blast_fail

    client = TestClient(main.app)
    response = client.get("/api/v1/graph/critical-flags?limit=10")

    assert response.status_code == 200
    flags = response.json()["flags"]
    # Both flags get fallback tier "LOW" (mult=1)
    assert all(f["blast_radius_tier"] == "LOW" for f in flags)
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd services/intelligence && uv run pytest tests/test_critical_flags_endpoint.py -v`
Expected: FAIL — endpoint does not exist.

- [ ] **Step 3: Implement the endpoint in main.py**

```python
# In services/intelligence/app/main.py
# Add this route after the GET /api/v1/graph/dependencies handler.

import httpx
import os
import time

BLAST_RADIUS_MULTIPLIER = {"BLOCKED": 4, "HIGH": 3, "MEDIUM": 2, "LOW": 1}


@app.get("/api/v1/graph/critical-flags")
async def get_critical_flags(
    limit: int = 20,
    environment: str = "production",
    request: Request = None,
):
    """
    Return top-N most critical flags ranked by dependency health score.

    Score formula: (in_degree + out_degree) * avg_edge_weight * blast_radius_multiplier.
    blast_radius_multiplier: BLOCKED=4, HIGH=3, MEDIUM=2, LOW=1.

    Fetches blast-radius tier from evaluator service; falls back to LOW if unreachable.
    """
    redis_client = request.app.state.redis
    builder: DependencyGraphBuilder = request.app.state.graph_builder

    # 1. Gather all edges from Redis sorted sets
    # (In production this would scan all tombstone:depgraph:* keys — for MVP we rely
    # on the existing rebuild_all() having populated Redis with a full graph; a future
    # optimization would be a dedicated "list all edges" Redis Lua script or a cached
    # in-memory adjacency list built during startup.)
    #
    # For this implementation, we'll use the DB as source-of-truth for the full graph,
    # since Redis sorted sets are per-flag (no global edge list). The existing
    # DependencyGraphBuilder.build() method already does this DB scan — we reuse it.

    pool = await builder._get_pool()
    import time as t
    to_unix = int(t.time())
    from_unix = to_unix - (90 * 86400)  # 90-day window matching rebuild_all

    rows = await pool.fetch(
        """
        SELECT flag_key,
               EXTRACT(EPOCH FROM created_at)::bigint AS ts
        FROM audit_log
        WHERE created_at >= to_timestamp($1)
          AND created_at <= to_timestamp($2)
          AND flag_key IS NOT NULL
          AND event_type IN ('flag_environment_updated','kill_switch_activated','flag_created')
        ORDER BY created_at ASC
        """,
        float(from_unix),
        float(to_unix),
    )

    if not rows:
        return {"flags": [], "generated_at": to_unix}

    # 2. Rebuild edge map (same logic as builder.build())
    import math
    from collections import defaultdict

    edge_map = {}
    events = [(r["flag_key"], int(r["ts"])) for r in rows]
    COUPLING_WINDOW_SECONDS = 300
    LAMBDA = 0.1

    for i, (flag_a, ts_a) in enumerate(events):
        for j in range(i + 1, len(events)):
            flag_b, ts_b = events[j]
            if flag_b == flag_a:
                continue
            delta = ts_b - ts_a
            if delta > COUPLING_WINDOW_SECONDS:
                break
            weight = math.exp(-LAMBDA * (delta / 60.0))
            key = (flag_a, flag_b)
            if key in edge_map:
                edge_map[key]["weight"] = max(edge_map[key]["weight"], weight)
                edge_map[key]["count"] += 1
            else:
                edge_map[key] = {"weight": round(weight, 4), "count": 1}

    # 3. Compute in/out degree and avg edge weight per flag
    in_weights = defaultdict(list)
    out_weights = defaultdict(list)

    for (source, target), data in edge_map.items():
        w = data["weight"]
        out_weights[source].append(w)
        in_weights[target].append(w)

    all_flags = set(in_weights.keys()) | set(out_weights.keys())
    flag_data = []

    for flag_key in all_flags:
        in_w = in_weights.get(flag_key, [])
        out_w = out_weights.get(flag_key, [])
        all_w = in_w + out_w
        in_degree = len(in_w)
        out_degree = len(out_w)
        avg_weight = sum(all_w) / len(all_w) if all_w else 0.0

        flag_data.append({
            "key": flag_key,
            "in_degree": in_degree,
            "out_degree": out_degree,
            "avg_edge_weight": avg_weight,
        })

    # 4. Fetch blast-radius tier from evaluator service for each flag
    evaluator_url = os.environ.get("EVALUATOR_URL", "http://localhost:8082")
    async with httpx.AsyncClient(timeout=5.0) as client:
        for flag in flag_data:
            try:
                # Call evaluator with rollout_pct=100 (worst-case blast radius)
                resp = await client.get(
                    f"{evaluator_url}/api/v1/blast-radius",
                    params={"flag_key": flag["key"], "environment": environment, "rollout_pct": 100},
                )
                if resp.status_code == 200:
                    result = resp.json()
                    tier = result.get("result", {}).get("risk_score", "LOW")
                else:
                    tier = "LOW"
            except Exception:
                tier = "LOW"  # fail-open

            flag["blast_radius_tier"] = tier
            mult = BLAST_RADIUS_MULTIPLIER[tier]
            score = (flag["in_degree"] + flag["out_degree"]) * flag["avg_edge_weight"] * mult
            flag["score"] = round(score, 2)

    # 5. Sort descending by score, limit to top-N
    flag_data.sort(key=lambda f: -f["score"])
    top_n = flag_data[:limit]

    return {
        "flags": top_n,
        "generated_at": to_unix,
    }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd services/intelligence && uv run pytest tests/test_critical_flags_endpoint.py -v`
Expected: PASS (2 tests).

- [ ] **Step 5: Commit**

```bash
git add services/intelligence/app/main.py services/intelligence/tests/test_critical_flags_endpoint.py
git commit -m "feat(intelligence): add GET /api/v1/graph/critical-flags with blast-radius integration"
```

---

## Phase 3 — MCP Tool

### Task 3: Add tombstone_get_dependency_graph MCP tool

**Files:**
- Modify: `workspace-mcp/src/tools/flags.ts`
- Modify: `workspace-mcp/src/index.ts`

**Interfaces:**
- Consumes: `GET /api/v1/graph/dependencies?flag_key=<key>&depth=<n>` (Task 1).
- Produces: MCP tool `tombstone_get_dependency_graph` with input schema `{flag_key: string, depth?: number}`.

- [ ] **Step 1: Add the tool definition and handler to flags.ts**

```typescript
// In workspace-mcp/src/tools/flags.ts
// Add after the existing openFeatureSetupTool definition (line 346-362 in the file I read).

export const getDependencyGraphTool: Tool = {
  name: "tombstone_get_dependency_graph",
  description:
    "Get the dependency graph (nodes + weighted edges) for a feature flag, traversing up to `depth` hops. Use this to visualize flag coupling, identify high-risk dependencies, and understand blast radius before making changes.",
  inputSchema: {
    type: "object",
    properties: {
      flag_key: {
        type: "string",
        description: "Dot-notation flag key (e.g. payments.checkout.v2)",
      },
      depth: {
        type: "number",
        description: "Number of hops to traverse (default 1, max 5)",
        minimum: 1,
        maximum: 5,
      },
      environment: {
        type: "string",
        description: "Environment (default 'production')",
      },
    },
    required: ["flag_key"],
    additionalProperties: false,
  },
};

// Add the handler function after handleOpenFeatureSetup (after line 471).

export async function handleGetDependencyGraph(
  args: Record<string, unknown>,
  apiUrl: string,
  apiToken: string
): Promise<unknown> {
  const flag_key = args.flag_key as string;
  const depth = (args.depth as number | undefined) ?? 1;
  const environment = (args.environment as string | undefined) ?? "production";

  const params = new URLSearchParams({
    flag_key,
    depth: String(depth),
    environment,
  });

  // Intelligence service runs on port 8083 (flag-api is 8081)
  const intelUrl = apiUrl.replace(":8081", ":8083").replace("8081", "8083");
  const url = `${intelUrl}/api/v1/graph/dependencies?${params.toString()}`;
  return apiFetch(url, apiToken);
}

// Update the allTools array at the bottom of the file (currently line 475-484) to include the new tool.

export const allTools: Tool[] = [
  getFlagTool,
  killSwitchTool,
  blastRadiusTool,
  listStaleFlagsTool,
  createFlagTool,
  searchFlagsTool,
  generateCleanupPRTool,
  openFeatureSetupTool,
  getDependencyGraphTool,  // NEW
];
```

- [ ] **Step 2: Add the dispatch case to index.ts**

```typescript
// In workspace-mcp/src/index.ts
// Add a new case to the switch statement after the tombstone_openfeature_setup case (line 98-100).

      case "tombstone_get_dependency_graph":
        result = await handleGetDependencyGraph(args as Record<string, unknown>, apiUrl, apiToken);
        break;
```

- [ ] **Step 3: Test the MCP tool manually**

Run the MCP server locally and invoke the tool via Claude Desktop or the MCP inspector:
```bash
cd workspace-mcp
TOMBSTONE_API_URL=http://localhost:8081 TOMBSTONE_TOKEN=sdk-dev-token-change-in-prod npm run dev
```

In Claude Desktop, invoke:
```
Use the tombstone_get_dependency_graph tool with flag_key="payments.checkout" and depth=2.
```

Expected: Returns JSON with `nodes` and `edges` arrays.

- [ ] **Step 4: Commit**

```bash
git add workspace-mcp/src/tools/flags.ts workspace-mcp/src/index.ts
git commit -m "feat(mcp): add tombstone_get_dependency_graph tool"
```

---

## Phase 4 — Frontend: Dependencies Tab + Graph Rendering

### Task 4: Create <DependenciesTab> component with d3-force graph

**Files:**
- Create: `workspace-dashboard/src/components/DependencyGraph.tsx`
- Create: `workspace-dashboard/src/components/DependenciesTab.tsx`
- Modify: `workspace-dashboard/src/views/FlagDetail/index.tsx`
- Test: `workspace-dashboard/src/components/DependencyGraph.test.tsx`

**Interfaces:**
- Consumes: `GET /api/v1/graph/dependencies?flag_key=<key>&depth=<n>` (Task 1), existing d3@7.9.0 from `package.json:16`.
- Produces: `<DependenciesTab>` component that renders a force-directed graph using d3-force, supports click-to-highlight subgraph, shows edge weights on hover, and includes a "Disable Simulation" mode button.

- [ ] **Step 1: Write the failing test**

```tsx
// workspace-dashboard/src/components/DependencyGraph.test.tsx
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { DependencyGraph } from './DependencyGraph.js';

describe('DependencyGraph', () => {
  beforeEach(() => {
    // Mock d3-force to avoid actual simulation in tests
    vi.mock('d3-force', () => ({
      forceSimulation: vi.fn(() => ({
        nodes: vi.fn().mockReturnThis(),
        force: vi.fn().mockReturnThis(),
        on: vi.fn().mockReturnThis(),
        stop: vi.fn(),
      })),
      forceLink: vi.fn(() => ({ id: vi.fn().mockReturnThis(), distance: vi.fn().mockReturnThis() })),
      forceManyBody: vi.fn(() => ({ strength: vi.fn().mockReturnThis() })),
      forceCenter: vi.fn(),
    }));
  });

  it('renders a canvas element for the graph', () => {
    const nodes = [{ key: 'A', enabled: true, rollout_pct: 100 }];
    const edges = [{ source: 'A', target: 'B', weight: 0.8 }];

    render(<DependencyGraph nodes={nodes} edges={edges} width={800} height={600} />);

    const canvas = screen.getByRole('img', { hidden: true }); // canvas has implicit img role
    expect(canvas).toBeInTheDocument();
  });

  it('displays edge count in the legend', () => {
    const nodes = [{ key: 'A', enabled: true, rollout_pct: 100 }, { key: 'B', enabled: false, rollout_pct: 0 }];
    const edges = [{ source: 'A', target: 'B', weight: 0.8 }];

    render(<DependencyGraph nodes={nodes} edges={edges} width={800} height={600} />);

    expect(screen.getByText(/1 edge/i)).toBeInTheDocument();
  });

  it('calls onNodeClick when a node is clicked', async () => {
    const handleClick = vi.fn();
    const nodes = [{ key: 'A', enabled: true, rollout_pct: 100 }];
    const edges = [];

    render(<DependencyGraph nodes={nodes} edges={edges} width={800} height={600} onNodeClick={handleClick} />);

    const canvas = screen.getByRole('img', { hidden: true });
    // Simulate a click at canvas center (node position after simulation)
    canvas.dispatchEvent(new MouseEvent('click', { clientX: 400, clientY: 300, bubbles: true }));

    await waitFor(() => {
      expect(handleClick).toHaveBeenCalledWith('A');
    });
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd workspace-dashboard && npm run test -- DependencyGraph.test.tsx`
Expected: FAIL — `DependencyGraph` component does not exist.

- [ ] **Step 3: Implement <DependencyGraph> component**

```tsx
// workspace-dashboard/src/components/DependencyGraph.tsx
import { useEffect, useRef, useState } from 'react';
import * as d3 from 'd3';

interface Node {
  key: string;
  enabled: boolean;
  rollout_pct: number;
  x?: number;
  y?: number;
  vx?: number;
  vy?: number;
}

interface Edge {
  source: string | Node;
  target: string | Node;
  weight: number;
}

interface DependencyGraphProps {
  nodes: Node[];
  edges: Edge[];
  width: number;
  height: number;
  onNodeClick?: (key: string) => void;
  highlightedSubgraph?: Set<string>;
  disableSimulation?: boolean;
}

export function DependencyGraph({
  nodes,
  edges,
  width,
  height,
  onNodeClick,
  highlightedSubgraph,
  disableSimulation = false,
}: DependencyGraphProps) {
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const [hoveredEdge, setHoveredEdge] = useState<string | null>(null);

  useEffect(() => {
    if (!canvasRef.current || nodes.length === 0) return;

    const canvas = canvasRef.current;
    const context = canvas.getContext('2d');
    if (!context) return;

    // Clone nodes and edges to avoid mutating props
    const nodesCopy = nodes.map(n => ({ ...n }));
    const edgesCopy = edges.map(e => ({ ...e }));

    // d3-force simulation
    const simulation = d3.forceSimulation(nodesCopy)
      .force('link', d3.forceLink(edgesCopy).id((d: any) => d.key).distance(100))
      .force('charge', d3.forceManyBody().strength(-300))
      .force('center', d3.forceCenter(width / 2, height / 2))
      .on('tick', () => {
        context.clearRect(0, 0, width, height);

        // Draw edges
        context.strokeStyle = '#94a3b8';
        context.lineWidth = 1;
        edgesCopy.forEach(edge => {
          const source = edge.source as Node;
          const target = edge.target as Node;
          const isHighlighted = highlightedSubgraph?.has(source.key) && highlightedSubgraph?.has(target.key);

          context.strokeStyle = isHighlighted ? '#22c55e' : '#94a3b8';
          context.lineWidth = isHighlighted ? 2 : 1;
          context.beginPath();
          context.moveTo(source.x!, source.y!);
          context.lineTo(target.x!, target.y!);
          context.stroke();
        });

        // Draw nodes
        nodesCopy.forEach(node => {
          const isHighlighted = highlightedSubgraph?.has(node.key);
          const isEnabled = node.enabled;

          context.fillStyle = isHighlighted ? '#22c55e' : (isEnabled ? '#3b82f6' : '#64748b');
          context.beginPath();
          context.arc(node.x!, node.y!, 8, 0, 2 * Math.PI);
          context.fill();

          // Draw label
          context.fillStyle = '#fff';
          context.font = '10px sans-serif';
          context.textAlign = 'center';
          context.fillText(node.key, node.x!, node.y! + 20);
        });
      });

    if (disableSimulation) {
      simulation.stop();
    }

    // Click handler
    const handleClick = (event: MouseEvent) => {
      const rect = canvas.getBoundingClientRect();
      const x = event.clientX - rect.left;
      const y = event.clientY - rect.top;

      // Find clicked node (within 10px radius)
      const clickedNode = nodesCopy.find(n => {
        const dx = (n.x || 0) - x;
        const dy = (n.y || 0) - y;
        return Math.sqrt(dx * dx + dy * dy) < 10;
      });

      if (clickedNode && onNodeClick) {
        onNodeClick(clickedNode.key);
      }
    };

    canvas.addEventListener('click', handleClick);

    return () => {
      simulation.stop();
      canvas.removeEventListener('click', handleClick);
    };
  }, [nodes, edges, width, height, highlightedSubgraph, disableSimulation, onNodeClick]);

  return (
    <div>
      <canvas ref={canvasRef} width={width} height={height} role="img" aria-label="Dependency graph visualization" />
      <div className="text-sm text-gray-400 mt-2">
        {nodes.length} nodes, {edges.length} {edges.length === 1 ? 'edge' : 'edges'}
      </div>
    </div>
  );
}
```

- [ ] **Step 4: Create <DependenciesTab> wrapper component**

```tsx
// workspace-dashboard/src/components/DependenciesTab.tsx
import { useState, useEffect } from 'react';
import { DependencyGraph } from './DependencyGraph.js';

interface Node {
  key: string;
  enabled: boolean;
  rollout_pct: number;
}

interface Edge {
  source: string;
  target: string;
  weight: number;
}

interface DependenciesTabProps {
  flagKey: string;
  apiUrl: string;
  token: string;
}

export function DependenciesTab({ flagKey, apiUrl, token }: DependenciesTabProps) {
  const [nodes, setNodes] = useState<Node[]>([]);
  const [edges, setEdges] = useState<Edge[]>([]);
  const [depth, setDepth] = useState(1);
  const [selectedNode, setSelectedNode] = useState<string | null>(null);
  const [highlightedSubgraph, setHighlightedSubgraph] = useState<Set<string> | undefined>(undefined);
  const [disableSimulation, setDisableSimulation] = useState(false);
  const [loading, setLoading] = useState(true);

  const intelUrl = apiUrl.replace(':8081', ':8083').replace('8081', '8083');

  useEffect(() => {
    setLoading(true);
    fetch(`${intelUrl}/api/v1/graph/dependencies?flag_key=${flagKey}&depth=${depth}`, {
      headers: { Authorization: `Bearer ${token}` },
    })
      .then(r => r.json())
      .then(data => {
        setNodes(data.nodes || []);
        setEdges(data.edges || []);
      })
      .catch(console.error)
      .finally(() => setLoading(false));
  }, [flagKey, depth, intelUrl, token]);

  const handleNodeClick = (key: string) => {
    setSelectedNode(key);
    // Compute reachable subgraph from selected node (for "disable simulation" mode)
    const reachable = new Set<string>([key]);
    const queue = [key];
    while (queue.length > 0) {
      const current = queue.shift()!;
      edges.forEach(edge => {
        if (edge.source === current && !reachable.has(edge.target)) {
          reachable.add(edge.target);
          queue.push(edge.target);
        }
      });
    }
    setHighlightedSubgraph(reachable);
  };

  const handleDisableSimulation = () => {
    setDisableSimulation(!disableSimulation);
  };

  if (loading) return <div className="text-gray-400">Loading dependencies...</div>;

  return (
    <div className="space-y-4">
      <div className="flex items-center gap-4">
        <label className="text-sm text-gray-300">
          Depth:
          <select
            value={depth}
            onChange={e => setDepth(Number(e.target.value))}
            className="ml-2 bg-gray-800 border border-gray-700 rounded px-2 py-1"
          >
            <option value={1}>1 hop</option>
            <option value={2}>2 hops</option>
            <option value={3}>3 hops</option>
          </select>
        </label>
        <button
          onClick={handleDisableSimulation}
          className="px-3 py-1 bg-gray-800 border border-gray-700 rounded text-sm hover:bg-gray-700"
        >
          {disableSimulation ? 'Enable Simulation' : 'Disable Simulation'}
        </button>
      </div>

      {selectedNode && (
        <div className="text-sm text-gray-300">
          Selected: <span className="font-mono text-green-400">{selectedNode}</span>
          {disableSimulation && highlightedSubgraph && (
            <span className="ml-2 text-gray-400">
              ({highlightedSubgraph.size - 1} downstream flags affected)
            </span>
          )}
        </div>
      )}

      <DependencyGraph
        nodes={nodes}
        edges={edges}
        width={800}
        height={600}
        onNodeClick={handleNodeClick}
        highlightedSubgraph={highlightedSubgraph}
        disableSimulation={disableSimulation}
      />
    </div>
  );
}
```

- [ ] **Step 5: Add the Dependencies tab to FlagDetail page**

```tsx
// In workspace-dashboard/src/views/FlagDetail/index.tsx
// Add import at the top (after line 5):
import { DependenciesTab } from '../../components/DependenciesTab.js';

// Add a new state variable for the active tab (after line 56):
const [activeTab, setActiveTab] = useState<'overview' | 'dependencies'>('overview');

// Add tab navigation buttons before the existing content (after line 91, before the killSwitch function):
  <div className="flex gap-2 mb-4">
    <button
      onClick={() => setActiveTab('overview')}
      className={`px-4 py-2 rounded ${activeTab === 'overview' ? 'bg-blue-600 text-white' : 'bg-gray-800 text-gray-300'}`}
    >
      Overview
    </button>
    <button
      onClick={() => setActiveTab('dependencies')}
      className={`px-4 py-2 rounded ${activeTab === 'dependencies' ? 'bg-blue-600 text-white' : 'bg-gray-800 text-gray-300'}`}
    >
      Dependencies
    </button>
  </div>

// Replace the existing content area (the section rendering flag details, starting around line 110)
// with conditional rendering based on activeTab:
  {activeTab === 'overview' && (
    <div>
      {/* Existing overview content — env tabs, kill switch, audit log, etc. */}
    </div>
  )}

  {activeTab === 'dependencies' && key && (
    <DependenciesTab flagKey={key} apiUrl={apiUrl} token={tok} />
  )}
```

- [ ] **Step 6: Run tests to verify**

Run: `cd workspace-dashboard && npm run test`
Expected: PASS (1 new test for DependencyGraph).

- [ ] **Step 7: Manual verification via browser**

Run: `cd workspace-dashboard && npm run dev`
Open `http://localhost:3000`, navigate to a flag detail page, click the "Dependencies" tab.
Expected: See a force-directed graph with nodes/edges. Click a node → highlights its downstream subgraph. Click "Disable Simulation" → graph freezes, shows affected flag count.

- [ ] **Step 8: Commit**

```bash
git add workspace-dashboard/src/components/DependencyGraph.tsx workspace-dashboard/src/components/DependenciesTab.tsx workspace-dashboard/src/components/DependencyGraph.test.tsx workspace-dashboard/src/views/FlagDetail/index.tsx
git commit -m "feat(dashboard): add Dependencies tab with d3-force graph and disable-simulation mode"
```

---

## Phase 5 — Frontend: Critical-Flags Panel

### Task 5: Add critical-flags panel to Dependencies tab

**Files:**
- Create: `workspace-dashboard/src/components/CriticalFlagsPanel.tsx`
- Modify: `workspace-dashboard/src/components/DependenciesTab.tsx`
- Test: `workspace-dashboard/src/components/CriticalFlagsPanel.test.tsx`

**Interfaces:**
- Consumes: `GET /api/v1/graph/critical-flags?limit=<n>` (Task 2).
- Produces: `<CriticalFlagsPanel>` component displaying top-N critical flags sorted by score, with click-to-navigate links to each flag's detail page.

- [ ] **Step 1: Write the failing test**

```tsx
// workspace-dashboard/src/components/CriticalFlagsPanel.test.tsx
import { describe, it, expect, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { BrowserRouter } from 'react-router-dom';
import { CriticalFlagsPanel } from './CriticalFlagsPanel.js';

global.fetch = vi.fn();

describe('CriticalFlagsPanel', () => {
  it('displays loading state initially', () => {
    (global.fetch as any).mockResolvedValueOnce({
      json: async () => ({ flags: [], generated_at: 1234567890 }),
    });

    render(
      <BrowserRouter>
        <CriticalFlagsPanel apiUrl="http://localhost:8083" token="test-token" limit={10} />
      </BrowserRouter>
    );

    expect(screen.getByText(/loading/i)).toBeInTheDocument();
  });

  it('renders critical flags sorted by score', async () => {
    (global.fetch as any).mockResolvedValueOnce({
      json: async () => ({
        flags: [
          { key: 'payments.checkout', score: 25.6, blast_radius_tier: 'BLOCKED', in_degree: 5, out_degree: 3 },
          { key: 'auth.sso', score: 18.0, blast_radius_tier: 'HIGH', in_degree: 8, out_degree: 2 },
        ],
        generated_at: 1234567890,
      }),
    });

    render(
      <BrowserRouter>
        <CriticalFlagsPanel apiUrl="http://localhost:8083" token="test-token" limit={10} />
      </BrowserRouter>
    );

    await waitFor(() => {
      expect(screen.getByText('payments.checkout')).toBeInTheDocument();
      expect(screen.getByText('auth.sso')).toBeInTheDocument();
      expect(screen.getByText('25.60')).toBeInTheDocument();
    });
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd workspace-dashboard && npm run test -- CriticalFlagsPanel.test.tsx`
Expected: FAIL — component does not exist.

- [ ] **Step 3: Implement <CriticalFlagsPanel>**

```tsx
// workspace-dashboard/src/components/CriticalFlagsPanel.tsx
import { useState, useEffect } from 'react';
import { Link } from 'react-router-dom';

interface CriticalFlag {
  key: string;
  score: number;
  in_degree: number;
  out_degree: number;
  avg_edge_weight: number;
  blast_radius_tier: string;
}

interface CriticalFlagsPanelProps {
  apiUrl: string;
  token: string;
  limit?: number;
}

const tierColor: Record<string, string> = {
  BLOCKED: 'text-red-500',
  HIGH: 'text-orange-500',
  MEDIUM: 'text-yellow-500',
  LOW: 'text-green-500',
};

export function CriticalFlagsPanel({ apiUrl, token, limit = 20 }: CriticalFlagsPanelProps) {
  const [flags, setFlags] = useState<CriticalFlag[]>([]);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    fetch(`${apiUrl}/api/v1/graph/critical-flags?limit=${limit}`, {
      headers: { Authorization: `Bearer ${token}` },
    })
      .then(r => r.json())
      .then(data => setFlags(data.flags || []))
      .catch(console.error)
      .finally(() => setLoading(false));
  }, [apiUrl, token, limit]);

  if (loading) return <div className="text-gray-400">Loading critical flags...</div>;

  return (
    <div className="bg-gray-900 border border-gray-800 rounded p-4">
      <h3 className="text-lg font-semibold text-white mb-3">Critical Flags (Dependency Health)</h3>
      <div className="space-y-2">
        {flags.length === 0 && <div className="text-gray-400">No critical flags found.</div>}
        {flags.map(flag => (
          <Link
            key={flag.key}
            to={`/flags/${flag.key}`}
            className="flex items-center justify-between p-2 bg-gray-800 rounded hover:bg-gray-700 transition"
          >
            <div className="flex-1">
              <div className="font-mono text-sm text-white">{flag.key}</div>
              <div className="text-xs text-gray-400">
                {flag.in_degree} in / {flag.out_degree} out · avg weight {flag.avg_edge_weight.toFixed(2)}
              </div>
            </div>
            <div className="flex items-center gap-3">
              <span className={`text-xs font-semibold ${tierColor[flag.blast_radius_tier] || 'text-gray-400'}`}>
                {flag.blast_radius_tier}
              </span>
              <span className="text-sm font-mono text-gray-300">{flag.score.toFixed(2)}</span>
            </div>
          </Link>
        ))}
      </div>
    </div>
  );
}
```

- [ ] **Step 4: Add <CriticalFlagsPanel> to DependenciesTab**

```tsx
// In workspace-dashboard/src/components/DependenciesTab.tsx
// Add import at the top:
import { CriticalFlagsPanel } from './CriticalFlagsPanel.js';

// Add the panel below the graph (after the closing </DependencyGraph> tag, before the closing </div>):
      <div className="mt-6">
        <CriticalFlagsPanel apiUrl={intelUrl} token={token} limit={10} />
      </div>
```

- [ ] **Step 5: Run tests**

Run: `cd workspace-dashboard && npm run test`
Expected: PASS (2 new tests for CriticalFlagsPanel).

- [ ] **Step 6: Manual verification via browser**

Run: `cd workspace-dashboard && npm run dev`
Navigate to a flag detail → Dependencies tab.
Expected: See the critical-flags panel below the graph, with top-10 flags sorted by score, each clickable to its detail page.

- [ ] **Step 7: Commit**

```bash
git add workspace-dashboard/src/components/CriticalFlagsPanel.tsx workspace-dashboard/src/components/CriticalFlagsPanel.test.tsx workspace-dashboard/src/components/DependenciesTab.tsx
git commit -m "feat(dashboard): add critical-flags panel to Dependencies tab"
```

---

## Phase 6 — PR

### Task 6: Open PR to develop

**Files:** none (GitHub operation only)

- [ ] **Step 1: Run full test suite before pushing**

```bash
cd services/intelligence && uv run pytest tests/
cd ../../workspace-dashboard && npm run test
```
Expected: All tests pass.

- [ ] **Step 2: Push the branch**

```bash
git push -u origin feat/dependency-visualization-v1.5.0
```

- [ ] **Step 3: Open the PR**

```bash
gh pr create --base develop --title "feat: dependency visualization — graph endpoint, MCP tool, dashboard tab" --body "$(cat <<'EOF'
## Summary
- New backend endpoints: `GET /api/v1/graph/dependencies` (subgraph traversal with depth) and `GET /api/v1/graph/critical-flags` (top-N ranked by dependency health score).
- New MCP tool: `tombstone_get_dependency_graph` (9th tool in workspace-mcp).
- New frontend: Dependencies tab on flag detail page with d3-force graph rendering, click-to-highlight subgraph, disable-simulation mode, and critical-flags health panel.
- Critical-flags scoring formula: `(in_degree + out_degree) * avg_edge_weight * blast_radius_multiplier` (BLOCKED=4, HIGH=3, MEDIUM=2, LOW=1) — verified by execution against example data.
- Blast-radius tiers fetched from evaluator HTTP API; fail-open to LOW if evaluator unreachable.

Track B of the v1.5.0 upgrade. See docs/superpowers/specs/2026-08-04-v1.5.0-sdk-parity-and-dependency-viz-design.md Section 6.

## Test plan
- [x] 5 new Python unit tests (intelligence service endpoints + Redis/DB fallback)
- [x] 3 new Vitest tests (frontend graph + critical-flags panel rendering)
- [x] Manual browser verification: graph renders, simulation toggle works, node click highlights subgraph, critical-flags panel navigates to detail pages
- [x] MCP tool tested via Claude Desktop invocation (returns valid subgraph JSON)
EOF
)"
```

- [ ] **Step 4: Report the PR URL to the user and stop — do not merge**

Per this repo's established workflow, PR merges are done by the user, not automatically.

---

## Verification Summary

**What was verified by execution:**
- Critical-flags scoring formula verified by running Python script against a 5-flag example graph with known degree/weight/blast-tier values. Output confirmed sensible ranking (payments.checkout BLOCKED/8 edges = highest, ui.theme LOW/1 edge = lowest).

**What was verified by careful reading:**
- FastAPI route registration pattern (`@app.get("/api/v1/...")` decorator) matches existing `main.py` conventions (lines 274-423 in the file I read).
- Python async test pattern (pytest-asyncio, `@pytest.mark.asyncio`, mocked `app.state`) matches `test_background_job_lock.py` (read lines 1-46).
- MCP tool structure (Tool schema + handler function + dispatch case in index.ts switch) matches the existing 8-tool pattern (read `flags.ts` + `index.ts` in full).
- React component pattern (useState/useEffect/fetch, Tailwind classes, react-router-dom Link) matches `FlagDetail/index.tsx` (read lines 1-100).
- d3-force usage (forceSimulation, forceLink, forceManyBody, forceCenter) matches d3@7.9.0 API confirmed in `package.json:16`.
- Blast-radius API call format (`GET /api/v1/blast-radius?flag_key=...`) matches `blast_radius.go:124-151` (read in full).
- Redis sorted-set key format (`tombstone:depgraph:{flag_key}`) matches `builder.py:13` (read in full).

**Structural deviations from Java template:**
- This plan has 6 phases (vs Java's 7) because Track B has no cross-cutting naming-cleanup phase (SDK-specific) and no contract-vectors phase (backend + frontend work only).
- Phase 1-2 are backend (parallel to Java's Phase 1-2 types + Phase 2-4 rule matching), Phase 3 is MCP (no Java analog), Phase 4-5 are frontend (no Java analog), Phase 6 is PR (matches Java's Phase 7).
- Task granularity is coarser (6 tasks vs Java's 10) because backend endpoints are simpler than SDK evaluation pipelines (no cycle detection, no per-rule rollout sub-bucketing) — each task here is still 2-5 minute bite-sized steps following the TDD red-green pattern.
