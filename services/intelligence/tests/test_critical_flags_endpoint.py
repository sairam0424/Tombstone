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
            #
            # Key insight: we must space out edge pairs to avoid spurious cross-couplings.
            # Between each edge pair, we add a gap > COUPLING_WINDOW_SECONDS (300 seconds)
            # so that different edges don't accidentally couple with each other.
            events = []
            ts = 1000000
            import math
            COUPLING_WINDOW_SECONDS = 300

            for i, (source, target, expected_weight) in enumerate(self.edges):
                # Calculate required delta: expected_weight = exp(-0.1 * (delta / 60))
                # ln(expected_weight) = -0.1 * (delta / 60)
                # delta = -ln(expected_weight) * 600
                delta = int(-math.log(max(expected_weight, 0.001)) * 600)
                delta = min(delta, 290)  # Keep under COUPLING_WINDOW_SECONDS

                # Space each edge pair far apart (gap of 500s between edge pairs)
                # to prevent spurious coupling between different edges
                pair_start = ts + i * 500
                events.append({"flag_key": source, "ts": pair_start})
                events.append({"flag_key": target, "ts": pair_start + delta})
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


@patch("app.main.httpx.AsyncClient.get")
def test_critical_flags_scoring_formula_a_b_c_d_ranking(mock_evaluator_get, app_with_critical_flags_mock):
    """
    Verify the scoring formula produces the correct A > B > C > D ranking.

    This is a regression test for the critical-flags ranking formula:
    score = (in_degree + out_degree) * avg_edge_weight * blast_radius_multiplier

    The test constructs a 5-edge graph matching the plan's verification example:
    A -> B (0.8)
    A -> C (0.6)
    B -> C (0.7)
    D -> A (0.9)
    D -> C (0.5)

    With blast_radius_tier assignments:
    A: BLOCKED (mult=4)
    B: HIGH (mult=3)
    C: MEDIUM (mult=2)
    D: LOW (mult=1)

    Expected scores (formula applied):
    A: (in=1, out=2) * avg=0.767 * 4 ≈ 9.20  (from edges: in=[0.9], out=[0.8, 0.6])
    B: (in=1, out=1) * avg=0.75  * 3 ≈ 4.50  (from edges: in=[0.8], out=[0.7])
    C: (in=3, out=0) * avg=0.6   * 2 ≈ 3.60  (from edges: in=[0.6, 0.7, 0.5])
    D: (in=0, out=2) * avg=0.7   * 1 ≈ 1.40  (from edges: out=[0.9, 0.5])

    Ranking: A (9.20) > B (4.50) > C (3.60) > D (1.40)
    """
    from app import main
    from unittest.mock import AsyncMock
    main.app.state.graph_builder = app_with_critical_flags_mock.state.graph_builder
    main.app.state.redis = app_with_critical_flags_mock.state.redis

    # Create a custom mock pool that generates exact events for the target graph
    # To ensure we get exactly the edges we want, we manually create events
    # that are timed to produce the target weights with no spurious couplings.
    class ExactGraphMockPool:
        async def fetch(self, query: str, *args):
            if "audit_log" in query:
                # Manually construct events to generate exactly these weighted edges:
                # A -> B (0.8): delta ~ 133s gives exp(-0.1 * 133/60) ≈ 0.80
                # A -> C (0.6): delta ~ 407s (CAPPED at 290) gives exp(-0.1 * 290/60) ≈ 0.62
                # B -> C (0.7): delta ~ 214s gives exp(-0.1 * 214/60) ≈ 0.70
                # D -> A (0.9): delta ~ 63s gives exp(-0.1 * 63/60) ≈ 0.90
                # D -> C (0.5): delta ~ 663s (CAPPED at 290) gives exp(-0.1 * 290/60) ≈ 0.62
                #
                # Strategy: space each pair 1000+ seconds apart to prevent inter-pair coupling,
                # then tightly couple the two events within each pair.
                events = [
                    # Edge A -> B (0.8), pair 0
                    {"flag_key": "A", "ts": 1000000},
                    {"flag_key": "B", "ts": 1000133},
                    # Edge A -> C (0.6), pair 1
                    {"flag_key": "A", "ts": 2000000},
                    {"flag_key": "C", "ts": 2000290},
                    # Edge B -> C (0.7), pair 2
                    {"flag_key": "B", "ts": 3000000},
                    {"flag_key": "C", "ts": 3000214},
                    # Edge D -> A (0.9), pair 3
                    {"flag_key": "D", "ts": 4000000},
                    {"flag_key": "A", "ts": 4000063},
                    # Edge D -> C (0.5), pair 4
                    {"flag_key": "D", "ts": 5000000},
                    {"flag_key": "C", "ts": 5000290},
                ]
                return sorted(events, key=lambda x: x["ts"])
            return []

    mock_pool = ExactGraphMockPool()
    mock_get_pool = AsyncMock(return_value=mock_pool)
    main.app.state.graph_builder._get_pool = mock_get_pool

    # Mock evaluator blast-radius responses
    def mock_evaluator_call(url: str, **kwargs):
        from types import SimpleNamespace
        params = kwargs.get("params", {})
        flag_key = params.get("flag_key", "")

        tier_map = {"A": "BLOCKED", "B": "HIGH", "C": "MEDIUM", "D": "LOW"}
        tier = tier_map.get(flag_key, "LOW")

        return SimpleNamespace(
            status_code=200,
            json=lambda t=tier: {"result": {"risk_score": t}}
        )

    mock_evaluator_get.side_effect = mock_evaluator_call

    client = TestClient(main.app)
    response = client.get("/api/v1/graph/critical-flags?limit=10")

    assert response.status_code == 200
    data = response.json()
    flags = data["flags"]

    # Must have exactly 4 flags
    assert len(flags) == 4, f"Expected 4 flags, got {len(flags)}"

    # Verify ranking order: A > B > C > D
    assert flags[0]["key"] == "A", f"Expected first flag to be A, got {flags[0]['key']}"
    assert flags[1]["key"] == "B", f"Expected second flag to be B, got {flags[1]['key']}"
    assert flags[2]["key"] == "C", f"Expected third flag to be C, got {flags[2]['key']}"
    assert flags[3]["key"] == "D", f"Expected fourth flag to be D, got {flags[3]['key']}"

    # Verify scores are in descending order
    assert flags[0]["score"] > flags[1]["score"], \
        f"A score {flags[0]['score']} should be > B score {flags[1]['score']}"
    assert flags[1]["score"] > flags[2]["score"], \
        f"B score {flags[1]['score']} should be > C score {flags[2]['score']}"
    assert flags[2]["score"] > flags[3]["score"], \
        f"C score {flags[2]['score']} should be > D score {flags[3]['score']}"

    # Verify blast_radius_tier assignments
    assert flags[0]["blast_radius_tier"] == "BLOCKED"
    assert flags[1]["blast_radius_tier"] == "HIGH"
    assert flags[2]["blast_radius_tier"] == "MEDIUM"
    assert flags[3]["blast_radius_tier"] == "LOW"

    # Verify degree counts
    # A: in=1 (D->A), out=2 (A->B, A->C)
    assert flags[0]["in_degree"] == 1, f"A in_degree should be 1, got {flags[0]['in_degree']}"
    assert flags[0]["out_degree"] == 2, f"A out_degree should be 2, got {flags[0]['out_degree']}"

    # B: in=1 (A->B), out=1 (B->C)
    assert flags[1]["in_degree"] == 1, f"B in_degree should be 1, got {flags[1]['in_degree']}"
    assert flags[1]["out_degree"] == 1, f"B out_degree should be 1, got {flags[1]['out_degree']}"

    # C: in=3 (A->C, B->C, D->C), out=0
    assert flags[2]["in_degree"] == 3, f"C in_degree should be 3, got {flags[2]['in_degree']}"
    assert flags[2]["out_degree"] == 0, f"C out_degree should be 0, got {flags[2]['out_degree']}"

    # D: in=0, out=2 (D->A, D->C)
    assert flags[3]["in_degree"] == 0, f"D in_degree should be 0, got {flags[3]['in_degree']}"
    assert flags[3]["out_degree"] == 2, f"D out_degree should be 2, got {flags[3]['out_degree']}"

    # Verify approximate scores (allowing for floating point rounding)
    # Calculated from actual edge weights: A->B(0.8012), A->C(0.6167), B->C(0.7), D->A(0.9003), D->C(0.6167)
    # A: (1+2)*0.7727*4 ≈ 9.27
    assert 9.0 < flags[0]["score"] < 9.5, f"A score should be ~9.27, got {flags[0]['score']}"

    # B: (1+1)*0.7506*3 ≈ 4.50
    assert 4.4 < flags[1]["score"] < 4.7, f"B score should be ~4.50, got {flags[1]['score']}"

    # C: (3+0)*0.6445*2 ≈ 3.87
    assert 3.7 < flags[2]["score"] < 4.0, f"C score should be ~3.87, got {flags[2]['score']}"

    # D: (0+2)*0.7585*1 ≈ 1.52
    assert 1.4 < flags[3]["score"] < 1.7, f"D score should be ~1.52, got {flags[3]['score']}"
