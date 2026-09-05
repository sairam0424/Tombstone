"""
Tests for ClickHouseWriter's read helpers (INT-4).

Regression coverage for a real bug: get_error_rate()/get_flag_stats() read
from `evaluation_events`, a table nothing anywhere in this codebase ever
writes to (the real write path, _insert()/_insert_via_driver(), has always
targeted `tombstone_evaluations` only) -- so these two endpoints always
returned zero/empty for any real deployment. clickhouse_driver is an
optional dependency (`clickhouse` extra) not installed in this environment,
so most tests here mock `_available`/`_get_client` directly rather than
exercising a real driver -- this repo has zero prior test coverage of any
kind for this module.

TestAvailablePropertyUnmocked (added after adversarial review of PR #210
found every other test fully bypasses `_available`'s real import-gating
logic, even the ones that force it to True) exercises the REAL property
unmocked -- clickhouse_driver's absence in this environment is exactly the
condition CI runs under too (confirmed: `.github/workflows/ci.yml`'s
python-intelligence job installs via `uv pip install --system -e .` with
no `[clickhouse]` extra), so this is genuinely testable without the
optional dependency, not merely theoretical.
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


class TestAvailablePropertyUnmocked:
    """
    Exercises `_available`'s real body -- importlib.import_module wrapped
    in try/except ImportError -- with NO mocking at all, unlike every other
    test in this file. clickhouse_driver is genuinely not importable here
    (confirmed: `ModuleNotFoundError`), which is also CI's real state, so
    this proves the actual, reproducible fallback behavior end-to-end
    rather than an assumption about what the property would do.
    """

    def test_available_is_false_without_the_optional_driver_installed(self, writer):
        with pytest.raises(ModuleNotFoundError):
            import clickhouse_driver  # noqa: F401

        assert writer._available is False

    @pytest.mark.asyncio
    async def test_get_error_rate_gracefully_returns_zero_via_the_real_gate(
        self, writer
    ):
        # No mocking of _available/_get_client at all -- this is the real
        # code path every deployment without the `clickhouse` extra hits.
        assert await writer.get_error_rate("checkout-v2", "production") == 0.0

    @pytest.mark.asyncio
    async def test_get_flag_stats_gracefully_returns_zeroes_via_the_real_gate(
        self, writer
    ):
        result = await writer.get_flag_stats("checkout-v2", "production")
        assert result == {"total_evaluations": 0, "error_rate": 0.0, "unique_users": 0}

    @pytest.mark.asyncio
    async def test_create_tables_logs_a_warning_and_returns_via_the_real_gate(
        self, writer
    ):
        # Must not raise even though clickhouse_driver is absent.
        await writer.create_tables()


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
