"""Reference oracle for Tombstone cross-language contract vectors.

Implements ONLY the canonical model resolved in
docs/superpowers/specs/2026-08-04-v1.5.0-sdk-parity-and-dependency-viz-design.md
Section 3 — this is NOT a copy of any shipped SDK, since neither TS nor
Python fully implements the canonical choices (e.g. Python lacks
dot-notation attribute resolution and GEO operators; TS lacks semver/date
operators and per-rule rollout sub-bucketing).

This script is a one-time generator. It is deleted after vectors.json is
regenerated and manually spot-checked — it is not shipped SDK code and has
no ongoing maintenance contract.
"""
from __future__ import annotations

import json
import re
from datetime import datetime

import mmh3

FNV_OFFSET = 2166136261
FNV_PRIME = 16777619


def murmur3_v1_bucket(flag_key: str, user_id: str) -> int:
    """MurmurHash3 unsigned 32-bit, seed=0, mod 100."""
    return mmh3.hash(flag_key + user_id, seed=0, signed=False) % 100


def _fnv1a_raw(s: str) -> int:
    h = FNV_OFFSET
    for b in s.encode("utf-8"):
        h ^= b
        h = (h * FNV_PRIME) & 0xFFFFFFFF
    return h & 0xFFFFFFFF


def fnv1a_v2_bucket(flag_key: str, user_id: str) -> float:
    """Double-pass FNV-1a, UTF-8 byte iteration, mod 10000, as a fraction."""
    h1 = _fnv1a_raw(flag_key + user_id)
    h2 = _fnv1a_raw(str(h1))
    return (h2 % 10000) / 10000


from dataclasses import dataclass


def padded_version(v: str) -> str:
    """Ported byte-for-byte from flagmind-python's matching.py:27-39."""
    v = re.sub(r"(^v|\+.*$)", "", v)
    parts = re.split(r"[-.]", v)
    padded = [p.rjust(5, " ") if re.match(r"^\d+$", p) else p for p in parts]
    if len(padded) == 3:
        padded.append("~")
    return ".".join(padded)


@dataclass
class Condition:
    attribute: str
    operator: str
    values: list
    negate: bool = False


def _resolve_attribute(attribute: str, attrs: dict):
    """Canonical resolution: dot-notation nested paths with flat-attrs fallback (TS's approach)."""
    segments = attribute.split(".")
    current = attrs
    for seg in segments:
        if not isinstance(current, dict) or seg not in current:
            current = None
            break
        current = current[seg]
    if current is not None:
        return current
    if len(segments) == 1 and attribute in attrs:
        return attrs[attribute]
    return None


def evaluate_condition(condition: Condition, attrs: dict):
    """Returns True/False, or the string 'inconclusive' if the condition cannot be evaluated."""
    raw = _resolve_attribute(condition.attribute, attrs)
    if raw is None:
        return "inconclusive"
    attr_val = str(raw)
    op = condition.operator.lower()
    op = {"not_in": "nin", "prefix": "startswith", "suffix": "endswith"}.get(op, op)
    values = condition.values

    is_geo = condition.attribute in ("geo.country", "geo.region")

    if op in ("eq", "in"):
        if is_geo:
            result = attr_val.upper() in [str(v).upper() for v in values]
        else:
            result = attr_val in values
    elif op in ("neq", "nin"):
        if is_geo:
            result = attr_val.upper() not in [str(v).upper() for v in values]
        else:
            result = attr_val not in values
    elif op in ("contains", "startswith", "endswith"):
        low_attr = attr_val.upper()
        low_vals = [str(v).upper() for v in values]
        if op == "contains":
            result = any(v in low_attr for v in low_vals)
        elif op == "startswith":
            result = any(low_attr.startswith(v) for v in low_vals)
        else:
            result = any(low_attr.endswith(v) for v in low_vals)
    elif op in ("gt", "gte", "lt", "lte"):
        try:
            n_attr = float(attr_val)
            n_val = float(values[0])
        except (ValueError, IndexError):
            return "inconclusive"
        result = {"gt": n_attr > n_val, "gte": n_attr >= n_val,
                  "lt": n_attr < n_val, "lte": n_attr <= n_val}[op]
    elif op in ("semver_gt", "semver_gte", "semver_lt", "semver_lte", "semver_eq"):
        if not values:
            return "inconclusive"
        a = padded_version(attr_val)
        b = padded_version(values[0])
        result = {"semver_gt": a > b, "semver_gte": a >= b, "semver_lt": a < b,
                  "semver_lte": a <= b, "semver_eq": a == b}[op]
    elif op in ("date_before", "date_after"):
        try:
            dt_attr = datetime.fromisoformat(attr_val.replace("Z", "+00:00"))
            dt_val = datetime.fromisoformat(values[0].replace("Z", "+00:00"))
        except (ValueError, IndexError):
            return "inconclusive"
        result = dt_attr < dt_val if op == "date_before" else dt_attr > dt_val
    else:
        return "inconclusive"

    if result == "inconclusive":
        return result
    return (not result) if condition.negate else result
