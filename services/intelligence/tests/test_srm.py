"""
Tests for app.experiments.srm.srm_check — the EXP-2 Sample Ratio Mismatch
(SRM) chi-square gate. See srm.py's module docstring for why SRM must be
checked before trusting any statistical result computed from a split.
"""

from __future__ import annotations

import pytest
from scipy import stats  # type: ignore[import]

from app.experiments.srm import SRM_ALPHA, srm_check


class TestSRMCheck:
    def test_a_clean_even_split_is_not_a_mismatch(self):
        result = srm_check(n_control=5000, n_treatment=5000, expected_ratio=0.5)

        assert result.is_mismatch is False
        assert result.p_value > SRM_ALPHA

    def test_a_severely_lopsided_split_is_flagged_as_a_mismatch(self):
        """
        6000/4000 against an expected 50/50 split is a huge, unambiguous
        divergence -- real production SRM incidents are typically only a
        few percentage points off, so this fixture is deliberately extreme
        to make the test's own correctness obvious without needing to
        pin an exact p-value.
        """
        result = srm_check(n_control=6000, n_treatment=4000, expected_ratio=0.5)

        assert result.is_mismatch is True
        assert result.p_value < SRM_ALPHA

    def test_matches_scipy_chisquare_directly(self):
        """
        Ground-truth check: srm_check's chi2_stat/p_value must be exactly
        what scipy.stats.chisquare itself computes for the same inputs --
        proves this is a thin, faithful wrapper, not a reimplementation
        that could silently drift from the real chi-square test.
        """
        n_control, n_treatment = 5300, 4700
        total = n_control + n_treatment
        expected_chi2, expected_p = stats.chisquare(
            f_obs=[n_control, n_treatment],
            f_exp=[total * 0.5, total * 0.5],
        )

        result = srm_check(n_control, n_treatment, expected_ratio=0.5)

        assert result.chi2_stat == pytest.approx(float(expected_chi2))
        assert result.p_value == pytest.approx(float(expected_p))

    def test_respects_a_non_even_expected_ratio(self):
        """
        A 90/10 control-heavy rollout is intentional, not a mismatch --
        srm_check must compare against the CONFIGURED ratio, not assume
        every experiment is a 50/50 split.
        """
        # Exactly 90/10 of a 10,000 total -- matches expected_ratio=0.9 perfectly.
        result = srm_check(n_control=9000, n_treatment=1000, expected_ratio=0.9)

        assert result.is_mismatch is False
        assert result.p_value == pytest.approx(1.0)

    def test_the_same_9000_1000_split_IS_a_mismatch_against_a_50_50_expectation(self):
        """Same raw counts as the test above, but checked against the WRONG
        expected ratio -- proves expected_ratio actually changes the outcome,
        not just accepted-and-ignored."""
        result = srm_check(n_control=9000, n_treatment=1000, expected_ratio=0.5)

        assert result.is_mismatch is True

    def test_both_arms_empty_is_not_computable_and_does_not_block(self):
        result = srm_check(n_control=0, n_treatment=0, expected_ratio=0.5)

        assert result.is_mismatch is False
        assert result.p_value == 1.0
        assert result.chi2_stat == 0.0

    def test_degenerate_expected_ratio_of_zero_is_not_computable(self):
        result = srm_check(n_control=100, n_treatment=100, expected_ratio=0.0)

        assert result.is_mismatch is False

    def test_degenerate_expected_ratio_of_one_is_not_computable(self):
        result = srm_check(n_control=100, n_treatment=100, expected_ratio=1.0)

        assert result.is_mismatch is False

    def test_default_expected_ratio_is_an_even_split(self):
        even = srm_check(n_control=5000, n_treatment=5000)
        assert even.is_mismatch is False

        uneven = srm_check(n_control=6000, n_treatment=4000)
        assert uneven.is_mismatch is True
