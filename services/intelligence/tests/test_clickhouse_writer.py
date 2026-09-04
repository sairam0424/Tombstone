"""
Tests for ClickHouseWriter's read helpers (INT-4).

Regression coverage for a real bug: get_error_rate()/get_flag_stats() read
from `evaluation_events`, a table nothing anywhere in this codebase ever
writes to (the real write path, _insert()/_insert_via_driver(), has always
targeted `tombstone_evaluations` only) -- so these two endpoints always
returned zero/empty for any real deployment. clickhouse_driver is an
optional dependency (`clickhouse` extra) not installed in this environment,
so `_available`/`_get_client` are mocked directly rather than exercising a
real driver -- this repo has zero prior test coverage of any kind for this
module.
"""

from __future__ import annotations

from unittest.mock import MagicMock, PropertyMock, patch

import pytest

from app.telemetry.clickhouse_writer import ClickHouseWriter


@pytest.fixture
def writer():
    return ClickHouseWriter(host="ch.internal")


def _mock_client(execute_return):
    client = MagicMock()
    client.execute.return_value = execute_return
    return client


class TestGetErrorRate:
    @pytest.mark.asyncio
    async def test_queries_tombstone_evaluations_not_evaluation_events(self, writer):
        client = _mock_client([(2, 10)])
        with (
            patch.object(
                ClickHouseWriter, "_available", new_callable=PropertyMock
            ) as mock_available,
            patch.object(writer, "_get_client", return_value=client),
        ):
            mock_available.return_value = True
            rate = await writer.get_error_rate("checkout-v2", "production", minutes=10)

        assert rate == pytest.approx(0.2)
        query = client.execute.call_args[0][0]
        assert "tombstone_evaluations" in query
        assert "evaluation_events" not in query
        assert "reason = 'ERROR'" in query
        assert "is_error" not in query

    @pytest.mark.asyncio
    async def test_no_rows_returns_zero(self, writer):
        client = _mock_client([(0, 0)])
        with (
            patch.object(
                ClickHouseWriter, "_available", new_callable=PropertyMock
            ) as mock_available,
            patch.object(writer, "_get_client", return_value=client),
        ):
            mock_available.return_value = True
            rate = await writer.get_error_rate("checkout-v2", "production")

        assert rate == 0.0

    @pytest.mark.asyncio
    async def test_unavailable_returns_zero_without_querying(self, writer):
        with patch.object(
            ClickHouseWriter, "_available", new_callable=PropertyMock
        ) as mock_available:
            mock_available.return_value = False
            rate = await writer.get_error_rate("checkout-v2", "production")

        assert rate == 0.0


class TestGetFlagStats:
    @pytest.mark.asyncio
    async def test_queries_tombstone_evaluations_with_user_hash_column(self, writer):
        client = _mock_client([(100, 5, 42)])
        with (
            patch.object(
                ClickHouseWriter, "_available", new_callable=PropertyMock
            ) as mock_available,
            patch.object(writer, "_get_client", return_value=client),
        ):
            mock_available.return_value = True
            stats = await writer.get_flag_stats("checkout-v2", "production", hours=24)

        assert stats == {
            "total_evaluations": 100,
            "error_rate": pytest.approx(0.05),
            "unique_users": 42,
        }
        query = client.execute.call_args[0][0]
        assert "tombstone_evaluations" in query
        assert "evaluation_events" not in query
        # Column renamed vs the old phantom-table schema: user_id_hash -> user_hash.
        assert "uniqExact(user_hash)" in query
        assert "user_id_hash" not in query

    @pytest.mark.asyncio
    async def test_no_rows_returns_zeroed_dict(self, writer):
        client = _mock_client([(0, 0, 0)])
        with (
            patch.object(
                ClickHouseWriter, "_available", new_callable=PropertyMock
            ) as mock_available,
            patch.object(writer, "_get_client", return_value=client),
        ):
            mock_available.return_value = True
            stats = await writer.get_flag_stats("checkout-v2", "production")

        assert stats == {"total_evaluations": 0, "error_rate": 0.0, "unique_users": 0}


class TestCreateTables:
    @pytest.mark.asyncio
    async def test_only_creates_tombstone_evaluations_not_the_phantom_table(
        self, writer
    ):
        client = MagicMock()
        with (
            patch.object(
                ClickHouseWriter, "_available", new_callable=PropertyMock
            ) as mock_available,
            patch.object(writer, "_get_client", return_value=client),
        ):
            mock_available.return_value = True
            await writer.create_tables()

        executed = [call.args[0] for call in client.execute.call_args_list]
        joined = "\n".join(executed)
        assert "tombstone_evaluations" in joined
        assert "evaluation_events" not in joined
