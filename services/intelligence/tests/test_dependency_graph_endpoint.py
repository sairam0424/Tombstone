"""
Tests for GET /api/v1/graph/dependencies endpoint.

Requires a test fixture for `app` (FastAPI instance) with mocked
`app.state.redis` and `app.state.graph_builder`. Uses the same async test
harness pattern as `test_background_job_lock.py` (pytest-asyncio).
"""

from __future__ import annotations

import pytest
from fastapi import FastAPI
from fastapi.testclient import TestClient


class MockPool:
    """Mock asyncpg pool for flags table query."""

    async def fetch(self, query: str, *args):
        # Return empty list — the test doesn't need node metadata
        return []


class MockGraphBuilder:
    """Minimal mock for DependencyGraphBuilder matching the public API
    that GET /api/v1/graph/dependencies consumes."""

    def __init__(self):
        self.redis_data: dict[str, list[dict]] = {}
        self.db_fallback_data: dict[str, dict] = {}

    async def _get_pool(self):
        """Mock pool for the endpoint's flags table query."""
        return MockPool()

    async def get_impact_fast(
        self, flag_key: str, redis_client, project_id: str
    ) -> list[dict] | None:
        return self.redis_data.get(flag_key)

    async def get_impact(
        self, flag_key: str, environment: str, project_id: str, days: int
    ) -> dict:
        return self.db_fallback_data.get(
            flag_key,
            {
                "flag_key": flag_key,
                "environment": environment,
                "co_changed_with": [],
            },
        )


@pytest.fixture
def app_with_mocked_builder():
    """Returns a FastAPI app with mocked graph_builder and redis state."""
    app = FastAPI()
    app.state.graph_builder = MockGraphBuilder()
    app.state.redis = object()  # non-None sentinel
    return app


def test_dependencies_endpoint_redis_hit_depth_1(app_with_mocked_builder):
    """Redis hit with depth=1 returns direct neighbors only."""
    from app import main  # imports the route we're testing

    # Inject the mocked app state into main's global app instance
    # (alternatively, refactor main.py to accept app as param — deferred to follow-up)
    main.app.state.graph_builder = app_with_mocked_builder.state.graph_builder
    main.app.state.redis = app_with_mocked_builder.state.redis

    # Seed Redis mock data
    mock_builder: MockGraphBuilder = main.app.state.graph_builder
    mock_builder.redis_data["payments.checkout"] = [
        {"flag_key": "payments.processor", "weight": 0.8},
        {"flag_key": "fraud.detection", "weight": 0.6},
    ]

    client = TestClient(main.app)
    response = client.get(
        "/api/v1/graph/dependencies?flag_key=payments.checkout&depth=1"
    )

    assert response.status_code == 200
    data = response.json()
    assert data["flag_key"] == "payments.checkout"
    assert data["depth"] == 1
    assert len(data["edges"]) == 2
    assert data["edges"][0]["source"] == "payments.checkout"
    assert data["edges"][0]["target"] == "payments.processor"
    assert data["edges"][0]["weight"] == 0.8


def test_dependencies_endpoint_redis_miss_db_fallback(app_with_mocked_builder):
    """Redis miss triggers DB fallback via get_impact()."""
    from app import main

    main.app.state.graph_builder = app_with_mocked_builder.state.graph_builder
    main.app.state.redis = app_with_mocked_builder.state.redis

    mock_builder: MockGraphBuilder = main.app.state.graph_builder
    # Redis returns None (cold start)
    mock_builder.redis_data["new.flag"] = None
    mock_builder.db_fallback_data["new.flag"] = {
        "flag_key": "new.flag",
        "environment": "production",
        "co_changed_with": [
            {"flag_key": "old.flag", "co_change_count": 3, "avg_seconds_apart": 120.0}
        ],
    }

    client = TestClient(main.app)
    response = client.get("/api/v1/graph/dependencies?flag_key=new.flag&depth=1")

    assert response.status_code == 200
    data = response.json()
    assert data["flag_key"] == "new.flag"
    # Fallback path infers weight from co_change_count (higher count → higher coupling)
    assert len(data["edges"]) == 1
    assert data["edges"][0]["target"] == "old.flag"


def test_dependencies_endpoint_depth_2_traversal(app_with_mocked_builder):
    """depth=2 traverses to 2nd-degree neighbors."""
    from app import main

    main.app.state.graph_builder = app_with_mocked_builder.state.graph_builder
    main.app.state.redis = app_with_mocked_builder.state.redis

    mock_builder: MockGraphBuilder = main.app.state.graph_builder
    mock_builder.redis_data["A"] = [{"flag_key": "B", "weight": 0.9}]
    mock_builder.redis_data["B"] = [{"flag_key": "C", "weight": 0.7}]

    client = TestClient(main.app)
    response = client.get("/api/v1/graph/dependencies?flag_key=A&depth=2")

    assert response.status_code == 200
    data = response.json()
    assert data["depth"] == 2
    # Edges: A→B and B→C
    edge_pairs = {(e["source"], e["target"]) for e in data["edges"]}
    assert ("A", "B") in edge_pairs
    assert ("B", "C") in edge_pairs
