from tombstone.evaluation import evaluate
from tombstone.types import (
    EvaluationContext, FlagEnvironmentState, TargetingRule, PropertyCondition
)


def _ctx(user_id="u1", **attrs) -> EvaluationContext:
    return EvaluationContext(user_id=user_id, attrs=attrs)


def _flag(key, enabled=True, rollout_pct=100.0, prerequisites=None, targeting_rules=None):
    return FlagEnvironmentState(
        flag_key=key, enabled=enabled, rollout_pct=rollout_pct,
        safe_default=False, environment="prod",
        prerequisites=prerequisites or [],
        targeting_rules=targeting_rules or [],
    )


def test_prerequisite_met_allows_evaluation():
    parent = _flag("parent-flag", enabled=True, rollout_pct=100.0)
    child = _flag(
        "child-flag",
        prerequisites=[{"flag_key": "parent-flag", "required_value": True}],
    )
    all_flags = {"parent-flag": parent, "child-flag": child}
    result = evaluate(child, _ctx(), False, "child-flag", all_flags=all_flags)
    assert result.value is True
    assert result.reason == "FALLTHROUGH"


def test_prerequisite_not_met_returns_default():
    parent = _flag("parent-flag", enabled=False)  # disabled → evaluates to False
    child = _flag(
        "child-flag",
        prerequisites=[{"flag_key": "parent-flag", "required_value": True}],
    )
    all_flags = {"parent-flag": parent, "child-flag": child}
    result = evaluate(child, _ctx(), False, "child-flag", all_flags=all_flags)
    assert result.value is False
    assert result.reason == "PREREQUISITE_FAILED"


def test_prerequisite_missing_flag_returns_default():
    child = _flag(
        "child-flag",
        prerequisites=[{"flag_key": "ghost-flag", "required_value": True}],
    )
    result = evaluate(child, _ctx(), False, "child-flag", all_flags={})
    assert result.value is False
    assert result.reason == "PREREQUISITE_FAILED"


def test_prerequisite_cached_result_used():
    """evaluation_cache prevents re-evaluation of the same prerequisite."""
    parent = _flag("parent-flag", enabled=False)  # would fail if evaluated
    child = _flag(
        "child-flag",
        prerequisites=[{"flag_key": "parent-flag", "required_value": True}],
    )
    all_flags = {"parent-flag": parent}
    # Pre-populate cache with True — parent is "met" from cache, not re-evaluated
    cache = {"parent-flag": True}
    result = evaluate(child, _ctx(), False, "child-flag", all_flags=all_flags, evaluation_cache=cache)
    assert result.value is True  # cache said True, so prerequisite was met


def test_targeting_rule_match_returns_rule_variation():
    flag = _flag(
        "my-flag",
        rollout_pct=0.0,  # fallthrough would be False
        targeting_rules=[
            TargetingRule(
                id="us-only",
                conditions=[PropertyCondition(attribute="country", operator="eq", values=["US"], negate=False)],
                rollout_pct=100.0,
                variation=True,
            )
        ],
    )
    result = evaluate(flag, _ctx(country="US"), False, "my-flag")
    assert result.value is True
    assert result.reason == "RULE_MATCH"


def test_targeting_rule_no_match_falls_through():
    flag = _flag(
        "my-flag",
        rollout_pct=100.0,
        targeting_rules=[
            TargetingRule(
                id="us-only",
                conditions=[PropertyCondition(attribute="country", operator="eq", values=["US"], negate=False)],
                rollout_pct=100.0,
                variation="special",
            )
        ],
    )
    result = evaluate(flag, _ctx(country="CA"), False, "my-flag")
    assert result.reason == "FALLTHROUGH"
    assert result.value is True  # 100% rollout fallthrough
