from __future__ import annotations
import math
import time
from dataclasses import dataclass, field
import asyncpg

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
            self._pool = await asyncpg.create_pool(self._db_url)
        return self._pool

    async def build(self, environment: str, from_unix: int, to_unix: int) -> CausalGraph:
        pool = await self._get_pool()
        rows = await pool.fetch(
            """
            SELECT flag_key,
                   EXTRACT(EPOCH FROM created_at)::bigint AS ts,
                   event_type
            FROM audit_log
            WHERE environment = $1
              AND created_at >= to_timestamp($2)
              AND created_at <= to_timestamp($3)
              AND flag_key IS NOT NULL
              AND event_type IN ('flag_environment_updated','kill_switch_activated','flag_created')
            ORDER BY created_at ASC
            """,
            environment, float(from_unix), float(to_unix),
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

        flag_rows = await pool.fetch(
            """
            SELECT f.key, f.owner_id, f.state, fe.enabled, fe.rollout_pct
            FROM flags f
            LEFT JOIN flag_environments fe ON fe.flag_id = f.id AND fe.environment = $1
            WHERE f.key = ANY($2::text[])
            """,
            environment, list(unique_flags),
        ) if unique_flags else []

        nodes = [
            GraphNode(
                flag_key=r["key"], enabled=bool(r["enabled"] or False),
                rollout_pct=int(r["rollout_pct"] or 0), state=r["state"] or "UNKNOWN",
                owner_id=r["owner_id"] or "unknown", environment=environment,
            ) for r in flag_rows
        ]
        edges = [
            GraphEdge(source=k[0], target=k[1], weight=v["weight"], co_change_count=v["count"])
            for k, v in sorted(edge_map.items(), key=lambda x: -x[1]["weight"])[:50]
        ]
        return CausalGraph(nodes=nodes, edges=edges, generated_at=to_unix, event_count=len(rows))

    async def get_impact(self, flag_key: str, environment: str, days: int = 30) -> dict:
        pool = await self._get_pool()
        to_unix = int(time.time())
        from_unix = to_unix - (days * 86400)
        rows = await pool.fetch(
            """
            WITH flag_changes AS (
                SELECT EXTRACT(EPOCH FROM created_at)::bigint AS ts
                FROM audit_log
                WHERE flag_key = $1 AND environment = $2
                  AND created_at >= to_timestamp($3)
            ),
            nearby AS (
                SELECT a.flag_key,
                       COUNT(*) AS co_count,
                       AVG(ABS(EXTRACT(EPOCH FROM a.created_at) - fc.ts)) AS avg_apart
                FROM audit_log a CROSS JOIN flag_changes fc
                WHERE a.flag_key != $1 AND a.environment = $2
                  AND ABS(EXTRACT(EPOCH FROM a.created_at) - fc.ts) < 300
                GROUP BY a.flag_key ORDER BY co_count DESC LIMIT 10
            )
            SELECT * FROM nearby
            """,
            flag_key, environment, float(from_unix),
        )
        return {
            "flag_key": flag_key,
            "environment": environment,
            "co_changed_with": [
                {"flag_key": r["flag_key"], "co_change_count": int(r["co_count"]), "avg_seconds_apart": round(float(r["avg_apart"]), 1)}
                for r in rows
            ],
        }
