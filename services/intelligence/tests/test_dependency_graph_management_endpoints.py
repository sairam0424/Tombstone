"""
Tests for POST /api/v1/dependency-graph and GET /api/v1/dependency-graph/
impact/{flag_key} -- the two endpoints in app/main.py's graph subsystem
that, per adversarial review of PR #206 (INT-2 tenancy fix), had zero
endpoint-level test coverage of any kind, project_id or otherwise.

test_background_job_lock.py's docstring mentions "the HTTP-triggered
POST /api/v1/dependency-graph handler" but its own tests only exercise a
bare asyncio.Lock in isolation, never the real FastAPI route -- so this
file is the first real HTTP-level test for build_dependency_graph, and the
only one for get_flag_impact.
"""

from __future__ import annotations

import asyncio

import pytest
from fastapi import FastAPI
from fastapi.testclient import TestClient


class MockGraphBuilder:
    def __init__(self):
        self.build_calls: list[tuple[str, str, int, int]] = []
        self.get_impact_fast_calls: list[tuple[str, str]] = []
        self.get_impact_calls: list[tuple[str, str, str, int]] = []
        self.redis_data: dict[str, list[dict] | None] = {}

    async def build(self, project_id, environment, from_unix, to_unix):
        self.build_calls.append((project_id, environment, from_unix, to_unix))

        class _Graph:
            nodes = []
            edges = []
            generated_at = to_unix
            event_count = 0

        return _Graph()

    async def get_impact_fast(self, flag_key, redis_client, project_id):
        self.get_impact_fast_calls.append((flag_key, project_id))
        return self.redis_data.get(flag_key)

    async def get_impact(self, flag_key, environment, project_id, days):
        self.get_impact_calls.append((flag_key, environment, project_id, days))
        return {"flag_key": flag_key, "environment": environment, "co_changed_with": []}


@pytest.fixture
def app_with_mocked_builder():
    app = FastAPI()
    app.state.graph_builder = MockGraphBuilder()
    app.state.redis = object()
    app.state.background_job_lock = asyncio.Lock()
    return app


def _wire(app_fixture):
    from app import main

    main.app.state.graph_builder = app_fixture.state.graph_builder
    main.app.state.redis = app_fixture.state.redis
    main.app.state.background_job_lock = app_fixture.state.background_job_lock
    return main


class TestBuildDependencyGraph:
    def test_defaults_project_id_when_absent(self, app_with_mocked_builder):
        from app.graph.builder import DEFAULT_PROJECT_ID

        main = _wire(app_with_mocked_builder)
        mock_builder: MockGraphBuilder = main.app.state.graph_builder

        client = TestClient(main.app)
        response = client.post("/api/v1/dependency-graph?from_unix=100&to_unix=200")

        assert response.status_code == 200
        assert mock_builder.build_calls == [
            (DEFAULT_PROJECT_ID, "production", 100, 200)
        ]

    def test_passes_explicit_project_id_and_environment(self, app_with_mocked_builder):
        main = _wire(app_with_mocked_builder)
        mock_builder: MockGraphBuilder = main.app.state.graph_builder

        client = TestClient(main.app)
        response = client.post(
            "/api/v1/dependency-graph"
            "?project_id=explicit-project-xyz&environment=staging"
            "&from_unix=100&to_unix=200"
        )

        assert response.status_code == 200
        assert mock_builder.build_calls == [
            ("explicit-project-xyz", "staging", 100, 200)
        ]

    def test_returns_409_when_background_job_lock_is_held(
        self, app_with_mocked_builder
    ):
        main = _wire(app_with_mocked_builder)

        async def _acquire_and_check():
            async with main.app.state.background_job_lock:
                client = TestClient(main.app)
                return client.post("/api/v1/dependency-graph?from_unix=100&to_unix=200")

        response = asyncio.run(_acquire_and_check())
        assert response.status_code == 409


class TestGetFlagImpact:
    def test_defaults_project_id_when_absent(self, app_with_mocked_builder):
        from app.graph.builder import DEFAULT_PROJECT_ID

        main = _wire(app_with_mocked_builder)
        mock_builder: MockGraphBuilder = main.app.state.graph_builder
        mock_builder.redis_data["checkout-v2"] = [
            {"flag_key": "fraud-check", "weight": 0.5}
        ]

        client = TestClient(main.app)
        response = client.get("/api/v1/dependency-graph/impact/checkout-v2")

        assert response.status_code == 200
        assert mock_builder.get_impact_fast_calls == [
            ("checkout-v2", DEFAULT_PROJECT_ID)
        ]

    def test_passes_explicit_project_id(self, app_with_mocked_builder):
        main = _wire(app_with_mocked_builder)
        mock_builder: MockGraphBuilder = main.app.state.graph_builder
        mock_builder.redis_data["checkout-v2"] = [
            {"flag_key": "fraud-check", "weight": 0.5}
        ]

        client = TestClient(main.app)
        response = client.get(
            "/api/v1/dependency-graph/impact/checkout-v2?project_id=explicit-project-xyz"
        )

        assert response.status_code == 200
        assert mock_builder.get_impact_fast_calls == [
            ("checkout-v2", "explicit-project-xyz")
        ]

    def test_redis_miss_falls_back_to_get_impact_with_same_project_id(
        self, app_with_mocked_builder
    ):
        main = _wire(app_with_mocked_builder)
        mock_builder: MockGraphBuilder = main.app.state.graph_builder
        mock_builder.redis_data["checkout-v2"] = None  # cold start

        client = TestClient(main.app)
        response = client.get(
            "/api/v1/dependency-graph/impact/checkout-v2?project_id=explicit-project-xyz"
        )

        assert response.status_code == 200
        assert mock_builder.get_impact_calls == [
            ("checkout-v2", "production", "explicit-project-xyz", 30)
        ]
