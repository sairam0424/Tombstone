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
