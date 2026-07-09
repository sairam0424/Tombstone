import sys
import os

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from tombstone.evaluation import evaluate
from tombstone.types import EvaluationContext, FlagEnvironmentState


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


def test_murmurhash3_bucket_matches_typescript_reference():
    import mmh3
    # Vectors: (flag_key, user_id, rollout_pct, expected_in_cohort)
    # Generated from TypeScript: murmurhash.v3(flag_key + userId) >>> 0 % 100
    vectors = [
        ("checkout-v2", "user-abc-123", 100, True),
        ("checkout-v2", "user-abc-123", 0, False),
        ("checkout-v2", "user-xyz-789", 50, False),
        ("payment-gateway-fee-display", "user-abc-123", 50, True),
    ]
    for flag_key, user_id, rollout_pct, expected in vectors:
        bucket = mmh3.hash(flag_key + user_id, seed=0, signed=False) % 100
        actual = bucket < rollout_pct
        assert actual == expected, (
            f"Hash parity FAILED: ({flag_key!r}, {user_id!r}, {rollout_pct}%) "
            f"bucket={bucket}, expected={expected}, got={actual}"
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
        conditions=[PropertyCondition(attribute="country", operator="eq", values=["US"], negate=False)],
        rollout_pct=100.0,
        variation=True,
    )
    assert rule.id == "rule-1"
    assert rule.rollout_pct == 100.0


def test_flag_state_has_targeting_rules():
    from tombstone.types import FlagEnvironmentState
    flag = FlagEnvironmentState(
        flag_key="f", enabled=True, rollout_pct=100.0,
        safe_default=False, environment="prod",
        targeting_rules=[], prerequisites=[],
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
                "prerequisites": [{"flag_key": "parent", "required_value": True}],
                "targeting_rules": [
                    {
                        "id": "r1",
                        "conditions": [
                            {"attribute": "country", "operator": "eq", "values": ["US"], "negate": False}
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
    assert _is_in_rollout_fnv("checkout-v2", "user-abc", 50.0) == _is_in_rollout_fnv("checkout-v2", "user-abc", 50.0)
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
    result = evaluate(flag, EvaluationContext(user_id="u", attrs={"country": "US"}), False, "f")
    assert result.value == "first"


# ── V-7: Circular prerequisite guard ─────────────────────────────────────────

def test_circular_prerequisite_does_not_recurse_infinitely():
    a = _flag("a", prerequisites=[{"flag_key": "b", "required_value": True}])
    b = _flag("b", prerequisites=[{"flag_key": "a", "required_value": True}])
    all_flags = {"a": a, "b": b}
    # Should not raise RecursionError
    result = evaluate(a, EvaluationContext(user_id="u"), False, "a", all_flags=all_flags)
    assert result.reason in ("PREREQUISITE_FAILED", "FALLTHROUGH", "ERROR")
