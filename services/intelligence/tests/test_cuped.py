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
        assert reduction_pct > 50.0  # strong correlation -> big reduction

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
