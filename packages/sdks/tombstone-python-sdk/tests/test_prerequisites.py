from tombstone.evaluation import evaluate
from tombstone.types import (
    EvaluationContext,
    FlagEnvironmentState,
    TargetingRule,
    PropertyCondition,
)


def _ctx(user_id="u1", **attrs) -> EvaluationContext:
    return EvaluationContext(user_id=user_id, attrs=attrs)


def _flag(
    key, enabled=True, rollout_pct=100.0, prerequisites=None, targeting_rules=None
):
    return FlagEnvironmentState(
        flag_key=key,
        enabled=enabled,
        rollout_pct=rollout_pct,
        safe_default=False,
        environment="prod",
        prerequisites=prerequisites or [],
        targeting_rules=targeting_rules or [],
    )


def test_prerequisite_met_allows_evaluation():
    parent = _flag("parent-flag", enabled=True, rollout_pct=100.0)
    child = _flag(
        "child-flag",
        prerequisites=[{"flag_key": "parent-flag", "required_variation": "true"}],
    )
    all_flags = {"parent-flag": parent, "child-flag": child}
    result = evaluate(child, _ctx(), False, "child-flag", all_flags=all_flags)
    assert result.value is True
    assert result.reason == "FALLTHROUGH"


def test_prerequisite_not_met_returns_default():
    parent = _flag("parent-flag", enabled=False)  # disabled → evaluates to False
    child = _flag(
        "child-flag",
        prerequisites=[{"flag_key": "parent-flag", "required_variation": "true"}],
    )
    all_flags = {"parent-flag": parent, "child-flag": child}
    result = evaluate(child, _ctx(), False, "child-flag", all_flags=all_flags)
    assert result.value is False
    assert result.reason == "PREREQUISITE_FAILED"


def test_prerequisite_missing_flag_returns_default():
    child = _flag(
        "child-flag",
        prerequisites=[{"flag_key": "ghost-flag", "required_variation": "true"}],
    )
    result = evaluate(child, _ctx(), False, "child-flag", all_flags={})
    assert result.value is False
    assert result.reason == "PREREQUISITE_FAILED"


def test_prerequisite_cached_result_used():
    """evaluation_cache prevents re-evaluation of the same prerequisite."""
    parent = _flag("parent-flag", enabled=False)  # would fail if evaluated
    child = _flag(
        "child-flag",
        prerequisites=[{"flag_key": "parent-flag", "required_variation": "true"}],
    )
    all_flags = {"parent-flag": parent}
    # Pre-populate cache with the wire's own string form (not a bool) — parent
    # is "met" from cache, not re-evaluated.
    cache = {"parent-flag": "true"}
    result = evaluate(
        child, _ctx(), False, "child-flag", all_flags=all_flags, evaluation_cache=cache
    )
    assert result.value is True  # cache said "true", so prerequisite was met


def test_prerequisite_uses_real_wire_key_names_not_prereq_flag_key_or_required_value():
    """Regression: flag-api's real snapshot/REST wire sends "flag_key" (not
    "prereq_flag_key" -- that's only flag_prerequisites' own DB column name)
    and "required_variation" (a string, not "required_value" as a bool).
    Before flag-api's prereq_flag_key->flag_key wire rename, _check_prerequisites
    already read "flag_key" -- the CORRECT proto-contract key -- but the
    backend was sending "prereq_flag_key", so dep_key always resolved to ""
    (dict.get's fallback), and any hard gate (gate=True, the default) then
    unconditionally blocked the parent flag regardless of the dependency's
    real state. This constructs the prerequisite dict exactly as flag-api's
    snapshot endpoint sends it today, proving a real, satisfied dependency is
    no longer unconditionally treated as failed.

    Uses required_variation="false" (not "true") deliberately -- adversarial
    review of an earlier version of this test found that "true" doesn't
    actually distinguish fixed from pre-fix behavior here: the pre-fix code's
    `prereq.get("required_value", True)` fallback default (a bool True) and
    Python's own str()-free bool comparison happened to coincidentally agree
    with a satisfied "true" dependency regardless of the key-name bug, so the
    test passed against BOTH pre-fix and post-fix code -- zero real
    regression coverage despite its own docstring's claim. "false" does not
    coincide with that default: pre-fix code always compares against the
    bool True regardless of what required_variation says, so a genuinely
    satisfied "false" dependency was pre-fix wrongly treated as unmet and
    blocked; post-fix it correctly proceeds.
    """
    parent = _flag("parent-flag", enabled=False)  # -> safe_default -> "false"
    child = _flag(
        "child-flag",
        prerequisites=[
            {
                "id": "prereq-1",
                "flag_key": "parent-flag",
                "required_variation": "false",
                "gate": True,
                "priority": 0,
            }
        ],
    )
    all_flags = {"parent-flag": parent, "child-flag": child}
    result = evaluate(child, _ctx(), False, "child-flag", all_flags=all_flags)
    assert result.value is True
    assert result.reason == "FALLTHROUGH"


def test_prerequisite_honors_dependencys_own_safe_default_not_a_hardcoded_false():
    """Regression: _check_prerequisites' recursive evaluate() call for a
    DISABLED dependency must use that dependency's own configured
    safe_default (a real, per-flag field), not the hardcoded literal False
    passed as the recursive call's own default_value parameter. Before this
    fix, evaluation.py's OFF-path (Step 1) never consulted safe_default at
    all -- the only one of the 4 SDKs that didn't; Java's EvaluationEngine
    documents parsing safeDefault as "the canonical model" (found by
    adversarial review of PR #229). Dormant at the top level (every existing
    fixture happened to set safe_default equal to its own default_value), but
    a real divergence via prerequisites: a disabled dependency configured
    with safe_default="true" must satisfy a required_variation="true" hard
    gate, even though the caller's own default_value for that recursive call
    is a hardcoded False.
    """
    parent = FlagEnvironmentState(
        flag_key="parent-flag",
        enabled=False,
        rollout_pct=100.0,
        safe_default="true",  # the real wire type: a string, not a bool
        environment="prod",
    )
    child = _flag(
        "child-flag",
        prerequisites=[
            {"flag_key": "parent-flag", "required_variation": "true", "gate": True}
        ],
    )
    all_flags = {"parent-flag": parent, "child-flag": child}
    result = evaluate(child, _ctx(), False, "child-flag", all_flags=all_flags)
    assert result.value is True
    assert result.reason == "FALLTHROUGH"


def test_soft_prerequisite_unmet_is_skipped_not_blocking():
    """gate=False: an unmet dependency must not block the parent flag."""
    parent = _flag("parent-flag", enabled=False)  # evaluates to False
    child = _flag(
        "child-flag",
        rollout_pct=100.0,
        prerequisites=[
            {"flag_key": "parent-flag", "required_variation": "true", "gate": False}
        ],
    )
    all_flags = {"parent-flag": parent, "child-flag": child}
    result = evaluate(child, _ctx(), False, "child-flag", all_flags=all_flags)
    assert result.value is True
    assert result.reason == "FALLTHROUGH"


def test_stringify_variation_matches_lowercase_wire_convention_not_python_str():
    """Regression: Python's str(True) is "True" (capital T); the wire's
    boolean convention (and Java's String.valueOf(Boolean)) is lowercase
    "true"/"false". A prerequisite comparing a stringified True against a
    real "true" required_variation must not silently fail from a casing
    mismatch alone.
    """
    from tombstone.evaluation import _stringify_variation

    assert _stringify_variation(True) == "true"
    assert _stringify_variation(False) == "false"
    assert _stringify_variation("variation-b") == "variation-b"


def test_targeting_rule_match_returns_rule_variation():
    flag = _flag(
        "my-flag",
        rollout_pct=0.0,  # fallthrough would be False
        targeting_rules=[
            TargetingRule(
                id="us-only",
                conditions=[
                    PropertyCondition(
                        attribute="country", operator="eq", values=["US"], negate=False
                    )
                ],
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
                conditions=[
                    PropertyCondition(
                        attribute="country", operator="eq", values=["US"], negate=False
                    )
                ],
                rollout_pct=100.0,
                variation="special",
            )
        ],
    )
    result = evaluate(flag, _ctx(country="CA"), False, "my-flag")
    assert result.reason == "FALLTHROUGH"
    assert result.value is True  # 100% rollout fallthrough
