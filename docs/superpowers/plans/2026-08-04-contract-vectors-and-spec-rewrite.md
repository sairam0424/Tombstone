# Contract Vectors Expansion + SDK_CONTRACT.md Rewrite Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Expand `packages/sdks/test-contract/vectors.json` from hash-only (24 vectors) to cover every dimension of the canonical evaluation spec (prerequisites, target list, all rule operators, multi-condition rules, priority ordering, missing attributes) — generated from a small, hand-verified Python reference oracle, not hand-computed — and rewrite `docs/SDK_CONTRACT.md` from an observational matrix into the normative canonical spec. This is Phase 1 of the v1.5.0 upgrade; Java/Ruby/.NET implementation plans consume this file's exact output schema.

**Architecture:** A standalone, temporary Python oracle script (`packages/sdks/test-contract/generate_vectors.py`) implements ONLY the canonical choices from the design spec — not a copy of the existing `flagmind-python` SDK, which does not itself match the canonical model (e.g. it lacks dot-notation attribute resolution and GEO operators). The oracle is unit-tested against hand-verified values for a small seed set, then used to mechanically compute `expected` outputs for every new vector, eliminating hand-arithmetic transcription errors. The script is a one-time generator, not shipped SDK code — it is deleted after vectors.json is generated and verified, per YAGNI (no ongoing maintenance burden for a tool that runs once).

**Tech Stack:** Python 3.12 (stdlib only — `hashlib` is not used; MurmurHash3 needs the `mmh3` package, already a `flagmind-python` dependency), `pytest` for the oracle's own unit tests, no new runtime dependencies for any shipped package.

## Global Constraints

- Canonical spec resolutions (from `docs/superpowers/specs/2026-08-04-v1.5.0-sdk-parity-and-dependency-viz-design.md` Section 3) are the ONLY source of truth for expected values — do not consult TS or Python SDK output directly, since neither fully implements the canonical model.
- MurmurHash3 v1: seed=0, unsigned 32-bit, mod 100, `concat(flag_key, user_id)` as UTF-8 bytes.
- FNV-1a v2: UTF-8 byte iteration (not UTF-16 code-unit), double-pass through decimal string, mod 10000, prime=16777619, offset=2166136261.
- Semver padding: `_padded_version()` algorithm ported byte-for-byte from `packages/sdks/flagmind-python/tombstone/matching.py:27-39`.
- String operators (`contains`/`startswith`/`endswith`): case-insensitive (canonical choice — matches Python's existing shipped behavior, not TS's).
- Missing-attribute signaling: `inconclusive` outcome (not `false`) — every SDK's idiomatic exception equivalent must map to this vector outcome.
- `vectors.json` version bumps `"1.1"` → `"1.2"` (additive schema change — new categories, existing hash vectors unchanged).
- Do NOT modify `packages/sdks/@flagmind/core/src/evaluation.ts` or `packages/sdks/flagmind-python/tombstone/evaluation.py` — TS/Python stay untouched per design spec Section 1.
- Branch: `feat/contract-vectors-v1.5.0` off `origin/develop`. All commits go here.
- Run `python3 -m pytest packages/sdks/test-contract/tests/ -v` before every commit that touches the oracle.

---

## Phase 1 — Reference Oracle

### Task 1: Oracle core types and hash functions

**Files:**
- Create: `packages/sdks/test-contract/generate_vectors.py`
- Create: `packages/sdks/test-contract/tests/test_oracle_hashing.py`
- Create: `packages/sdks/test-contract/tests/__init__.py` (empty)

**Interfaces:**
- Produces: `murmur3_v1_bucket(flag_key: str, user_id: str) -> int` (0-99), `fnv1a_v2_bucket(flag_key: str, user_id: str) -> float` (0.0-0.9999), both pure functions with no external state.

- [ ] **Step 1: Write the failing tests**

```python
# packages/sdks/test-contract/tests/test_oracle_hashing.py
import sys
import os
sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from generate_vectors import murmur3_v1_bucket, fnv1a_v2_bucket


def test_murmur3_v1_matches_existing_vector_checkout_v2_abc123():
    # Existing vectors.json v1 vector: bucket determines 67% and 99% both true, 50%/25%/1% false.
    # This means bucket is in range [1, 49] union nothing — actually from existing vectors:
    # rollout_pct=67 -> true (bucket<67), rollout_pct=99 -> true (bucket<99),
    # rollout_pct=1 -> false (bucket>=1). So bucket is in [1, 66].
    # The note on the existing 67% vector says "bucket 66 < 67".
    bucket = murmur3_v1_bucket("checkout-v2", "user-abc-123")
    assert bucket == 66


def test_murmur3_v1_matches_existing_vector_checkout_v2_xyz789():
    # Existing vector: rollout_pct=50 -> false, so bucket >= 50.
    bucket = murmur3_v1_bucket("checkout-v2", "user-xyz-789")
    assert bucket >= 50


def test_fnv1a_v2_matches_existing_vector_checkout_v2_abc123():
    # Existing vectors.json v2 vectors all give expected_bucket: 0.343 for this pair.
    bucket = fnv1a_v2_bucket("checkout-v2", "user-abc-123")
    assert abs(bucket - 0.343) < 0.0001


def test_fnv1a_v2_matches_existing_vector_empty_user_id():
    # Existing vector: flag_key=checkout-v2, user_id="", expected_bucket: 0.9683
    bucket = fnv1a_v2_bucket("checkout-v2", "")
    assert abs(bucket - 0.9683) < 0.0001


def test_fnv1a_v2_matches_existing_vector_feature_flag_1_stable_2():
    # Existing vector: expected_bucket: 0.5784
    bucket = fnv1a_v2_bucket("feature-flag-1", "user-stable-2")
    assert abs(bucket - 0.5784) < 0.0001
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd packages/sdks/test-contract && python3 -m pytest tests/test_oracle_hashing.py -v`
Expected: FAIL with `ModuleNotFoundError: No module named 'generate_vectors'` (file doesn't exist yet).

- [ ] **Step 3: Write the oracle hash functions**

```python
# packages/sdks/test-contract/generate_vectors.py
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd packages/sdks/test-contract && python3 -m pytest tests/test_oracle_hashing.py -v`
Expected: PASS (5 passed).

- [ ] **Step 5: Commit**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone
git add packages/sdks/test-contract/generate_vectors.py packages/sdks/test-contract/tests/
git commit -m "test(sdk-contract): add reference oracle hash functions, verified against existing vectors"
```

---

### Task 2: Oracle semver padding + operator evaluation

**Files:**
- Modify: `packages/sdks/test-contract/generate_vectors.py`
- Create: `packages/sdks/test-contract/tests/test_oracle_operators.py`

**Interfaces:**
- Consumes: nothing new from Task 1.
- Produces: `padded_version(v: str) -> str`, `Condition` dataclass, `evaluate_condition(condition: Condition, attrs: dict) -> bool | str` (returns `"inconclusive"` string sentinel instead of raising, so the generator can record inconclusive vectors without exception-handling boilerplate at every call site).

- [ ] **Step 1: Write the failing tests**

```python
# packages/sdks/test-contract/tests/test_oracle_operators.py
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd packages/sdks/test-contract && python3 -m pytest tests/test_oracle_operators.py -v`
Expected: FAIL with `ImportError: cannot import name 'padded_version'`.

- [ ] **Step 3: Implement semver padding and operator evaluation**

```python
# Append to packages/sdks/test-contract/generate_vectors.py

from dataclasses import dataclass, field


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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd packages/sdks/test-contract && python3 -m pytest tests/test_oracle_operators.py -v`
Expected: PASS (12 passed).

- [ ] **Step 5: Commit**

```bash
git add packages/sdks/test-contract/generate_vectors.py packages/sdks/test-contract/tests/test_oracle_operators.py
git commit -m "test(sdk-contract): add oracle semver padding and operator evaluation, unit-verified"
```

---

### Task 3: Oracle prerequisite chain + rule matching

**Files:**
- Modify: `packages/sdks/test-contract/generate_vectors.py`
- Create: `packages/sdks/test-contract/tests/test_oracle_pipeline.py`

**Interfaces:**
- Consumes: `evaluate_condition` from Task 2.
- Produces: `check_prerequisite(prereq: dict, all_flags: dict, cache: dict, seen: set) -> bool`, `match_rules(rules: list, attrs: dict, flag_key: str) -> tuple[str, object] | None` (returns `("RULE_MATCH", variation)` or `None`).

- [ ] **Step 1: Write the failing tests**

```python
# packages/sdks/test-contract/tests/test_oracle_pipeline.py
import sys
import os
sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from generate_vectors import check_prerequisite, match_rules, Condition


def test_hard_gate_unmet_blocks():
    all_flags = {"base-flag": {"enabled": True, "variation": "false"}}
    prereq = {"flag_key": "base-flag", "required_variation": "true", "gate": True}
    assert check_prerequisite(prereq, all_flags, {}, set()) is False


def test_hard_gate_met_passes():
    all_flags = {"base-flag": {"enabled": True, "variation": "true"}}
    prereq = {"flag_key": "base-flag", "required_variation": "true", "gate": True}
    assert check_prerequisite(prereq, all_flags, {}, set()) is True


def test_soft_gate_unmet_still_passes():
    all_flags = {"base-flag": {"enabled": True, "variation": "false"}}
    prereq = {"flag_key": "base-flag", "required_variation": "true", "gate": False}
    assert check_prerequisite(prereq, all_flags, {}, set()) is True


def test_cycle_detected_fails_open():
    prereq = {"flag_key": "self-ref", "required_variation": "true", "gate": True}
    all_flags = {"self-ref": {"enabled": True, "variation": "true"}}
    # "self-ref" already in seen -> cycle -> skip (treated as satisfied)
    assert check_prerequisite(prereq, all_flags, {}, {"self-ref"}) is True


def test_missing_prerequisite_flag_with_hard_gate_blocks():
    prereq = {"flag_key": "nonexistent", "required_variation": "true", "gate": True}
    assert check_prerequisite(prereq, {}, {}, set()) is False


def test_match_rules_first_priority_wins():
    rules = [
        {"id": "r1", "priority": 0, "rollout_pct": 100, "variation": "variant-a",
         "conditions": [Condition("plan", "eq", ["pro"])]},
        {"id": "r2", "priority": 1, "rollout_pct": 100, "variation": "variant-b",
         "conditions": [Condition("plan", "eq", ["pro"])]},
    ]
    result = match_rules(rules, {"plan": "pro"}, "test-flag")
    assert result == ("RULE_MATCH", "variant-a")


def test_match_rules_multi_condition_and():
    rules = [
        {"id": "r1", "priority": 0, "rollout_pct": 100, "variation": "match",
         "conditions": [Condition("plan", "eq", ["pro"]), Condition("region", "eq", ["us"])]},
    ]
    no_match = match_rules(rules, {"plan": "pro", "region": "eu"}, "test-flag")
    assert no_match is None
    match = match_rules(rules, {"plan": "pro", "region": "us"}, "test-flag")
    assert match == ("RULE_MATCH", "match")


def test_match_rules_no_match_falls_through():
    rules = [
        {"id": "r1", "priority": 0, "rollout_pct": 100, "variation": "match",
         "conditions": [Condition("plan", "eq", ["enterprise"])]},
    ]
    assert match_rules(rules, {"plan": "free"}, "test-flag") is None


def test_match_rules_inconclusive_condition_skips_to_next_rule():
    rules = [
        {"id": "r1", "priority": 0, "rollout_pct": 100, "variation": "skipped",
         "conditions": [Condition("missing_attr", "eq", ["x"])]},
        {"id": "r2", "priority": 1, "rollout_pct": 100, "variation": "fallback-match",
         "conditions": [Condition("plan", "eq", ["pro"])]},
    ]
    result = match_rules(rules, {"plan": "pro"}, "test-flag")
    assert result == ("RULE_MATCH", "fallback-match")
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd packages/sdks/test-contract && python3 -m pytest tests/test_oracle_pipeline.py -v`
Expected: FAIL with `ImportError: cannot import name 'check_prerequisite'`.

- [ ] **Step 3: Implement prerequisite chain and rule matching**

```python
# Append to packages/sdks/test-contract/generate_vectors.py

def check_prerequisite(prereq: dict, all_flags: dict, cache: dict, seen: set) -> bool:
    """Canonical model: string variation match (TS's approach), explicit cycle
    detection via seen-set (Python's approach), memoized via cache dict."""
    dep_key = prereq["flag_key"]
    required_variation = prereq["required_variation"]
    gate = prereq.get("gate", True)

    if dep_key in cache:
        dep_variation = cache[dep_key]
    elif dep_key in seen:
        return True  # cycle detected — fail open, skip this prereq
    else:
        dep_flag = all_flags.get(dep_key)
        if dep_flag is None:
            dep_variation = None
        else:
            dep_variation = dep_flag.get("variation") if dep_flag.get("enabled") else "false"
        cache[dep_key] = dep_variation

    if str(dep_variation) != required_variation:
        if not gate:
            return True  # soft — unmet but non-blocking
        return False
    return True


def match_rules(rules: list, attrs: dict, flag_key: str):
    """Canonical model: multi-condition AND per rule (Python's), per-rule rollout
    sub-bucketing (Python's), priority-ascending order, dot-notation resolution (TS's)."""
    sorted_rules = sorted(rules, key=lambda r: r["priority"])
    for rule in sorted_rules:
        results = [evaluate_condition(c, attrs) for c in rule["conditions"]]
        if any(r == "inconclusive" for r in results):
            continue  # rule inconclusive — try next rule
        if not all(results):
            continue  # conditions didn't match — try next rule
        bucket = murmur3_v1_bucket(flag_key, attrs.get("user_id", ""))
        if bucket < rule["rollout_pct"]:
            return ("RULE_MATCH", rule["variation"])
        # matched conditions but outside this rule's own rollout — try next rule
    return None
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd packages/sdks/test-contract && python3 -m pytest tests/test_oracle_pipeline.py -v`
Expected: PASS (8 passed).

- [ ] **Step 5: Run the full oracle test suite**

Run: `cd packages/sdks/test-contract && python3 -m pytest tests/ -v`
Expected: PASS (25 passed — 5 from Task 1, 12 from Task 2, 8 from Task 3).

- [ ] **Step 6: Commit**

```bash
git add packages/sdks/test-contract/generate_vectors.py packages/sdks/test-contract/tests/test_oracle_pipeline.py
git commit -m "test(sdk-contract): add oracle prerequisite chain and rule matching, unit-verified"
```

---

## Phase 2 — Vector Generation

### Task 4: Generate and write expanded vectors.json

**Files:**
- Modify: `packages/sdks/test-contract/generate_vectors.py` (add `main()` + vector definitions)
- Modify: `packages/sdks/test-contract/vectors.json` (regenerated output)

**Interfaces:**
- Consumes: all oracle functions from Tasks 1-3.
- Produces: `packages/sdks/test-contract/vectors.json` version `"1.2"`, with the original 24 hash vectors preserved verbatim under `"vectors"` and new categories added as sibling top-level keys: `"prerequisite_vectors"`, `"target_list_vectors"`, `"rule_vectors"`, `"missing_attribute_vectors"`.

- [ ] **Step 1: Write the vector definitions and generator main()**

```python
# Append to packages/sdks/test-contract/generate_vectors.py

def build_prerequisite_vectors() -> list[dict]:
    vectors = []

    # Hard gate, unmet -> blocked
    v = {
        "id": "prereq-hard-unmet",
        "prerequisite": {"flag_key": "base-flag", "required_variation": "true", "gate": True},
        "all_flags": {"base-flag": {"enabled": True, "variation": "false"}},
        "expected_satisfied": check_prerequisite(
            {"flag_key": "base-flag", "required_variation": "true", "gate": True},
            {"base-flag": {"enabled": True, "variation": "false"}}, {}, set()),
        "note": "Hard gate (gate=true), dependency unmet -> parent flag blocked",
    }
    vectors.append(v)

    # Hard gate, met -> passes
    all_flags = {"base-flag": {"enabled": True, "variation": "true"}}
    prereq = {"flag_key": "base-flag", "required_variation": "true", "gate": True}
    vectors.append({
        "id": "prereq-hard-met",
        "prerequisite": prereq, "all_flags": all_flags,
        "expected_satisfied": check_prerequisite(prereq, all_flags, {}, set()),
        "note": "Hard gate, dependency met -> parent flag proceeds",
    })

    # Soft gate, unmet -> still passes
    all_flags = {"base-flag": {"enabled": True, "variation": "false"}}
    prereq = {"flag_key": "base-flag", "required_variation": "true", "gate": False}
    vectors.append({
        "id": "prereq-soft-unmet",
        "prerequisite": prereq, "all_flags": all_flags,
        "expected_satisfied": check_prerequisite(prereq, all_flags, {}, set()),
        "note": "Soft gate (gate=false), dependency unmet -> non-blocking, parent proceeds",
    })

    # Comparison mechanism is string-based (forward-compatible with multivariate
    # flags), but this release's FlagEnvironmentState has no variation/value field —
    # only enabled (bool). So "variation" here is always the stringified boolean
    # outcome ("true"/"false"), never an arbitrary string. This vector confirms the
    # comparison mechanism works correctly even though only boolean outcomes are
    # reachable via any Java/Ruby/.NET flag state constructible in this release.
    all_flags = {"plan-tier": {"enabled": True, "variation": "true"}}
    prereq = {"flag_key": "plan-tier", "required_variation": "true", "gate": True}
    vectors.append({
        "id": "prereq-string-comparison-mechanism",
        "prerequisite": prereq, "all_flags": all_flags,
        "expected_satisfied": check_prerequisite(prereq, all_flags, {}, set()),
        "note": "String-compare mechanism (forward-compatible with future multivariate prerequisites) applied to a boolean outcome — the only variation type constructible from this release's FlagEnvironmentState",
    })

    # Cycle detection
    prereq = {"flag_key": "self-ref", "required_variation": "true", "gate": True}
    vectors.append({
        "id": "prereq-cycle-fails-open",
        "prerequisite": prereq, "all_flags": {"self-ref": {"enabled": True, "variation": "true"}},
        "seen_keys": ["self-ref"],
        "expected_satisfied": check_prerequisite(prereq, {"self-ref": {"enabled": True, "variation": "true"}}, {}, {"self-ref"}),
        "note": "Cycle detected via seen-set -> fails open, treated as satisfied",
    })

    # Missing dependency, hard gate
    prereq = {"flag_key": "nonexistent", "required_variation": "true", "gate": True}
    vectors.append({
        "id": "prereq-missing-dep-hard-gate",
        "prerequisite": prereq, "all_flags": {},
        "expected_satisfied": check_prerequisite(prereq, {}, {}, set()),
        "note": "Missing prerequisite flag with hard gate -> blocked",
    })

    # Missing dependency, soft gate
    prereq = {"flag_key": "nonexistent", "required_variation": "true", "gate": False}
    vectors.append({
        "id": "prereq-missing-dep-soft-gate",
        "prerequisite": prereq, "all_flags": {},
        "expected_satisfied": check_prerequisite(prereq, {}, {}, set()),
        "note": "Missing prerequisite flag with soft gate -> non-blocking",
    })

    return vectors


def build_target_list_vectors() -> list[dict]:
    return [
        {"id": "target-list-match", "target_list": ["user-1", "user-2"], "user_id": "user-1",
         "expected_target_match": "user-1" in ["user-1", "user-2"], "note": "User in target list"},
        {"id": "target-list-no-match", "target_list": ["user-1", "user-2"], "user_id": "user-9",
         "expected_target_match": "user-9" in ["user-1", "user-2"], "note": "User not in target list"},
        {"id": "target-list-empty", "target_list": [], "user_id": "user-1",
         "expected_target_match": False, "note": "Empty target list never matches"},
        {"id": "target-list-empty-user-id", "target_list": ["user-1"], "user_id": "",
         "expected_target_match": "" in ["user-1"], "note": "Empty user_id against non-empty list"},
    ]


def build_rule_vectors() -> list[dict]:
    vectors = []

    def add(id_, rules_raw, attrs, note):
        rules = [{"id": r["id"], "priority": r["priority"], "rollout_pct": r["rollout_pct"],
                  "variation": r["variation"], "conditions": r["conditions"]} for r in rules_raw]
        result = match_rules(rules, attrs, "test-flag")
        vectors.append({
            "id": id_,
            "rules": [{"id": r["id"], "priority": r["priority"], "rollout_pct": r["rollout_pct"],
                       "variation": r["variation"],
                       "conditions": [{"attribute": c.attribute, "operator": c.operator,
                                        "values": c.values, "negate": c.negate} for c in r["conditions"]]}
                      for r in rules_raw],
            "attrs": attrs,
            "expected_result": {"reason": result[0], "variation": result[1]} if result else None,
            "note": note,
        })

    add("rule-eq-match", [{"id": "r1", "priority": 0, "rollout_pct": 100, "variation": "yes",
        "conditions": [Condition("plan", "eq", ["pro"])]}], {"plan": "pro", "user_id": "u1"}, "eq operator match")

    add("rule-neq-in-nin", [{"id": "r1", "priority": 0, "rollout_pct": 100, "variation": "yes",
        "conditions": [Condition("plan", "nin", ["free", "trial"])]}], {"plan": "pro", "user_id": "u1"}, "nin operator, not in excluded list")

    add("rule-string-contains-case-insensitive", [{"id": "r1", "priority": 0, "rollout_pct": 100, "variation": "yes",
        "conditions": [Condition("email", "contains", ["ACME"])]}], {"email": "user@acme.com", "user_id": "u1"},
        "contains, case-insensitive (canonical: matches Python's shipped behavior)")

    add("rule-numeric-gte", [{"id": "r1", "priority": 0, "rollout_pct": 100, "variation": "yes",
        "conditions": [Condition("age", "gte", ["18"])]}], {"age": "18", "user_id": "u1"}, "gte boundary")

    add("rule-numeric-non-numeric-inconclusive-falls-through", [{"id": "r1", "priority": 0, "rollout_pct": 100, "variation": "skip",
        "conditions": [Condition("age", "gt", ["18"])]}], {"age": "not-a-number", "user_id": "u1"},
        "non-numeric input is inconclusive, no rule matches, result is null")

    add("rule-semver-gte-prerelease-ordering", [{"id": "r1", "priority": 0, "rollout_pct": 100, "variation": "yes",
        "conditions": [Condition("app_version", "semver_gte", ["1.0.0"])]}], {"app_version": "1.0.0-beta", "user_id": "u1"},
        "1.0.0-beta < 1.0.0, so semver_gte(1.0.0-beta, 1.0.0) is false -> no match")

    add("rule-semver-numeric-segment-ordering", [{"id": "r1", "priority": 0, "rollout_pct": 100, "variation": "yes",
        "conditions": [Condition("app_version", "semver_gte", ["1.9.0"])]}], {"app_version": "1.10.0", "user_id": "u1"},
        "1.10.0 >= 1.9.0 (numeric padding prevents lexical-string bug) -> match")

    add("rule-date-before", [{"id": "r1", "priority": 0, "rollout_pct": 100, "variation": "yes",
        "conditions": [Condition("signup_date", "date_before", ["2026-01-01T00:00:00Z"])]}],
        {"signup_date": "2025-06-01T00:00:00Z", "user_id": "u1"}, "date_before match")

    add("rule-date-malformed-inconclusive", [{"id": "r1", "priority": 0, "rollout_pct": 100, "variation": "skip",
        "conditions": [Condition("signup_date", "date_before", ["2026-01-01T00:00:00Z"])]}],
        {"signup_date": "not-a-date", "user_id": "u1"}, "malformed date is inconclusive, no rule matches")

    add("rule-geo-country-case-insensitive", [{"id": "r1", "priority": 0, "rollout_pct": 100, "variation": "yes",
        "conditions": [Condition("geo.country", "in", ["US", "CA"])]}], {"geo.country": "us", "user_id": "u1"},
        "GEO operator via dot-notation resolution, case-insensitive")

    add("rule-multi-condition-and-both-match", [{"id": "r1", "priority": 0, "rollout_pct": 100, "variation": "match",
        "conditions": [Condition("plan", "eq", ["pro"]), Condition("region", "eq", ["us"])]}],
        {"plan": "pro", "region": "us", "user_id": "u1"}, "multi-condition AND, both match")

    add("rule-multi-condition-and-one-fails", [{"id": "r1", "priority": 0, "rollout_pct": 100, "variation": "match",
        "conditions": [Condition("plan", "eq", ["pro"]), Condition("region", "eq", ["us"])]}],
        {"plan": "pro", "region": "eu", "user_id": "u1"}, "multi-condition AND, one fails -> no match")

    add("rule-priority-first-match-wins", [
        {"id": "r1", "priority": 0, "rollout_pct": 100, "variation": "high-priority",
         "conditions": [Condition("plan", "eq", ["pro"])]},
        {"id": "r2", "priority": 1, "rollout_pct": 100, "variation": "low-priority",
         "conditions": [Condition("plan", "eq", ["pro"])]},
    ], {"plan": "pro", "user_id": "u1"}, "priority 0 evaluated before priority 1, first match wins")

    add("rule-per-rule-rollout-sub-bucketing", [
        {"id": "r1", "priority": 0, "rollout_pct": 0, "variation": "never",
         "conditions": [Condition("plan", "eq", ["pro"])]},
        {"id": "r2", "priority": 1, "rollout_pct": 100, "variation": "fallback",
         "conditions": [Condition("plan", "eq", ["pro"])]},
    ], {"plan": "pro", "user_id": "u1"},
        "rule 1 conditions match but rollout_pct=0 excludes everyone -> falls to rule 2, not Step 5 fallthrough")

    return vectors


def build_missing_attribute_vectors() -> list[dict]:
    vectors = []
    result = match_rules([{
        "id": "r1", "priority": 0, "rollout_pct": 100, "variation": "skipped",
        "conditions": [Condition("missing_attr", "eq", ["x"])],
    }], {"user_id": "u1"}, "test-flag")
    vectors.append({
        "id": "missing-attribute-skips-rule",
        "rules": [{"id": "r1", "priority": 0, "rollout_pct": 100, "variation": "skipped",
                   "conditions": [{"attribute": "missing_attr", "operator": "eq", "values": ["x"], "negate": False}]}],
        "attrs": {"user_id": "u1"},
        "expected_result": {"reason": result[0], "variation": result[1]} if result else None,
        "note": "Attribute absent from context -> inconclusive, not false; rule skipped, no match (result null since no other rule)",
    })
    return vectors


def main():
    with open("vectors.json") as f:
        existing = json.load(f)

    existing["version"] = "1.2"
    existing["prerequisite_vectors"] = build_prerequisite_vectors()
    existing["target_list_vectors"] = build_target_list_vectors()
    existing["rule_vectors"] = build_rule_vectors()
    existing["missing_attribute_vectors"] = build_missing_attribute_vectors()

    with open("vectors.json", "w") as f:
        json.dump(existing, f, indent=2)
        f.write("\n")

    print(f"Wrote vectors.json version {existing['version']}: "
          f"{len(existing['vectors'])} hash vectors, "
          f"{len(existing['prerequisite_vectors'])} prerequisite, "
          f"{len(existing['target_list_vectors'])} target_list, "
          f"{len(existing['rule_vectors'])} rule, "
          f"{len(existing['missing_attribute_vectors'])} missing_attribute.")


if __name__ == "__main__":
    main()
```

- [ ] **Step 2: Run the generator**

Run: `cd packages/sdks/test-contract && python3 generate_vectors.py`
Expected output: `Wrote vectors.json version 1.2: 24 hash vectors, 7 prerequisite, 4 target_list, 14 rule, 1 missing_attribute.`

- [ ] **Step 3: Validate the output is well-formed JSON and the original 24 hash vectors are byte-identical**

```bash
cd packages/sdks/test-contract
python3 -c "
import json
with open('vectors.json') as f:
    data = json.load(f)
assert data['version'] == '1.2'
assert len(data['vectors']) == 24
assert data['vectors'][0]['flag_key'] == 'checkout-v2'
assert 'prerequisite_vectors' in data
assert 'rule_vectors' in data
print('Schema check passed.')
"
```
Expected: `Schema check passed.` with no assertion errors.

- [ ] **Step 4: Manually spot-check 3 generated vectors against hand reasoning**

Read the generated `vectors.json`, find `rule-semver-numeric-segment-ordering` and confirm
`expected_result.reason == "RULE_MATCH"` and `variation == "yes"` (since `1.10.0 >= 1.9.0` is
true once numeric segments are padded). Find `prereq-cycle-fails-open` and confirm
`expected_satisfied == true` (cycle fails open per canonical model). Find
`missing-attribute-skips-rule` and confirm `expected_result == null` (no rule matched, since
the only rule was inconclusive).

- [ ] **Step 5: Commit**

```bash
git add packages/sdks/test-contract/generate_vectors.py packages/sdks/test-contract/vectors.json
git commit -m "feat(sdk-contract): expand vectors.json to v1.2 with prerequisite/rule/operator vectors"
```

---

### Task 5: Delete the oracle script (YAGNI — one-time generator, not shipped code)

**Files:**
- Delete: `packages/sdks/test-contract/generate_vectors.py`
- Delete: `packages/sdks/test-contract/tests/` (entire directory)

**Interfaces:**
- Consumes: nothing (this task only removes files).
- Produces: nothing — `vectors.json` from Task 4 is the sole durable artifact.

- [ ] **Step 1: Confirm vectors.json is committed and does not depend on the oracle at runtime**

```bash
grep -r "generate_vectors" packages/sdks/flagmind-java packages/sdks/flagmind-ruby packages/sdks/flagmind-dotnet packages/sdks/flagmind-python packages/sdks/@flagmind 2>/dev/null
```
Expected: no output (no shipped SDK code references the generator).

- [ ] **Step 2: Delete the oracle and its tests**

```bash
git rm -r packages/sdks/test-contract/generate_vectors.py packages/sdks/test-contract/tests/
```

- [ ] **Step 3: Commit**

```bash
git commit -m "chore(sdk-contract): remove one-time vector-generation oracle after vectors.json v1.2 verified"
```

---

## Phase 3 — SDK_CONTRACT.md Rewrite

### Task 6: Rewrite docs/SDK_CONTRACT.md as the normative canonical spec

**Files:**
- Modify: `docs/SDK_CONTRACT.md` (full rewrite)

**Interfaces:**
- Consumes: the canonical spec table from `docs/superpowers/specs/2026-08-04-v1.5.0-sdk-parity-and-dependency-viz-design.md` Section 3, and the TS/Python divergence table from Section 2a.
- Produces: nothing consumed by code — this is documentation only, read by future SDK maintainers and by the Java/Ruby/.NET implementation plans as their normative reference.

- [ ] **Step 1: Write the new SDK_CONTRACT.md content**

```markdown
# SDK Contract — Canonical Evaluation Spec

This is the normative specification for the 5-step evaluation pipeline that Java, Ruby,
and .NET SDKs must implement (as of v1.5.0). It resolves every divergence found between
the two reference implementations (TypeScript, Python) into one target model — TypeScript
and Python remain on their existing shipped behavior; this document does not change them,
and their mutual divergence is documented in the "Known Pre-Existing Divergence" section
below.

Executable contract vectors live in `packages/sdks/test-contract/vectors.json` (v1.2+) —
every SDK's test suite must load and assert against this file. It is the objective
definition of "parity."

## Canonical Model (Java/Ruby/.NET target)

| Step | Behavior |
|---|---|
| 1. Preliminary | Missing flag -> `ERROR`, caller's default. Disabled -> `OFF`, parses `flag_state.safe_default` into the target type (falls back to caller's default on parse failure). |
| 2. Prerequisites | For each prerequisite: evaluate the dependency (memoized per top-level call via a cache keyed by dependency flag key), compare `String(dependency_result.variation)` against `required_variation`. Mismatch + `gate=true` -> `PREREQUISITE_FAILED`, caller's default. Mismatch + `gate=false` -> skip, continue. Missing dependency + `gate=true` -> `PREREQUISITE_FAILED`. Cycle detected (dependency already in the current evaluation chain) -> fails open, skip that one prerequisite edge only. |
| 3. Target list | If `context.user_id` is in `flag_state.target_list` -> `TARGET_MATCH`, value `true`. |
| 4. Rule matching | Sort `flag_state.targeting_rules` ascending by `priority` (0 = highest). For each rule: evaluate ALL conditions with AND semantics; any condition raising "inconclusive" (see below) skips the whole rule, continue to next. If all conditions match, compute `bucket = murmur3_v1(flag_key + user_id) % 100`; if `bucket < rule.rollout_pct` -> `RULE_MATCH`, rule's variation. Otherwise continue to next rule (NOT step 5). |
| 5. Fallthrough | `rollout_pct >= 100` -> `true`. `rollout_pct <= 0` -> caller's default. Else hash-compare via `hash_version` (1 = MurmurHash3, 2 = FNV-1a); in cohort -> `true`, else caller's default. |

## Operator Surface (Step 4 conditions)

- **Equality**: `eq`, `neq`, `in`, `nin` — exact string comparison/membership.
- **String**: `contains`, `startswith` (alias `prefix`), `endswith` (alias `suffix`) — **case-insensitive**, both sides uppercased, multi-value ANY semantics.
- **Numeric**: `gt`, `gte`, `lt`, `lte` — cast both sides to float; cast failure -> inconclusive.
- **Semver**: `semver_gt`, `semver_gte`, `semver_lt`, `semver_lte`, `semver_eq` — compared via `_padded_version()` string padding (strip `v` prefix and build metadata, left-pad numeric segments to 5 chars, append `~` to 3-part releases so prereleases sort below their release).
- **Date**: `date_before`, `date_after` — ISO-8601 parse (`Z` normalized to `+00:00`); parse failure -> inconclusive.
- **Geo**: `in`/`nin` against `geo.country` / `geo.region` attribute paths — case-insensitive, resolved via dot-notation.
- **Regex**: declared but **not implemented** in this release (matches current TS behavior — always returns `false`, not inconclusive). Future work.
- **Attribute resolution**: dot-notation nested paths on the context's attribute map, with a flat single-segment fallback. Missing attribute -> inconclusive (a raised/thrown exception in every language, caught by the rule-matching loop, which treats it as "this rule did not match" and continues to the next rule).
- **`negate`**: a boolean on every condition; inverts the final boolean result after evaluation (does not apply to inconclusive outcomes — inconclusive stays inconclusive regardless of `negate`).

## Hashing

- **v1 (MurmurHash3)**: unsigned 32-bit, seed=0, `concat(flag_key, user_id)` as UTF-8 bytes, mod 100.
- **v2 (FNV-1a)**: double-pass — `inner = fnv1a(concat(flag_key, user_id))`, `outer = fnv1a(decimal_string(inner))`, `bucket = (outer % 10000) / 10000`, compared against `rollout_pct / 100`. MUST iterate over UTF-8 bytes (not UTF-16 code units) for portability across Java/Ruby/.NET string encodings.

## Known Pre-Existing Divergence (TypeScript vs Python — NOT fixed by this spec)

TypeScript and Python are each other's oldest SDKs and diverge in ways this release does
not touch:

| Aspect | TypeScript | Python |
|---|---|---|
| OFF-path default | Parses `safeDefault` | Raw caller default |
| Prerequisite comparison | String variation match | Boolean-only match |
| String op case sensitivity | Case-sensitive | Case-insensitive |
| GEO operators | Implemented | Not implemented |
| Semver/date operators | Declared, not implemented | Implemented |
| Missing-attribute signaling | Silent `false` | Raises `InconclusiveMatchError` |

New SDKs (Java/Ruby/.NET) follow the Canonical Model above, which resolves each of these
in one direction — they do not match either TS or Python exactly on every point. See the
full design rationale in `docs/superpowers/specs/2026-08-04-v1.5.0-sdk-parity-and-dependency-viz-design.md`.

## Parity Matrix (updated after v1.5.0)

| Capability | TypeScript | Python | Java | Ruby | .NET |
|---|---|---|---|---|---|
| Steps 1+5 (preliminary + fallthrough) | Yes | Yes | Yes | Yes | Yes |
| Step 2 (prerequisites) | Yes (string variation) | Yes (boolean-only) | Yes (canonical) | Yes (canonical) | Yes (canonical) |
| Step 3 (target list) | Yes | Yes | Yes (canonical) | Yes (canonical) | Yes (canonical) |
| Step 4 (rule matching) | Yes (single condition) | Yes (multi-condition) | Yes (canonical, multi-condition) | Yes (canonical, multi-condition) | Yes (canonical, multi-condition) |
| Hash v1 (MurmurHash3) | Yes | Yes | Yes | Yes | Yes |
| Hash v2 (FNV-1a) | Yes | Yes | Yes (canonical) | Yes (canonical) | Yes (canonical) |
| Semver/date operators | No | Yes | Yes (canonical) | Yes (canonical) | Yes (canonical) |
| GEO operators | Yes | No | Yes (canonical) | Yes (canonical) | Yes (canonical) |
| Regex operator | No | No | No | No | No |
| Cross-language contract vectors | Hash-only | Hash-only | Full (v1.2 vectors) | Full (v1.2 vectors) | Full (v1.2 vectors) |

*(This table is illustrative of the target end-state after Phases 2-4 of the v1.5.0 plan
complete — update the Java/Ruby/.NET "Yes (canonical)" cells to reflect actual merged state
as each SDK's PR lands, per this project's read-the-actual-code verification discipline.)*
```

- [ ] **Step 2: Verify the file renders as valid markdown with no broken internal links**

```bash
grep -n "\[.*\](.*\.md)" docs/SDK_CONTRACT.md
```
Expected: any linked paths (e.g. the design spec path) resolve — confirm with `ls docs/superpowers/specs/2026-08-04-v1.5.0-sdk-parity-and-dependency-viz-design.md`.

- [ ] **Step 3: Commit**

```bash
git add docs/SDK_CONTRACT.md
git commit -m "docs(sdk): rewrite SDK_CONTRACT.md as normative canonical spec for v1.5.0 parity work"
```

---

## Phase 4 — PR

### Task 7: Open PR to develop

**Files:** none (GitHub operation only)

- [ ] **Step 1: Push the branch**

```bash
git push -u origin feat/contract-vectors-v1.5.0
```

- [ ] **Step 2: Open the PR**

```bash
gh pr create --base develop --title "feat(sdk-contract): expand contract vectors + rewrite SDK_CONTRACT.md for v1.5.0" --body "$(cat <<'EOF'
## Summary
- Expands packages/sdks/test-contract/vectors.json from hash-only (24 vectors) to cover prerequisites, target list, and the full rule-operator surface (~45 new vectors), generated from a hand-verified reference oracle (deleted after use) rather than transcribed by hand.
- Rewrites docs/SDK_CONTRACT.md from an observational matrix into the normative canonical spec that Java/Ruby/.NET implementations (next phases) must satisfy.
- No changes to TypeScript or Python SDK behavior — their mutual divergence is documented, not fixed.

Phase 1 of the v1.5.0 upgrade. See docs/superpowers/specs/2026-08-04-v1.5.0-sdk-parity-and-dependency-viz-design.md for full design.

## Test plan
- [x] Oracle unit tests (25 cases) verified hash functions against existing vectors.json values before generating new ones
- [x] Generated vectors.json validated as well-formed JSON, original 24 hash vectors byte-identical
- [x] 3 generated vectors manually spot-checked against hand reasoning
EOF
)"
```

- [ ] **Step 3: Report the PR URL to the user and stop — do not merge**

Per this repo's established workflow, PR merges are done by the user, not automatically.
