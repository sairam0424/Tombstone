from __future__ import annotations

import logging
import math
import time
from dataclasses import dataclass, field
from datetime import datetime
from urllib.parse import quote

import asyncpg

logger = logging.getLogger(__name__)

DEPGRAPH_TTL = 90 * 24 * 3600  # 90 days

# Matches the default already used by GET /api/v1/stale's project_id param
# (app/main.py) — the seed "Default" project's real UUID. Real multi-project
# deployments must pass an explicit project_id to every endpoint below; this
# default exists only so a single-project deployment (the only kind that
# exists today) keeps working with no client changes.
DEFAULT_PROJECT_ID = "00000000-0000-0000-0000-000000000001"


def depgraph_key(project_id: str, flag_key: str) -> str:
    """Redis key for a (project_id, flag_key) pair in the dependency graph.

    Percent-encodes both components (safe='') before joining with ':' --
    without this, a colon inside either raw component could make two
    DIFFERENT (project_id, flag_key) pairs format to the IDENTICAL Redis
    key string (e.g. project_id="X", flag_key="a:b" collides with
    project_id="X:a", flag_key="b"), letting a crafted flag_key/project_id
    pair read another tenant's data despite this being a project-scoped key.
    Neither field is validated to exclude colons at the HTTP layer, and
    flag_key is a customer-chosen string, so this can't be ruled out by
    convention alone.
    """
    return f"tombstone:depgraph:{quote(project_id, safe='')}:{quote(flag_key, safe='')}"


@dataclass
class GraphNode:
    flag_key: str
    enabled: bool
    rollout_pct: int
    state: str
    owner_id: str
    environment: str


@dataclass
class GraphEdge:
    source: str
    target: str
    weight: float
    co_change_count: int


@dataclass
class CausalGraph:
    nodes: list = field(default_factory=list)
    edges: list = field(default_factory=list)
    generated_at: int = 0
    event_count: int = 0


class DependencyGraphBuilder:
    COUPLING_WINDOW_SECONDS = 300
    LAMBDA = 0.1

    def __init__(self, db_url: str):
        self._db_url = db_url
        self._pool = None

    async def _get_pool(self):
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

    # ------------------------------------------------------------------
    # Redis sorted-set helpers (incremental path)
    # ------------------------------------------------------------------

    async def update_on_flag_change(
        self,
        flag_key: str,
        environment: str,
        changed_at: datetime,
        redis_client,
        project_id: str,
    ) -> None:
        """Called on every flag change event.  O(n_recent) where n = flags
        changed in the same environment within the last 300 seconds."""
        pool = await self._get_pool()
        changed_at_ts = changed_at.timestamp()
        window_start = changed_at_ts - self.COUPLING_WINDOW_SECONDS

        rows = await pool.fetch(
            """
            SELECT flag_key,
                   EXTRACT(EPOCH FROM created_at)::float AS ts
            FROM audit_log
            WHERE environment = $1
              AND project_id = $2
              AND flag_key IS NOT NULL
              AND flag_key != $3
              AND event_type IN ('flag_environment_updated','kill_switch_activated','flag_created')
              AND EXTRACT(EPOCH FROM created_at) >= $4
              AND EXTRACT(EPOCH FROM created_at) <= $5
            """,
            environment,
            project_id,
            flag_key,
            window_start,
            changed_at_ts + self.COUPLING_WINDOW_SECONDS,
        )

        if not rows:
            return

        pipe = redis_client.pipeline()
        src_redis_key = depgraph_key(project_id, flag_key)

        for row in rows:
            co_key = row["flag_key"]
            delta_minutes = abs(changed_at_ts - float(row["ts"])) / 60.0
            weight = math.exp(-self.LAMBDA * delta_minutes)

            dst_redis_key = depgraph_key(project_id, co_key)

            # GT flag: only update score if the new value is higher
            pipe.zadd(src_redis_key, {co_key: weight}, gt=True)
            pipe.expire(src_redis_key, DEPGRAPH_TTL)
            pipe.zadd(dst_redis_key, {flag_key: weight}, gt=True)
            pipe.expire(dst_redis_key, DEPGRAPH_TTL)

        await pipe.execute()

    async def get_impact_fast(
        self, flag_key: str, redis_client, project_id: str
    ) -> list[dict]:
        """O(log n) Redis lookup replacing the O(n²) DB scan for get_impact().

        Falls back to None if the key is missing (cold start) — caller
        must detect None and fall back to get_impact().
        """
        redis_key = depgraph_key(project_id, flag_key)
        # ZRANGEBYSCORE with scores, sorted by score desc
        raw = await redis_client.zrangebyscore(redis_key, 0, "+inf", withscores=True)
        if not raw:
            return None  # type: ignore[return-value]  # sentinel for cold-start

        # raw is list of (member, score); sort descending
        results = sorted(
            [
                {
                    "flag_key": member.decode()
                    if isinstance(member, bytes)
                    else member,
                    "weight": round(score, 4),
                }
                for member, score in raw
            ],
            key=lambda x: x["weight"],
            reverse=True,
        )
        return results

    async def rebuild_all(self, db_pool, redis_client) -> None:
        """Full rebuild from audit_log — runs on startup and can be scheduled
        daily at 2am.  Writes results into Redis sorted sets so the incremental
        path is always warm after the first startup.

        Scans every project's audit_log in one query (this is the "rebuild
        everything" job, not scoped to a single request's project), but two
        flags changed close together are only ever paired as "co-changed" if
        they belong to the SAME project — otherwise two unrelated tenants'
        same-named or coincidentally-timed changes would get cross-correlated
        into one shared Redis key and edge_map entry.
        """
        logger.info("dep graph rebuild: starting")
        to_unix = int(time.time())
        from_unix = to_unix - (90 * 86400)  # look back 90 days

        rows = await db_pool.fetch(
            """
            SELECT flag_key,
                   project_id,
                   EXTRACT(EPOCH FROM created_at)::bigint AS ts,
                   event_type
            FROM audit_log
            WHERE created_at >= to_timestamp($1)
              AND created_at <= to_timestamp($2)
              AND flag_key IS NOT NULL
              AND project_id IS NOT NULL
              AND event_type IN ('flag_environment_updated','kill_switch_activated','flag_created')
            ORDER BY created_at ASC
            """,
            float(from_unix),
            float(to_unix),
        )

        if not rows:
            logger.info("dep graph rebuild: no events found")
            return

        events = [(r["flag_key"], r["project_id"], int(r["ts"])) for r in rows]
        edge_map: dict[tuple, float] = {}

        for i, (flag_a, project_a, ts_a) in enumerate(events):
            for j in range(i + 1, len(events)):
                flag_b, project_b, ts_b = events[j]
                delta = ts_b - ts_a
                if delta > self.COUPLING_WINDOW_SECONDS:
                    break
                if flag_b == flag_a or project_b != project_a:
                    continue
                weight = math.exp(-self.LAMBDA * (delta / 60.0))
                key = (project_a, flag_a, flag_b)
                if key in edge_map:
                    edge_map[key] = max(edge_map[key], weight)
                else:
                    edge_map[key] = round(weight, 4)

        if not edge_map:
            logger.info("dep graph rebuild: no edges computed")
            return

        pipe = redis_client.pipeline()
        for (project_id, flag_a, flag_b), weight in edge_map.items():
            key_a = depgraph_key(project_id, flag_a)
            key_b = depgraph_key(project_id, flag_b)
            pipe.zadd(key_a, {flag_b: weight}, gt=True)
            pipe.expire(key_a, DEPGRAPH_TTL)
            pipe.zadd(key_b, {flag_a: weight}, gt=True)
            pipe.expire(key_b, DEPGRAPH_TTL)

        await pipe.execute()

        unique_flags = len({(p, f) for p, fa, fb in edge_map for f in (fa, fb)})
        logger.info(
            "dep graph rebuilt: %d flags, %d edges", unique_flags, len(edge_map)
        )

    # ------------------------------------------------------------------
    # Original full-graph build (visual graph endpoint — DB scan, unchanged)
    # ------------------------------------------------------------------

    async def build(
        self, project_id: str, environment: str, from_unix: int, to_unix: int
    ) -> CausalGraph:
        pool = await self._get_pool()
        rows = await pool.fetch(
            """
            SELECT flag_key,
                   EXTRACT(EPOCH FROM created_at)::bigint AS ts,
                   event_type
            FROM audit_log
            WHERE environment = $1
              AND project_id = $2
              AND created_at >= to_timestamp($3)
              AND created_at <= to_timestamp($4)
              AND flag_key IS NOT NULL
              AND event_type IN ('flag_environment_updated','kill_switch_activated','flag_created')
            ORDER BY created_at ASC
            """,
            environment,
            project_id,
            float(from_unix),
            float(to_unix),
        )
        if not rows:
            return CausalGraph(generated_at=to_unix, event_count=0)

        edge_map = {}
        events = [(r["flag_key"], r["ts"]) for r in rows]
        unique_flags = {r["flag_key"] for r in rows}

        for i, (flag_a, ts_a) in enumerate(events):
            for j in range(i + 1, len(events)):
                flag_b, ts_b = events[j]
                if flag_b == flag_a:
                    continue
                delta = ts_b - ts_a
                if delta > self.COUPLING_WINDOW_SECONDS:
                    break
                weight = math.exp(-self.LAMBDA * (delta / 60.0))
                key = (flag_a, flag_b)
                if key in edge_map:
                    edge_map[key]["weight"] = max(edge_map[key]["weight"], weight)
                    edge_map[key]["count"] += 1
                else:
                    edge_map[key] = {"weight": round(weight, 4), "count": 1}

        flag_rows = (
            await pool.fetch(
                """
            SELECT f.key, f.owner_id, f.state, fe.enabled, fe.rollout_pct
            FROM flags f
            LEFT JOIN flag_environments fe ON fe.flag_id = f.id AND fe.environment = $1
            WHERE f.project_id = $2 AND f.key = ANY($3::text[])
            """,
                environment,
                project_id,
                list(unique_flags),
            )
            if unique_flags
            else []
        )

        nodes = [
            GraphNode(
                flag_key=r["key"],
                enabled=bool(r["enabled"] or False),
                rollout_pct=int(r["rollout_pct"] or 0),
                state=r["state"] or "UNKNOWN",
                owner_id=r["owner_id"] or "unknown",
                environment=environment,
            )
            for r in flag_rows
        ]
        edges = [
            GraphEdge(
                source=k[0], target=k[1], weight=v["weight"], co_change_count=v["count"]
            )
            for k, v in sorted(edge_map.items(), key=lambda x: -x[1]["weight"])[:50]
        ]
        return CausalGraph(
            nodes=nodes, edges=edges, generated_at=to_unix, event_count=len(rows)
        )

    # ------------------------------------------------------------------
    # Original get_impact — kept as DB fallback
    # ------------------------------------------------------------------

    async def get_impact(
        self, flag_key: str, environment: str, project_id: str, days: int = 30
    ) -> dict:
        pool = await self._get_pool()
        to_unix = int(time.time())
        from_unix = to_unix - (days * 86400)
        rows = await pool.fetch(
            """
            WITH flag_changes AS (
                SELECT EXTRACT(EPOCH FROM created_at)::bigint AS ts
                FROM audit_log
                WHERE flag_key = $1 AND environment = $2 AND project_id = $3
                  AND created_at >= to_timestamp($4)
            ),
            nearby AS (
                SELECT a.flag_key,
                       COUNT(*) AS co_count,
                       AVG(ABS(EXTRACT(EPOCH FROM a.created_at) - fc.ts)) AS avg_apart
                FROM audit_log a CROSS JOIN flag_changes fc
                WHERE a.flag_key != $1 AND a.environment = $2 AND a.project_id = $3
                  AND ABS(EXTRACT(EPOCH FROM a.created_at) - fc.ts) < 300
                GROUP BY a.flag_key ORDER BY co_count DESC LIMIT 10
            )
            SELECT * FROM nearby
            """,
            flag_key,
            environment,
            project_id,
            float(from_unix),
        )
        return {
            "flag_key": flag_key,
            "environment": environment,
            "co_changed_with": [
                {
                    "flag_key": r["flag_key"],
                    "co_change_count": int(r["co_count"]),
                    "avg_seconds_apart": round(float(r["avg_apart"]), 1),
                }
                for r in rows
            ],
        }
