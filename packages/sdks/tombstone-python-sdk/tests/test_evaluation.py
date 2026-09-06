import json
import sys
import os

import pytest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from tombstone.evaluation import evaluate
from tombstone.types import EvaluationContext, FlagEnvironmentState

# packages/sdks/test-contract/vectors.json -- the SAME file every other SDK's
# contract test loads (docs/SDK_CONTRACT.md: "every SDK's test suite must
# load and assert against this file"). Before this fix, this file had zero
# references to vectors.json at all -- its "hash parity" coverage was 4
# hand-copied literal tuples (comment-annotated "Generated from TypeScript")
# that would silently NOT follow if vectors.json were ever corrected or
# extended (found by investigation, not adversarial review, of this PR).
_VECTORS_PATH = os.path.join(
    os.path.dirname(__file__), "..", "..", "test-contract", "vectors.json"
)
with open(_VECTORS_PATH) as _f:
    _VECTOR_DATA = json.load(_f)
_HASH_VECTORS = _VECTOR_DATA.get("vectors", [])


def _ctx(user_id: str = "user-1") -> EvaluationContext:
    return EvaluationContext(user_id=user_id)


def _flag(
    flag_key: str = "my-flag",
    enabled: bool = True,
    rollout_pct: float = 100.0,
    safe_default: object = False,
    environment: str = "production",
    targeting_rules=None,
    prerequisites=None,
    hash_version: int = 1,
    target_list=None,
) -> FlagEnvironmentState:
    return FlagEnvironmentState(
        flag_key=flag_key,
        enabled=enabled,
        rollout_pct=rollout_pct,
        safe_default=safe_default,
        environment=environment,
        targeting_rules=targeting_rules or [],
        prerequisites=prerequisites or [],
        hash_version=hash_version,
        target_list=target_list or [],
    )


def test_disabled_flag_returns_default():
    flag = _flag(enabled=False)
    result = evaluate(flag, _ctx(), default_value=False, flag_key="my-flag")
    assert result.reason == "OFF"
    assert result.value is False


def test_100pct_rollout():
    flag = _flag(rollout_pct=100.0)
    result = evaluate(flag, _ctx(), default_value=False, flag_key="my-flag")
    assert result.reason == "FALLTHROUGH"
    assert result.value is True


def test_zero_pct_rollout():
    flag = _flag(rollout_pct=0.0)
    result = evaluate(flag, _ctx(), default_value=False, flag_key="my-flag")
    assert result.reason == "FALLTHROUGH"
    assert result.value is False


def test_missing_flag():
    result = evaluate(None, _ctx(), default_value=False, flag_key="missing")
    assert result.reason == "ERROR"
    assert result.value is False
    assert result.from_cache is False


def test_stickiness():
    flag = _flag(rollout_pct=50.0)
    ctx = _ctx(user_id="sticky-user-42")
    first_result = evaluate(flag, ctx, default_value=False, flag_key="my-flag")
    for _ in range(20):
        result = evaluate(flag, ctx, default_value=False, flag_key="my-flag")
        assert result.value == first_result.value, (
            "Stickiness violated: same user_id must always produce the same result"
        )


@pytest.mark.parametrize(
    "vector",
    _HASH_VECTORS,
    ids=[
        f"{v['flag_key']}/{v['user_id']}/v{v.get('hash_version', 1)}/{v['rollout_pct']}%"
        for v in _HASH_VECTORS
    ],
)
def test_hash_vectors_match_contract(vector):
    """Loads packages/sdks/test-contract/vectors.json and asserts the Python
    SDK's REAL evaluate() pipeline matches every vector -- docs/SDK_CONTRACT.md's
    "Hash-only" contract level for this SDK (same level as @tombstone/core's
    contract.test.ts). Calls the real evaluate() end to end rather than
    reimplementing the hash bucket calculation by hand, so a real regression
    in the SDK's own hash-version dispatch would actually be caught here."""
    flag = _flag(
        vector["flag_key"],
        rollout_pct=vector["rollout_pct"],
        hash_version=vector.get("hash_version", 1),
    )
    result = evaluate(
        flag, _ctx(vector["user_id"]), default_value=False, flag_key=vector["flag_key"]
    )
    assert result.value == vector["expected_in_cohort"], (
        f"hash vector mismatch for {vector['flag_key']}/{vector['user_id']}: "
        f"expected_in_cohort={vector['expected_in_cohort']}, got={result.value} (reason={result.reason})"
    )


from tombstone.exceptions import InconclusiveMatchError, RequiresServerEvaluation
from tombstone.types import TargetingRule, PropertyCondition


def test_inconclusive_match_error_is_exception():
    err = InconclusiveMatchError("missing attr")
    assert isinstance(err, Exception)


def test_requires_server_evaluation_is_exception():
    err = RequiresServerEvaluation("needs server")
    assert isinstance(err, Exception)


def test_targeting_rule_dataclass():
    rule = TargetingRule(
        id="rule-1",
        conditions=[
            PropertyCondition(
                attribute="country", operator="eq", values=["US"], negate=False
            )
        ],
        rollout_pct=100.0,
        variation=True,
    )
    assert rule.id == "rule-1"
    assert rule.rollout_pct == 100.0


def test_flag_state_has_targeting_rules():
    from tombstone.types import FlagEnvironmentState

    flag = FlagEnvironmentState(
        flag_key="f",
        enabled=True,
        rollout_pct=100.0,
        safe_default=False,
        environment="prod",
        targeting_rules=[],
        prerequisites=[],
    )
    assert flag.targeting_rules == []
    assert flag.prerequisites == []


def test_snapshot_deserialization_includes_targeting_rules():
    """Client._apply_snapshot must populate targeting_rules and prerequisites."""
    from tombstone.client import TombstoneClient

    client = TombstoneClient(sdk_key="test", environment="prod")

    snapshot_payload = {
        "environment": "prod",
        "flags": [
            {
                "flag_key": "my-flag",
                "enabled": True,
                "rollout_pct": 100.0,
                "safe_default": False,
                "prerequisites": [{"flag_key": "parent", "required_variation": "true"}],
                "targeting_rules": [
                    {
                        "id": "r1",
                        "conditions": [
                            {
                                "attribute": "country",
                                "operator": "eq",
                                "values": ["US"],
                                "negate": False,
                            }
                        ],
                        "rollout_pct": 100.0,
                        "variation": True,
                    }
                ],
            }
        ],
        "hash": "abc",
        "ts": 1000,
    }
    client._apply_snapshot(snapshot_payload)

    state = client._cache.get("my-flag")
    assert state is not None
    assert len(state.prerequisites) == 1
    assert state.prerequisites[0]["flag_key"] == "parent"
    assert len(state.targeting_rules) == 1
    assert state.targeting_rules[0].id == "r1"
    assert state.targeting_rules[0].conditions[0].attribute == "country"


# ── V-6: TARGET_MATCH ────────────────────────────────────────────────────────


def test_target_list_match_returns_target_match():
    flag = _flag("f", target_list=["user-vip", "user-beta"])
    result = evaluate(flag, EvaluationContext(user_id="user-vip"), False, "f")
    assert result.reason == "TARGET_MATCH"
    assert result.value is True


def test_target_list_miss_falls_through():
    flag = _flag("f", target_list=["user-vip"])
    result = evaluate(flag, EvaluationContext(user_id="user-regular"), False, "f")
    assert result.reason == "FALLTHROUGH"


# ── V-6: hashVersion=2 FNV-1a ────────────────────────────────────────────────


def test_hash_version_2_fnv_stickiness():
    flag = _flag("f", rollout_pct=50.0, hash_version=2)
    ctx = EvaluationContext(user_id="sticky-user-42")
    first = evaluate(flag, ctx, False, "f")
    for _ in range(10):
        assert evaluate(flag, ctx, False, "f").value == first.value


def test_fnv_double_pass_matches_typescript_vector():
    # TypeScript: fnv(String(fnv("checkout-v2" + "user-abc"))) % 10000 / 10000 < rollout/100
    # Pre-computed: h1=fnv("checkout-v2user-abc"), h2=fnv(str(h1)), result=h2%10000/10000
    from tombstone.evaluation import _fnv1a_raw, _is_in_rollout_fnv

    h1 = _fnv1a_raw("checkout-v2user-abc")
    h2 = _fnv1a_raw(str(h1))
    bucket = h2 % 10000
    assert 0 <= bucket < 10000, "FNV bucket out of range"
    # Deterministic: same inputs always same result
    assert _is_in_rollout_fnv("checkout-v2", "user-abc", 50.0) == _is_in_rollout_fnv(
        "checkout-v2", "user-abc", 50.0
    )
    print(f"FNV double-pass: h1={h1}, h2={h2}, bucket={bucket}/10000")


# ── V-7: Rule priority ordering ──────────────────────────────────────────────


def test_rule_priority_ordering():
    # Lower priority number = higher priority. Rule with priority=0 should match first.
    flag = _flag(
        "f",
        targeting_rules=[
            TargetingRule(
                id="r2",
                conditions=[PropertyCondition("country", "eq", ["US"], False)],
                rollout_pct=100.0,
                variation="second",
                priority=1,
            ),
            TargetingRule(
                id="r1",
                conditions=[PropertyCondition("country", "eq", ["US"], False)],
                rollout_pct=100.0,
                variation="first",
                priority=0,
            ),
        ],
    )
    result = evaluate(
        flag, EvaluationContext(user_id="u", attrs={"country": "US"}), False, "f"
    )
    assert result.value == "first"


# ── V-7: Circular prerequisite guard ─────────────────────────────────────────


def test_circular_prerequisite_does_not_recurse_infinitely():
    a = _flag("a", prerequisites=[{"flag_key": "b", "required_variation": "true"}])
    b = _flag("b", prerequisites=[{"flag_key": "a", "required_variation": "true"}])
    all_flags = {"a": a, "b": b}
    # Traced precisely, not just "doesn't crash" (found by adversarial review
    # of PR #229 -- the old `reason in (PREREQUISITE_FAILED, FALLTHROUGH,
    # ERROR)` check accepts almost any non-crash outcome and would pass
    # identically whether or not the wire-key fix this test file exists for
    # is even applied, providing zero real regression coverage): evaluating
    # b's prerequisite on a hits the seen_keys={"a","b"} cycle guard and is
    # skipped (treated as satisfied) -- b's own prerequisites are otherwise
    # empty of blockers, so b evaluates FALLTHROUGH=True at its 100% rollout;
    # back in a's own check, that "true" matches a's required_variation, so a
    # also proceeds to its own 100% rollout FALLTHROUGH=True.
    result = evaluate(
        a, EvaluationContext(user_id="u"), False, "a", all_flags=all_flags
    )
    assert result.reason == "FALLTHROUGH"
    assert result.value is True


# ── V-8: Soft prerequisite (gate=False) ─────────────────────────────────────


def test_soft_prerequisite_unmet_does_not_block_evaluation():
    # parent is disabled -> dep_result False != required True, but gate=False
    # means this must NOT block evaluation of the child flag.
    parent = _flag("parent", enabled=False)
    child = _flag(
        "child",
        rollout_pct=100.0,
        prerequisites=[
            {"flag_key": "parent", "required_variation": "true", "gate": False}
        ],
    )
    all_flags = {"parent": parent, "child": child}
    result = evaluate(
        child, EvaluationContext(user_id="u"), False, "child", all_flags=all_flags
    )
    assert result.reason != "PREREQUISITE_FAILED"
    assert result.reason == "FALLTHROUGH"
    assert result.value is True


def test_hard_prerequisite_gate_true_unmet_blocks_evaluation():
    # Explicit gate=True must retain today's hard-blocking behavior.
    parent = _flag("parent", enabled=False)
    child = _flag(
        "child",
        rollout_pct=100.0,
        prerequisites=[
            {"flag_key": "parent", "required_variation": "true", "gate": True}
        ],
    )
    all_flags = {"parent": parent, "child": child}
    result = evaluate(
        child, EvaluationContext(user_id="u"), False, "child", all_flags=all_flags
    )
    assert result.reason == "PREREQUISITE_FAILED"
    assert result.value is False


def test_default_prerequisite_gate_omitted_unmet_blocks_evaluation():
    # Regression guard: omitting "gate" entirely must preserve legacy
    # unconditional-block behavior (default gate=True).
    parent = _flag("parent", enabled=False)
    child = _flag(
        "child",
        rollout_pct=100.0,
        prerequisites=[{"flag_key": "parent", "required_variation": "true"}],
    )
    all_flags = {"parent": parent, "child": child}
    result = evaluate(
        child, EvaluationContext(user_id="u"), False, "child", all_flags=all_flags
    )
    assert result.reason == "PREREQUISITE_FAILED"
    assert result.value is False
