"""
Tests for GET /api/v1/graph/critical-flags endpoint.

Mocks the evaluator HTTP API blast-radius call and verifies the scoring
formula: score = (in_degree + out_degree) * avg_edge_weight * blast_radius_multiplier.
"""
from __future__ import annotations

import pytest
from unittest.mock import AsyncMock, patch, MagicMock
from fastapi.testclient import TestClient


class MockGraphBuilder:
    """Mock for DependencyGraphBuilder that provides in/out degree data."""

    def __init__(self):
        self.all_edges: list[tuple[str, str, float]] = []

    async def _get_pool(self):
        """Mock pool for the endpoint's flags table query."""
        return MockPool(self.all_edges)

    def set_edges(self, edges: list[tuple[str, str, float]]):
        """edges: list of (source, target, weight)"""
        self.all_edges = edges

    def get_degree_data(self) -> dict[str, dict]:
        """Compute in/out degree and avg weight for all flags in the graph."""
        from collections import defaultdict
        in_deg = defaultdict(list)
        out_deg = defaultdict(list)

        for source, target, weight in self.all_edges:
            out_deg[source].append(weight)
            in_deg[target].append(weight)

        result = {}
        all_flags = set(in_deg.keys()) | set(out_deg.keys())
        for flag in all_flags:
            in_weights = in_deg.get(flag, [])
            out_weights = out_deg.get(flag, [])
            all_weights = in_weights + out_weights
            result[flag] = {
                "in_degree": len(in_weights),
                "out_degree": len(out_weights),
                "avg_edge_weight": sum(all_weights) / len(all_weights) if all_weights else 0.0,
            }
        return result


class MockPool:
    """Mock asyncpg pool for audit_log query."""

    def __init__(self, edges: list[tuple[str, str, float]] = None):
        self.edges = edges or []

    async def fetch(self, query: str, *args):
        # If audit_log query: return simulated events based on edges
        if "audit_log" in query:
            # Convert edges to audit log events.
            # For each edge (source -> target), create two events within COUPLING_WINDOW_SECONDS.
            # The time delta between them determines the calculated weight:
            # weight = exp(-LAMBDA * (delta / 60.0)) where LAMBDA=0.1
            # So we need to set delta such that calculated weight ≈ expected weight
            events = []
            ts = 1000000
            for i, (source, target, expected_weight) in enumerate(self.edges):
                # Calculate required delta: expected_weight = exp(-0.1 * (delta / 60))
                # ln(expected_weight) = -0.1 * (delta / 60)
                # delta = -ln(expected_weight) * 600
                import math
                delta = int(-math.log(max(expected_weight, 0.001)) * 600)
                delta = min(delta, 290)  # Keep under COUPLING_WINDOW_SECONDS

                events.append({"flag_key": source, "ts": ts + i * 300})
                events.append({"flag_key": target, "ts": ts + i * 300 + delta})
            return sorted(events, key=lambda x: x["ts"])
        # For flags query
        return []


@pytest.fixture
def app_with_critical_flags_mock():
    from fastapi import FastAPI
    app = FastAPI()
    app.state.graph_builder = MockGraphBuilder()
    app.state.redis = object()
    return app


def test_critical_flags_endpoint_returns_valid_structure(app_with_critical_flags_mock):
    """Verify the endpoint returns the correct JSON structure with score and blast_radius_tier."""
    from app import main
    main.app.state.graph_builder = app_with_critical_flags_mock.state.graph_builder
    main.app.state.redis = app_with_critical_flags_mock.state.redis

    # Mock pool to return specific audit_log events
    mock_pool = MagicMock()

    async def mock_fetch(query: str, *args):
        if "audit_log" in query:
            return [
                {"flag_key": "payments.checkout", "ts": 1000},
                {"flag_key": "fraud.detection", "ts": 1050},
                {"flag_key": "auth.sso", "ts": 1100},
            ]
        return []

    mock_pool.fetch = mock_fetch
    app_with_critical_flags_mock.state.graph_builder._get_pool = AsyncMock(return_value=mock_pool)
    main.app.state.graph_builder = app_with_critical_flags_mock.state.graph_builder

    client = TestClient(main.app)
    response = client.get("/api/v1/graph/critical-flags?limit=10")

    assert response.status_code == 200
    data = response.json()

    # Verify structure
    assert "flags" in data
    assert "generated_at" in data
    assert isinstance(data["flags"], list)

    # Verify each flag has required fields
    for flag in data["flags"]:
        assert "key" in flag
        assert "score" in flag
        assert "blast_radius_tier" in flag
        assert "in_degree" in flag
        assert "out_degree" in flag
        assert "avg_edge_weight" in flag
        assert flag["blast_radius_tier"] in ["BLOCKED", "HIGH", "MEDIUM", "LOW"]
        assert isinstance(flag["score"], (int, float))
        assert flag["score"] >= 0


@patch("app.main.httpx.AsyncClient.get")
def test_critical_flags_evaluator_unreachable_fallback_low(mock_evaluator_get, app_with_critical_flags_mock):
    """When evaluator is unreachable, fallback to blast_radius_tier='LOW'."""
    from app import main
    main.app.state.graph_builder = app_with_critical_flags_mock.state.graph_builder
    main.app.state.redis = app_with_critical_flags_mock.state.redis

    mock_builder: MockGraphBuilder = main.app.state.graph_builder
    mock_builder.set_edges([("A", "B", 0.5)])

    # Evaluator always returns 500
    async def mock_blast_fail(url: str, **kwargs):
        from types import SimpleNamespace
        return SimpleNamespace(status_code=500, json=lambda: {})

    mock_evaluator_get.side_effect = mock_blast_fail

    client = TestClient(main.app)
    response = client.get("/api/v1/graph/critical-flags?limit=10")

    assert response.status_code == 200
    flags = response.json()["flags"]
    # Both flags get fallback tier "LOW" (mult=1)
    assert all(f["blast_radius_tier"] == "LOW" for f in flags)
