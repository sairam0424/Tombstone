import asyncio
import json
import logging
from abc import ABC, abstractmethod
from dataclasses import dataclass
from datetime import datetime, timezone
from typing import TYPE_CHECKING

try:
    from aiokafka import AIOKafkaConsumer
except ImportError:  # pragma: no cover — aiokafka not installed in test env
    AIOKafkaConsumer = None  # type: ignore[assignment,misc]

from app.anomaly.detector import AnomalyDetector

if TYPE_CHECKING:
    from app.search.embedding_sync import EmbeddingSyncService

logger = logging.getLogger(__name__)


class EventConsumer(ABC):
    """Abstract base for all event consumer backends."""

    @abstractmethod
    async def start(self) -> None:
        """Initialize connections and consumer groups."""
        ...

    @abstractmethod
    async def stop(self) -> None:
        """Gracefully shut down and release resources."""
        ...

    @abstractmethod
    async def run(self) -> None:
        """Run the consumer loop until cancelled."""
        ...

    def __aiter__(self):  # type: ignore[override]
        """Protocol compliance — run() drives consumption; aiter is unused."""
        return iter([])


class KafkaEventConsumer(EventConsumer):
    """
    Thin wrapper around the existing TelemetryConsumer.
    Preserves all original Kafka behaviour unchanged.
    """

    def __init__(
        self,
        brokers: str,
        anomaly_detector,
        embedding_sync=None,
        graph_builder=None,
        redis_client=None,
    ) -> None:
        self._inner = TelemetryConsumer(
            brokers=brokers,
            anomaly_detector=anomaly_detector,
            embedding_sync=embedding_sync,
            graph_builder=graph_builder,
            redis_client=redis_client,
        )

    async def start(self) -> None:
        pass  # TelemetryConsumer.run() handles its own startup

    async def stop(self) -> None:
        if self._inner._consumer is not None:
            await self._inner._consumer.stop()

    def __aiter__(self):
        # Not used — KafkaEventConsumer drives itself via run()
        return iter([])

    async def run(self):
        """Delegate to the existing TelemetryConsumer.run() loop."""
        await self._inner.run()

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


class RedisStreamsEventConsumer(EventConsumer):
    """
    Reads from tombstone:stream:{environment} Redis Streams via XREADGROUP.
    Replaces aiokafka for Fly.io free-tier deployment.
    Consumer group: intelligence-worker  (distinct from gateway's gateway-workers).
    At-least-once delivery via XACK + Pending Entries List.
    """

    _GROUP = "intelligence-worker"
    _BLOCK_MS = 1000   # 1s block timeout — yields control to event loop
    _COUNT = 10        # messages per XREADGROUP call

    def __init__(
        self,
        redis_url: str,
        anomaly_detector,
        environments: list[str] | None = None,
        embedding_sync=None,
        graph_builder=None,
    ) -> None:
        self._redis_url = redis_url
        self._detector = anomaly_detector
        self._environments = environments or ["production"]
        self._embedding_sync = embedding_sync
        self._graph_builder = graph_builder
        self._redis = None
        self._running = False
        # Build stream keys from environment names — must match flag-api format
        self._streams = [f"tombstone:stream:{env}" for env in self._environments]

    async def start(self) -> None:
        import redis.asyncio as aioredis
        if self._redis is None:
            self._redis = aioredis.from_url(self._redis_url, decode_responses=True)
        # Create consumer groups idempotently (BUSYGROUP = already exists = OK)
        for stream in self._streams:
            try:
                await self._redis.xgroup_create(
                    stream, self._GROUP, id="$", mkstream=True
                )
                logger.info("RedisStreamsEventConsumer: created group %s on %s", self._GROUP, stream)
            except Exception as exc:
                if "BUSYGROUP" in str(exc):
                    logger.debug("RedisStreamsEventConsumer: group %s already exists on %s", self._GROUP, stream)
                else:
                    logger.warning("RedisStreamsEventConsumer: xgroup_create error on %s: %s", stream, exc)

    async def stop(self) -> None:
        self._running = False
        if self._redis is not None:
            await self._redis.aclose()
            self._redis = None

    def __aiter__(self):
        return self

    async def __anext__(self):
        raise StopAsyncIteration  # run() drives itself; __aiter__ is for protocol compliance

    async def run(self) -> None:
        """
        Infinite loop: XREADGROUP -> dispatch to detector/graph_builder -> XACK.
        Mirrors TelemetryConsumer.run() semantics so main.py can treat both identically.
        """
        import os
        consumer_name = f"intelligence-{os.environ.get('FLY_MACHINE_ID', 'local')}"
        self._running = True
        logger.info("RedisStreamsEventConsumer: starting on streams %s as %s", self._streams, consumer_name)

        # Window buffer: same pattern as TelemetryConsumer._window
        window: dict[str, dict] = {}   # "flag_key:env" -> {"errors": int, "total": int}
        flush_interval = 10.0
        last_flush = asyncio.get_event_loop().time()

        while self._running:
            try:
                # Read from all streams in one call
                stream_args = {s: ">" for s in self._streams}
                results = await self._redis.xreadgroup(
                    self._GROUP,
                    consumer_name,
                    stream_args,
                    count=self._COUNT,
                    block=self._BLOCK_MS,
                )

                for stream_key, messages in (results or []):
                    for msg_id, data in messages:
                        await self._dispatch(data, window)
                        await self._redis.xack(stream_key, self._GROUP, msg_id)

                # Flush detector every 10s (same cadence as TelemetryConsumer._flush_loop)
                now = asyncio.get_event_loop().time()
                if now - last_flush >= flush_interval:
                    await self._flush(window)
                    window.clear()
                    last_flush = now

            except asyncio.CancelledError:
                break
            except Exception as exc:
                logger.warning("RedisStreamsEventConsumer: read error: %s", exc)
                await asyncio.sleep(1.0)

        await self.stop()

    async def _dispatch(self, data: dict, window: dict) -> None:
        """Route a stream message to the right handler based on event type."""
        event_type = data.get("event", "")

        # flag.evaluated -> accumulate in window for anomaly detection
        if event_type in ("flag_evaluated", "FALLTHROUGH", "RULE_MATCH", "OFF", "TARGET_MATCH"):
            flag_key = data.get("flag_key", "")
            environment = data.get("environment", "production")
            is_error = data.get("is_error") in ("true", "True", "1", True)
            if flag_key:
                wk = f"{flag_key}:{environment}"
                if wk not in window:
                    window[wk] = {"errors": 0, "total": 0}
                window[wk]["total"] += 1
                if is_error:
                    window[wk]["errors"] += 1

        # flag.created or flag.updated -> trigger embedding sync
        elif event_type in ("flag_created", "flag_environment_updated", "kill_switch_activated"):
            flag_key = data.get("flag_key", "")
            if flag_key and self._embedding_sync is not None:
                payload_str = data.get("payload", "{}")
                try:
                    import json as _json
                    payload = _json.loads(payload_str) if isinstance(payload_str, str) else payload_str
                    await self._embedding_sync.on_flag_event(
                        event_type=event_type,
                        flag_key=flag_key,
                        name=payload.get("name", flag_key),
                        description=payload.get("description", ""),
                        tags=payload.get("tags", []),
                    )
                except Exception as exc:
                    logger.warning("RedisStreamsEventConsumer: embedding sync failed for %s: %s", flag_key, exc)

            # Also update dep graph
            if flag_key and self._graph_builder is not None and self._redis is not None:
                try:
                    from datetime import datetime, timezone
                    environment = data.get("environment", "production")
                    await self._graph_builder.update_on_flag_change(
                        flag_key=flag_key,
                        environment=environment,
                        changed_at=datetime.now(timezone.utc),
                        redis_client=self._redis,
                    )
                except Exception as exc:
                    logger.warning("RedisStreamsEventConsumer: graph update failed: %s", exc)

    async def _flush(self, window: dict) -> None:
        """Flush accumulated window counts to the anomaly detector."""
        for wk, counts in window.items():
            if counts["total"] > 0:
                flag_key = wk.split(":")[0]
                self._detector.record(flag_key, counts["errors"], counts["total"])


def create_consumer(backend: str, **kwargs) -> "EventConsumer":
    """
    Factory for event consumer backends.

    backend='kafka' kwargs: brokers, anomaly_detector, embedding_sync, graph_builder, redis_client
    backend='redis' kwargs: redis_url, anomaly_detector, environments, embedding_sync, graph_builder
    """
    if backend == "kafka":
        return KafkaEventConsumer(
            brokers=kwargs["brokers"],
            anomaly_detector=kwargs["anomaly_detector"],
            embedding_sync=kwargs.get("embedding_sync"),
            graph_builder=kwargs.get("graph_builder"),
            redis_client=kwargs.get("redis_client"),
        )
    if backend == "redis":
        return RedisStreamsEventConsumer(
            redis_url=kwargs["redis_url"],
            anomaly_detector=kwargs["anomaly_detector"],
            environments=kwargs.get("environments", ["production"]),
            embedding_sync=kwargs.get("embedding_sync"),
            graph_builder=kwargs.get("graph_builder"),
        )
    raise ValueError(f"Unknown consumer backend: {backend!r}. Choose 'kafka' or 'redis'.")
