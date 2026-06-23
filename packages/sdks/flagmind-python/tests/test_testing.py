import sys
import os

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from tombstone.testing import TombstoneTestClient


# ---------------------------------------------------------------------------
# override()
# ---------------------------------------------------------------------------

def test_override_returns_correct_value():
    client = TombstoneTestClient.create_isolated()
    client.override("checkout_v2", True)
    assert client.evaluate("checkout_v2", False) is True


def test_override_chains_fluently():
    client = (
        TombstoneTestClient.create_isolated()
        .override("flag_a", "enabled")
        .override("flag_b", 42)
    )
    assert client.evaluate("flag_a", "off") == "enabled"
    assert client.evaluate("flag_b", 0) == 42


def test_override_any_type():
    client = TombstoneTestClient.create_isolated()
    client.override("str_flag", "variant-b")
    client.override("num_flag", 99)
    client.override("obj_flag", {"key": "val"})
    assert client.evaluate("str_flag", "default") == "variant-b"
    assert client.evaluate("num_flag", 0) == 99
    assert client.evaluate("obj_flag", {}) == {"key": "val"}


# ---------------------------------------------------------------------------
# clear_overrides()
# ---------------------------------------------------------------------------

def test_clear_overrides_resets_to_default():
    client = (
        TombstoneTestClient.create_isolated()
        .override("checkout_v2", True)
        .override("new_ui", "v2")
    )
    client.clear_overrides()
    assert client.evaluate("checkout_v2", False) is False
    assert client.evaluate("new_ui", "v1") == "v1"


# ---------------------------------------------------------------------------
# clear_override() (single key)
# ---------------------------------------------------------------------------

def test_clear_override_removes_only_targeted_key():
    client = (
        TombstoneTestClient.create_isolated()
        .override("flag_a", True)
        .override("flag_b", True)
    )
    client.clear_override("flag_a")
    assert client.evaluate("flag_a", False) is False, "flag_a override should be cleared"
    assert client.evaluate("flag_b", False) is True, "flag_b override should remain"


# ---------------------------------------------------------------------------
# assign_to_bucket()
# ---------------------------------------------------------------------------

def test_assign_to_bucket_forces_user_into_cohort():
    client = TombstoneTestClient.create_isolated()
    client.assign_to_bucket("experiment_flag", "user-123", True)
    assert client.is_enabled("experiment_flag", user_id="user-123") is True


def test_assign_to_bucket_forces_user_out_of_cohort():
    client = TombstoneTestClient.create_isolated()
    client.assign_to_bucket("experiment_flag", "user-abc", False)
    assert client.is_enabled("experiment_flag", user_id="user-abc") is False


def test_bucket_assignment_does_not_affect_other_users():
    client = TombstoneTestClient.create_isolated()
    client.assign_to_bucket("experiment_flag", "user-123", True)
    # user-xyz has no assignment — defaults to False
    assert client.is_enabled("experiment_flag", user_id="user-xyz") is False


def test_override_takes_precedence_over_bucket_assignment():
    client = TombstoneTestClient.create_isolated()
    client.override("experiment_flag", "control")
    client.assign_to_bucket("experiment_flag", "user-123", True)
    assert client.evaluate("experiment_flag", "default") == "control"


# ---------------------------------------------------------------------------
# create_isolated()
# ---------------------------------------------------------------------------

def test_create_isolated_returns_default_for_all_flags():
    client = TombstoneTestClient.create_isolated()
    assert client.evaluate("any_flag", False) is False
    assert client.evaluate("any_flag", "fallback") == "fallback"
    assert client.evaluate("any_flag", 0) == 0


# ---------------------------------------------------------------------------
# with_flags()
# ---------------------------------------------------------------------------

def test_with_flags_preconfigures_multiple_flags():
    client = TombstoneTestClient.with_flags(
        {
            "checkout_v2": True,
            "new_pricing": "beta",
            "max_retries": 3,
        }
    )
    assert client.evaluate("checkout_v2", False) is True
    assert client.evaluate("new_pricing", "stable") == "beta"
    assert client.evaluate("max_retries", 1) == 3


def test_with_flags_unknown_flag_returns_default():
    client = TombstoneTestClient.with_flags({"known_flag": True})
    assert client.evaluate("unknown_flag", "fallback") == "fallback"


# ---------------------------------------------------------------------------
# is_enabled()
# ---------------------------------------------------------------------------

def test_is_enabled_true_when_override_is_true():
    client = TombstoneTestClient.create_isolated()
    client.override("my_flag", True)
    assert client.is_enabled("my_flag") is True


def test_is_enabled_false_when_override_is_false():
    client = TombstoneTestClient.create_isolated()
    client.override("my_flag", False)
    assert client.is_enabled("my_flag") is False


def test_is_enabled_false_for_unset_flag():
    client = TombstoneTestClient.create_isolated()
    assert client.is_enabled("unset_flag") is False


def test_is_enabled_uses_bucket_assignment_when_user_id_provided():
    client = TombstoneTestClient.create_isolated()
    client.assign_to_bucket("my_flag", "user-1", True)
    assert client.is_enabled("my_flag", user_id="user-1") is True
    assert client.is_enabled("my_flag", user_id="user-2") is False


# ---------------------------------------------------------------------------
# Immutability
# ---------------------------------------------------------------------------

def test_override_is_immutable_does_not_mutate_prior_state():
    client = TombstoneTestClient.create_isolated()
    client.override("flag_a", "first")
    snapshot = client.evaluate("flag_a", "none")
    client.override("flag_a", "second")
    # snapshot captured before second override — still "first"
    assert snapshot == "first"
    assert client.evaluate("flag_a", "none") == "second"


def test_clear_overrides_creates_new_dict():
    client = TombstoneTestClient.create_isolated()
    client.override("flag_a", True)
    value_before = client.evaluate("flag_a", False)
    client.clear_overrides()
    value_after = client.evaluate("flag_a", False)
    assert value_before is True
    assert value_after is False


def test_assign_to_bucket_does_not_mutate_existing_bucket_map():
    client = TombstoneTestClient.create_isolated()
    client.assign_to_bucket("flag_a", "user-1", True)
    # capture whether user-2 was in cohort BEFORE second assignment
    before_second = client.is_enabled("flag_a", user_id="user-2")
    client.assign_to_bucket("flag_a", "user-2", True)
    assert before_second is False, "user-2 should not have been in cohort before assignment"
    assert client.is_enabled("flag_a", user_id="user-2") is True
