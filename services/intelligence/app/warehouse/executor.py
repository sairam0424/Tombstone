import asyncio
from concurrent.futures import ThreadPoolExecutor

WAREHOUSE_QUERY_TIMEOUT_S = 30.0
_executor = ThreadPoolExecutor(max_workers=4, thread_name_prefix="warehouse-io")


async def run_warehouse_query(fn, *args, timeout: float = WAREHOUSE_QUERY_TIMEOUT_S, **kwargs):
    """Runs a blocking warehouse-driver call on a dedicated bounded thread pool,
    isolated from the embedding model's use of the default executor, with a
    bounded timeout. Mirrors the httpx.AsyncClient(timeout=15.0) pattern already
    used for the Anthropic LLM call in app/experiments/routes.py — the equivalent
    guard for the warehouse driver path, which previously had no timeout at all
    and shared Python's default thread pool with unrelated to_thread consumers.

    Note: asyncio.wait_for does NOT actually stop the underlying thread if it
    times out — the thread keeps running in the background until it finishes
    naturally. This still bounds how long the CALLING coroutine waits, which is
    the actual goal here (don't let one hung query block a request handler
    forever), even though it doesn't free the worker thread early. Given
    max_workers=4, a few genuinely-hung queries could still exhaust this pool's
    capacity over time — that's an accepted tradeoff for now; a future
    enhancement could track/cancel/recycle stuck threads if this becomes a real
    problem in practice.
    """
    loop = asyncio.get_running_loop()
    future = loop.run_in_executor(_executor, lambda: fn(*args, **kwargs))
    return await asyncio.wait_for(future, timeout=timeout)
