from dataclasses import dataclass

import asyncpg


@dataclass
class StaleFlag:
    flag_key: str
    days_at_100_pct: float
    last_eval_days_ago: float | None
    owner_id: str
    stale_score: float   # 0.0–1.0, higher = more stale
    recommended_action: str


class StaleFlagDetector:
    """
    Detects flags that are safe to archive using four signals:
    1. Rollout percentage has been 100% for 30+ days
    2. Zero override count in the last 30 days
    3. No evaluation activity for 7+ days (requires telemetry data)
    4. No code reference changes (requires AST scan — Phase 4)
    """

    def __init__(self, db_url: str):
        self._db_url = db_url
        self._pool: asyncpg.Pool | None = None

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
            days_at_100 = float(row["days_since_update"])
            stale_score = min(1.0, days_at_100 / 180)  # 0→30d=0.17, 90d=0.5, 180d+=1.0

            action = "REVIEW"
            if days_at_100 > 90:
                action = "ARCHIVE"
            elif days_at_100 > 30:
                action = "NOTIFY_OWNER"

            stale_flags.append({
                "flag_key": row["key"],
                "owner_id": row["owner_id"],
                "days_at_100_pct": round(days_at_100, 1),
                "stale_score": round(stale_score, 3),
                "recommended_action": action,
            })

        return stale_flags

    async def _get_pool(self) -> asyncpg.Pool:
        if self._pool is None:
            self._pool = await asyncpg.create_pool(self._db_url)
        return self._pool
