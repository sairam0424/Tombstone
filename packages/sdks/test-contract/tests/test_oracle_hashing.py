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
