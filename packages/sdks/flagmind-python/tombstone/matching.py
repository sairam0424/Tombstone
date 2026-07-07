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
    op = condition.operator.lower()
    # Normalize wire-format aliases to SDK operator names
    _OP_ALIASES = {
        "not_in": "nin", "prefix": "startswith", "suffix": "endswith",
        "semver_gte": "semver_gte", "semver_lte": "semver_lte",
        "date_before": "date_before", "date_after": "date_after",
    }
    op = _OP_ALIASES.get(op, op)
    values = condition.values  # always a list

    result: bool

    if op in ("eq", "neq", "in", "nin"):
        match op:
            case "eq":
                result = attr_val in values
            case "neq":
                result = attr_val not in values
            case "in":
                result = attr_val in values
            case "nin":
                result = attr_val not in values
            case _:
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
            raise InconclusiveMatchError(f"Numeric cast failed for '{condition.attribute}': {exc}") from exc
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
            raise InconclusiveMatchError(f"Date parse failed for '{condition.attribute}': {exc}") from exc
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
