import pytest
from tombstone.matching import match_property
from tombstone.types import EvaluationContext, PropertyCondition
from tombstone.exceptions import InconclusiveMatchError


def _ctx(**attrs) -> EvaluationContext:
    return EvaluationContext(user_id="u1", attrs=attrs)


def _cond(attr, op, values, negate=False) -> PropertyCondition:
    return PropertyCondition(attribute=attr, operator=op, values=values, negate=negate)


# ── equality ──────────────────────────────────────────────────────────────────

def test_eq_match():
    assert match_property(_cond("country", "eq", ["US"]), _ctx(country="US")) is True

def test_eq_no_match():
    assert match_property(_cond("country", "eq", ["US"]), _ctx(country="CA")) is False

def test_neq_match():
    assert match_property(_cond("plan", "neq", ["free"]), _ctx(plan="pro")) is True

def test_in_match():
    assert match_property(_cond("plan", "in", ["free", "pro"]), _ctx(plan="pro")) is True

def test_nin_no_match():
    assert match_property(_cond("plan", "nin", ["free", "pro"]), _ctx(plan="pro")) is False

def test_negate_flips_result():
    assert match_property(_cond("country", "eq", ["US"], negate=True), _ctx(country="US")) is False


# ── string operators ──────────────────────────────────────────────────────────

def test_contains():
    assert match_property(_cond("email", "contains", ["@acme"]), _ctx(email="bob@acme.com")) is True

def test_contains_case_insensitive():
    assert match_property(_cond("email", "contains", ["@ACME"]), _ctx(email="bob@acme.com")) is True

def test_startswith():
    assert match_property(_cond("role", "startsWith", ["admin"]), _ctx(role="admin_eu")) is True

def test_endswith():
    assert match_property(_cond("email", "endsWith", [".io"]), _ctx(email="bob@acme.io")) is True

def test_string_multi_value_any():
    # matches if context value contains ANY of the values
    assert match_property(_cond("email", "contains", ["@acme", "@beta"]), _ctx(email="x@beta.io")) is True


# ── numeric operators ─────────────────────────────────────────────────────────

def test_gt_match():
    assert match_property(_cond("score", "gt", ["50"]), _ctx(score="75")) is True

def test_gte_match():
    assert match_property(_cond("score", "gte", ["75"]), _ctx(score="75")) is True

def test_lt_no_match():
    assert match_property(_cond("score", "lt", ["50"]), _ctx(score="75")) is False

def test_lte_match():
    assert match_property(_cond("score", "lte", ["100"]), _ctx(score="75")) is True


# ── semver operators ──────────────────────────────────────────────────────────

def test_semver_gt():
    assert match_property(_cond("app_version", "semver_gt", ["1.0.0"]), _ctx(app_version="2.0.0")) is True

def test_semver_gte():
    assert match_property(_cond("app_version", "semver_gte", ["2.0.0"]), _ctx(app_version="2.0.0")) is True

def test_semver_lt():
    assert match_property(_cond("app_version", "semver_lt", ["3.0.0"]), _ctx(app_version="2.1.0")) is True

def test_semver_eq():
    assert match_property(_cond("app_version", "semver_eq", ["1.2.3"]), _ctx(app_version="1.2.3")) is True

def test_semver_pre_release_less_than_release():
    # 1.0.0-beta < 1.0.0 (pre-release is less than release per semver)
    assert match_property(_cond("app_version", "semver_lt", ["1.0.0"]), _ctx(app_version="1.0.0-beta")) is True


# ── UPPERCASE / wire-format operator normalization ────────────────────────────

def test_uppercase_operator_eq():
    assert match_property(_cond("country", "EQ", ["US"]), _ctx(country="US")) is True

def test_prefix_alias_for_startswith():
    assert match_property(_cond("role", "PREFIX", ["admin"]), _ctx(role="admin_eu")) is True

def test_suffix_alias_for_endswith():
    assert match_property(_cond("email", "SUFFIX", [".io"]), _ctx(email="bob@acme.io")) is True


# ── missing attribute ─────────────────────────────────────────────────────────

def test_missing_attribute_raises_inconclusive():
    with pytest.raises(InconclusiveMatchError):
        match_property(_cond("country", "eq", ["US"]), _ctx())
