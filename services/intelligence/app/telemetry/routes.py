import logging
from typing import Any

from fastapi import APIRouter, Request

logger = logging.getLogger(__name__)

router = APIRouter(prefix="/api/v1/telemetry", tags=["telemetry"])


@router.post("/ingest")
async def ingest_events(events: list[dict[str, Any]], request: Request):
    """Ingest a batch of evaluation events into ClickHouse."""
    ch = getattr(request.app.state, "clickhouse", None)
    if ch is not None and ch._available:
        await ch.write_batch(events)
        return {"ingested": len(events)}
    return {"ingested": 0, "note": "ClickHouse unavailable — events discarded"}


@router.get("/stats/{flag_key}")
async def get_flag_stats(
    flag_key: str,
    request: Request,
    environment: str = "production",
    hours: int = 24,
):
    """Return evaluation stats for a flag over the last N hours."""
    ch = getattr(request.app.state, "clickhouse", None)
    if ch is None:
        return {"flag_key": flag_key, "total_evaluations": 0, "error_rate": 0.0, "unique_users": 0}
    stats = await ch.get_flag_stats(flag_key, environment, hours=hours)
    return {"flag_key": flag_key, "environment": environment, "hours": hours, **stats}


@router.get("/error-rate/{flag_key}")
async def get_error_rate(
    flag_key: str,
    request: Request,
    environment: str = "production",
    minutes: int = 10,
):
    """Return the error rate for a flag over the last N minutes."""
    ch = getattr(request.app.state, "clickhouse", None)
    if ch is None:
        return {"flag_key": flag_key, "error_rate": 0.0}
    rate = await ch.get_error_rate(flag_key, environment, minutes=minutes)
    return {"flag_key": flag_key, "environment": environment, "minutes": minutes, "error_rate": rate}
