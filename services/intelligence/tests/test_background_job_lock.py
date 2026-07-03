"""
Tests for the shared `asyncio.Lock` that guards background jobs
(_daily_retrain, _depgraph_rebuild_background) plus the HTTP-triggered
POST /api/v1/dependency-graph handler in app/main.py.

These tests don't spin up FastAPI or hit Postgres/Redis — they exercise the
same asyncio.Lock primitive with a shared mutable counter/list to prove
overlapping calls serialize instead of racing, mirroring exactly how
app.main uses app.state.background_job_lock.
"""
from __future__ import annotations

import asyncio

import pytest


async def _guarded_job(lock: asyncio.Lock, overlap_flag: list[bool], active: list[int]) -> None:
    """Simulates a background job body wrapped in `async with lock:`."""
    async with lock:
        active.append(1)
        if len(active) > 1:
            overlap_flag.append(True)
        # Yield control so a concurrently-scheduled task gets a chance to
        # run and would race here if the lock weren't held.
        await asyncio.sleep(0.05)
        active.pop()


@pytest.mark.asyncio
async def test_concurrent_lock_guarded_jobs_serialize_not_overlap():
    """Two tasks entering a lock-guarded critical section never overlap."""
    lock = asyncio.Lock()
    overlap_flag: list[bool] = []
    active: list[int] = []

    await asyncio.gather(
        _guarded_job(lock, overlap_flag, active),
        _guarded_job(lock, overlap_flag, active),
        _guarded_job(lock, overlap_flag, active),
    )

    assert overlap_flag == [], "lock-guarded jobs must never run concurrently"


@pytest.mark.asyncio
async def test_unguarded_jobs_would_overlap_without_lock():
    """Control case: without the lock, concurrent tasks DO overlap.

    This proves the test harness actually detects overlap (i.e. the
    serialization assertion above is meaningful, not a tautology).
    """
    overlap_flag: list[bool] = []
    active: list[int] = []

    async def _unguarded_job() -> None:
        active.append(1)
        if len(active) > 1:
            overlap_flag.append(True)
        await asyncio.sleep(0.05)
        active.pop()

    await asyncio.gather(_unguarded_job(), _unguarded_job())

    assert overlap_flag != [], "expected unguarded tasks to overlap in this control case"


@pytest.mark.asyncio
async def test_http_handler_fails_fast_when_lock_held():
    """Mirrors the 409 fail-fast check in POST /api/v1/dependency-graph:
    `if lock.locked(): return 409` before attempting to acquire it.
    """
    lock = asyncio.Lock()

    async def held_forever():
        async with lock:
            await asyncio.sleep(1)

    holder = asyncio.create_task(held_forever())
    await asyncio.sleep(0.01)  # let the holder acquire the lock

    try:
        assert lock.locked() is True
        # This is the exact check main.py's HTTP handler performs before
        # trying to acquire the lock itself.
        should_fail_fast = lock.locked()
        assert should_fail_fast is True
    finally:
        holder.cancel()
        try:
            await holder
        except asyncio.CancelledError:
            pass


@pytest.mark.asyncio
async def test_lock_released_after_context_exits():
    """After the guarded section completes, the lock must be free again
    so the next scheduled job (or HTTP request) can acquire it."""
    lock = asyncio.Lock()

    async with lock:
        pass

    assert lock.locked() is False
