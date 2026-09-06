import asyncio
import logging
import os
import time
import math
import httpx
from collections import deque, defaultdict
from contextlib import asynccontextmanager
from datetime import datetime, timezone

from fastapi import FastAPI, Request
from fastapi.middleware.cors import CORSMiddleware
from fastapi.responses import JSONResponse
from app.anomaly.detector import AnomalyDetector
from app.anomaly.rule_generator import generate_rule
from app.cleanup.routes import router as cleanup_router
from app.correlation.correlator import IncidentCorrelator
from app.experiments.routes import router as experiments_router
from app.graph.builder import DEFAULT_PROJECT_ID, DependencyGraphBuilder
from app.integrations.webhook_receiver import router as webhooks_router
from app.kafka.consumer import create_consumer
from app.observability.metrics import RedMetricsMiddleware, metrics_response
from app.rollout.linucb import LinUCBBandit
from app.rollout.routes import router as rollout_router
from app.rollout.thompson import ThompsonSamplingEngine
from app.search.embedding_model import create_embedding_model
from app.search.embedding_sync import EmbeddingSyncService
from app.search.retriever import FlagSearchRetriever
from app.stale.detector import StaleFlagDetector
from app.telemetry.clickhouse_writer import ClickHouseWriter
from app.telemetry.routes import router as telemetry_router

logger = logging.getLogger(__name__)


async def _try_redis_connect(url: str):
    """Create a redis.asyncio client; return None if unavailable (fails open)."""
    try:
        import redis.asyncio as aioredis

        client = aioredis.from_url(url, decode_responses=False)
        await client.ping()
        return client
    except Exception as exc:
        logger.warning("Redis unavailable (%s) — dep graph will use DB fallback", exc)
        return None


async def _depgraph_rebuild_background(
    builder: DependencyGraphBuilder, redis_client, lock: asyncio.Lock
) -> None:
    """Background task: rebuild dep graph on startup, then daily at 02:00 UTC.

    Guarded by `lock` (shared with `_daily_retrain` and the HTTP-triggered
    `POST /api/v1/dependency-graph` handler) so overlapping rebuilds never
    race on the shared `builder` state.
    """
    pool = await builder._get_pool()
    try:
        async with lock:
            await builder.rebuild_all(pool, redis_client)
    except Exception as exc:
        logger.warning("dep graph initial rebuild failed: %s", exc)

    while True:
        _now_ts = asyncio.get_running_loop().time()
        import datetime as _dt

        now = _dt.datetime.utcnow()
        # Next 02:00 UTC
        next_2am = now.replace(hour=2, minute=0, second=0, microsecond=0)
        if next_2am <= now:
            next_2am = next_2am + _dt.timedelta(days=1)
        sleep_secs = (next_2am - now).total_seconds()
        await asyncio.sleep(sleep_secs)
        try:
            pool = await builder._get_pool()
            async with lock:
                await builder.rebuild_all(pool, redis_client)
        except Exception as exc:
            logger.warning("dep graph scheduled rebuild failed: %s", exc)


@asynccontextmanager
async def lifespan(app: FastAPI):
    # Startup
    # Guards mutual exclusion between the two daily background jobs
    # (_daily_retrain, _depgraph_rebuild_background) and the HTTP-triggered
    # POST /api/v1/dependency-graph handler — all three touch shared
    # in-memory builder/ensemble state and must never run concurrently.
    app.state.background_job_lock = asyncio.Lock()
    app.state.anomaly = AnomalyDetector()
    app.state.correlator = IncidentCorrelator(
        db_url=os.environ["DB_URL"],
        pagerduty_token=os.environ.get("PAGERDUTY_TOKEN", ""),
    )
    # Build embedding model — swap EMBEDDING_BACKEND=bedrock for Fly.io free tier
    _embedding_backend = os.environ.get("EMBEDDING_BACKEND", "local")
    if _embedding_backend == "bedrock":
        _embedding_model = create_embedding_model(
            "bedrock",
            access_key_id=os.environ["BEDROCK_ACCESS_KEY_ID"],
            secret_access_key=os.environ["BEDROCK_SECRET_ACCESS_KEY"],
            region=os.environ.get("BEDROCK_REGION", "us-east-1"),
        )
    else:
        _embedding_model = create_embedding_model("local")
    app.state.embedding_model = _embedding_model

    app.state.searcher = FlagSearchRetriever(
        db_url=os.environ["DB_URL"],
        embedding_model=app.state.embedding_model,
    )
    app.state.embedding_sync = EmbeddingSyncService(
        db_url=os.environ["DB_URL"],
        embedding_model=app.state.embedding_model,
    )
    # Thompson Sampling engine for autonomous rollout
    app.state.rollout_engine = ThompsonSamplingEngine()

    # LinUCB contextual bandit for context-aware rollout decisions
    app.state.linucb_bandit = LinUCBBandit(alpha=1.0, d=5)
    redis_url = os.environ.get("REDIS_URL", "redis://localhost:6379")
    try:
        import redis.asyncio as aioredis

        redis_client = aioredis.from_url(redis_url, decode_responses=False)
        await app.state.linucb_bandit.load_from_redis(redis_client)
        await redis_client.aclose()
    except Exception:
        import logging as _logging

        _logging.getLogger(__name__).warning(
            "LinUCB Redis restore skipped (Redis unavailable) — starting fresh"
        )

    app.state.graph_builder = DependencyGraphBuilder(db_url=os.environ["DB_URL"])

    # Redis — optional; fails open so service starts even if Redis is down
    redis_url = os.environ.get("REDIS_URL", "redis://localhost:6379")
    app.state.redis = await _try_redis_connect(redis_url)

    # Restore Thompson posteriors from Redis (fails open)
    if app.state.redis is not None:
        try:
            await app.state.rollout_engine.load_all_from_redis(app.state.redis)
        except Exception as exc:  # noqa: BLE001
            logger.warning("Failed to restore Thompson posteriors from Redis: %s", exc)

    # INT-6: constructed after app.state.redis so the real evaluation-count
    # signal (EVAL-3's telemetry buckets) is available; both
    # AST_REWRITER_URL and AST_REWRITER_REPO_PATH are optional (unset means
    # the code-reference signal is always None/"unknown", which
    # StaleFlagDetector treats as "assume still referenced", never as
    # "safe to archive" -- see its own doc comment). AST_REWRITER_REPO_PATH
    # is a path on ast-rewriter's OWN filesystem (its scanner walks a local
    # directory), not on this service -- a real deployment needs the
    # target app repo checked out somewhere ast-rewriter can read; today
    # that's this repo's own checkout as the dogfood case, not a
    # multi-tenant customer-repo mechanism (not yet designed).
    app.state.stale = StaleFlagDetector(
        db_url=os.environ["DB_URL"],
        redis_client=app.state.redis,
        ast_rewriter_url=os.environ.get("AST_REWRITER_URL"),
        repo_path=os.environ.get("AST_REWRITER_REPO_PATH"),
    )

    # ClickHouse telemetry pipeline (optional)
    # Schema (run once in ClickHouse):
    # CREATE TABLE tombstone_evaluations (
    #   flag_key String, environment String, user_hash String,
    #   result String, reason String, latency_ms Float64, ts DateTime
    # ) ENGINE = MergeTree() ORDER BY (flag_key, ts);
    ch_host = os.environ.get("CLICKHOUSE_HOST", "")
    redis_client = getattr(app.state, "redis", None)
    if ch_host:
        app.state.clickhouse = ClickHouseWriter(host=ch_host, redis_client=redis_client)
        await app.state.clickhouse.create_tables()
    else:
        app.state.clickhouse = ClickHouseWriter(
            host="localhost", redis_client=redis_client
        )  # unavailable but won't crash
    await app.state.clickhouse.start()

    # Start background event consumer (drives embedding sync + dep-graph updates)
    # CONSUMER_BACKEND=redis uses Redis Streams (Fly.io free tier, no Kafka needed)
    # CONSUMER_BACKEND=kafka (default) preserves existing TelemetryConsumer behaviour
    _consumer_backend = os.environ.get("CONSUMER_BACKEND", "kafka")
    if _consumer_backend == "redis":
        _consumer = create_consumer(
            "redis",
            redis_url=os.environ.get("REDIS_URL", "redis://localhost:6379"),
            anomaly_detector=app.state.anomaly,
            environments=os.environ.get("TOMBSTONE_ENVIRONMENTS", "production").split(
                ","
            ),
            embedding_sync=app.state.embedding_sync,
            graph_builder=app.state.graph_builder if app.state.redis else None,
        )
        await _consumer.start()
    else:
        _consumer = create_consumer(
            "kafka",
            brokers=os.environ.get("KAFKA_BROKERS", "localhost:9092"),
            anomaly_detector=app.state.anomaly,
            embedding_sync=app.state.embedding_sync,
            graph_builder=app.state.graph_builder if app.state.redis else None,
            redis_client=app.state.redis,
        )
    consumer_task = asyncio.create_task(_consumer.run())

    # Start daily Isolation Forest retraining task (runs at 02:00 UTC)
    retrain_task = asyncio.create_task(_daily_retrain(app))

    # Background dep graph rebuild (only when Redis is available)
    rebuild_task = None
    if app.state.redis is not None:
        rebuild_task = asyncio.create_task(
            _depgraph_rebuild_background(
                app.state.graph_builder, app.state.redis, app.state.background_job_lock
            )
        )

    await app.state.searcher.initialize()
    await app.state.embedding_sync.initialize()

    yield

    # Shutdown
    await _consumer.stop()
    consumer_task.cancel()
    retrain_task.cancel()
    if rebuild_task is not None:
        rebuild_task.cancel()
    try:
        await consumer_task
    except asyncio.CancelledError:
        pass
    try:
        await retrain_task
    except asyncio.CancelledError:
        pass
    if rebuild_task is not None:
        try:
            await rebuild_task
        except asyncio.CancelledError:
            pass

    if app.state.redis is not None:
        await app.state.redis.aclose()


async def _daily_retrain(app: FastAPI) -> None:
    """
    Background task: retrain Isolation Forest models for all tracked flags daily.
    Wakes at 02:00 UTC each day to avoid peak traffic windows.

    Guarded by `app.state.background_job_lock` (shared with
    `_depgraph_rebuild_background` and the HTTP-triggered
    `POST /api/v1/dependency-graph` handler) — these paths don't share state
    with each other today, but the lock gives us one mutual-exclusion point
    for all "long-running background job" work so a future overlap can't
    silently race.
    """
    while True:
        now = datetime.now(timezone.utc)
        # Seconds until next 02:00 UTC
        target_hour = 2
        seconds_until = (
            (target_hour - now.hour - 1) * 3600
            + (60 - now.minute - 1) * 60
            + (60 - now.second)
        ) % 86400
        if seconds_until == 0:
            seconds_until = 86400
        await asyncio.sleep(seconds_until)
        try:
            async with app.state.background_job_lock:
                count = app.state.anomaly.get_ensemble().retrain_all()
        except Exception:  # noqa: BLE001 — retrain errors must not crash the service
            count = 0
        # Log silently — no logger dependency injected here
        _ = count  # retrained {count} flag models


app = FastAPI(title="Tombstone Intelligence", version="0.1.0", lifespan=lifespan)

app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],
    allow_methods=["GET", "POST"],
    allow_headers=["Authorization", "Content-Type"],
)
# OBS-1 (Python slice): added AFTER CORSMiddleware so it wraps (runs
# outside) it, matching the Go services' convention of registering RED
# metrics outer to CORS/rate-limiting — a CORS preflight short-circuit is
# still recorded (as "unmatched", never the raw path; see
# app/observability/metrics.py's _route_label docstring), not silently
# skipped. Starlette wraps middleware in REVERSE registration order (the
# last one added via add_middleware ends up outermost), so this ordering
# is deliberate, not incidental.
app.add_middleware(RedMetricsMiddleware)

app.include_router(experiments_router)
app.include_router(webhooks_router)
app.include_router(cleanup_router)
app.include_router(rollout_router)
app.include_router(telemetry_router)


@app.get("/health")
async def health():
    return {"status": "ok", "service": "intelligence"}


# OBS-1: Prometheus scrape endpoint — public, no auth middleware, matching
# /health above. Distinct from app/telemetry/routes.py's POST
# /api/v1/telemetry/ingest, which ingests SDK evaluation events, not
# observability metrics.
@app.get("/metrics")
async def metrics():
    return metrics_response()


@app.get("/api/v1/search")
async def search_flags(q: str, limit: int = 10):
    """NLP semantic search across all flags."""
    results = await app.state.searcher.search(q, limit=limit)
    return {"query": q, "results": results}


@app.get("/api/v1/anomaly/{flag_key}")
async def get_flag_anomaly(flag_key: str):
    """Get anomaly status for a specific flag."""
    score = app.state.anomaly.get_score(flag_key)
    return {"flag_key": flag_key, "anomaly_score": score, "is_anomaly": score > 2.5}


@app.get("/api/v1/anomaly/{flag_key}/ensemble")
async def get_flag_anomaly_ensemble(flag_key: str):
    """
    Full ensemble anomaly breakdown for a flag (Phase 3.1).

    Returns per-model votes (Z-score, Isolation Forest, EWMA), per-granularity
    votes (10s / 60s / 5m), composite score, and final anomaly verdict.
    Falls back to Z-score when < 50 observations are available.
    """
    result = app.state.anomaly.detect(flag_key)
    return {"flag_key": flag_key, **result}


@app.get("/api/v1/stale")
async def get_stale_flags(project_id: str = DEFAULT_PROJECT_ID):
    """Get flags that are candidates for cleanup."""
    stale = await app.state.stale.detect(project_id)
    return {"stale_flags": stale, "count": len(stale)}


@app.post("/api/v1/dependency-graph")
async def build_dependency_graph(
    request: Request,
    project_id: str = DEFAULT_PROJECT_ID,
    environment: str = "production",
    from_unix: int = 0,
    to_unix: int = 0,
):
    import time as t

    if to_unix == 0:
        to_unix = int(t.time())
    if from_unix == 0:
        from_unix = to_unix - 3600

    # Fail fast rather than block this HTTP request behind a potentially
    # long-running background job (daily retrain or dep-graph rebuild).
    # Small TOCTOU race between this check and acquiring the lock below is
    # acceptable: worst case is an occasional missed 409 where the caller
    # blocks briefly instead of getting fast-failed — not a correctness bug.
    if request.app.state.background_job_lock.locked():
        return JSONResponse(
            status_code=409,
            content={"error": "background rebuild in progress, retry shortly"},
        )

    async with request.app.state.background_job_lock:
        graph = await request.app.state.graph_builder.build(
            project_id, environment, from_unix, to_unix
        )
    return {
        "nodes": [
            {
                "flag_key": n.flag_key,
                "enabled": n.enabled,
                "rollout_pct": n.rollout_pct,
                "state": n.state,
                "owner_id": n.owner_id,
            }
            for n in graph.nodes
        ],
        "edges": [
            {
                "source": e.source,
                "target": e.target,
                "weight": e.weight,
                "co_change_count": e.co_change_count,
            }
            for e in graph.edges
        ],
        "generated_at": graph.generated_at,
        "event_count": graph.event_count,
    }


@app.get("/api/v1/dependency-graph/impact/{flag_key}")
async def get_flag_impact(
    flag_key: str,
    request: Request,
    project_id: str = DEFAULT_PROJECT_ID,
    environment: str = "production",
    days: int = 30,
):
    """Return co-changed flags for flag_key.

    Uses Redis sorted-set O(log n) lookup when Redis is available and the key
    is warm.  Falls back to the original O(n²) DB scan otherwise.
    """
    redis_client = request.app.state.redis
    builder: DependencyGraphBuilder = request.app.state.graph_builder

    if redis_client is not None:
        fast_result = await builder.get_impact_fast(flag_key, redis_client, project_id)
        if fast_result is not None:
            return {
                "flag_key": flag_key,
                "environment": environment,
                "source": "redis",
                "co_changed_with": fast_result,
            }
        # Redis key absent (cold start) — fall through to DB
        logger.info("dep graph Redis miss for %s — falling back to DB", flag_key)

    return await builder.get_impact(flag_key, environment, project_id, days)


@app.get("/api/v1/graph/dependencies")
async def get_dependency_subgraph(
    flag_key: str,
    depth: int = 1,
    project_id: str = DEFAULT_PROJECT_ID,
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
            neighbors = await builder.get_impact_fast(
                current_key, redis_client, project_id
            )
            if neighbors is None:
                # Redis miss — fall back to DB
                logger.info(
                    "dep graph Redis miss for %s — falling back to DB", current_key
                )
                db_result = await builder.get_impact(
                    current_key, environment, project_id, days=30
                )
                co_changed = db_result.get("co_changed_with", [])
                # DB fallback returns {"flag_key": str, "co_change_count": int, "avg_seconds_apart": float}
                # Infer weight from co_change_count: higher count → higher coupling
                neighbors = [
                    {
                        "flag_key": item["flag_key"],
                        "weight": min(1.0, item["co_change_count"] / 10.0),
                    }
                    for item in co_changed
                ]

        if not neighbors:
            continue

        for neighbor in neighbors:
            neighbor_key = neighbor["flag_key"]
            weight = neighbor["weight"]
            edges.append(
                {"source": current_key, "target": neighbor_key, "weight": weight}
            )
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
            WHERE f.project_id = $2 AND f.key = ANY($3::text[])
            """,
            environment,
            project_id,
            list(visited),
        )
        nodes = [
            {
                "key": r["key"],
                "enabled": bool(r["enabled"] or False),
                "rollout_pct": int(r["rollout_pct"] or 0),
            }
            for r in rows
        ]

    return {
        "flag_key": flag_key,
        "depth": depth,
        "environment": environment,
        "nodes": nodes,
        "edges": edges,
    }


BLAST_RADIUS_MULTIPLIER = {"BLOCKED": 4, "HIGH": 3, "MEDIUM": 2, "LOW": 1}


@app.get("/api/v1/graph/critical-flags")
async def get_critical_flags(
    limit: int = 20,
    project_id: str = DEFAULT_PROJECT_ID,
    environment: str = "production",
    request: Request = None,
):
    """
    Return top-N most critical flags ranked by dependency health score.

    Score formula: (in_degree + out_degree) * avg_edge_weight * blast_radius_multiplier.
    blast_radius_multiplier: BLOCKED=4, HIGH=3, MEDIUM=2, LOW=1.

    Fetches blast-radius tier from evaluator service; falls back to LOW if unreachable.
    """
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
    to_unix = int(time.time())
    from_unix = to_unix - (90 * 86400)  # 90-day window matching rebuild_all

    rows = await pool.fetch(
        """
        SELECT flag_key,
               EXTRACT(EPOCH FROM created_at)::bigint AS ts
        FROM audit_log
        WHERE created_at >= to_timestamp($1)
          AND created_at <= to_timestamp($2)
          AND project_id = $3
          AND flag_key IS NOT NULL
          AND event_type IN ('flag_environment_updated','kill_switch_activated','flag_created')
        ORDER BY created_at ASC
        """,
        float(from_unix),
        float(to_unix),
        project_id,
    )

    if not rows:
        return {"flags": [], "generated_at": to_unix}

    # 2. Rebuild edge map (same logic as builder.build())
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

        flag_data.append(
            {
                "key": flag_key,
                "in_degree": in_degree,
                "out_degree": out_degree,
                "avg_edge_weight": avg_weight,
            }
        )

    # 4. Fetch blast-radius tier from evaluator service for each flag
    evaluator_url = os.environ.get("EVALUATOR_URL", "http://localhost:8082")
    async with httpx.AsyncClient(timeout=5.0) as client:
        for flag in flag_data:
            try:
                # Call evaluator with rollout_pct=100 (worst-case blast radius)
                resp = await client.get(
                    f"{evaluator_url}/api/v1/blast-radius",
                    params={
                        "flag_key": flag["key"],
                        "environment": environment,
                        "rollout_pct": 100,
                        "project_id": project_id,
                    },
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
            score = (
                (flag["in_degree"] + flag["out_degree"])
                * flag["avg_edge_weight"]
                * mult
            )
            flag["score"] = round(score, 2)

    # 5. Sort descending by score, limit to top-N
    flag_data.sort(key=lambda f: -f["score"])
    top_n = flag_data[:limit]

    return {
        "flags": top_n,
        "generated_at": to_unix,
    }


@app.post("/api/v1/correlate")
async def correlate_incident(
    incident_id: str,
    affected_service: str,
    incident_start_unix: int,
    project_id: str = DEFAULT_PROJECT_ID,
):
    """Correlate a PagerDuty incident with recent flag changes."""
    candidates = await app.state.correlator.correlate(
        incident_id=incident_id,
        affected_service=affected_service,
        incident_start_unix=incident_start_unix,
        project_id=project_id,
    )
    return {"incident_id": incident_id, "candidates": candidates}


@app.post("/api/v1/intelligence/generate-rule")
async def generate_anomaly_rule(flag_key: str, request: Request):
    """
    Trigger Argos 3-agent LLM pipeline to generate an anomaly detection rule.

    Requires ANTHROPIC_API_KEY env var. Returns graceful 503 when absent.
    Generated rules are stored as pending-approval signals — never auto-activated.
    """
    if not os.environ.get("ANTHROPIC_API_KEY"):
        return JSONResponse(
            status_code=503,
            content={
                "error": "ANTHROPIC_API_KEY not configured",
                "detail": "Set ANTHROPIC_API_KEY env var to enable LLM rule generation",
            },
        )

    # Extract error rate history from the in-memory anomaly detector
    detector = request.app.state.anomaly
    metrics = detector._metrics.get(flag_key)
    if not metrics or len(metrics.error_rates) < 30:
        obs = len(metrics.error_rates) if metrics else 0
        return JSONResponse(
            status_code=422,
            content={"error": f"insufficient data: {obs} observations (need >=30)"},
        )

    error_rates = list(metrics.error_rates)
    signals_dir = (
        "signals"  # relative to repo root; service runs from repo root in Docker
    )

    result = await generate_rule(flag_key, error_rates, signals_dir)

    if result.error:
        return JSONResponse(status_code=500, content={"error": result.error})

    return {
        "flag_key": result.flag_key,
        "description": result.description,
        "precision": result.precision,
        "recall": result.recall,
        "signal_path": result.signal_path,
        "status": "pending-approval",
        "message": (
            "Rule generated and stored as pending-approval signal. "
            "Owner must approve before activation."
        ),
    }
