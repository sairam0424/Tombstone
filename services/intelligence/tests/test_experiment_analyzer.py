"""
Tests for ExperimentAnalyzer.analyze_from_stats — the sufficient-statistics
analysis path used by POST /api/v1/experiments/analyze against real
warehouse aggregates, replacing the `[mean] * sample_size` reconstruction
that fabricated statistical significance (EXP-1).
"""

from __future__ import annotations

import numpy as np
import pytest
from scipy import stats  # type: ignore[import]

from app.experiments.analyzer import ExperimentAnalyzer
from app.experiments.models import ExperimentDefinition
from app.warehouse.connector import AggregatedMetric


def _make_experiment(
    stat_method: str, min_sample_size: int = 100
) -> ExperimentDefinition:
    return ExperimentDefinition(
        id="exp-1",
        flag_key="test-flag",
        control_variant="false",
        treatment_variant="true",
        stat_method=stat_method,
        min_sample_size=min_sample_size,
    )


def _aggregate(arr: np.ndarray, variant: str) -> AggregatedMetric:
    """Build an AggregatedMetric the way a real warehouse connector's SQL would — from a true per-user array."""
    return AggregatedMetric(
        variant=variant,
        sample_size=len(arr),
        mean=float(np.mean(arr)),
        std=float(np.std(arr, ddof=1)) if len(arr) > 1 else 0.0,
        variance=float(np.var(arr, ddof=1)) if len(arr) > 1 else 0.0,
        sum=float(np.sum(arr)),
        conversion_count=int(np.sum(arr > 0)),
    )


class TestFrequentist:
    def test_matches_true_ttest_ind_on_the_real_per_user_data(self):
        rng = np.random.default_rng(42)
        control_arr = rng.normal(loc=10.0, scale=2.0, size=500)
        treatment_arr = rng.normal(loc=10.5, scale=2.0, size=500)

        control = _aggregate(control_arr, "control")
        treatment = _aggregate(treatment_arr, "treatment")
        experiment = _make_experiment("frequentist", min_sample_size=10)

        result = ExperimentAnalyzer().analyze_from_stats(
            experiment, control, treatment, "revenue"
        )

        _, expected_p = stats.ttest_ind(treatment_arr, control_arr)
        assert result.p_value == pytest.approx(round(float(expected_p), 4), abs=1e-3)

    def test_does_not_fabricate_significance_from_reconstructed_means(self):
        """
        Regression test for the bug this PR fixes: reconstructing
        `[mean] * n` per variant (the old code) collapses within-variant
        variance to ~0, so a t-test on that array reports p ~ 0.0 for ANY
        nonzero mean difference (empirically confirmed during scoping:
        t=5.69e15, p=0.0 for this exact mean/n pair). Given the TRUE
        variance, this 2pp difference on n=1000 each is not significant.
        """
        control = AggregatedMetric(
            variant="control",
            sample_size=1000,
            mean=0.30,
            std=0.4583,
            variance=0.21,
            sum=300.0,
            conversion_count=300,
        )
        treatment = AggregatedMetric(
            variant="treatment",
            sample_size=1000,
            mean=0.32,
            std=0.4665,
            variance=0.2176,
            sum=320.0,
            conversion_count=320,
        )
        experiment = _make_experiment("frequentist", min_sample_size=10)

        result = ExperimentAnalyzer().analyze_from_stats(
            experiment, control, treatment, "conversion"
        )

        assert result.p_value is not None
        assert result.p_value > 0.05
        assert result.is_significant is False

    def test_requires_at_least_two_observations_per_variant(self):
        control = AggregatedMetric(
            variant="control",
            sample_size=1,
            mean=1.0,
            std=0.0,
            variance=0.0,
            sum=1.0,
            conversion_count=1,
        )
        treatment = AggregatedMetric(
            variant="treatment",
            sample_size=1,
            mean=2.0,
            std=0.0,
            variance=0.0,
            sum=2.0,
            conversion_count=1,
        )
        experiment = _make_experiment("frequentist", min_sample_size=1)

        result = ExperimentAnalyzer().analyze_from_stats(
            experiment, control, treatment, "metric"
        )

        assert result.p_value is None
        assert result.is_significant is False


class TestBayesian:
    def test_uses_the_real_conversion_count_not_the_reconstructed_100_percent(self):
        """
        Regression test for the second bug: the old bayesian branch computed
        conversion_count as `np.sum([mean]*n > 0)`, which is `n` for ANY
        positive mean — corrupting a true 30% conversion rate into a
        reported 100%. analyze_from_stats() must report the real rate.
        """
        control = AggregatedMetric(
            variant="control",
            sample_size=1000,
            mean=0.30,
            std=0.4583,
            variance=0.21,
            sum=300.0,
            conversion_count=300,
        )
        treatment = AggregatedMetric(
            variant="treatment",
            sample_size=1000,
            mean=0.32,
            std=0.4665,
            variance=0.2176,
            sum=320.0,
            conversion_count=320,
        )
        experiment = _make_experiment("bayesian", min_sample_size=10)

        result = ExperimentAnalyzer().analyze_from_stats(
            experiment, control, treatment, "conversion"
        )

        assert result.control.conversion_rate == pytest.approx(0.30)
        assert result.treatment.conversion_rate == pytest.approx(0.32)

    def test_clear_conversion_lift_reports_high_probability_treatment_wins(self):
        control = AggregatedMetric(
            variant="control",
            sample_size=5000,
            mean=0.10,
            std=0.3,
            variance=0.09,
            sum=500.0,
            conversion_count=500,
        )
        treatment = AggregatedMetric(
            variant="treatment",
            sample_size=5000,
            mean=0.20,
            std=0.4,
            variance=0.16,
            sum=1000.0,
            conversion_count=1000,
        )
        experiment = _make_experiment("bayesian", min_sample_size=10)

        result = ExperimentAnalyzer().analyze_from_stats(
            experiment, control, treatment, "conversion"
        )

        assert result.probability_beats_control is not None
        assert result.probability_beats_control > 0.99
        assert result.is_significant is True


class TestMinSampleSizeGuard:
    def test_insufficient_sample_size_forces_not_significant(self):
        control = AggregatedMetric(
            variant="control",
            sample_size=5,
            mean=0.1,
            std=0.3,
            variance=0.09,
            sum=0.5,
            conversion_count=1,
        )
        treatment = AggregatedMetric(
            variant="treatment",
            sample_size=5,
            mean=0.9,
            std=0.3,
            variance=0.09,
            sum=4.5,
            conversion_count=5,
        )
        experiment = _make_experiment("bayesian", min_sample_size=100)

        result = ExperimentAnalyzer().analyze_from_stats(
            experiment, control, treatment, "conversion"
        )

        assert result.is_significant is False


class TestConversionCountClamping:
    def test_conversion_count_exceeding_sample_size_does_not_crash_bayesian(self):
        """
        Regression test for a bug found by adversarial review: an
        AggregatedMetric with conversion_count > sample_size (e.g. from a
        future/alternate connector or data anomaly -- the real connectors'
        SQL always upholds the invariant) used to compute a negative Beta
        shape parameter, and np.random.beta() raised an uncaught ValueError.
        analyze_from_stats() must clamp instead of crashing.
        """
        control = AggregatedMetric(
            variant="control",
            sample_size=100,
            mean=1.5,
            std=0.3,
            variance=0.09,
            sum=150.0,
            conversion_count=150,
        )
        treatment = AggregatedMetric(
            variant="treatment",
            sample_size=100,
            mean=1.0,
            std=0.3,
            variance=0.09,
            sum=100.0,
            conversion_count=100,
        )
        experiment = _make_experiment("bayesian", min_sample_size=10)

        result = ExperimentAnalyzer().analyze_from_stats(
            experiment, control, treatment, "conversion"
        )

        assert result.control.conversion_rate <= 1.0
        assert result.treatment.conversion_rate <= 1.0
        assert result.probability_beats_control is not None

    def test_negative_conversion_count_does_not_crash_bayesian(self):
        control = AggregatedMetric(
            variant="control",
            sample_size=100,
            mean=0.1,
            std=0.3,
            variance=0.09,
            sum=10.0,
            conversion_count=-5,
        )
        treatment = AggregatedMetric(
            variant="treatment",
            sample_size=100,
            mean=0.2,
            std=0.3,
            variance=0.09,
            sum=20.0,
            conversion_count=20,
        )
        experiment = _make_experiment("bayesian", min_sample_size=10)

        result = ExperimentAnalyzer().analyze_from_stats(
            experiment, control, treatment, "conversion"
        )

        assert result.control.conversion_rate >= 0.0
        assert result.probability_beats_control is not None


class TestZeroVarianceSafetyValve:
    def test_both_variants_zero_variance_reports_non_computable_not_p_zero(self):
        """
        Regression test: ttest_ind_from_stats(std1=0, std2=0, ...) returns
        t=inf/p=0.0 with no warning for ANY nonzero mean difference -- e.g. a
        fixed per-plan price that genuinely never varies within a variant.
        That's a real difference, but not one a t-test can validly infer
        significance for; analyze_from_stats() must report it as
        non-computable (p_value=None) instead of a misleadingly exact 0.0.
        """
        control = AggregatedMetric(
            variant="control",
            sample_size=1000,
            mean=10.0,
            std=0.0,
            variance=0.0,
            sum=10000.0,
            conversion_count=1000,
        )
        treatment = AggregatedMetric(
            variant="treatment",
            sample_size=1000,
            mean=12.0,
            std=0.0,
            variance=0.0,
            sum=12000.0,
            conversion_count=1000,
        )
        experiment = _make_experiment("frequentist", min_sample_size=10)

        result = ExperimentAnalyzer().analyze_from_stats(
            experiment, control, treatment, "price"
        )

        assert result.p_value is None
        assert result.is_significant is False

    def test_one_variant_with_real_variance_still_computes_normally(self):
        control = AggregatedMetric(
            variant="control",
            sample_size=500,
            mean=10.0,
            std=2.0,
            variance=4.0,
            sum=5000.0,
            conversion_count=500,
        )
        treatment = AggregatedMetric(
            variant="treatment",
            sample_size=500,
            mean=10.0,
            std=0.0,
            variance=0.0,
            sum=5000.0,
            conversion_count=500,
        )
        experiment = _make_experiment("frequentist", min_sample_size=10)

        result = ExperimentAnalyzer().analyze_from_stats(
            experiment, control, treatment, "metric"
        )

        assert result.p_value is not None


class TestStdReportingConvention:
    def test_reported_std_uses_population_convention_matching_analyze(self):
        """
        analyze()'s VariantStats.std uses np.std(arr) (ddof=0, population).
        analyze_from_stats() derives std from the warehouse's sample (ddof=1)
        VARIANCE() aggregate -- it must convert to the same population
        convention when reporting VariantStats.std, so the same field means
        the same thing regardless of which stat_method branch produced it.
        """
        n = 100
        sample_variance = 1.0
        control = AggregatedMetric(
            variant="control",
            sample_size=n,
            mean=10.0,
            std=1.0,
            variance=sample_variance,
            sum=1000.0,
            conversion_count=50,
        )
        treatment = AggregatedMetric(
            variant="treatment",
            sample_size=n,
            mean=10.0,
            std=1.0,
            variance=sample_variance,
            sum=1000.0,
            conversion_count=50,
        )
        experiment = _make_experiment("frequentist", min_sample_size=10)

        result = ExperimentAnalyzer().analyze_from_stats(
            experiment, control, treatment, "metric"
        )

        expected_population_std = np.sqrt(sample_variance * (n - 1) / n)
        assert result.control.std == pytest.approx(float(expected_population_std))
        assert result.control.std != pytest.approx(1.0)  # not the raw sample std


class TestRelativeLift:
    def test_relative_lift_computed_from_real_means(self):
        control = AggregatedMetric(
            variant="control",
            sample_size=100,
            mean=10.0,
            std=1.0,
            variance=1.0,
            sum=1000.0,
            conversion_count=100,
        )
        treatment = AggregatedMetric(
            variant="treatment",
            sample_size=100,
            mean=12.0,
            std=1.0,
            variance=1.0,
            sum=1200.0,
            conversion_count=100,
        )
        experiment = _make_experiment("frequentist", min_sample_size=10)

        result = ExperimentAnalyzer().analyze_from_stats(
            experiment, control, treatment, "metric"
        )

        assert result.relative_lift == pytest.approx(0.2)


class TestSequentialFromStats:
    def test_matches_true_analyze_sequential_on_the_real_per_user_data(self):
        rng = np.random.default_rng(7)
        control_arr = rng.normal(loc=100.0, scale=15.0, size=800)
        treatment_arr = rng.normal(loc=110.0, scale=15.0, size=800)

        control = _aggregate(control_arr, "control")
        treatment = _aggregate(treatment_arr, "treatment")

        expected = ExperimentAnalyzer().analyze_sequential(
            list(control_arr), list(treatment_arr), "metric"
        )
        actual = ExperimentAnalyzer().analyze_sequential_from_stats(
            control, treatment, "metric"
        )

        assert actual.metric_name == expected.metric_name
        assert actual.is_significant == expected.is_significant
        assert actual.relative_lift == pytest.approx(expected.relative_lift)

    def test_does_not_force_continue_for_genuine_nonzero_variance(self):
        """
        Regression test for the bug this fixes: the old `[mean] * n`
        reconstruction made every element in each variant identical, so
        `var_pooled` was always EXACTLY 0.0 -- which forces this method's
        own pre-existing zero-variance guard to report
        e_value=1.0/ci=[0,0]/recommendation="continue" unconditionally.
        mSPRT via /analyze's warehouse-aggregate path has therefore never
        been able to report anything else, regardless of the real
        underlying e-value. Feeding real variance must escape that branch.
        """
        control = AggregatedMetric(
            variant="control",
            sample_size=500,
            mean=10.0,
            std=2.0,
            variance=4.0,
            sum=5000.0,
            conversion_count=500,
        )
        treatment = AggregatedMetric(
            variant="treatment",
            sample_size=500,
            mean=12.0,
            std=2.2,
            variance=4.84,
            sum=6000.0,
            conversion_count=500,
        )

        result = ExperimentAnalyzer().analyze_sequential_from_stats(
            control, treatment, "metric"
        )

        assert "e=1.0000|ci=[0.0000,0.0000]|continue" not in result.metric_name

    def test_reconstructed_array_pattern_always_forces_continue(self):
        """
        Documents the exact bug being fixed, reproduced against the OLD
        method: reconstructing `[mean] * n` per variant and calling the
        original analyze_sequential() always lands on the degenerate
        var_pooled==0 branch, no matter how large the real mean difference.
        """
        old_result = ExperimentAnalyzer().analyze_sequential(
            [10.0] * 500, [12.0] * 500, "metric"
        )
        assert "e=1.0000|ci=[0.0000,0.0000]|continue" in old_result.metric_name
        assert old_result.is_significant is False

    def test_genuine_zero_variance_still_forces_continue(self):
        """The zero-variance guard must still fire for REAL zero variance."""
        control = AggregatedMetric(
            variant="control",
            sample_size=500,
            mean=10.0,
            std=0.0,
            variance=0.0,
            sum=5000.0,
            conversion_count=500,
        )
        treatment = AggregatedMetric(
            variant="treatment",
            sample_size=500,
            mean=12.0,
            std=0.0,
            variance=0.0,
            sum=6000.0,
            conversion_count=500,
        )

        result = ExperimentAnalyzer().analyze_sequential_from_stats(
            control, treatment, "metric"
        )

        assert "e=1.0000|ci=[0.0000,0.0000]|continue" in result.metric_name
        assert result.is_significant is False

    def test_requires_at_least_two_observations_per_variant(self):
        control = AggregatedMetric(
            variant="control",
            sample_size=1,
            mean=1.0,
            std=0.0,
            variance=0.0,
            sum=1.0,
            conversion_count=1,
        )
        treatment = AggregatedMetric(
            variant="treatment",
            sample_size=1,
            mean=2.0,
            std=0.0,
            variance=0.0,
            sum=2.0,
            conversion_count=1,
        )

        result = ExperimentAnalyzer().analyze_sequential_from_stats(
            control, treatment, "metric"
        )

        assert result.p_value is None
        assert result.is_significant is False

    def test_conversion_rate_stays_within_bounds_even_with_bad_counts(self):
        control = AggregatedMetric(
            variant="control",
            sample_size=100,
            mean=1.5,
            std=0.3,
            variance=0.09,
            sum=150.0,
            conversion_count=150,
        )
        treatment = AggregatedMetric(
            variant="treatment",
            sample_size=100,
            mean=1.0,
            std=0.3,
            variance=0.09,
            sum=100.0,
            conversion_count=-5,
        )

        result = ExperimentAnalyzer().analyze_sequential_from_stats(
            control, treatment, "metric"
        )

        assert 0.0 <= result.control.conversion_rate <= 1.0
        assert 0.0 <= result.treatment.conversion_rate <= 1.0
