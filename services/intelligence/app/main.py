import asyncio
import logging
import os
from contextlib import asynccontextmanager
from datetime import datetime, timezone

from fastapi import FastAPI
from fastapi.middleware.cors import CORSMiddleware

from fastapi import Request  # noqa: F401 — used in endpoint signatures below
from app.anomaly.detector import AnomalyDetector
from app.cleanup.routes import router as cleanup_router
from app.correlation.correlator import IncidentCorrelator
from app.experiments.routes import router as experiments_router
from app.graph.builder import DependencyGraphBuilder
from app.integrations.webhook_receiver import router as webhooks_router
from app.kafka.consumer import TelemetryConsumer
from app.rollout.linucb import LinUCBBandit
from app.rollout.routes import router as rollout_router
from app.rollout.thompson import ThompsonSamplingEngine
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


async def _depgraph_rebuild_background(builder: DependencyGraphBuilder, redis_client) -> None:
    """Background task: rebuild dep graph on startup, then daily at 02:00 UTC."""
    pool = await builder._get_pool()
    try:
        await builder.rebuild_all(pool, redis_client)
    except Exception as exc:
        logger.warning("dep graph initial rebuild failed: %s", exc)

    while True:
        now_ts = asyncio.get_event_loop().time()
        import time as _t
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
            await builder.rebuild_all(pool, redis_client)
        except Exception as exc:
            logger.warning("dep graph scheduled rebuild failed: %s", exc)


@asynccontextmanager
async def lifespan(app: FastAPI):
    # Startup
    app.state.anomaly = AnomalyDetector()
    app.state.correlator = IncidentCorrelator(
        db_url=os.environ["DB_URL"],
        pagerduty_token=os.environ.get("PAGERDUTY_TOKEN", ""),
    )
    app.state.searcher = FlagSearchRetriever(db_url=os.environ["DB_URL"])
    app.state.embedding_sync = EmbeddingSyncService(db_url=os.environ["DB_URL"])
    app.state.stale = StaleFlagDetector(db_url=os.environ["DB_URL"])

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

    # Start background Kafka consumer (drives embedding sync + dep-graph updates)
    consumer = TelemetryConsumer(
        brokers=os.environ.get("KAFKA_BROKERS", "localhost:9092"),
        anomaly_detector=app.state.anomaly,
        embedding_sync=app.state.embedding_sync,
        graph_builder=app.state.graph_builder if app.state.redis else None,
        redis_client=app.state.redis,
    )
    consumer_task = asyncio.create_task(consumer.run())

    # Start daily Isolation Forest retraining task (runs at 02:00 UTC)
    retrain_task = asyncio.create_task(_daily_retrain(app))

    # Background dep graph rebuild (only when Redis is available)
    rebuild_task = None
    if app.state.redis is not None:
        rebuild_task = asyncio.create_task(
            _depgraph_rebuild_background(app.state.graph_builder, app.state.redis)
        )

    await app.state.searcher.initialize()
    await app.state.embedding_sync.initialize()

    yield

    # Shutdown
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
    """
    while True:
        now = datetime.now(timezone.utc)
        # Seconds until next 02:00 UTC
        target_hour = 2
        seconds_until = ((target_hour - now.hour - 1) * 3600
                         + (60 - now.minute - 1) * 60
                         + (60 - now.second)) % 86400
        if seconds_until == 0:
            seconds_until = 86400
        await asyncio.sleep(seconds_until)
        try:
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

app.include_router(experiments_router)
app.include_router(webhooks_router)
app.include_router(cleanup_router)
app.include_router(rollout_router)
app.include_router(telemetry_router)


@app.get("/health")
async def health():
    return {"status": "ok", "service": "intelligence"}


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
async def get_stale_flags(project_id: str = "00000000-0000-0000-0000-000000000001"):
    """Get flags that are candidates for cleanup."""
    stale = await app.state.stale.detect(project_id)
    return {"stale_flags": stale, "count": len(stale)}


@app.post("/api/v1/dependency-graph")
async def build_dependency_graph(request: Request, environment: str = "production", from_unix: int = 0, to_unix: int = 0):
    import time as t
    if to_unix == 0:
        to_unix = int(t.time())
    if from_unix == 0:
        from_unix = to_unix - 3600
    graph = await request.app.state.graph_builder.build(environment, from_unix, to_unix)
    return {
        "nodes": [{"flag_key": n.flag_key, "enabled": n.enabled, "rollout_pct": n.rollout_pct, "state": n.state, "owner_id": n.owner_id} for n in graph.nodes],
        "edges": [{"source": e.source, "target": e.target, "weight": e.weight, "co_change_count": e.co_change_count} for e in graph.edges],
        "generated_at": graph.generated_at, "event_count": graph.event_count,
    }


@app.get("/api/v1/dependency-graph/impact/{flag_key}")
async def get_flag_impact(flag_key: str, request: Request, environment: str = "production", days: int = 30):
    """Return co-changed flags for flag_key.

    Uses Redis sorted-set O(log n) lookup when Redis is available and the key
    is warm.  Falls back to the original O(n²) DB scan otherwise.
    """
    redis_client = request.app.state.redis
    builder: DependencyGraphBuilder = request.app.state.graph_builder

    if redis_client is not None:
        fast_result = await builder.get_impact_fast(flag_key, redis_client)
        if fast_result is not None:
            return {
                "flag_key": flag_key,
                "environment": environment,
                "source": "redis",
                "co_changed_with": fast_result,
            }
        # Redis key absent (cold start) — fall through to DB
        logger.info("dep graph Redis miss for %s — falling back to DB", flag_key)

    return await builder.get_impact(flag_key, environment, days)


@app.post("/api/v1/correlate")
async def correlate_incident(incident_id: str, affected_service: str, incident_start_unix: int):
    """Correlate a PagerDuty incident with recent flag changes."""
    candidates = await app.state.correlator.correlate(
        incident_id=incident_id,
        affected_service=affected_service,
        incident_start_unix=incident_start_unix,
    )
    return {"incident_id": incident_id, "candidates": candidates}
