import sys
import os
import threading
import time

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from tombstone.client import TombstoneClient


def _client() -> TombstoneClient:
    client = TombstoneClient(sdk_key="test", environment="prod")
    # Shrink the debounce window so the test stays fast and deterministic
    # instead of waiting the real ~500ms.
    client._refetch_debounce_seconds = 0.05
    return client


def _install_refetch_counter(client: TombstoneClient) -> dict:
    """Stub the snapshot refetch so no HTTP happens; count invocations.

    Mirrors how the existing tests exercise the client without a network by
    driving its methods directly (see test_evaluation.test_snapshot_*).
    """
    state = {"count": 0, "fired": threading.Event()}

    def _fake_fetch() -> None:
        state["count"] += 1
        state["fired"].set()

    client._fetch_snapshot = _fake_fetch
    return state


def _lag_frame(lag_ms: int) -> list[str]:
    # Exactly what the gateway writes (event: lag\ndata: {"lag_ms":N}\n\n)
    # as it appears after httpx's iter_lines() splits the frame.
    return ["event: lag", f'data: {{"lag_ms":{lag_ms}}}', ""]


def test_single_lag_event_triggers_one_refetch():
    client = _client()
    state = _install_refetch_counter(client)

    client._consume_sse_lines(iter(_lag_frame(42)))

    assert state["fired"].wait(timeout=1.0), "debounced refetch never fired"
    # Let the window fully elapse to confirm no second refetch sneaks in.
    time.sleep(0.15)
    assert state["count"] == 1
    client.close()


def test_burst_of_lag_events_triggers_single_refetch():
    client = _client()
    state = _install_refetch_counter(client)

    # Five lag frames back-to-back inside the debounce window must coalesce
    # into exactly ONE snapshot refetch.
    lines: list[str] = []
    for i in range(5):
        lines += _lag_frame(i)
    client._consume_sse_lines(iter(lines))

    assert state["fired"].wait(timeout=1.0), "debounced refetch never fired"
    # Wait well past the debounce window to be sure no second timer fires.
    time.sleep(0.15)
    assert state["count"] == 1
    client.close()
