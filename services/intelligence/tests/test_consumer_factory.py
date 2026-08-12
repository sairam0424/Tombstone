from __future__ import annotations

import pytest
from unittest.mock import MagicMock, AsyncMock


def test_create_consumer_returns_kafka_by_default():
    from app.kafka.consumer import create_consumer, KafkaEventConsumer
    consumer = create_consumer(
        "kafka",
        brokers="localhost:9092",
        anomaly_detector=MagicMock(),
    )
    assert isinstance(consumer, KafkaEventConsumer)


def test_create_consumer_returns_redis_streams():
    from app.kafka.consumer import create_consumer, RedisStreamsEventConsumer
    consumer = create_consumer(
        "redis",
        redis_url="redis://localhost:6379",
        anomaly_detector=MagicMock(),
        environments=["production"],
    )
    assert isinstance(consumer, RedisStreamsEventConsumer)


def test_create_consumer_unknown_backend_raises():
    from app.kafka.consumer import create_consumer
    with pytest.raises(ValueError, match="Unknown consumer backend"):
        create_consumer("rabbitmq", brokers="x", anomaly_detector=MagicMock())


@pytest.mark.asyncio
async def test_redis_consumer_start_creates_groups():
    """RedisStreamsEventConsumer.start() calls XGROUP CREATE on each stream."""
    from app.kafka.consumer import RedisStreamsEventConsumer

    mock_redis = AsyncMock()
    mock_redis.xgroup_create = AsyncMock(return_value="OK")

    consumer = RedisStreamsEventConsumer(
        redis_url="redis://localhost:6379",
        anomaly_detector=MagicMock(),
        environments=["production", "staging"],
    )
    consumer._redis = mock_redis  # inject mock

    await consumer.start()

    assert mock_redis.xgroup_create.call_count == 2
    calls = [c.args[0] for c in mock_redis.xgroup_create.call_args_list]
    assert "tombstone:stream:production" in calls
    assert "tombstone:stream:staging" in calls
