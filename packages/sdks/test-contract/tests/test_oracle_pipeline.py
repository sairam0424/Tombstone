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
