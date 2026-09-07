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
    assert (
        match_property(_cond("plan", "in", ["free", "pro"]), _ctx(plan="pro")) is True
    )


def test_nin_no_match():
    assert (
        match_property(_cond("plan", "nin", ["free", "pro"]), _ctx(plan="pro")) is False
    )


def test_negate_flips_result():
    assert (
        match_property(_cond("country", "eq", ["US"], negate=True), _ctx(country="US"))
        is False
    )


# ── geo case-insensitivity + GEO_COUNTRY/GEO_REGION operator ──────────────────
# This SDK previously had NO case-insensitive geo matching at all (eq/in/neq/
# nin were always case-sensitive, even for geo.country/geo.region) -- adding
# it here for the first time, mirroring the TS/Java/Ruby/.NET SDKs' own
# is_geo/isGeo logic (PRs #246-249).


def test_eq_geo_country_is_case_insensitive():
    assert (
        match_property(
            _cond("geo.country", "in", ["US", "CA"]), _ctx(**{"geo.country": "us"})
        )
        is True
    )


def test_geo_country_operator_is_recognized_and_case_insensitive():
    # flag-api's targeting_rules.operator CHECK constraint (schema.sql) has
    # GEO_COUNTRY/GEO_REGION as real, distinct operator VALUES, not just an
    # attribute-name convention -- without the alias mapping, this would
    # raise InconclusiveMatchError (unknown operator) instead of matching.
    assert (
        match_property(
            _cond("geo.country", "GEO_COUNTRY", ["US", "CA"]),
            _ctx(**{"geo.country": "us"}),
        )
        is True
    )


def test_geo_region_operator_is_case_insensitive_even_with_a_non_canonical_attribute_name():
    # Found by adversarial review of the Java/Ruby/.NET SDKs' identical fix
    # (PRs #247-249): is_geo must also check the raw operator string, not
    # just the attribute name -- nothing (backend or SDK) validates that
    # operator=GEO_REGION implies attribute=="geo.region".
    assert (
        match_property(_cond("region", "GEO_REGION", ["CA-ON"]), _ctx(region="ca-on"))
        is True
    )


def test_nin_with_empty_values_never_matches():
    # An EMPTY values list must never match "neq"/"nin": `attr_val not in
    # values` on an empty list is vacuously True, which would make a rule
    # with an empty/missing "values" list match EVERY context
    # unconditionally. Same bug class found and fixed in the TS/Java/Ruby/
    # .NET SDKs' own NOT_IN/"neq"/"nin" branches (PRs #246-249).
    assert match_property(_cond("plan", "nin", []), _ctx(plan="anything")) is False


def test_nin_with_empty_values_never_matches_for_geo_attribute():
    assert (
        match_property(_cond("geo.country", "nin", []), _ctx(**{"geo.country": "US"}))
        is False
    )


def test_nin_with_non_empty_values_still_excludes_correctly():
    cond = _cond("plan", "nin", ["banned", "suspended"])
    assert match_property(cond, _ctx(plan="banned")) is False
    assert match_property(cond, _ctx(plan="pro")) is True


def test_nin_with_non_empty_values_is_case_insensitive_for_geo_attribute():
    cond = _cond("geo.country", "nin", ["US"])
    assert match_property(cond, _ctx(**{"geo.country": "us"})) is False
    assert match_property(cond, _ctx(**{"geo.country": "ca"})) is True


# ── REGEX contract compliance ───────────────────────────────────────────────
# docs/SDK_CONTRACT.md:32 -- REGEX is declared but deliberately NOT
# implemented in this release, across all 5 SDKs. It must return a definite
# False (matching TS's documented behavior), NOT raise InconclusiveMatchError
# like a genuinely unknown operator would -- the throw-vs-false distinction
# matters specifically for negate=True (raising skips the whole rule
# regardless of negate; the contract's literal "false, negated -> true"
# semantics require a definite result).


def test_regex_returns_false_rather_than_raising():
    cond = _cond("email", "REGEX", [r"^admin.*@corp\.com$"])
    assert match_property(cond, _ctx(email="admin1@corp.com")) is False


def test_negated_regex_returns_true():
    cond = _cond("email", "REGEX", [r"^admin.*@corp\.com$"], negate=True)
    assert match_property(cond, _ctx(email="admin1@corp.com")) is True


# ── string operators ──────────────────────────────────────────────────────────


def test_contains():
    assert (
        match_property(
            _cond("email", "contains", ["@acme"]), _ctx(email="bob@acme.com")
        )
        is True
    )


def test_contains_case_insensitive():
    assert (
        match_property(
            _cond("email", "contains", ["@ACME"]), _ctx(email="bob@acme.com")
        )
        is True
    )


def test_startswith():
    assert (
        match_property(_cond("role", "startsWith", ["admin"]), _ctx(role="admin_eu"))
        is True
    )


def test_endswith():
    assert (
        match_property(_cond("email", "endsWith", [".io"]), _ctx(email="bob@acme.io"))
        is True
    )


def test_string_multi_value_any():
    # matches if context value contains ANY of the values
    assert (
        match_property(
            _cond("email", "contains", ["@acme", "@beta"]), _ctx(email="x@beta.io")
        )
        is True
    )


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
    assert (
        match_property(
            _cond("app_version", "semver_gt", ["1.0.0"]), _ctx(app_version="2.0.0")
        )
        is True
    )


def test_semver_gte():
    assert (
        match_property(
            _cond("app_version", "semver_gte", ["2.0.0"]), _ctx(app_version="2.0.0")
        )
        is True
    )


def test_semver_lt():
    assert (
        match_property(
            _cond("app_version", "semver_lt", ["3.0.0"]), _ctx(app_version="2.1.0")
        )
        is True
    )


def test_semver_eq():
    assert (
        match_property(
            _cond("app_version", "semver_eq", ["1.2.3"]), _ctx(app_version="1.2.3")
        )
        is True
    )


def test_semver_pre_release_less_than_release():
    # 1.0.0-beta < 1.0.0 (pre-release is less than release per semver)
    assert (
        match_property(
            _cond("app_version", "semver_lt", ["1.0.0"]), _ctx(app_version="1.0.0-beta")
        )
        is True
    )


# ── missing attribute ─────────────────────────────────────────────────────────


def test_missing_attribute_raises_inconclusive():
    with pytest.raises(InconclusiveMatchError):
        match_property(_cond("country", "eq", ["US"]), _ctx())
