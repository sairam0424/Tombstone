"""
Tests for app.warehouse.executor.run_warehouse_query — the dedicated bounded
thread pool + timeout wrapper introduced to replace bare asyncio.to_thread
calls in the warehouse connectors (bigquery.py, snowflake.py, databricks.py,
warehouse/connector.py).
"""
from __future__ import annotations

import asyncio
import time

import pytest

from app.warehouse.executor import run_warehouse_query


def _sleep_then_return(seconds: float, value: str) -> str:
    """Blocking (non-async) function — simulates a synchronous warehouse driver call."""
    time.sleep(seconds)
    return value


@pytest.mark.asyncio
async def test_run_warehouse_query_returns_normally_when_within_timeout():
    result = await run_warehouse_query(_sleep_then_return, 0.05, "ok", timeout=1.0)
    assert result == "ok"


@pytest.mark.asyncio
async def test_run_warehouse_query_raises_timeout_error_when_exceeded():
    with pytest.raises(asyncio.TimeoutError):
        await run_warehouse_query(_sleep_then_return, 0.5, "too-slow", timeout=0.05)


@pytest.mark.asyncio
async def test_run_warehouse_query_passes_args_and_kwargs_through():
    def _combine(a: int, b: int, *, label: str) -> str:
        return f"{label}:{a + b}"

    result = await run_warehouse_query(_combine, 2, 3, label="sum", timeout=1.0)
    assert result == "sum:5"


@pytest.mark.asyncio
async def test_run_warehouse_query_propagates_function_exceptions():
    def _boom() -> None:
        raise ValueError("driver error")

    with pytest.raises(ValueError, match="driver error"):
        await run_warehouse_query(_boom, timeout=1.0)


@pytest.mark.asyncio
async def test_run_warehouse_query_default_timeout_is_module_constant():
    """Confirms the timeout kwarg defaults to WAREHOUSE_QUERY_TIMEOUT_S when
    the caller doesn't override it (callers in bigquery.py/snowflake.py/
    databricks.py/connector.py rely on this default)."""
    import inspect

    from app.warehouse.executor import WAREHOUSE_QUERY_TIMEOUT_S

    sig = inspect.signature(run_warehouse_query)
    assert sig.parameters["timeout"].default == WAREHOUSE_QUERY_TIMEOUT_S


@pytest.mark.asyncio
async def test_run_warehouse_query_timeout_does_not_kill_underlying_thread():
    """Documents the verified caveat from the module docstring: wait_for
    bounds how long the CALLER waits, but the underlying thread keeps
    running to completion in the background — it is not cancelled."""
    thread_finished = []

    def _slow_then_mark_done() -> str:
        time.sleep(0.15)
        thread_finished.append(True)
        return "done"

    with pytest.raises(asyncio.TimeoutError):
        await run_warehouse_query(_slow_then_mark_done, timeout=0.02)

    # The calling coroutine already raised TimeoutError, but the thread is
    # still running in the background pool and will complete shortly after.
    assert thread_finished == []
    await asyncio.sleep(0.3)
    assert thread_finished == [True]
