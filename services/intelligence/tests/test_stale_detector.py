"""
Tests for StaleFlagDetector (INT-6).

Before this fix, the detector's own docstring claimed four signals but
only ever queried one (days at 100% rollout); ARCHIVE was recommended
purely off days-since-update with no verification that the flag had no
real code references or recent traffic. These tests exercise the real
call_site_count/recent_evaluation_count signals and the ARCHIVE gate
against mocked Postgres/ast-rewriter/Redis, mirroring the mocking style
already used by test_graph_builder_tenancy.py.
"""

from __future__ import annotations

import json
from types import SimpleNamespace
from unittest.mock import patch

import pytest

from app.stale.detector import StaleFlagDetector, _telemetry_bucket_key


class MockDBPool:
    def __init__(self, rows):
        self._rows = rows

    async def fetch(self, query, *args):
        return self._rows


class MockRedis:
    """Minimal fake of redis.asyncio's client -- only mget is used."""

    def __init__(self, bucket_by_key: dict[str, dict]):
        self._buckets = bucket_by_key

    async def mget(self, keys):
        return [
            json.dumps(self._buckets[k]).encode() if k in self._buckets else None
            for k in keys
        ]


def _row(key="stale-flag", owner="team-a", days_since_update=100.0):
    return {
        "key": key,
        "owner_id": owner,
        "rollout_pct": 100,
        "days_since_update": days_since_update,
        "days_since_creation": days_since_update + 30,
    }


def _detector(
    rows, redis_client=None, ast_rewriter_url=None, repo_path=None
) -> StaleFlagDetector:
    d = StaleFlagDetector(
        db_url="postgresql://unused/unused",
        redis_client=redis_client,
        ast_rewriter_url=ast_rewriter_url,
        repo_path=repo_path,
    )
    d._pool = MockDBPool(rows)  # bypass real asyncpg.create_pool
    return d


class TestArchiveGateRequiresVerifiedZeroReferences:
    @pytest.mark.asyncio
    async def test_archive_requires_call_site_count_known_and_zero(self):
        """days_at_100 > 90 alone must NOT be enough -- ast-rewriter
        unconfigured (call_site_count unknown) must cap below ARCHIVE,
        even for a flag stale far longer than the 90-day threshold."""
        detector = _detector([_row(days_since_update=200.0)])
        result = await detector.detect("project-a")
        assert len(result) == 1
        assert result[0]["call_site_count"] is None
        assert result[0]["recommended_action"] != "ARCHIVE"
        assert result[0]["recommended_action"] == "NOTIFY_OWNER"

    @pytest.mark.asyncio
    async def test_archive_blocked_by_real_nonzero_call_sites(self):
        """A flag with REAL code references, however long stale, must
        never be recommended for ARCHIVE."""
        detector = _detector(
            [_row(days_since_update=200.0)],
            ast_rewriter_url="http://ast:8085",
            repo_path="/repo",
        )

        async def fake_post(self, url, json):  # noqa: A002
            return SimpleNamespace(
                status_code=200,
                json=lambda: {
                    "call_site_count": 3,
                    "call_sites": [],
                    "summary": "3 call sites",
                },
                raise_for_status=lambda: None,
            )

        with patch("app.stale.detector.httpx.AsyncClient.post", fake_post):
            result = await detector.detect("project-a")

        assert result[0]["call_site_count"] == 3
        assert result[0]["recommended_action"] != "ARCHIVE"

    @pytest.mark.asyncio
    async def test_archive_fires_with_verified_zero_references_and_zero_traffic(self):
        """The one case ARCHIVE should actually fire: long-stale, verified
        zero code references, verified zero recent evaluations."""
        detector = _detector(
            [_row(days_since_update=200.0)],
            redis_client=MockRedis({}),
            ast_rewriter_url="http://ast:8085",
            repo_path="/repo",
        )

        async def fake_post(self, url, json):  # noqa: A002
            return SimpleNamespace(
                status_code=200,
                json=lambda: {
                    "call_site_count": 0,
                    "call_sites": [],
                    "summary": "no call sites",
                },
                raise_for_status=lambda: None,
            )

        with patch("app.stale.detector.httpx.AsyncClient.post", fake_post):
            result = await detector.detect("project-a")

        assert result[0]["call_site_count"] == 0
        assert result[0]["recent_evaluation_count"] == 0
        assert result[0]["recommended_action"] == "ARCHIVE"

    @pytest.mark.asyncio
    async def test_archive_blocked_by_real_recent_evaluations_despite_zero_code_refs(
        self,
    ):
        """Zero code references in the scanned repo but nonzero recent
        evaluation traffic is a real contradiction (most likely a scan
        gap, e.g. the wrong repo) -- must not be treated as safe to
        archive just because the static scan came back empty."""
        now_hour = __import__("time").time() // 3600
        bucket_key = _telemetry_bucket_key("stale-flag", "production", int(now_hour))
        detector = _detector(
            [_row(days_since_update=200.0)],
            redis_client=MockRedis({bucket_key: {"total": 50, "errors": 0}}),
            ast_rewriter_url="http://ast:8085",
            repo_path="/repo",
        )

        async def fake_post(self, url, json):  # noqa: A002
            return SimpleNamespace(
                status_code=200,
                json=lambda: {
                    "call_site_count": 0,
                    "call_sites": [],
                    "summary": "no call sites",
                },
                raise_for_status=lambda: None,
            )

        with patch("app.stale.detector.httpx.AsyncClient.post", fake_post):
            result = await detector.detect("project-a")

        assert result[0]["recent_evaluation_count"] == 50
        assert result[0]["recommended_action"] != "ARCHIVE"

    @pytest.mark.asyncio
    async def test_ast_rewriter_failure_yields_unknown_not_zero(self):
        """A real scan error (network failure, 500, etc.) must report
        call_site_count=None, never silently coerce to 0 (which would
        incorrectly unlock ARCHIVE)."""
        detector = _detector(
            [_row(days_since_update=200.0)],
            ast_rewriter_url="http://ast:8085",
            repo_path="/repo",
        )

        async def fake_post_raises(self, url, json):  # noqa: A002
            raise ConnectionError("ast-rewriter unreachable")

        with patch("app.stale.detector.httpx.AsyncClient.post", fake_post_raises):
            result = await detector.detect("project-a")

        assert result[0]["call_site_count"] is None
        assert result[0]["recommended_action"] != "ARCHIVE"

    @pytest.mark.asyncio
    async def test_days_at_100_gates_review_vs_notify_owner(self):
        """Below the 30-day threshold, action stays REVIEW regardless of
        the other signals -- unchanged pre-existing behavior."""
        detector = _detector([_row(days_since_update=15.0)])
        result = await detector.detect("project-a")
        assert result[0]["recommended_action"] == "REVIEW"


class TestRecentEvaluationCount:
    @pytest.mark.asyncio
    async def test_sums_across_the_lookback_window(self):
        now_hour = int(__import__("time").time() // 3600)
        buckets = {
            _telemetry_bucket_key("stale-flag", "production", now_hour): {
                "total": 10,
                "errors": 0,
            },
            _telemetry_bucket_key("stale-flag", "production", now_hour - 1): {
                "total": 20,
                "errors": 1,
            },
        }
        detector = _detector(
            [_row(days_since_update=200.0)], redis_client=MockRedis(buckets)
        )
        result = await detector.detect("project-a")
        assert result[0]["recent_evaluation_count"] == 30

    @pytest.mark.asyncio
    async def test_no_redis_client_yields_zero_not_an_error(self):
        detector = _detector([_row(days_since_update=200.0)], redis_client=None)
        result = await detector.detect("project-a")
        assert result[0]["recent_evaluation_count"] == 0
