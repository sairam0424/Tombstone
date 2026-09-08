import json
import sys
import os
import threading
import time

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from tombstone.client import TombstoneClient, _parse_targeting_rules
from tombstone.types import (
    EvaluationContext,
    FlagEnvironmentState,
    TargetingRule,
    PropertyCondition,
)


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


def test_apply_prerequisites_event_with_ts_equal_to_cached_is_applied():
    """Pins down the strict `<` comparison in _apply_prerequisites_event,
    not a `<=` regression. A "clearly older" ts alone (the test above)
    cannot distinguish the two: both correctly reject that input. Only an
    equal-ts input tells them apart -- `<=` would incorrectly reject this
    one too, silently dropping a live update that arrived at the exact
    same ts as what's already cached."""
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

    client._apply_prerequisites_event(
        json.dumps(
            {
                "flag_key": "my-flag",
                "environment": "prod",
                "prerequisites": [
                    {"flag_key": "new-parent", "required_variation": "true"}
                ],
                "ts": 5_000,
            }
        )
    )

    updated = client._cache["my-flag"]
    assert updated.prerequisites == [
        {"flag_key": "new-parent", "required_variation": "true"}
    ], "an equal-ts event must be applied, not dropped as stale"
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


# ── _apply_snapshot global monotonicity + live-event tie-break ─────────────
# Found by adversarial review while adding targeting_rules_updated
# consumption: this SDK's _apply_snapshot had NEITHER a global monotonicity
# guard (rejecting a snapshot older than the last one actually applied) NOR
# any per-flag tie-break protecting a fresher live update across a slower
# snapshot -- a real, pre-existing gap in the ALREADY-SHIPPED prerequisites-
# streaming implementation, not merely a targeting_rules gap. Retrofitted
# for prerequisites here, proactively applied for targeting_rules from the
# start.


def _snapshot(ts, flag_key="my-flag", prerequisites=None, targeting_rules=None):
    flag = {
        "flag_key": flag_key,
        "enabled": True,
        "rollout_pct": 100.0,
        "safe_default": False,
    }
    if prerequisites is not None:
        flag["prerequisites"] = prerequisites
    if targeting_rules is not None:
        flag["targeting_rules"] = targeting_rules
    return {"environment": "prod", "ts": ts, "flags": [flag]}


def _rule_wire(
    rule_id,
    attribute="email",
    operator="eq",
    values=None,
    variation="matched",
    priority=0,
):
    return {
        "id": rule_id,
        "rule_type": "USER",
        "attribute": attribute,
        "operator": operator,
        "values": values if values is not None else ["x@example.com"],
        "variation": variation,
        "priority": priority,
    }


def test_apply_snapshot_rejects_an_incoming_snapshot_older_than_the_last_one_applied():
    client = _client()
    client._apply_snapshot(_snapshot(5_000, prerequisites=[{"flag_key": "p1"}]))
    client._apply_snapshot(
        _snapshot(3_000, prerequisites=[{"flag_key": "p2"}])
    )  # older -- rejected wholesale

    assert client._cache["my-flag"].prerequisites == [{"flag_key": "p1"}]
    client.close()


def test_apply_snapshot_still_applies_a_genuinely_newer_snapshot_after_an_older_one_was_rejected():
    client = _client()
    client._apply_snapshot(_snapshot(5_000, prerequisites=[{"flag_key": "p1"}]))
    client._apply_snapshot(
        _snapshot(3_000, prerequisites=[{"flag_key": "p2"}])
    )  # rejected
    client._apply_snapshot(
        _snapshot(7_000, prerequisites=[{"flag_key": "p3"}])
    )  # genuinely newer

    assert client._cache["my-flag"].prerequisites == [{"flag_key": "p3"}]
    client.close()


def test_apply_snapshot_preserves_a_fresher_live_prerequisites_update():
    client = _client()
    old_parent = [{"flag_key": "old-parent"}]
    client._apply_snapshot(_snapshot(1_000, prerequisites=old_parent))

    new_parent = [{"flag_key": "new-parent"}]
    client._apply_prerequisites_event(
        _prereq_frame("my-flag", "prod", new_parent, 2_000)[1][6:]
    )

    # The slower snapshot now resolves. Its ts=1_500 is newer than the
    # cache's LAST SNAPSHOT ts (1_000), so it passes the whole-snapshot
    # check -- but it still carries the OLD (pre-live-update) prerequisites.
    client._apply_snapshot(_snapshot(1_500, prerequisites=old_parent))

    updated = client._cache["my-flag"]
    assert updated.prerequisites == new_parent
    assert updated.prerequisites_updated_at == 2_000
    client.close()


def test_apply_snapshot_preserves_a_fresher_live_targeting_rules_update():
    client = _client()
    old_rule = [_rule_wire("old-rule")]
    client._apply_snapshot(_snapshot(1_000, targeting_rules=old_rule))

    new_rule = [_rule_wire("new-rule")]
    client._apply_targeting_rules_event(
        _rules_frame("my-flag", "prod", new_rule, 2_000)[1][6:]
    )

    client._apply_snapshot(_snapshot(1_500, targeting_rules=old_rule))

    updated = client._cache["my-flag"]
    assert updated.targeting_rules[0].id == "new-rule"
    assert updated.targeting_rules_updated_at == 2_000
    client.close()


def test_apply_snapshot_preserves_the_live_prerequisites_update_on_an_exact_ts_tie():
    client = _client()
    old_parent = [{"flag_key": "old-parent"}]
    client._apply_snapshot(_snapshot(1_000, prerequisites=old_parent))

    new_parent = [{"flag_key": "new-parent"}]
    client._apply_prerequisites_event(
        _prereq_frame("my-flag", "prod", new_parent, 2_000)[1][6:]
    )

    client._apply_snapshot(_snapshot(2_000, prerequisites=old_parent))

    updated = client._cache["my-flag"]
    assert updated.prerequisites == new_parent
    assert updated.prerequisites_updated_at == 2_000
    client.close()


def test_apply_snapshot_second_snapshot_sharing_the_exact_same_ts_no_live_event_still_applies_its_own_prerequisites():
    client = _client()
    client._apply_snapshot(_snapshot(5_000, prerequisites=[{"flag_key": "parent-a"}]))
    client._apply_snapshot(_snapshot(5_000, prerequisites=[{"flag_key": "parent-b"}]))

    updated = client._cache["my-flag"]
    assert updated.prerequisites == [{"flag_key": "parent-b"}]
    assert updated.prerequisites_updated_at == 5_000
    client.close()


def test_apply_snapshot_second_snapshot_sharing_the_exact_same_ts_no_live_event_still_applies_its_own_targeting_rules():
    client = _client()
    client._apply_snapshot(_snapshot(5_000, targeting_rules=[_rule_wire("rule-a")]))
    client._apply_snapshot(_snapshot(5_000, targeting_rules=[_rule_wire("rule-b")]))

    updated = client._cache["my-flag"]
    assert updated.targeting_rules[0].id == "rule-b"
    assert updated.targeting_rules_updated_at == 5_000
    client.close()


def test_apply_snapshot_third_snapshot_tying_a_live_events_ts_after_a_second_tied_snapshot_already_resolved_the_race():
    client = _client()
    client._apply_snapshot(_snapshot(1_000, prerequisites=[{"flag_key": "old-parent"}]))
    client._apply_prerequisites_event(
        _prereq_frame("my-flag", "prod", [{"flag_key": "live-parent"}], 2_000)[1][6:]
    )

    # First tied snapshot after the live event -- must preserve.
    client._apply_snapshot(
        _snapshot(2_000, prerequisites=[{"flag_key": "snap-b-parent"}])
    )
    assert client._cache["my-flag"].prerequisites == [{"flag_key": "live-parent"}]

    # A SECOND, independent snapshot arrives, also tying ts=2_000. No new
    # live event raced THIS one -- the protection was already consumed.
    client._apply_snapshot(
        _snapshot(2_000, prerequisites=[{"flag_key": "snap-c-parent"}])
    )

    updated = client._cache["my-flag"]
    assert updated.prerequisites == [{"flag_key": "snap-c-parent"}]
    assert updated.prerequisites_updated_at == 2_000
    client.close()


# Found missing (for the analogous TS SDK feature) by adversarial review of
# PR #246, and only correctly ISOLATED after a second round of adversarial
# review of the Java SDK's own first draft of this same test (PR #247): a
# naive version where the untouched field's own ts is strictly OLDER than
# the tying snapshot's ts lets the ts>=snapshot_ts half of the AND condition
# alone force the correct outcome regardless of what the live-provenance
# boolean is set to -- so it doesn't actually test anything. This version
# ties BOTH the live event and the final snapshot at the SAME ts as the
# very first load, so the boolean is the ONLY variable that can distinguish
# a correct pass from a false one.


def test_a_live_prerequisites_event_does_not_protect_targeting_rules():
    client = _client()
    old_prereq = [{"flag_key": "old-parent"}]
    old_rule = [_rule_wire("old-rule")]
    client._apply_snapshot(
        _snapshot(1_000, prerequisites=old_prereq, targeting_rules=old_rule)
    )

    # Only a live PREREQUISITES event fires, tying the SAME ts=1_000 --
    # targeting_rules gets no live event at all.
    new_prereq = [{"flag_key": "new-parent"}]
    client._apply_prerequisites_event(
        _prereq_frame("my-flag", "prod", new_prereq, 1_000)[1][6:]
    )

    # A second snapshot ties the SAME ts=1_000 again. Both existing
    # *_updated_at fields are now >= 1_000 -- carrying stale prerequisites
    # (correctly preserved) but genuinely NEW targeting_rules (must NOT be
    # blocked).
    genuinely_new_rule = [_rule_wire("genuinely-new-rule")]
    client._apply_snapshot(
        _snapshot(1_000, prerequisites=old_prereq, targeting_rules=genuinely_new_rule)
    )

    state = client._cache["my-flag"]
    assert state.prerequisites == new_prereq
    assert state.targeting_rules[0].id == "genuinely-new-rule"
    client.close()


def test_a_live_targeting_rules_event_does_not_protect_prerequisites():
    client = _client()
    old_prereq = [{"flag_key": "old-parent"}]
    old_rule = [_rule_wire("old-rule")]
    client._apply_snapshot(
        _snapshot(1_000, prerequisites=old_prereq, targeting_rules=old_rule)
    )

    new_rule = [_rule_wire("new-rule")]
    client._apply_targeting_rules_event(
        _rules_frame("my-flag", "prod", new_rule, 1_000)[1][6:]
    )

    genuinely_new_prereq = [{"flag_key": "genuinely-new-parent"}]
    client._apply_snapshot(
        _snapshot(1_000, prerequisites=genuinely_new_prereq, targeting_rules=old_rule)
    )

    state = client._cache["my-flag"]
    assert state.targeting_rules[0].id == "new-rule"
    assert state.prerequisites == genuinely_new_prereq
    client.close()


def test_apply_targeting_rules_event_compares_against_targeting_rules_updated_at_not_prerequisites_updated_at():
    # Found by adversarial review of the Java/Ruby/.NET SDKs' own equivalent
    # tests (PRs #247-249): every other test above only ever sets
    # prerequisites_updated_at and targeting_rules_updated_at to the SAME
    # value (both come from _apply_snapshot alone) -- so a copy-paste bug in
    # _apply_targeting_rules_event's own staleness guard (comparing against
    # existing.prerequisites_updated_at instead of
    # existing.targeting_rules_updated_at) would go completely undetected.
    # This test deliberately DIVERGES the two fields first.
    client = _client()
    client._apply_snapshot(_snapshot(1_000, targeting_rules=[_rule_wire("old-rule")]))
    # Both *_updated_at fields are 1_000 here.

    # A live PREREQUISITES event bumps ONLY prerequisites_updated_at to
    # 5_000 -- targeting_rules_updated_at must stay at 1_000.
    client._apply_prerequisites_event(
        _prereq_frame("my-flag", "prod", [{"flag_key": "new-parent"}], 5_000)[1][6:]
    )
    assert client._cache["my-flag"].targeting_rules_updated_at == 1_000

    # ts=2_000 is NEWER than targeting_rules_updated_at (1_000, the correct
    # field) but OLDER than prerequisites_updated_at (5_000, the WRONG
    # field a copy-paste bug might compare against instead).
    new_rule = [_rule_wire("new-rule")]
    client._apply_targeting_rules_event(
        _rules_frame("my-flag", "prod", new_rule, 2_000)[1][6:]
    )

    updated = client._cache["my-flag"]
    assert updated.targeting_rules[0].id == "new-rule"
    assert updated.targeting_rules_updated_at == 2_000
    client.close()


# ── Live targeting_rules-streaming (services/flag-api's TargetingRulesEvent) ──


def _rules_frame(flag_key, environment, targeting_rules, ts) -> list[str]:
    data = json.dumps(
        {
            "flag_key": flag_key,
            "environment": environment,
            "targeting_rules": targeting_rules,
            "ts": ts,
        }
    )
    return ["event: targeting_rules_updated", f"data: {data}", ""]


def test_consume_sse_lines_routes_targeting_rules_updated_to_its_own_handler():
    client = _client()
    client._cache["my-flag"] = FlagEnvironmentState(
        flag_key="my-flag",
        enabled=True,
        rollout_pct=100.0,
        safe_default=False,
        environment="prod",
        targeting_rules_updated_at=1_000,
    )

    client._consume_sse_lines(
        iter(_rules_frame("my-flag", "prod", [_rule_wire("r1")], 2_000))
    )

    updated = client._cache["my-flag"]
    assert updated.enabled is True, "targeting_rules_updated must not touch enabled"
    assert updated.rollout_pct == 100.0, (
        "targeting_rules_updated must not touch rollout_pct"
    )
    assert len(updated.targeting_rules) == 1
    assert updated.targeting_rules[0].id == "r1"
    assert updated.targeting_rules_updated_at == 2_000
    client.close()


def test_apply_targeting_rules_event_replaces_the_full_list():
    client = _client()
    client._cache["my-flag"] = FlagEnvironmentState(
        flag_key="my-flag",
        enabled=True,
        rollout_pct=100.0,
        safe_default=False,
        environment="prod",
        targeting_rules=_parse_targeting_rules([_rule_wire("old-rule")]),
        targeting_rules_updated_at=1_000,
    )

    client._apply_targeting_rules_event(
        json.dumps(
            {
                "flag_key": "my-flag",
                "environment": "prod",
                "targeting_rules": [_rule_wire("new-rule")],
                "ts": 2_000,
            }
        )
    )

    updated = client._cache["my-flag"]
    assert len(updated.targeting_rules) == 1
    assert updated.targeting_rules[0].id == "new-rule", (
        "must be a full replacement, not a merge with the old list"
    )
    assert updated.targeting_rules_updated_at == 2_000
    client.close()


def test_apply_targeting_rules_event_for_an_unknown_flag_is_a_noop():
    client = _client()

    client._apply_targeting_rules_event(
        json.dumps(
            {
                "flag_key": "never-seen-flag",
                "environment": "prod",
                "targeting_rules": [_rule_wire("r1")],
                "ts": 1_000,
            }
        )
    )

    assert "never-seen-flag" not in client._cache
    client.close()


def test_apply_targeting_rules_event_rejects_a_stale_out_of_order_delivery():
    client = _client()
    client._cache["my-flag"] = FlagEnvironmentState(
        flag_key="my-flag",
        enabled=True,
        rollout_pct=100.0,
        safe_default=False,
        environment="prod",
        targeting_rules=_parse_targeting_rules([_rule_wire("current-rule")]),
        targeting_rules_updated_at=5_000,
    )

    client._apply_targeting_rules_event(
        json.dumps(
            {
                "flag_key": "my-flag",
                "environment": "prod",
                "targeting_rules": [_rule_wire("stale-rule")],
                "ts": 3_000,
            }
        )
    )

    updated = client._cache["my-flag"]
    assert updated.targeting_rules[0].id == "current-rule", (
        "a stale (older-ts) event must not overwrite the newer cached state"
    )
    assert updated.targeting_rules_updated_at == 5_000
    client.close()


def test_apply_targeting_rules_event_with_ts_equal_to_cached_is_applied():
    client = _client()
    client._cache["my-flag"] = FlagEnvironmentState(
        flag_key="my-flag",
        enabled=True,
        rollout_pct=100.0,
        safe_default=False,
        environment="prod",
        targeting_rules=_parse_targeting_rules([_rule_wire("current-rule")]),
        targeting_rules_updated_at=5_000,
    )

    client._apply_targeting_rules_event(
        json.dumps(
            {
                "flag_key": "my-flag",
                "environment": "prod",
                "targeting_rules": [_rule_wire("new-rule")],
                "ts": 5_000,
            }
        )
    )

    updated = client._cache["my-flag"]
    assert updated.targeting_rules[0].id == "new-rule", (
        "an equal-ts event must be applied, not dropped as stale"
    )
    assert updated.targeting_rules_updated_at == 5_000
    client.close()


def test_apply_targeting_rules_event_with_an_empty_list_clears_existing_rules():
    client = _client()
    client._cache["my-flag"] = FlagEnvironmentState(
        flag_key="my-flag",
        enabled=True,
        rollout_pct=100.0,
        safe_default=False,
        environment="prod",
        targeting_rules=_parse_targeting_rules([_rule_wire("old-rule")]),
        targeting_rules_updated_at=1_000,
    )

    client._apply_targeting_rules_event(
        json.dumps(
            {
                "flag_key": "my-flag",
                "environment": "prod",
                "targeting_rules": [],
                "ts": 2_000,
            }
        )
    )

    updated = client._cache["my-flag"]
    assert updated.targeting_rules == []
    assert updated.targeting_rules_updated_at == 2_000
    client.close()


def test_snapshot_seeds_targeting_rules_updated_at_from_the_snapshot_ts():
    client = _client()
    client._apply_snapshot(_snapshot(9_000, targeting_rules=[]))

    assert client._cache["my-flag"].targeting_rules_updated_at == 9_000
    client.close()


# ── real flat wire-shape adapter (_parse_targeting_rules) ───────────────────
# The EARLIER version of this parsing (both in _apply_snapshot and the
# original draft of _apply_targeting_rules_event) assumed a NESTED
# "conditions" key per rule, which does not exist anywhere on flag-api's
# real wire format -- every real targeting rule parsed as an EMPTY
# conditions list, making targeting_rules completely unreachable. Found
# while wiring the real backend format into this SDK's live-event path for
# the first time.


def test_apply_snapshot_parses_the_real_flat_wire_shape_not_a_nested_conditions_key():
    client = _client()
    client._apply_snapshot(
        _snapshot(
            1_000,
            targeting_rules=[
                _rule_wire(
                    "rule-1",
                    attribute="email",
                    operator="CONTAINS",
                    values=["@acme.com"],
                    priority=3,
                )
            ],
        )
    )

    rule = client._cache["my-flag"].targeting_rules[0]
    assert rule.id == "rule-1"
    assert rule.priority == 3
    assert rule.rollout_pct == 100.0
    assert len(rule.conditions) == 1
    assert rule.conditions[0].attribute == "email"
    assert rule.conditions[0].operator == "CONTAINS"
    assert rule.conditions[0].values == ["@acme.com"]
    client.close()


def test_targeting_rule_whole_number_json_float_values_round_trip_without_a_trailing_dot_zero():
    # flag-api's JSONB "values" column can round-trip a whole-number value
    # as a JSON float (e.g. 21.0) -- must render as "21", not "21.0", or an
    # eq/in/neq/nin condition silently fails to match a context attribute
    # supplied as a plain int or bare numeric string. Found by adversarial
    # review of the Ruby/.NET SDKs' identical adapters (PRs #248/#249).
    client = _client()
    client._apply_snapshot(
        _snapshot(
            1_000,
            targeting_rules=[
                _rule_wire(
                    "rule-1",
                    attribute="age_bracket",
                    operator="IN",
                    values=[21.0, 65.0],
                )
            ],
        )
    )

    condition = client._cache["my-flag"].targeting_rules[0].conditions[0]
    assert condition.values == ["21", "65"]
    client.close()


def test_targeting_rule_huge_integer_value_preserves_exact_precision():
    # Python's json module parses a plain integer literal (no decimal
    # point) into its native arbitrary-precision int -- exact at any size,
    # unlike the other 4 SDKs' native numeric types, which needed their own
    # fix for this. Confirmed here rather than assumed.
    client = _client()
    client._apply_snapshot(
        _snapshot(
            1_000,
            targeting_rules=[
                _rule_wire(
                    "rule-1",
                    attribute="big_id",
                    operator="EQ",
                    values=[9007199254740993],
                )
            ],
        )
    )

    condition = client._cache["my-flag"].targeting_rules[0].conditions[0]
    assert condition.values == ["9007199254740993"]
    client.close()


def test_targeting_rule_non_whole_float_values_preserve_their_fractional_part():
    client = _client()
    client._apply_snapshot(
        _snapshot(
            1_000,
            targeting_rules=[
                _rule_wire("rule-1", attribute="score", operator="EQ", values=[21.5])
            ],
        )
    )

    condition = client._cache["my-flag"].targeting_rules[0].conditions[0]
    assert condition.values == ["21.5"]
    client.close()


def test_a_malformed_non_dict_entry_inside_targeting_rules_is_skipped_not_raised():
    client = _client()
    client._apply_snapshot(
        _snapshot(1_000, targeting_rules=[None, _rule_wire("rule-1")])
    )

    rules = client._cache["my-flag"].targeting_rules
    assert len(rules) == 1
    assert rules[0].id == "rule-1"
    client.close()


def test_a_targeting_rule_missing_the_values_key_entirely_parses_with_empty_values_not_an_error():
    client = _client()
    rule = _rule_wire("rule-1")
    del rule["values"]
    client._apply_snapshot(_snapshot(1_000, targeting_rules=[rule]))

    condition = client._cache["my-flag"].targeting_rules[0].conditions[0]
    assert condition.values == []
    client.close()


# ── malformed field type-safety (found by adversarial review of PR #251) ──


def test_nan_and_infinity_in_values_do_not_crash_stringify_wire_value():
    from tombstone.client import _stringify_wire_value

    assert _stringify_wire_value(float("nan")) == "nan"
    assert _stringify_wire_value(float("inf")) == "inf"
    assert _stringify_wire_value(float("-inf")) == "-inf"


def test_a_targeting_rule_with_nan_in_values_does_not_drop_the_whole_flag():
    # Python's json module parses the extended (non-strict-JSON) tokens
    # NaN/Infinity/-Infinity by default. An earlier version of
    # _stringify_wire_value's float branch called int(v) unconditionally,
    # which raises ValueError for NaN / OverflowError for +-Infinity --
    # uncaught inside _apply_snapshot's per-flag try block, this dropped
    # the ENTIRE flag from the cache, not just the one malformed value.
    client = _client()
    client._apply_snapshot(
        _snapshot(
            1_000,
            targeting_rules=[
                _rule_wire(
                    "rule-1", attribute="score", operator="gt", values=[float("nan")]
                )
            ],
        )
    )

    assert "my-flag" in client._cache, (
        "the whole flag must not be dropped for one malformed value"
    )
    condition = client._cache["my-flag"].targeting_rules[0].conditions[0]
    assert condition.values == ["nan"]
    client.close()


def test_a_targeting_rules_updated_event_with_infinity_in_values_does_not_drop_the_whole_event():
    client = _client()
    client._cache["my-flag"] = FlagEnvironmentState(
        flag_key="my-flag",
        enabled=True,
        rollout_pct=100.0,
        safe_default=False,
        environment="prod",
        targeting_rules=_parse_targeting_rules([_rule_wire("old-rule")]),
        targeting_rules_updated_at=1_000,
    )

    client._apply_targeting_rules_event(
        json.dumps(
            {
                "flag_key": "my-flag",
                "environment": "prod",
                "targeting_rules": [
                    _rule_wire(
                        "new-rule",
                        attribute="score",
                        operator="gt",
                        values=[float("inf")],
                    )
                ],
                "ts": 2_000,
            }
        )
    )

    updated = client._cache["my-flag"]
    assert updated.targeting_rules[0].id == "new-rule", (
        "the event must still apply, not be dropped wholesale for one malformed value"
    )
    assert updated.targeting_rules_updated_at == 2_000
    client.close()


def test_a_nested_dict_or_list_in_values_renders_as_empty_string_not_python_repr():
    from tombstone.client import _stringify_wire_value

    assert _stringify_wire_value({"a": 1}) == ""
    assert _stringify_wire_value([1, 2, 3]) == ""


def test_a_non_string_operator_is_skipped_as_a_single_rule_not_a_whole_flag_evaluation_error():
    # A present-but-wrong-typed "operator" (e.g. a JSON number) previously
    # passed through unvalidated into PropertyCondition.operator, and
    # match_property's unconditional .lower() call raised AttributeError
    # (not InconclusiveMatchError) -- which _match_targeting_rules' narrow
    # `except InconclusiveMatchError` does not catch, turning ONE
    # malformed rule into a total evaluation failure (reason="ERROR") for
    # the whole flag.
    #
    # The context DELIBERATELY supplies a real "email" attribute value
    # matching the rule's own attribute -- an earlier draft of this test
    # omitted it, which let _get_attr's OWN "attribute not present" ->
    # InconclusiveMatchError fire first and mask whether the operator's
    # own .lower() call was ever reached at all (confirmed by inject-
    # confirms-catches: removing the operator type-guard did NOT fail
    # this test until the attribute value was added).
    client = _client()
    client._apply_snapshot(
        _snapshot(
            1_000,
            flag_key="f1",
            targeting_rules=[_rule_wire("bad-rule", operator=42, values=["x"])],
        )
    )

    result = client.evaluate(
        "f1", EvaluationContext(user_id="u1", attrs={"email": "x@example.com"})
    )
    assert result.reason != "ERROR", (
        "a malformed operator must not fail the whole flag's evaluation"
    )
    assert result.value is True, "a 100% rollout must still fall through normally"
    client.close()


def test_a_non_int_priority_defaults_to_a_low_not_high_priority():
    # priority=0 is the HIGHEST priority per evaluation.py's ascending
    # sort -- silently coercing a malformed (non-int) priority to 0 would
    # let a broken rule jump to the FRONT of the evaluation order and win
    # ties against every correctly-typed rule.
    client = _client()
    client._apply_snapshot(
        _snapshot(
            1_000,
            targeting_rules=[
                _rule_wire("malformed-priority-rule", priority="3"),
                _rule_wire("well-formed-rule", priority=5),
            ],
        )
    )

    rules = client._cache["my-flag"].targeting_rules
    malformed = next(r for r in rules if r.id == "malformed-priority-rule")
    well_formed = next(r for r in rules if r.id == "well-formed-rule")
    assert malformed.priority > well_formed.priority, (
        "a malformed priority must sort AFTER every well-formed rule, not before"
    )
    client.close()


# ── previously-untested defensive defaults (found by adversarial review) ──


def test_a_targeting_rule_missing_attribute_operator_id_or_variation_parses_with_safe_defaults():
    client = _client()
    bare_rule = {"values": ["x"]}
    client._apply_snapshot(_snapshot(1_000, targeting_rules=[bare_rule]))

    rule = client._cache["my-flag"].targeting_rules[0]
    assert rule.id == ""
    assert rule.variation is True
    condition = rule.conditions[0]
    assert condition.attribute == ""
    assert condition.operator == ""
    client.close()
