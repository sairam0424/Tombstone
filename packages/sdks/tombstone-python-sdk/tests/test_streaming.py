import json
import sys
import os
import threading
import time

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from tombstone.client import TombstoneClient
from tombstone.types import FlagEnvironmentState, TargetingRule, PropertyCondition


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


# ── SDK-4 investigation: _apply_event must MERGE, not overwrite ────────────


def test_apply_event_preserves_prerequisites_and_targeting_rules():
    """Regression test for a real, live bug: flag-api's real FlagEvent never
    carries prerequisites/targeting_rules (confirmed against
    services/flag-api/internal/api/v1/flags.go's FlagEvent struct), so
    _apply_event previously overwrote them to empty on EVERY SSE event for
    a flag -- a kill-switch, a rollback step, literally any enabled/
    rollout_pct change -- silently disabling prerequisite-gating and
    rule-matching client-side until the next full snapshot refetch."""
    client = _client()
    seeded_rule = TargetingRule(
        id="r1",
        conditions=[
            PropertyCondition(
                attribute="country", operator="eq", values=["US"], negate=False
            )
        ],
        rollout_pct=100.0,
        variation=True,
    )
    client._cache["my-flag"] = FlagEnvironmentState(
        flag_key="my-flag",
        enabled=True,
        rollout_pct=50.0,
        safe_default=False,
        environment="prod",
        targeting_rules=[seeded_rule],
        prerequisites=[
            {"flag_key": "parent", "required_variation": "true", "gate": True}
        ],
    )

    # A real SSE event as flag-api actually publishes it -- no
    # targeting_rules/prerequisites field at all, just a rollout-pct change.
    event = {
        "flag_key": "my-flag",
        "enabled": True,
        "rollout_pct": 75,
        "environment": "prod",
    }
    client._apply_event(json.dumps(event))

    updated = client._cache["my-flag"]
    assert updated.rollout_pct == 75.0, "the event's own field must still apply"
    assert updated.targeting_rules == [seeded_rule], (
        "targeting_rules must survive an unrelated event"
    )
    assert updated.prerequisites == [
        {"flag_key": "parent", "required_variation": "true", "gate": True}
    ], "prerequisites must survive an unrelated event"
    client.close()


def test_apply_event_for_a_never_before_seen_flag_defaults_to_empty():
    """Sanity check the fix didn't break the first-ever event for a flag
    not already in cache -- there's nothing to merge against, so empty
    defaults (not a crash) are correct."""
    client = _client()
    event = {"flag_key": "brand-new-flag", "enabled": True, "rollout_pct": 100}
    client._apply_event(json.dumps(event))

    state = client._cache["brand-new-flag"]
    assert state.targeting_rules == []
    assert state.prerequisites == []
    client.close()


def test_apply_event_field_omitted_and_explicit_null_both_fall_back():
    """Regression test for a real gap found by adversarial review:
    event.get(key, fallback) only falls back when a key is ABSENT -- a key
    present with an explicit JSON null returns None itself, not the
    fallback. For this merge, "the event doesn't tell us this field" and
    "the event explicitly says null" must mean the same thing: fall back
    to whatever's already cached, never overwrite a real cached value
    with a bare None. flag-api never sends an explicit null for these
    fields today (confirmed: every producer uses non-pointer Go types),
    but the merge helper must not conflate "provided a real value" with
    "produced None for any reason" once some future event schema does."""
    client = _client()
    client._cache["my-flag"] = FlagEnvironmentState(
        flag_key="my-flag",
        enabled=True,
        rollout_pct=50.0,
        safe_default=False,
        environment="prod",
        hash_version=3,
        target_list=["user1"],
    )

    # Key genuinely omitted -- falls back to the existing cached value.
    client._apply_event(
        json.dumps({"flag_key": "my-flag", "enabled": True, "rollout_pct": 75})
    )
    updated = client._cache["my-flag"]
    assert updated.hash_version == 3
    assert updated.target_list == ["user1"]

    # Key explicitly present with null -- ALSO falls back, not a bare None.
    client._apply_event(
        json.dumps(
            {
                "flag_key": "my-flag",
                "enabled": True,
                "rollout_pct": 80,
                "hash_version": None,
                "target_list": None,
            }
        )
    )
    updated = client._cache["my-flag"]
    assert updated.hash_version == 3
    assert updated.target_list == ["user1"]
    client.close()


# ── Live prerequisites-streaming (services/flag-api's PrerequisitesEvent) ──


def _prereq_frame(flag_key, environment, prerequisites, ts) -> list[str]:
    data = json.dumps(
        {
            "flag_key": flag_key,
            "environment": environment,
            "prerequisites": prerequisites,
            "ts": ts,
        }
    )
    return ["event: prerequisites_updated", f"data: {data}", ""]


def test_consume_sse_lines_routes_prerequisites_updated_to_its_own_handler():
    """Regression test proving prerequisites_updated is NOT routed through
    _apply_event -- that payload shape has no enabled/rollout_pct keys at
    all, so _apply_event would silently zero them out (enabled=False,
    rollout_pct=0.0) for a flag that was never actually disabled."""
    client = _client()
    client._cache["my-flag"] = FlagEnvironmentState(
        flag_key="my-flag",
        enabled=True,
        rollout_pct=100.0,
        safe_default=False,
        environment="prod",
        prerequisites_updated_at=1_000,
    )

    client._consume_sse_lines(
        iter(
            _prereq_frame(
                "my-flag",
                "prod",
                [{"flag_key": "parent", "required_variation": "true", "gate": True}],
                2_000,
            )
        )
    )

    updated = client._cache["my-flag"]
    assert updated.enabled is True, "prerequisites_updated must not touch enabled"
    assert updated.rollout_pct == 100.0, (
        "prerequisites_updated must not touch rollout_pct"
    )
    assert updated.prerequisites == [
        {"flag_key": "parent", "required_variation": "true", "gate": True}
    ]
    assert updated.prerequisites_updated_at == 2_000
    client.close()


def test_apply_prerequisites_event_replaces_the_full_list():
    client = _client()
    client._cache["my-flag"] = FlagEnvironmentState(
        flag_key="my-flag",
        enabled=True,
        rollout_pct=100.0,
        safe_default=False,
        environment="prod",
        prerequisites=[
            {"flag_key": "old-parent", "required_variation": "true", "gate": True}
        ],
        prerequisites_updated_at=1_000,
    )

    client._apply_prerequisites_event(
        json.dumps(
            {
                "flag_key": "my-flag",
                "environment": "prod",
                "prerequisites": [
                    {
                        "flag_key": "new-parent",
                        "required_variation": "false",
                        "gate": False,
                    }
                ],
                "ts": 2_000,
            }
        )
    )

    updated = client._cache["my-flag"]
    assert updated.prerequisites == [
        {"flag_key": "new-parent", "required_variation": "false", "gate": False}
    ], "must be a full replacement, not a merge with the old list"
    assert updated.prerequisites_updated_at == 2_000
    client.close()


def test_apply_prerequisites_event_for_an_unknown_flag_is_a_noop():
    """No cached entry to merge a partial (prerequisites-only) update into
    -- the next full snapshot refetch is what correctly picks up a flag
    this client has never seen before, not a live prerequisites event."""
    client = _client()

    client._apply_prerequisites_event(
        json.dumps(
            {
                "flag_key": "never-seen-flag",
                "environment": "prod",
                "prerequisites": [{"flag_key": "parent", "required_variation": "true"}],
                "ts": 1_000,
            }
        )
    )

    assert "never-seen-flag" not in client._cache
    client.close()


def test_apply_prerequisites_event_rejects_a_stale_out_of_order_delivery():
    """Regression test for the ordering hazard disclosed in flag-api's own
    PrerequisitesEvent doc comment: concurrent AddPrerequisite/
    DeletePrerequisite calls on the same flag can have their events arrive
    out of real commit order under scheduling delays. An incoming event
    whose ts is OLDER than what's already cached must be dropped, not
    unconditionally applied just because it arrived later on the wire."""
    client = _client()
    client._cache["my-flag"] = FlagEnvironmentState(
        flag_key="my-flag",
        enabled=True,
        rollout_pct=100.0,
        safe_default=False,
        environment="prod",
        prerequisites=[
            {"flag_key": "current-parent", "required_variation": "true", "gate": True}
        ],
        prerequisites_updated_at=5_000,
    )

    # A stale event (ts=3_000, older than the cached 5_000) arrives late.
    client._apply_prerequisites_event(
        json.dumps(
            {
                "flag_key": "my-flag",
                "environment": "prod",
                "prerequisites": [
                    {"flag_key": "stale-parent", "required_variation": "true"}
                ],
                "ts": 3_000,
            }
        )
    )

    updated = client._cache["my-flag"]
    assert updated.prerequisites == [
        {"flag_key": "current-parent", "required_variation": "true", "gate": True}
    ], "a stale (older-ts) event must not overwrite the newer cached state"
    assert updated.prerequisites_updated_at == 5_000
    client.close()


def test_snapshot_seeds_prerequisites_updated_at_from_the_snapshot_ts():
    client = _client()
    client._apply_snapshot(
        {
            "environment": "prod",
            "ts": 9_000,
            "flags": [
                {
                    "flag_key": "my-flag",
                    "enabled": True,
                    "rollout_pct": 100.0,
                    "safe_default": False,
                    "prerequisites": [],
                }
            ],
        }
    )

    assert client._cache["my-flag"].prerequisites_updated_at == 9_000
    client.close()
