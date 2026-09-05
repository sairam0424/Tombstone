"""
Production-hardened ClickHouse writer with:
- Batched inserts (max 1000 events or 5s, whichever comes first)
- Exponential backoff retry (3 attempts: 1s, 2s, 4s)
- Dead-letter queue (Redis list: tombstone:clickhouse:dlq)
- Background DLQ replayer (every 60s)
- Graceful shutdown: flush pending batch before exit

Legacy synchronous read helpers (get_error_rate, get_flag_stats, create_tables)
are preserved for backward compatibility and continue to use clickhouse_driver.
"""

import asyncio
import importlib
import json
import logging
import time
from collections import deque
from dataclasses import dataclass, field
from datetime import datetime, timezone

try:
    import httpx as _httpx
except ImportError:  # pragma: no cover
    _httpx = None  # type: ignore[assignment]

logger = logging.getLogger(__name__)

BATCH_MAX = 1000
BATCH_TIMEOUT_S = 5.0
DLQ_KEY = "tombstone:clickhouse:dlq"
DLQ_MAX = 10_000  # cap DLQ size to prevent unbounded Redis growth

# ClickHouse schema (run once in ClickHouse):
# CREATE TABLE tombstone_evaluations (
#   flag_key    String,
#   environment String,
#   user_hash   String,
#   result      String,
#   reason      String,
#   latency_ms  Float64,
#   ts          DateTime
# ) ENGINE = MergeTree() ORDER BY (flag_key, ts);


@dataclass
class ClickHouseWriter:
    """
    Dual-mode ClickHouse client:
      - Async batch writer (httpx HTTP interface) — record() / start() / shutdown()
      - Sync query helpers  (clickhouse_driver)   — get_error_rate() / get_flag_stats()
    """

    host: str
    port: int = 9000
    http_port: int = 8123
    database: str = "tombstone"
    user: str = "default"
    password: str = ""
    redis_client: object = field(default=None, repr=False)

    # --- internal batch state (not dataclass-managed) ---
    def __post_init__(self) -> None:
        self._batch: deque = deque()
        self._last_flush: float = time.monotonic()
        self._lock: asyncio.Lock = asyncio.Lock()
        self._flush_task: asyncio.Task | None = None
        self._dlq_task: asyncio.Task | None = None

    # ------------------------------------------------------------------
    # Public lifecycle
    # ------------------------------------------------------------------

    async def start(self) -> None:
        """Start background flush + DLQ replay tasks."""
        self._flush_task = asyncio.create_task(self._flush_loop(), name="ch-flush")
        self._dlq_task = asyncio.create_task(self._dlq_replayer(), name="ch-dlq")
        logger.info("ClickHouseWriter background tasks started")

    async def shutdown(self) -> None:
        """Flush remaining batch before exit, then cancel background tasks."""
        for task in (self._flush_task, self._dlq_task):
            if task:
                task.cancel()
        # Drain any buffered events
        async with self._lock:
            remaining = list(self._batch)
            self._batch.clear()
        if remaining:
            logger.info(
                "ClickHouseWriter shutdown: flushing %d buffered events", len(remaining)
            )
            await self._write_with_retry(remaining)
        logger.info("ClickHouseWriter shutdown complete")

    # ------------------------------------------------------------------
    # Public write API
    # ------------------------------------------------------------------

    async def record(
        self,
        flag_key: str,
        environment: str,
        user_hash: str,
        result: str,
        reason: str,
        latency_ms: float,
    ) -> None:
        """Non-blocking: append one evaluation event to the in-memory batch."""
        async with self._lock:
            self._batch.append(
                {
                    "flag_key": flag_key,
                    "environment": environment,
                    "user_hash": user_hash,
                    "result": result,
                    "reason": reason,
                    "latency_ms": latency_ms,
                    "ts": datetime.now(timezone.utc).isoformat(timespec="seconds"),
                }
            )

    async def write_batch(self, events: list[dict]) -> None:
        """Direct batch write (used by the /telemetry/ingest route)."""
        if not events:
            return
        await self._write_with_retry(events)

    # ------------------------------------------------------------------
    # Background tasks
    # ------------------------------------------------------------------

    async def _flush_loop(self) -> None:
        """Periodically flush the in-memory batch to ClickHouse."""
        while True:
            await asyncio.sleep(1.0)
            batch: list = []
            async with self._lock:
                age = time.monotonic() - self._last_flush
                if len(self._batch) >= BATCH_MAX or (
                    self._batch and age >= BATCH_TIMEOUT_S
                ):
                    batch = list(self._batch)
                    self._batch.clear()
                    self._last_flush = time.monotonic()
            if batch:
                await self._write_with_retry(batch)

    async def _dlq_replayer(self) -> None:
        """
        Every 60 s, pop one DLQ item and retry.
        On failure the item lands back in the DLQ via _write_with_retry.
        """
        while True:
            await asyncio.sleep(60)
            if not self._redis_available():
                continue
            try:
                item = await self.redis_client.rpop(DLQ_KEY)  # type: ignore[union-attr]
                if item:
                    data = json.loads(item)
                    logger.info(
                        "DLQ replayer: retrying batch of %d events",
                        len(data.get("batch", [])),
                    )
                    await self._write_with_retry(data["batch"])
            except Exception as exc:
                logger.warning("DLQ replayer error: %s", exc)

    # ------------------------------------------------------------------
    # Retry + DLQ helpers
    # ------------------------------------------------------------------

    async def _write_with_retry(self, batch: list, attempt: int = 0) -> None:
        """3 attempts with exponential backoff (1 s, 2 s, 4 s). On final failure -> DLQ."""
        try:
            await self._insert(batch)
        except Exception as exc:
            if attempt < 3:
                delay = 2**attempt  # 1 s, 2 s, 4 s
                logger.warning(
                    "ClickHouse insert failed (attempt %d/3, retry in %ds): %s",
                    attempt + 1,
                    delay,
                    exc,
                )
                await asyncio.sleep(delay)
                await self._write_with_retry(batch, attempt + 1)
            else:
                logger.error(
                    "ClickHouse insert failed after 3 attempts — routing to DLQ: %s",
                    exc,
                )
                await self._to_dlq(batch, str(exc))

    async def _to_dlq(self, batch: list, error: str) -> None:
        """Push failed batch onto the Redis dead-letter queue."""
        if not self._redis_available():
            logger.error("DLQ unavailable (no Redis): %d events dropped", len(batch))
            return
        try:
            payload = json.dumps({"batch": batch, "error": error})
            await self.redis_client.lpush(DLQ_KEY, payload)  # type: ignore[union-attr]
            await self.redis_client.ltrim(DLQ_KEY, 0, DLQ_MAX - 1)  # type: ignore[union-attr]
            logger.info("DLQ: %d events queued (error: %s)", len(batch), error)
        except Exception as exc:
            logger.error(
                "Failed to write to DLQ: %s — %d events dropped", exc, len(batch)
            )

    # ------------------------------------------------------------------
    # ClickHouse HTTP insert
    # ------------------------------------------------------------------

    async def _insert(self, batch: list) -> None:
        """
        INSERT batch into ClickHouse tombstone_evaluations via the HTTP interface.

        Row format matches the tombstone_evaluations table schema:
          flag_key String, environment String, user_hash String,
          result String, reason String, latency_ms Float64, ts DateTime
        """
        if not self.host:
            return

        if _httpx is None:
            # Fall back to clickhouse_driver if httpx is not installed
            await self._insert_via_driver(batch)
            return

        # Build TSV body — ClickHouse TabSeparated format
        rows = "\n".join(
            "\t".join(
                [
                    str(e.get("flag_key", "")),
                    str(e.get("environment", "")),
                    str(e.get("user_hash", "")),
                    str(e.get("result", "")),
                    str(e.get("reason", "")),
                    str(float(e.get("latency_ms", 0.0))),
                    # ClickHouse DateTime expects 'YYYY-MM-DD HH:MM:SS'
                    str(e.get("ts", ""))
                    .replace("T", " ")
                    .replace("+00:00", "")
                    .split(".")[0],
                ]
            )
            for e in batch
        )

        url = (
            f"http://{self.host}:{self.http_port}/"
            f"?query=INSERT+INTO+{self.database}.tombstone_evaluations"
            f"+FORMAT+TabSeparated"
        )
        auth = (self.user, self.password) if self.password else None

        async with _httpx.AsyncClient(timeout=10.0) as client:
            resp = await client.post(
                url,
                content=rows.encode(),
                auth=auth,
            )
            if resp.status_code >= 400:
                raise RuntimeError(
                    f"ClickHouse HTTP {resp.status_code}: {resp.text[:200]}"
                )

        logger.debug("ClickHouse: inserted %d rows via HTTP", len(batch))

    async def _insert_via_driver(self, batch: list) -> None:
        """Fallback insert using clickhouse_driver (sync, run in thread)."""
        if not self._available:
            return

        def _sync() -> None:
            client = self._get_client()
            rows = [
                {
                    "flag_key": e.get("flag_key", ""),
                    "environment": e.get("environment", ""),
                    "user_hash": e.get("user_hash", ""),
                    "result": e.get("result", ""),
                    "reason": e.get("reason", ""),
                    "latency_ms": float(e.get("latency_ms", 0.0)),
                    "ts": e.get("ts"),
                }
                for e in batch
            ]
            client.execute(
                f"INSERT INTO {self.database}.tombstone_evaluations VALUES",
                rows,
            )

        await asyncio.to_thread(_sync)

    # ------------------------------------------------------------------
    # Legacy helpers (clickhouse_driver — kept for backward compatibility)
    #
    # INT-4: get_error_rate/get_flag_stats used to query evaluation_events
    # (a table nothing ever wrote to -- see create_tables' comment) via its
    # is_error/user_id_hash columns; fixed to query the table that's
    # actually written, tombstone_evaluations, which has no boolean
    # is_error column at all. `reason = 'ERROR'` is the only available
    # error signal on that table -- it matches the EvaluationReason enum
    # SDK callers report (OFF/FALLTHROUGH/RULE_MATCH/ERROR), but it means
    # "this evaluation itself failed" (e.g. flag not found/config error),
    # NOT "the flag's returned value caused a downstream failure" -- the
    # write path has no signal for the latter at all.
    #
    # This is a CONVENTION, not an enforced guarantee (found by
    # adversarial review of PR #210): POST /api/v1/telemetry/ingest (the
    # only real write path today) accepts `events: list[dict[str, Any]]`
    # with zero Pydantic/schema validation, and _insert()/
    # _insert_via_driver() read `reason` via `e.get("reason", "")` --  a
    # caller that omits the field, or uses a different casing/vocabulary
    # ("error"/"Error"/"FAILED"), lands rows with reason values that never
    # match the exact literal 'ERROR' (ClickHouse string comparison is
    # case-sensitive), silently under-reporting or zero-reporting the
    # error rate with no exception or warning distinguishing that from
    # "genuinely zero errors". No SDK or service in this repo actually
    # calls this endpoint today (ClickHouseWriter.record(), which takes a
    # typed reason argument, has zero callers repo-wide), so this gap is
    # real but currently unreachable in practice -- flagged for whoever
    # builds a real caller, not fixed here (would need a genuine schema/
    # validation decision on the ingest endpoint, out of INT-4's scope).
    # ------------------------------------------------------------------

    @property
    def _available(self) -> bool:
        try:
            importlib.import_module("clickhouse_driver")
            return bool(self.host)
        except ImportError:
            return False

    def _redis_available(self) -> bool:
        return self.redis_client is not None

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

        def _create() -> None:
            client = self._get_client()
            client.execute(f"CREATE DATABASE IF NOT EXISTS {self.database}")
            # INT-4: evaluation_events used to be created here too, but it is
            # a phantom table -- nothing anywhere in this codebase has ever
            # written to it (confirmed via a repo-wide grep for INSERT
            # references). _insert()/_insert_via_driver() above have always
            # written to tombstone_evaluations only. get_error_rate() and
            # get_flag_stats() below used to read evaluation_events instead
            # (a pre-existing bug, now fixed to read tombstone_evaluations,
            # the table that's actually populated) -- so evaluation_events
            # is now fully orphaned schema with no reader or writer.
            # Deliberately not created anymore; a real deployment with a
            # pre-existing evaluation_events table from before this fix is
            # unaffected (this only stops ensuring it exists on fresh setups).
            client.execute(
                f"""
                CREATE TABLE IF NOT EXISTS {self.database}.tombstone_evaluations (
                    flag_key    String,
                    environment String,
                    user_hash   String,
                    result      String,
                    reason      String,
                    latency_ms  Float64,
                    ts          DateTime
                )
                ENGINE = MergeTree()
                ORDER BY (flag_key, ts)
                """
            )
            logger.info("ClickHouse table tombstone_evaluations ready")

        await asyncio.to_thread(_create)

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
                    countIf(reason = 'ERROR') AS errors,
                    count() AS total
                FROM {self.database}.tombstone_evaluations
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
                    countIf(reason = 'ERROR')       AS error_count,
                    uniqExact(user_hash)             AS unique_users
                FROM {self.database}.tombstone_evaluations
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
