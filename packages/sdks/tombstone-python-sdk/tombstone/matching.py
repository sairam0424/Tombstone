# packages/sdks/flagmind-python/tombstone/matching.py
"""Property matching for targeting rules.

Operator surface mirrors the TypeScript SDK's evaluation.ts rule matching:
  equality    : eq, neq, in, nin
  string      : contains, startsWith, endsWith  (case-insensitive, multi-value any())
  numeric     : gt, gte, lt, lte
  semver      : semver_gt, semver_gte, semver_lt, semver_lte, semver_eq
  date        : date_before, date_after  (ISO-8601 strings)

Zero external dependencies — semver via _padded_version() (GrowthBook pattern).
"""

from __future__ import annotations

import re
from datetime import datetime, timezone

from tombstone.exceptions import InconclusiveMatchError
from tombstone.types import EvaluationContext, PropertyCondition

_SEMVER_OPS = {"semver_gt", "semver_gte", "semver_lt", "semver_lte", "semver_eq"}
_NUMERIC_OPS = {"gt", "gte", "lt", "lte"}
_DATE_OPS = {"date_before", "date_after"}
_STRING_OPS = {"contains", "startswith", "endswith"}
_GEO_ATTRIBUTES = {"geo.country", "geo.region"}


def _padded_version(v: str) -> str:
    """Normalize a semver string for pure-string comparison.

    Strips leading 'v' and build metadata (+...), splits on [-. ],
    left-pads each numeric segment to 5 chars, appends '~' to 3-part
    releases so that 1.0.0 > 1.0.0-beta (GrowthBook paddedVersionString).
    """
    v = re.sub(r"(^v|\+.*$)", "", v)
    parts = re.split(r"[-.]", v)
    padded = [p.rjust(5, " ") if re.match(r"^\d+$", p) else p for p in parts]
    if len(padded) == 3:
        padded.append("~")
    return ".".join(padded)


def _contains_ignore_case(values: list, attr_val: str) -> bool:
    upper = attr_val.upper()
    return any(str(v).upper() == upper for v in values)


def _get_attr(condition: PropertyCondition, context: EvaluationContext) -> str:
    """Retrieve the attribute value from context; raise InconclusiveMatchError if absent."""
    # Special-case built-in attributes
    if condition.attribute == "user_id":
        return context.user_id
    if condition.attribute == "org_id":
        return context.org_id
    val = context.attrs.get(condition.attribute)
    if val is None:
        raise InconclusiveMatchError(
            f"Attribute '{condition.attribute}' not present in evaluation context"
        )
    return str(val)


def match_property(condition: PropertyCondition, context: EvaluationContext) -> bool:
    """Return True if the context satisfies condition, False otherwise.

    Raises InconclusiveMatchError when the required attribute is absent.
    Never raises RequiresServerEvaluation — that is the caller's concern
    (e.g. cohort membership checks that need server data).
    """
    attr_val = _get_attr(condition, context)
    raw_op = condition.operator.lower()
    # Normalize wire-format aliases to SDK operator names
    _OP_ALIASES = {
        "not_in": "nin",
        "prefix": "startswith",
        "suffix": "endswith",
        "semver_gte": "semver_gte",
        "semver_lte": "semver_lte",
        "date_before": "date_before",
        "date_after": "date_after",
        # flag-api's targeting_rules.operator CHECK constraint (schema.sql)
        # has GEO_COUNTRY/GEO_REGION as real, distinct operator VALUES (not
        # just an attribute-name convention) -- without this mapping, a
        # targeting rule using either would hit the final "else" branch
        # below and raise InconclusiveMatchError on every evaluation,
        # silently never matching for any user. Mapped to "in" so it falls
        # into the eq/in branch, whose is_geo case-insensitive comparison
        # already implements the real geo-matching semantics correctly --
        # the identical gap the TS/Java/Ruby/.NET SDKs' own operator
        # normalization needed (PRs #246-249).
        "geo_country": "in",
        "geo_region": "in",
    }
    op = _OP_ALIASES.get(raw_op, raw_op)
    values = condition.values  # always a list
    # is_geo is true whenever EITHER the attribute is a recognized geo path
    # OR the operator itself declares geo semantics (GEO_COUNTRY/
    # GEO_REGION) -- checking the attribute name ALONE would mean a rule
    # using a non-canonical attribute (e.g. "country" instead of
    # "geo.country") with a real GEO_COUNTRY operator silently falls back
    # to case-SENSITIVE matching, even though nothing (backend or SDK)
    # validates that operator=GEO_COUNTRY implies attribute=="geo.country".
    # Found and fixed identically in the Java/Ruby/.NET SDKs' own
    # match_property/EvaluateCondition (PRs #247-249); this SDK previously
    # had NO case-insensitive geo matching at all (eq/in/neq/nin were
    # always case-sensitive) -- adding it here for the first time, not
    # just the attribute/operator-decoupling fix on top of an existing one.
    is_geo = condition.attribute in _GEO_ATTRIBUTES or raw_op in (
        "geo_country",
        "geo_region",
    )

    result: bool

    if op in ("eq", "in"):
        result = (
            _contains_ignore_case(values, attr_val) if is_geo else attr_val in values
        )

    elif op in ("neq", "nin"):
        # An EMPTY values list must never match "neq"/"nin": `attr_val not
        # in values` on an empty list is vacuously True (there is nothing
        # to find, so "not found" is trivially true), which would make a
        # rule with an empty/missing "values" list match EVERY context
        # unconditionally -- the opposite of "no exclusions configured, so
        # exclude nothing". Same bug class found and fixed in the
        # TypeScript/Java/Ruby/.NET SDKs' own NOT_IN/"neq"/"nin" branches
        # (PRs #246-249); fixed here proactively per those findings' own
        # explicit note that the remaining SDKs likely share it.
        result = bool(values) and (
            not _contains_ignore_case(values, attr_val)
            if is_geo
            else attr_val not in values
        )

    elif op == "regex":
        # docs/SDK_CONTRACT.md:32 -- REGEX is declared (a real, distinct
        # operator value in flag-api's targeting_rules.operator CHECK
        # constraint) but deliberately NOT IMPLEMENTED in this release,
        # across all 5 SDKs (parity matrix: "No" for every language) --
        # matching TypeScript's own default-false behavior. Returning a
        # definite False (not raising) matters specifically for
        # negate=True: raising would skip the whole rule regardless of
        # negate, while the contract's literal "false, negated -> true"
        # semantics require a definite result here. Does NOT implement
        # real regex matching, which stays deliberately deferred ("Future
        # work") for cross-SDK parity. Found missing by adversarial review
        # of the .NET SDK's PR #249 (which found the identical gap already
        # merged in Java, PR #247/#250); fixed here proactively.
        result = False

    elif op in _STRING_OPS:
        # Case-insensitive; matches if context value satisfies ANY value in list
        low_attr = attr_val.upper()
        low_vals = [v.upper() for v in values]
        match op:
            case "contains":
                result = any(v in low_attr for v in low_vals)
            case "startswith":
                result = any(low_attr.startswith(v) for v in low_vals)
            case "endswith":
                result = any(low_attr.endswith(v) for v in low_vals)
            case _:
                result = False

    elif op in _NUMERIC_OPS:
        try:
            n_attr = float(attr_val)
            n_val = float(values[0])
        except (ValueError, IndexError) as exc:
            raise InconclusiveMatchError(
                f"Numeric cast failed for '{condition.attribute}': {exc}"
            ) from exc
        match op:
            case "gt":
                result = n_attr > n_val
            case "gte":
                result = n_attr >= n_val
            case "lt":
                result = n_attr < n_val
            case "lte":
                result = n_attr <= n_val
            case _:
                result = False

    elif op in _SEMVER_OPS:
        if not values:
            raise InconclusiveMatchError("semver operator requires at least one value")
        a = _padded_version(attr_val)
        b = _padded_version(values[0])
        match op:
            case "semver_gt":
                result = a > b
            case "semver_gte":
                result = a >= b
            case "semver_lt":
                result = a < b
            case "semver_lte":
                result = a <= b
            case "semver_eq":
                result = a == b
            case _:
                result = False

    elif op in _DATE_OPS:
        try:
            dt_attr = datetime.fromisoformat(attr_val.replace("Z", "+00:00"))
            dt_val = datetime.fromisoformat(values[0].replace("Z", "+00:00"))
        except (ValueError, IndexError) as exc:
            raise InconclusiveMatchError(
                f"Date parse failed for '{condition.attribute}': {exc}"
            ) from exc
        match op:
            case "date_before":
                result = dt_attr < dt_val
            case "date_after":
                result = dt_attr > dt_val
            case _:
                result = False

    else:
        raise InconclusiveMatchError(f"Unknown operator: '{op}'")

    return (not result) if condition.negate else result
