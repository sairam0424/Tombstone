import asyncio
import json
import logging
from dataclasses import dataclass
from datetime import datetime, timezone
from typing import TYPE_CHECKING

from aiokafka import AIOKafkaConsumer

from app.anomaly.detector import AnomalyDetector

if TYPE_CHECKING:
    from app.search.embedding_sync import EmbeddingSyncService

logger = logging.getLogger(__name__)

TOPIC_FLAG_EVALUATED = "tombstone.flag.evaluated"
TOPIC_FLAG_CHANGED = "tombstone.flag.changed"
GROUP_ID = "intelligence-anomaly"

# Event types that constitute a "flag change" for dep-graph purposes
FLAG_CHANGE_EVENT_TYPES = frozenset({
    "flag_environment_updated",
    "kill_switch_activated",
    "flag_created",
})


@dataclass
class EvaluationEvent:
    flag_key: str
    environment: str
    is_error: bool
    error_count: int
    total_count: int


class TelemetryConsumer:
    """
    Consumes tombstone.flag.evaluated and tombstone.flag.changed events from Kafka.

    - tombstone.flag.evaluated  → feeds AnomalyDetector (error-rate windowing)
    - tombstone.flag.changed    → triggers EmbeddingSyncService for created/updated flags
                                  AND updates Redis dep-graph sorted sets incrementally

    Follows the Graph-Forge aiokafka pattern (manual commit, OTel headers).
    """

    def __init__(
        self,
        brokers: str,
        anomaly_detector: AnomalyDetector,
        embedding_sync: "EmbeddingSyncService | None" = None,
        graph_builder=None,
        redis_client=None,
    ):
        self._brokers = brokers
        self._detector = anomaly_detector
        self._embedding_sync = embedding_sync
        self._graph_builder = graph_builder
        self._redis_client = redis_client
        self._consumer: AIOKafkaConsumer | None = None
        self._window: dict[str, dict] = {}  # flag_key:env -> {errors, total}
        self._flush_interval = 10  # seconds

    async def run(self) -> None:
        topics = [TOPIC_FLAG_EVALUATED, TOPIC_FLAG_CHANGED]
        self._consumer = AIOKafkaConsumer(
            *topics,
            bootstrap_servers=self._brokers,
            group_id=GROUP_ID,
            enable_auto_commit=False,
            value_deserializer=lambda x: json.loads(x.decode("utf-8")),
            auto_offset_reset="latest",
        )
        await self._consumer.start()
        flush_task = asyncio.create_task(self._flush_loop())
        try:
            async for msg in self._consumer:
                if msg.topic == TOPIC_FLAG_CHANGED:
                    await self._handle_flag_change(msg.value)
                else:
                    await self._handle_evaluation(msg.value)
                await self._consumer.commit()
        except asyncio.CancelledError:
            pass
        finally:
            flush_task.cancel()
            await self._consumer.stop()

    async def _handle_evaluation(self, payload: dict) -> None:
        key = payload.get("flag_key", "")
        env = payload.get("environment", "production")
        window_key = f"{key}:{env}"
        if window_key not in self._window:
            self._window[window_key] = {"errors": 0, "total": 0}
        self._window[window_key]["total"] += 1
        if payload.get("is_error", False):
            self._window[window_key]["errors"] += 1

    async def _handle_flag_change(self, payload: dict) -> None:
        """Process a flag-change event.

        Performs two independent operations:
        1. Sync pgvector embedding when a flag is created or updated.
        2. Update the Redis dep-graph sorted sets incrementally.
        """
        event_type: str = payload.get("event_type", "")
        flag_key: str = payload.get("flag_key") or payload.get("key", "")
        environment = payload.get("environment", "production")

        # --- Embedding sync (pgvector search) ---
        if self._embedding_sync is not None and flag_key and event_type in {"flag.created", "flag.updated"}:
            try:
                await self._embedding_sync.on_flag_event(
                    event_type=event_type,
                    flag_key=flag_key,
                    name=payload.get("name", ""),
                    description=payload.get("description", ""),
                    tags=payload.get("tags", []),
                )
                logger.debug("Embedding sync triggered for %s (event=%s)", flag_key, event_type)
            except Exception as exc:
                logger.warning("Embedding sync failed for %s: %s", flag_key, exc)

        # --- Dep-graph incremental update (Redis sorted sets) ---
        if self._graph_builder is not None and self._redis_client is not None:
            if event_type not in FLAG_CHANGE_EVENT_TYPES or not flag_key:
                return

            # changed_at: prefer explicit field, fall back to now
            changed_at_raw = payload.get("changed_at") or payload.get("created_at")
            if changed_at_raw:
                try:
                    changed_at = datetime.fromisoformat(str(changed_at_raw).replace("Z", "+00:00"))
                except (ValueError, TypeError):
                    changed_at = datetime.now(tz=timezone.utc)
            else:
                changed_at = datetime.now(tz=timezone.utc)

            try:
                await self._graph_builder.update_on_flag_change(
                    flag_key=flag_key,
                    environment=environment,
                    changed_at=changed_at,
                    redis_client=self._redis_client,
                )
            except Exception as exc:
                logger.warning(
                    "dep graph incremental update failed for %s: %s", flag_key, exc
                )

    async def _flush_loop(self) -> None:
        while True:
            await asyncio.sleep(self._flush_interval)
            snapshot = self._window.copy()
            self._window.clear()
            for key, counts in snapshot.items():
                flag_key = key.split(":")[0]
                self._detector.record(flag_key, counts["errors"], counts["total"])
