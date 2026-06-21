import math
from dataclasses import dataclass

import asyncpg


@dataclass
class CorrelationCandidate:
    flag_key: str
    environment: str
    changed_at: int           # unix timestamp of the flag change
    actor: str
    event_type: str
    minutes_before_incident: float
    recency_score: float       # exp(-lambda * minutes)
    total_score: float
    rollback_url: str


class IncidentCorrelator:
    """
    On every PagerDuty/OpsGenie incident open, queries the audit_log for
    flag state changes in the 30 minutes before the incident and scores them
    by recency. Returns top-3 ranked candidates.
    """

    LAMBDA = 0.1   # exponential decay factor for recency
    WINDOW_MINUTES = 30

    def __init__(self, db_url: str, pagerduty_token: str = ""):
        self._db_url = db_url
        self._pagerduty_token = pagerduty_token
        self._pool: asyncpg.Pool | None = None

    async def _get_pool(self) -> asyncpg.Pool:
        if self._pool is None:
            self._pool = await asyncpg.create_pool(self._db_url)
        return self._pool

    async def correlate(
        self,
        incident_id: str,
        affected_service: str,
        incident_start_unix: int,
        window_minutes: int = WINDOW_MINUTES,
    ) -> list[dict]:
        pool = await self._get_pool()
        window_start = incident_start_unix - (window_minutes * 60)

        rows = await pool.fetch(
            """
            SELECT flag_key, COALESCE(environment,'production') AS environment,
                   actor, event_type,
                   EXTRACT(EPOCH FROM created_at)::bigint AS changed_at
            FROM audit_log
            WHERE event_type IN ('flag_environment_updated', 'kill_switch_activated',
                                  'flag_environment_updated')
              AND EXTRACT(EPOCH FROM created_at) >= $1
              AND EXTRACT(EPOCH FROM created_at) <= $2
            ORDER BY created_at DESC
            LIMIT 20
            """,
            float(window_start),
            float(incident_start_unix),
        )

        candidates: list[CorrelationCandidate] = []
        for row in rows:
            minutes_before = (incident_start_unix - row["changed_at"]) / 60
            recency_score = math.exp(-self.LAMBDA * minutes_before)
            total_score = recency_score  # extend with overlap/history score in Phase 3

            candidates.append(
                CorrelationCandidate(
                    flag_key=row["flag_key"],
                    environment=row["environment"],
                    changed_at=row["changed_at"],
                    actor=row["actor"],
                    event_type=row["event_type"],
                    minutes_before_incident=minutes_before,
                    recency_score=recency_score,
                    total_score=total_score,
                    rollback_url=f"/api/v1/flags/{row['flag_key']}/kill",
                )
            )

        # Sort descending by total_score, return top 3
        candidates.sort(key=lambda c: c.total_score, reverse=True)
        return [
            {
                "flag_key": c.flag_key,
                "environment": c.environment,
                "changed_at": c.changed_at,
                "actor": c.actor,
                "event_type": c.event_type,
                "minutes_before_incident": round(c.minutes_before_incident, 1),
                "correlation_score": round(c.total_score, 3),
                "confidence": "HIGH" if c.total_score > 0.7 else "MEDIUM" if c.total_score > 0.3 else "LOW",
                "rollback_url": c.rollback_url,
            }
            for c in candidates[:3]
        ]
