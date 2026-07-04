"""
Tests for RedisStreamsEventConsumer's dead-letter-queue handling.

Mirrors the Go gateway's coverage (services/gateway/internal/hub/dlq_test.go)
using a mocked redis.asyncio client, since this test environment does not
have a real Redis reachable (see AGENTS_LEARNING.md for docker-compose
availability notes). The important invariants pinned here:

  1. _dispatch now returns a bool success signal instead of silently
     swallowing failures.
  2. run()'s XACK is conditional on that signal — a failed dispatch leaves
     the message pending rather than acking it away.
  3. dlq_stream_key() matches the Go gateway's DLQStreamKey convention
     byte-for-byte: "<stream_key>:dlq".
  4. _reclaim_stale_pending XCLAIMs entries under the attempt budget and
     dead-letters (XADD + XACK) entries that exhausted it.
"""
from __future__ import annotations

import pytest
from unittest.mock import AsyncMock, MagicMock


def _make_consumer(**overrides):
    from app.kafka.consumer import RedisStreamsEventConsumer

    consumer = RedisStreamsEventConsumer(
        redis_url="redis://localhost:6379",
        anomaly_detector=MagicMock(),
        environments=["production"],
        embedding_sync=overrides.get("embedding_sync"),
        graph_builder=overrides.get("graph_builder"),
    )
    consumer._redis = AsyncMock()
    return consumer


class TestDispatchReturnsSuccessSignal:
    """_dispatch must report success/failure without changing its logging."""

    @pytest.mark.asyncio
    async def test_evaluation_event_returns_true(self):
        consumer = _make_consumer()
        window: dict = {}
        ok = await consumer._dispatch(
            {"event": "flag_evaluated", "flag_key": "my-flag", "environment": "production"},
            window,
        )
        assert ok is True
        assert window["my-flag:production"]["total"] == 1

    @pytest.mark.asyncio
    async def test_embedding_sync_failure_returns_false(self):
        embedding_sync = AsyncMock()
        embedding_sync.on_flag_event.side_effect = RuntimeError("boom")
        consumer = _make_consumer(embedding_sync=embedding_sync)

        ok = await consumer._dispatch(
            {
                "event": "flag_created",
                "flag_key": "my-flag",
                "payload": '{"name": "My Flag"}',
            },
            {},
        )
        assert ok is False

    @pytest.mark.asyncio
    async def test_graph_builder_failure_returns_false(self):
        graph_builder = AsyncMock()
        graph_builder.update_on_flag_change.side_effect = RuntimeError("boom")
        consumer = _make_consumer(graph_builder=graph_builder)

        ok = await consumer._dispatch(
            {"event": "kill_switch_activated", "flag_key": "my-flag", "environment": "production"},
            {},
        )
        assert ok is False

    @pytest.mark.asyncio
    async def test_successful_flag_change_returns_true(self):
        embedding_sync = AsyncMock()
        graph_builder = AsyncMock()
        consumer = _make_consumer(embedding_sync=embedding_sync, graph_builder=graph_builder)

        ok = await consumer._dispatch(
            {
                "event": "flag_created",
                "flag_key": "my-flag",
                "environment": "production",
                "payload": '{"name": "My Flag"}',
            },
            {},
        )
        assert ok is True
        embedding_sync.on_flag_event.assert_awaited_once()
        graph_builder.update_on_flag_change.assert_awaited_once()


class TestDLQStreamKeyConvention:
    """dlq_stream_key() must match the Go gateway's DLQStreamKey byte-for-byte."""

    def test_matches_go_gateway_convention(self):
        from app.kafka.consumer import RedisStreamsEventConsumer

        got = RedisStreamsEventConsumer.dlq_stream_key("tombstone:stream:production")
        assert got == "tombstone:stream:production:dlq"

    def test_derived_from_environment_matches_stream_key_format(self):
        from app.kafka.consumer import RedisStreamsEventConsumer

        consumer = _make_consumer()
        # self._streams is built the same way flag-api/gateway build StreamKey.
        assert consumer._streams == ["tombstone:stream:production"]
        dlq_key = RedisStreamsEventConsumer.dlq_stream_key(consumer._streams[0])
        assert dlq_key == "tombstone:stream:production:dlq"


class TestReclaimStalePending:
    """_reclaim_stale_pending: XCLAIM-retry under budget, dead-letter over budget."""

    @pytest.mark.asyncio
    async def test_retries_message_under_attempt_budget(self):
        consumer = _make_consumer()
        stream_key = "tombstone:stream:production"

        consumer._redis.xpending_range.return_value = [
            {"message_id": "1-0", "consumer": "old-consumer", "time_since_delivered": 40_000, "times_delivered": 1},
        ]
        consumer._redis.xclaim.return_value = [
            ("1-0", {"event": "flag_evaluated", "flag_key": "my-flag", "environment": "production"}),
        ]

        await consumer._reclaim_stale_pending(stream_key, "intelligence-test", {})

        consumer._redis.xclaim.assert_awaited_once_with(
            stream_key,
            consumer._GROUP,
            "intelligence-test",
            min_idle_time=consumer._RECLAIM_IDLE_THRESHOLD_MS,
            message_ids=["1-0"],
        )
        # Successful re-dispatch of the claimed message must ack it.
        consumer._redis.xack.assert_awaited_once_with(stream_key, consumer._GROUP, "1-0")
        consumer._redis.xadd.assert_not_awaited()

    @pytest.mark.asyncio
    async def test_dead_letters_message_over_attempt_budget(self):
        consumer = _make_consumer()
        stream_key = "tombstone:stream:production"

        consumer._redis.xpending_range.return_value = [
            {"message_id": "2-0", "consumer": "old-consumer", "time_since_delivered": 90_000, "times_delivered": 3},
        ]
        consumer._redis.xrange.return_value = [
            ("2-0", {"event": "flag_evaluated", "flag_key": "poison-flag", "payload": "{not valid json"}),
        ]

        await consumer._reclaim_stale_pending(stream_key, "intelligence-test", {})

        # Delivery count already at the attempt budget -> dead-letter path,
        # not a retry claim.
        consumer._redis.xclaim.assert_not_awaited()
        consumer._redis.xadd.assert_awaited_once()
        args, kwargs = consumer._redis.xadd.call_args
        assert args[0] == "tombstone:stream:production:dlq"
        assert kwargs.get("maxlen") == 10_000
        assert kwargs.get("approximate") is True

        consumer._redis.xack.assert_awaited_once_with(stream_key, consumer._GROUP, "2-0")

    @pytest.mark.asyncio
    async def test_no_pending_entries_is_noop(self):
        consumer = _make_consumer()
        stream_key = "tombstone:stream:production"
        consumer._redis.xpending_range.return_value = []

        await consumer._reclaim_stale_pending(stream_key, "intelligence-test", {})

        consumer._redis.xclaim.assert_not_awaited()
        consumer._redis.xadd.assert_not_awaited()
        consumer._redis.xack.assert_not_awaited()

    @pytest.mark.asyncio
    async def test_reclaimed_message_still_failing_stays_pending(self):
        """If a reclaimed message's dispatch fails again, it must NOT be
        acked — it stays in the PEL for the next sweep to pick up (and
        eventually dead-letter once delivery count crosses the budget)."""
        embedding_sync = AsyncMock()
        embedding_sync.on_flag_event.side_effect = RuntimeError("still broken")
        consumer = _make_consumer(embedding_sync=embedding_sync)
        stream_key = "tombstone:stream:production"

        consumer._redis.xpending_range.return_value = [
            {"message_id": "3-0", "consumer": "old-consumer", "time_since_delivered": 40_000, "times_delivered": 2},
        ]
        consumer._redis.xclaim.return_value = [
            (
                "3-0",
                {
                    "event": "flag_created",
                    "flag_key": "still-broken-flag",
                    "payload": '{"name": "x"}',
                },
            ),
        ]

        await consumer._reclaim_stale_pending(stream_key, "intelligence-test", {})

        consumer._redis.xack.assert_not_awaited()


class TestRunAcksOnlyOnDispatchSuccess:
    """run()'s per-message loop must XACK only when _dispatch succeeds."""

    @pytest.mark.asyncio
    async def test_failed_dispatch_is_not_acked(self, monkeypatch):
        consumer = _make_consumer()
        consumer._running = True

        call_count = {"n": 0}

        async def fake_xreadgroup(*args, **kwargs):
            call_count["n"] += 1
            if call_count["n"] == 1:
                return [
                    (
                        "tombstone:stream:production",
                        [("1-0", {"event": "flag_evaluated", "flag_key": "ok-flag", "environment": "production"})],
                    ),
                    (
                        "tombstone:stream:production",
                        [("2-0", {"event": "flag_created", "flag_key": "bad-flag", "payload": "{bad"})],
                    ),
                ]
            consumer._running = False
            return []

        embedding_sync = AsyncMock()
        embedding_sync.on_flag_event.side_effect = RuntimeError("boom")
        consumer._embedding_sync = embedding_sync

        redis_mock = consumer._redis
        redis_mock.xreadgroup = fake_xreadgroup
        redis_mock.xpending_range.return_value = []

        await consumer.run()

        # run() calls stop() on exit, which clears self._redis to None —
        # assert against the captured mock reference instead.
        acked_ids = {call.args[2] for call in redis_mock.xack.call_args_list}
        assert "1-0" in acked_ids
        assert "2-0" not in acked_ids
