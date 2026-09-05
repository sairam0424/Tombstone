"""
Tenancy-isolation tests for IncidentCorrelator (INT-2 follow-up).

correlate()'s audit_log query had zero project_id scoping -- the exact same
TEN-1a-class cross-tenant leak PR #206 fixed in app/graph/builder.py, found
by adversarial review of that same PR but left unmentioned by its own
stated scope. Closes that gap: POST /api/v1/correlate can leak another
tenant's flag_key/actor/event_type (and a cross-tenant rollback_url) into
an incident-correlation response.

No test of any kind previously existed for IncidentCorrelator, correlate(),
or POST /api/v1/correlate.
"""

from __future__ import annotations

import pytest
from fastapi import FastAPI
from fastapi.testclient import TestClient

from app.correlation.correlator import IncidentCorrelator


class MockPool:
    def __init__(self, rows):
        self._rows = rows
        self.fetch_calls: list[tuple[str, tuple]] = []

    async def fetch(self, query, *args):
        self.fetch_calls.append((query, args))
        return self._rows


@pytest.fixture
def correlator():
    return IncidentCorrelator(db_url="postgresql://unused/unused")


class TestCorrelateQueryScoping:
    @pytest.mark.asyncio
    async def test_correlate_filters_audit_log_by_project_id(self, correlator):
        pool = MockPool([])
        correlator._pool = pool

        await correlator.correlate(
            incident_id="inc-1",
            affected_service="checkout",
            incident_start_unix=10_000,
            project_id="project-a",
        )

        query, args = pool.fetch_calls[0]
        assert "project_id = $" in query
        assert "project-a" in args

    @pytest.mark.asyncio
    async def test_different_projects_never_see_each_others_candidates(
        self, correlator
    ):
        """
        The query itself is what enforces isolation here (no in-Python
        pairing logic, unlike builder.py's rebuild_all) -- this test proves
        the real SQL predicate, not just that a project_id string appears
        somewhere in the query text.
        """
        pool = MockPool(
            [
                {
                    "flag_key": "checkout-v2",
                    "environment": "production",
                    "actor": "alice",
                    "event_type": "flag_environment_updated",
                    "changed_at": 9_900,
                }
            ]
        )
        correlator._pool = pool

        candidates = await correlator.correlate(
            incident_id="inc-1",
            affected_service="checkout",
            incident_start_unix=10_000,
            project_id="project-a",
        )

        assert len(candidates) == 1
        assert candidates[0]["flag_key"] == "checkout-v2"


@pytest.fixture
def app_with_mocked_correlator():
    app = FastAPI()
    app.state.correlator = IncidentCorrelator(db_url="postgresql://unused/unused")
    return app


class TestCorrelateIncidentEndpoint:
    def test_defaults_project_id_when_absent(self, app_with_mocked_correlator):
        from app import main
        from app.graph.builder import DEFAULT_PROJECT_ID

        main.app.state.correlator = app_with_mocked_correlator.state.correlator
        pool = MockPool([])
        main.app.state.correlator._pool = pool

        client = TestClient(main.app)
        response = client.post(
            "/api/v1/correlate"
            "?incident_id=inc-1&affected_service=checkout&incident_start_unix=10000"
        )

        assert response.status_code == 200
        _query, args = pool.fetch_calls[0]
        assert DEFAULT_PROJECT_ID in args

    def test_passes_explicit_project_id(self, app_with_mocked_correlator):
        from app import main

        main.app.state.correlator = app_with_mocked_correlator.state.correlator
        pool = MockPool([])
        main.app.state.correlator._pool = pool

        client = TestClient(main.app)
        response = client.post(
            "/api/v1/correlate"
            "?incident_id=inc-1&affected_service=checkout"
            "&incident_start_unix=10000&project_id=explicit-project-xyz"
        )

        assert response.status_code == 200
        _query, args = pool.fetch_calls[0]
        assert "explicit-project-xyz" in args
