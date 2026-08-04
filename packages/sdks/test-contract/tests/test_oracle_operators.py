import sys
import os
sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from generate_vectors import padded_version, Condition, evaluate_condition


def test_padded_version_orders_numeric_segments_correctly():
    # 1.9.0 < 1.10.0 lexically would be wrong without padding; padded strings fix it.
    assert padded_version("1.9.0") < padded_version("1.10.0")


def test_padded_version_prerelease_sorts_below_release():
    # 1.0.0-beta < 1.0.0
    assert padded_version("1.0.0-beta") < padded_version("1.0.0")


def test_padded_version_strips_v_prefix_and_build_metadata():
    assert padded_version("v1.2.3+build.5") == padded_version("1.2.3")


def test_evaluate_eq_match():
    cond = Condition(attribute="plan", operator="eq", values=["pro"], negate=False)
    assert evaluate_condition(cond, {"plan": "pro"}) is True


def test_evaluate_eq_no_match():
    cond = Condition(attribute="plan", operator="eq", values=["pro"], negate=False)
    assert evaluate_condition(cond, {"plan": "free"}) is False


def test_evaluate_contains_case_insensitive():
    cond = Condition(attribute="email", operator="contains", values=["ACME"], negate=False)
    assert evaluate_condition(cond, {"email": "user@acme.com"}) is True


def test_evaluate_numeric_gt():
    cond = Condition(attribute="age", operator="gt", values=["18"], negate=False)
    assert evaluate_condition(cond, {"age": "21"}) is True


def test_evaluate_numeric_non_numeric_is_inconclusive():
    cond = Condition(attribute="age", operator="gt", values=["18"], negate=False)
    assert evaluate_condition(cond, {"age": "not-a-number"}) == "inconclusive"


def test_evaluate_semver_gte():
    cond = Condition(attribute="app_version", operator="semver_gte", values=["1.9.0"], negate=False)
    assert evaluate_condition(cond, {"app_version": "1.10.0"}) is True


def test_evaluate_date_before():
    cond = Condition(attribute="signup_date", operator="date_before", values=["2026-01-01T00:00:00Z"], negate=False)
    assert evaluate_condition(cond, {"signup_date": "2025-06-01T00:00:00Z"}) is True


def test_evaluate_missing_attribute_is_inconclusive():
    cond = Condition(attribute="missing_attr", operator="eq", values=["x"], negate=False)
    assert evaluate_condition(cond, {}) == "inconclusive"


def test_evaluate_negate_inverts_result():
    cond = Condition(attribute="plan", operator="eq", values=["pro"], negate=True)
    assert evaluate_condition(cond, {"plan": "pro"}) is False


def test_evaluate_geo_country_case_insensitive():
    # Canonical model resolves "geo.country" via dot-notation nesting (attrs["geo"]["country"]),
    # not as a flat literal key — mirrors TS's resolveAttribute, which is the canonical choice
    # for attribute resolution per the design spec.
    cond = Condition(attribute="geo.country", operator="in", values=["US", "CA"], negate=False)
    assert evaluate_condition(cond, {"geo": {"country": "us"}}) is True
