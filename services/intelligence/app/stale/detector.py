from __future__ import annotations

import json
import logging
from dataclasses import dataclass
from datetime import datetime, timezone
from urllib.parse import quote_plus

import asyncpg
import httpx


logger = logging.getLogger(__name__)


def _telemetry_bucket_key(flag_key: str, environment: str, unix_hour: int) -> str:
    """Matches services/evaluator/internal/telemetry/aggregator.go's
    persistTelemetryBucket key format exactly (EVAL-3): telemetry:{flag}:
    {env}:hour:{unix_hour}. quote_plus(..., safe="") is this codebase's
    Python-side equivalent of Go's circuit.EscapeKeyComponent (both
    percent-encode ':' so a flag_key/environment pair can never collide
    with a different pair once joined -- the same bug class already found
    and fixed twice this session under the INT-2/EVAL-2 name) -- and, found
    by adversarial review of this PR, MUST be quote_plus specifically, not
    plain quote: Go's url.QueryEscape encodes a literal space as '+', but
    quote(..., safe="") encodes it as '%20' -- a real, reachable divergence
    since flags.key/environment have no character restriction anywhere in
    flag-api. Verified byte-identical to url.QueryEscape across all 128
    ASCII codepoints plus several multi-byte UTF-8 samples (space, '+',
    accented and CJK characters) via a paired Go/Python comparison script
    before landing this fix -- quote_plus is not a guess.
    """
    return f"telemetry:{quote_plus(flag_key, safe='')}:{quote_plus(environment, safe='')}:hour:{unix_hour}"


@dataclass
class StaleFlag:
    flag_key: str
    days_at_100_pct: float
    owner_id: str
    stale_score: float  # 0.0-1.0, higher = more stale
    recommended_action: str
    call_site_count: (
        int | None
    )  # None = unknown (ast-rewriter unreachable/unconfigured)
    recent_evaluation_count: (
        int  # real telemetry volume; 0 if no data/Redis unavailable
    )


class StaleFlagDetector:
    """
    Detects flags that are safe to archive using real signals:

    1. Rollout percentage has been 100% for 30+ days (always available --
       the DB query below).
    2. Live code references via ast-rewriter's scanner (INT-6). This is
       what actually gates ARCHIVE: a flag with ANY real code reference,
       or an UNKNOWN reference count (ast-rewriter unreachable, or
       AST_REWRITER_REPO_PATH not configured), can never be recommended
       for archival regardless of how long it's been at 100%. Unknown
       defaults to "assume still referenced" (the safe direction), never
       "assume safe to archive" -- mirrors blast.BlastRadiusResult's own
       Confidence="LOW" philosophy (EVAL-3): absence of evidence is not
       evidence of safety.
    3. Recent real evaluation volume from telemetry.Aggregator's hourly
       Redis buckets (EVAL-3), scoped to the SAME "production" environment
       the staleness query itself checks. Reported for operator
       visibility and as a cross-check: zero code references in the
       scanned repo but nonzero recent evaluations is a real contradiction
       (most likely the scan missed the actual serving repo, not proof the
       flag is safe to remove) -- also capped below ARCHIVE.

    The original plan's fourth signal, "zero override count in the last
    30 days", is deliberately NOT implemented here: this schema has no
    per-flag override-tracking table, and what "override" should even
    mean (a manual admin exemption? a targeting-rule edit? a break-glass
    use?) is an open design question, not yet decided -- disclosed as
    deferred, not silently dropped.
    """

    def __init__(
        self,
        db_url: str,
        redis_client=None,
        ast_rewriter_url: str | None = None,
        repo_path: str | None = None,
    ):
        self._db_url = db_url
        self._pool: asyncpg.Pool | None = None
        self._redis = redis_client
        self._ast_rewriter_url = (
            ast_rewriter_url.rstrip("/") if ast_rewriter_url else None
        )
        self._repo_path = repo_path

    async def detect(self, project_id: str) -> list[dict]:
        pool = await self._get_pool()

        rows = await pool.fetch(
            """
            SELECT f.key, f.owner_id,
                   fe.rollout_pct,
                   EXTRACT(EPOCH FROM (now() - fe.updated_at)) / 86400 AS days_since_update,
                   EXTRACT(EPOCH FROM (now() - f.created_at)) / 86400 AS days_since_creation
            FROM flags f
            JOIN flag_environments fe ON fe.flag_id = f.id
            WHERE f.project_id = $1
              AND f.state = 'ACTIVE'
              AND fe.environment = 'production'
              AND fe.rollout_pct = 100
              AND fe.updated_at < now() - INTERVAL '30 days'
            ORDER BY fe.updated_at ASC
            LIMIT 50
            """,
            project_id,
        )

        stale_flags = []
        for row in rows:
            flag_key = row["key"]
            days_at_100 = float(row["days_since_update"])
            stale_score = min(1.0, days_at_100 / 180)  # 0->30d=0.17, 90d=0.5, 180d+=1.0

            call_site_count = await self._count_call_sites(flag_key)
            recent_evaluation_count = await self._recent_evaluation_count(flag_key)

            action = "REVIEW"
            if days_at_100 > 30:
                action = "NOTIFY_OWNER"
            # ARCHIVE requires ALL three: long-stale, zero verified code
            # references, and zero recent real traffic -- the literal
            # "gate ARCHIVE on zero live references" INT-6 asks for.
            if (
                days_at_100 > 90
                and call_site_count == 0
                and recent_evaluation_count == 0
            ):
                action = "ARCHIVE"

            stale_flags.append(
                {
                    "flag_key": flag_key,
                    "owner_id": row["owner_id"],
                    "days_at_100_pct": round(days_at_100, 1),
                    "stale_score": round(stale_score, 3),
                    "recommended_action": action,
                    "call_site_count": call_site_count,
                    "recent_evaluation_count": recent_evaluation_count,
                }
            )

        return stale_flags

    async def _count_call_sites(self, flag_key: str) -> int | None:
        """Calls ast-rewriter's real, working, previously-orphaned scanner
        (services/ast-rewriter/internal/scanner/scanner.go) -- confirmed
        via investigation to have zero real callers anywhere before this.
        Returns None (unknown, never treated as zero) on any failure:
        unset config, unreachable service, or a real scan error.
        """
        if not self._ast_rewriter_url or not self._repo_path:
            return None
        try:
            async with httpx.AsyncClient(timeout=15.0) as client:
                resp = await client.post(
                    f"{self._ast_rewriter_url}/api/v1/scan",
                    json={"flag_key": flag_key, "repo_path": self._repo_path},
                )
                resp.raise_for_status()
                data = resp.json()
                return int(data["call_site_count"])
        except Exception as exc:  # noqa: BLE001 -- any failure means "unknown", not "zero"
            logger.warning("ast-rewriter scan failed for %s: %s", flag_key, exc)
            return None

    async def _recent_evaluation_count(self, flag_key: str) -> int:
        """Sums the last 24 hourly telemetry buckets telemetry.Aggregator
        writes (EVAL-3) for flag_key in "production" -- the same
        environment the staleness query above scopes to. Fails open to 0
        (Redis unavailable is treated the same as "no observed traffic",
        matching main.py's own Redis-optional, fail-open convention for
        every other Redis-backed feature in this service).
        """
        if self._redis is None:
            return 0
        try:
            now_hour = int(
                datetime.now(timezone.utc)
                .replace(minute=0, second=0, microsecond=0)
                .timestamp()
                // 3600
            )
            keys = [
                _telemetry_bucket_key(flag_key, "production", now_hour - i)
                for i in range(24)
            ]
            values = await self._redis.mget(keys)
            total = 0
            for value in values:
                if value is None:
                    continue
                bucket = json.loads(value)
                total += int(bucket.get("total", 0))
            return total
        except Exception as exc:  # noqa: BLE001 -- observational signal, never fatal
            logger.warning("telemetry read failed for %s: %s", flag_key, exc)
            return 0

    async def _get_pool(self) -> asyncpg.Pool:
        if self._pool is None:
            # statement_cache_size=0: pooler-safe under DATA-2's PgBouncer
            # (transaction pooling) — see search/retriever.py's initialize()
            # for the full explanation.
            self._pool = await asyncpg.create_pool(
                self._db_url,
                min_size=1,
                max_size=3,
                max_inactive_connection_lifetime=30.0,
                statement_cache_size=0,
            )
        return self._pool
