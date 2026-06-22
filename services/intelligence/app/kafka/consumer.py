import asyncio
import json
from dataclasses import dataclass

from aiokafka import AIOKafkaConsumer

from app.anomaly.detector import AnomalyDetector


TOPIC_FLAG_EVALUATED = "tombstone.flag.evaluated"
GROUP_ID = "intelligence-anomaly"


@dataclass
class EvaluationEvent:
    flag_key: str
    environment: str
    is_error: bool
    error_count: int
    total_count: int


class TelemetryConsumer:
    """
    Consumes tombstone.flag.evaluated events from Kafka.
    Feeds aggregated counts to the AnomalyDetector every 10 seconds.
    Follows the Graph-Forge aiokafka pattern (manual commit, OTel headers).
    """

    def __init__(self, brokers: str, anomaly_detector: AnomalyDetector):
        self._brokers = brokers
        self._detector = anomaly_detector
        self._consumer: AIOKafkaConsumer | None = None
        self._window: dict[str, dict] = {}  # flag_key:env -> {errors, total}
        self._flush_interval = 10  # seconds

    async def run(self) -> None:
        self._consumer = AIOKafkaConsumer(
            TOPIC_FLAG_EVALUATED,
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
                await self._handle(msg.value)
                await self._consumer.commit()
        except asyncio.CancelledError:
            pass
        finally:
            flush_task.cancel()
            await self._consumer.stop()

    async def _handle(self, payload: dict) -> None:
        key = payload.get("flag_key", "")
        env = payload.get("environment", "production")
        window_key = f"{key}:{env}"
        if window_key not in self._window:
            self._window[window_key] = {"errors": 0, "total": 0}
        self._window[window_key]["total"] += 1
        if payload.get("is_error", False):
            self._window[window_key]["errors"] += 1

    async def _flush_loop(self) -> None:
        while True:
            await asyncio.sleep(self._flush_interval)
            snapshot = self._window.copy()
            self._window.clear()
            for key, counts in snapshot.items():
                flag_key = key.split(":")[0]
                self._detector.record(flag_key, counts["errors"], counts["total"])
