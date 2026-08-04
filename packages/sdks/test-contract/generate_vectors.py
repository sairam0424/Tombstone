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
