"""
Tests for CUPED (app/experiments/cuped.py) and POST /api/v1/experiments/
cuped-adjust. Zero tests of any kind existed for either before EXP-1 PR 3/3
formally closed CUPED's scope to this raw-per-user-data path only.
"""

from __future__ import annotations

import numpy as np
import pytest
from fastapi.testclient import TestClient

from app import main
from app.experiments.cuped import cuped_adjustment, cuped_effect_size


class TestCupedAdjustment:
    def test_correlated_covariate_reduces_variance(self):
        """
        A covariate that's a noisy copy of the outcome should let CUPED
        remove a real, substantial chunk of variance -- this is the whole
        point of the technique (typical real-world reduction: 20-40%).
        """
        rng = np.random.default_rng(42)
        n = 500
        pre_control = rng.normal(10, 2, n)
        pre_treatment = rng.normal(10, 2, n)
        # Outcome correlates strongly with the pre-experiment covariate,
        # plus a small treatment effect and noise.
        control = pre_control + rng.normal(0, 0.5, n)
        treatment = pre_treatment + 1.0 + rng.normal(0, 0.5, n)

        adj_treatment, adj_control, reduction_pct = cuped_adjustment(
            treatment, control, pre_treatment, pre_control
        )

        assert len(adj_treatment) == n
        assert len(adj_control) == n
        # Regression test: adversarial review found `reduction_pct > 50.0`
        # was far too loose -- two mutants (wrong denominator: cov/var(Y)
        # instead of cov/var(X); wrong np.cov axis: [0,0] instead of [0,1])
        # both still produced ~88% for this exact seeded dataset (true
        # value: 89.19%), because the covariate is engineered to correlate
        # almost perfectly with the outcome. Pinned to the exact
        # independently-computed value with a tight tolerance instead.
        assert reduction_pct == pytest.approx(89.19, abs=0.5)

    def test_theta_zeroes_out_residual_covariance_with_the_covariate(self):
        """
        Regression test closing the same gap as the tightened assertion
        above, but via a mathematically exact invariant instead of a
        pinned magic number: for theta computed correctly as
        Cov(Y,X)/Var(X), Cov(Y - theta*X, X) = Cov(Y,X) - theta*Var(X) = 0
        EXACTLY (an algebraic identity on the finite sample, not an
        asymptotic one) -- so a correct implementation's residual
        covariance with the covariate must be ~0 regardless of dataset,
        while a wrong-denominator or wrong-np.cov-axis bug produces a
        residual covariance close to the ORIGINAL Cov(Y,X) (independently
        confirmed: 0.37/0.45 for the two known mutants vs ~0.004 for the
        real implementation, on this exact dataset).
        """
        rng = np.random.default_rng(42)
        n = 500
        pre_control = rng.normal(10, 2, n)
        pre_treatment = rng.normal(10, 2, n)
        control = pre_control + rng.normal(0, 0.5, n)
        treatment = pre_treatment + 1.0 + rng.normal(0, 0.5, n)

        adj_treatment, adj_control, _ = cuped_adjustment(
            treatment, control, pre_treatment, pre_control
        )

        residual_cov = np.cov(
            np.concatenate([adj_treatment, adj_control]),
            np.concatenate([pre_treatment, pre_control]),
        )[0, 1]
        assert residual_cov == pytest.approx(0.0, abs=0.05)

    def test_zero_variance_covariate_is_a_no_op(self):
        treatment = np.array([1.0, 2.0, 3.0])
        control = np.array([1.5, 2.5, 3.5])
        pre_treatment = np.array([5.0, 5.0, 5.0])
        pre_control = np.array([5.0, 5.0, 5.0])

        adj_treatment, adj_control, reduction_pct = cuped_adjustment(
            treatment, control, pre_treatment, pre_control
        )

        assert list(adj_treatment) == list(treatment)
        assert list(adj_control) == list(control)
        assert reduction_pct == 0.0


class TestCupedEffectSize:
    def test_returns_all_expected_fields(self):
        rng = np.random.default_rng(7)
        n = 200
        pre_control = rng.normal(0, 1, n)
        pre_treatment = rng.normal(0, 1, n)
        control = pre_control + rng.normal(0, 1, n)
        treatment = pre_treatment + 2.0 + rng.normal(0, 1, n)

        result = cuped_effect_size(treatment, control, pre_treatment, pre_control)

        for key in (
            "effect_size",
            "p_value",
            "variance_reduction_pct",
            "adjusted_treatment_mean",
            "adjusted_control_mean",
            "ci_lower",
            "ci_upper",
            "t_statistic",
            "degrees_of_freedom",
        ):
            assert key in result

    def test_large_true_effect_is_significant(self):
        rng = np.random.default_rng(123)
        n = 300
        pre_control = rng.normal(0, 1, n)
        pre_treatment = rng.normal(0, 1, n)
        control = pre_control + rng.normal(0, 0.3, n)
        treatment = pre_treatment + 5.0 + rng.normal(0, 0.3, n)

        result = cuped_effect_size(treatment, control, pre_treatment, pre_control)

        assert result["p_value"] < 0.05
        assert result["effect_size"] == pytest.approx(5.0, abs=0.5)

    def test_reported_means_and_effect_size_use_the_adjusted_arrays_not_raw(self):
        """
        Regression test: adversarial review found that a mutant feeding the
        RAW treatment/control arrays into the t-test/means/CI/t_statistic
        (while still calling cuped_adjustment() correctly for
        variance_reduction_pct -- a copy-paste-shaped bug, not a total
        no-op) passed every other test in this file, including
        test_large_true_effect_is_significant (the true effect there is so
        much larger than the noise that adjusted and unadjusted results are
        statistically indistinguishable at that test's bound). This test
        independently computes the adjustment via cuped_adjustment()
        directly and cross-checks cuped_effect_size's reported means
        against it exactly -- a mutant using raw means would fail the first
        two assertions immediately, since the raw and adjusted means differ
        substantially for this dataset (strong covariate correlation).
        """
        rng = np.random.default_rng(99)
        n = 200
        # Deliberately imbalanced covariate BETWEEN groups (not just
        # correlated within each) -- pre_treatment's own distribution is
        # shifted +5 vs pre_control's, so the covariate adjustment must
        # subtract a large, unmistakable amount from the treatment mean.
        # This guarantees the raw and adjusted effect sizes differ by a
        # wide margin (~7.96 vs ~0.42 for this exact seed), so the
        # assertions below cannot pass coincidentally for both raw and
        # adjusted values.
        pre_control = rng.normal(0, 1, n)
        pre_treatment = rng.normal(5, 1, n)
        control = pre_control + rng.normal(0, 0.2, n)
        treatment = pre_treatment + 3.0 + rng.normal(0, 0.2, n)

        adj_t_ref, adj_c_ref, _ = cuped_adjustment(
            treatment, control, pre_treatment, pre_control
        )
        result = cuped_effect_size(treatment, control, pre_treatment, pre_control)

        assert result["adjusted_treatment_mean"] == pytest.approx(
            float(adj_t_ref.mean()), abs=1e-9
        )
        assert result["adjusted_control_mean"] == pytest.approx(
            float(adj_c_ref.mean()), abs=1e-9
        )
        raw_diff = float(np.mean(treatment) - np.mean(control))
        assert result["effect_size"] != pytest.approx(raw_diff, abs=1.0)


class TestCupedAdjustEndpoint:
    def _body(self, **overrides) -> dict:
        body = {
            "treatment": [11.0, 12.0, 13.0, 14.0, 15.0],
            "control": [9.0, 10.0, 11.0, 12.0, 13.0],
            "pre_treatment": [10.0, 11.0, 12.0, 13.0, 14.0],
            "pre_control": [10.0, 11.0, 12.0, 13.0, 14.0],
        }
        body.update(overrides)
        return body

    def test_valid_request_returns_200_with_expected_shape(self):
        client = TestClient(main.app)
        response = client.post("/api/v1/experiments/cuped-adjust", json=self._body())

        assert response.status_code == 200
        body = response.json()
        for key in (
            "effect_size",
            "p_value",
            "variance_reduction_pct",
            "adjusted_treatment_mean",
            "adjusted_control_mean",
            "ci_lower",
            "ci_upper",
            "t_statistic",
            "degrees_of_freedom",
            "is_significant",
        ):
            assert key in body

        # Regression test: adversarial review found this dataset is fully
        # deterministic and every value was hand-computable, yet the test
        # only checked key presence -- a route-wiring bug (e.g. swapping
        # adjusted_treatment_mean/adjusted_control_mean in the
        # CupedAdjustResponse construction, or swapping req.treatment/
        # req.control before calling cuped_effect_size) would still return
        # 200 with all keys present. Values independently computed by
        # calling cuped_effect_size() directly with this exact dataset.
        assert body["effect_size"] == pytest.approx(2.0, abs=1e-6)
        assert body["p_value"] == pytest.approx(9.3088e-08, rel=1e-3)
        assert body["variance_reduction_pct"] == pytest.approx(65.8436, abs=1e-3)
        assert body["adjusted_treatment_mean"] == pytest.approx(13.0, abs=1e-6)
        assert body["adjusted_control_mean"] == pytest.approx(11.0, abs=1e-6)
        assert body["t_statistic"] == pytest.approx(18.0, abs=1e-6)
        assert body["degrees_of_freedom"] == pytest.approx(8.0, abs=1e-6)
        assert body["is_significant"] is True

    def test_fewer_than_2_observations_returns_422(self):
        client = TestClient(main.app)
        response = client.post(
            "/api/v1/experiments/cuped-adjust",
            json=self._body(treatment=[1.0], pre_treatment=[1.0]),
        )
        assert response.status_code == 422

    def test_mismatched_treatment_length_returns_422(self):
        client = TestClient(main.app)
        response = client.post(
            "/api/v1/experiments/cuped-adjust",
            json=self._body(pre_treatment=[10.0, 11.0]),  # len 2 vs treatment's 5
        )
        assert response.status_code == 422

    def test_mismatched_control_length_returns_422(self):
        client = TestClient(main.app)
        response = client.post(
            "/api/v1/experiments/cuped-adjust",
            json=self._body(pre_control=[10.0, 11.0]),  # len 2 vs control's 5
        )
        assert response.status_code == 422
