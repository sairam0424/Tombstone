"""
Tests for POST /api/v1/experiments/analyze — the FastAPI route wiring around
ExperimentAnalyzer.analyze_from_stats() (EXP-1). Drives the real route via
TestClient with a mocked warehouse connector, matching the TestClient +
mocked-backing-dependency convention used in test_critical_flags_endpoint.py
and test_dependency_graph_endpoint.py.

Closes a test-coverage gap flagged by adversarial review of PR #203: no
prior test exercised analyze_experiment()'s own control/treatment
extraction, ExperimentDefinition construction, or stat_method branch
selection end-to-end — only the isolated analyzer method was covered.
"""

from __future__ import annotations

from unittest.mock import patch

from fastapi.testclient import TestClient

from app import main
from app.warehouse.connector import AggregatedMetric


class FakeConnector:
    """Stands in for a real WarehouseConnector — returns canned aggregates."""

    def __init__(self, metrics: dict[str, AggregatedMetric]):
        self._metrics = metrics

    async def query_experiment_metrics(self, **kwargs) -> dict[str, AggregatedMetric]:
        return self._metrics


class FailingConnector:
    async def query_experiment_metrics(self, **kwargs):
        raise RuntimeError("connection refused")


def _request_body(**overrides) -> dict:
    body = {
        "experiment_id": "exp-1",
        "flag_key": "test-flag",
        "metric_name": "conversion",
        "metric_sql": "CASE WHEN converted THEN 1 ELSE 0 END",
        "event_table": "events",
        "flag_event_table": "flag_events",
        "warehouse_type": "postgresql",
        "warehouse_dsn": "postgresql://unused/unused",
        "min_sample_size": 10,
    }
    body.update(overrides)
    return body


_CLOSE_METRICS = {
    "control": AggregatedMetric(
        variant="control",
        sample_size=1000,
        mean=0.30,
        std=0.4583,
        variance=0.21,
        sum=300.0,
        conversion_count=300,
    ),
    "treatment": AggregatedMetric(
        variant="treatment",
        sample_size=1000,
        mean=0.32,
        std=0.4665,
        variance=0.2176,
        sum=320.0,
        conversion_count=320,
    ),
}


@patch("app.experiments.routes.get_connector")
def test_analyze_default_bayesian_uses_real_conversion_count(mock_get_connector):
    """
    Regression test for the bug this PR fixed: the old [mean]*n reconstruction
    always reported 100% conversion for any positive mean, regardless of the
    real rate. Driven through the real route (default stat_method=bayesian).
    """
    mock_get_connector.return_value = FakeConnector(_CLOSE_METRICS)

    client = TestClient(main.app)
    response = client.post("/api/v1/experiments/analyze", json=_request_body())

    assert response.status_code == 200
    body = response.json()
    assert body["sample_sizes"] == {"control": 1000, "treatment": 1000}
    assert body["probability_beats_control"] is not None
    assert body["probability_beats_control"] < 0.95
    assert body["is_significant"] is False


@patch("app.experiments.routes.get_connector")
def test_analyze_frequentist_does_not_fabricate_significance(mock_get_connector):
    """
    Regression test for the other bug this PR fixed: the old reconstruction
    made a t-test report p ~= 0.0 for ANY nonzero mean difference. Driven
    through the real route with stat_method=frequentist.
    """
    mock_get_connector.return_value = FakeConnector(_CLOSE_METRICS)

    client = TestClient(main.app)
    response = client.post(
        "/api/v1/experiments/analyze", json=_request_body(stat_method="frequentist")
    )

    assert response.status_code == 200
    body = response.json()
    assert body["p_value"] is not None
    assert body["p_value"] > 0.05
    assert body["is_significant"] is False


@patch("app.experiments.routes.get_connector")
def test_analyze_missing_variant_returns_422(mock_get_connector):
    mock_get_connector.return_value = FakeConnector(
        {
            "control": AggregatedMetric(
                variant="control",
                sample_size=10,
                mean=1.0,
                std=0.0,
                variance=0.0,
                sum=10.0,
                conversion_count=10,
            ),
        }
    )

    client = TestClient(main.app)
    response = client.post("/api/v1/experiments/analyze", json=_request_body())

    assert response.status_code == 422


@patch("app.experiments.routes.get_connector")
def test_analyze_warehouse_failure_returns_502(mock_get_connector):
    mock_get_connector.return_value = FailingConnector()

    client = TestClient(main.app)
    response = client.post("/api/v1/experiments/analyze", json=_request_body())

    assert response.status_code == 502
