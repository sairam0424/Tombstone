import asyncio
import importlib
import logging
import os
from dataclasses import dataclass

logger = logging.getLogger(__name__)


@dataclass
class ClickHouseWriter:
    host: str
    port: int = 9000
    database: str = "tombstone"
    user: str = "default"
    password: str = ""

    @property
    def _available(self) -> bool:
        try:
            importlib.import_module("clickhouse_driver")
            return bool(self.host)
        except ImportError:
            return False

    def _get_client(self):
        clickhouse_driver = importlib.import_module("clickhouse_driver")
        return clickhouse_driver.Client(
            host=self.host,
            port=self.port,
            database=self.database,
            user=self.user,
            password=self.password,
        )

    async def create_tables(self) -> None:
        if not self._available:
            logger.warning(
                "clickhouse-driver not installed or CLICKHOUSE_HOST not set — "
                "ClickHouse telemetry pipeline disabled"
            )
            return

        def _create():
            client = self._get_client()
            client.execute(f"CREATE DATABASE IF NOT EXISTS {self.database}")
            client.execute(
                f"""
                CREATE TABLE IF NOT EXISTS {self.database}.evaluation_events (
                    flag_key        String,
                    environment     String,
                    variation       String,
                    reason          String,
                    is_error        UInt8,
                    user_id_hash    String,
                    ts              DateTime
                )
                ENGINE = MergeTree()
                ORDER BY (flag_key, environment, ts)
                PARTITION BY toYYYYMM(ts)
                """
            )
            logger.info("ClickHouse table evaluation_events ready")

        await asyncio.to_thread(_create)

    async def write_batch(self, events: list[dict]) -> None:
        if not self._available or not events:
            return

        def _insert():
            client = self._get_client()
            rows = [
                {
                    "flag_key": e.get("flag_key", ""),
                    "environment": e.get("environment", ""),
                    "variation": e.get("variation", ""),
                    "reason": e.get("reason", ""),
                    "is_error": int(bool(e.get("is_error", False))),
                    "user_id_hash": e.get("user_id_hash", ""),
                    "ts": e.get("ts"),
                }
                for e in events
            ]
            client.execute(
                f"INSERT INTO {self.database}.evaluation_events VALUES",
                rows,
            )

        await asyncio.to_thread(_insert)

    async def get_error_rate(
        self, flag_key: str, environment: str, minutes: int = 10
    ) -> float:
        if not self._available:
            return 0.0

        def _query() -> float:
            client = self._get_client()
            result = client.execute(
                f"""
                SELECT
                    countIf(is_error = 1) AS errors,
                    count() AS total
                FROM {self.database}.evaluation_events
                WHERE
                    flag_key = %(flag_key)s
                    AND environment = %(environment)s
                    AND ts >= now() - INTERVAL %(minutes)s MINUTE
                """,
                {"flag_key": flag_key, "environment": environment, "minutes": minutes},
            )
            if not result or result[0][1] == 0:
                return 0.0
            errors, total = result[0]
            return round(errors / total, 6)

        try:
            return await asyncio.to_thread(_query)
        except Exception as exc:
            logger.warning("ClickHouse get_error_rate failed: %s", exc)
            return 0.0

    async def get_flag_stats(
        self, flag_key: str, environment: str, hours: int = 24
    ) -> dict:
        if not self._available:
            return {"total_evaluations": 0, "error_rate": 0.0, "unique_users": 0}

        def _query() -> dict:
            client = self._get_client()
            result = client.execute(
                f"""
                SELECT
                    count()                         AS total_evaluations,
                    countIf(is_error = 1)           AS error_count,
                    uniqExact(user_id_hash)         AS unique_users
                FROM {self.database}.evaluation_events
                WHERE
                    flag_key = %(flag_key)s
                    AND environment = %(environment)s
                    AND ts >= now() - INTERVAL %(hours)s HOUR
                """,
                {"flag_key": flag_key, "environment": environment, "hours": hours},
            )
            if not result or result[0][0] == 0:
                return {"total_evaluations": 0, "error_rate": 0.0, "unique_users": 0}
            total, errors, unique = result[0]
            return {
                "total_evaluations": total,
                "error_rate": round(errors / total, 6) if total else 0.0,
                "unique_users": unique,
            }

        try:
            return await asyncio.to_thread(_query)
        except Exception as exc:
            logger.warning("ClickHouse get_flag_stats failed: %s", exc)
            return {"total_evaluations": 0, "error_rate": 0.0, "unique_users": 0}
