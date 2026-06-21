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
) -> FlagEnvironmentState:
    return FlagEnvironmentState(
        flag_key=flag_key,
        enabled=enabled,
        rollout_pct=rollout_pct,
        safe_default=safe_default,
        environment=environment,
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
