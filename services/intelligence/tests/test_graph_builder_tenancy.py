"""
Tenancy-isolation tests for DependencyGraphBuilder (INT-2's security fix).

Before this fix, every query in app/graph/builder.py, plus the three
GET/POST /api/v1/{dependency-graph,graph/*} endpoints in app/main.py and
evaluator's blast_radius.go, had zero project_id scoping -- a TEN-1a-class
cross-tenant data leak: any project's flag-change history, dependency
graph, and blast-radius data was readable by any other project, and (via
rebuild_all's cross-project co-change pairing) two unrelated tenants'
same-timed changes could get incorrectly cross-correlated into one shared
Redis key and edge_map entry.

These tests exercise DependencyGraphBuilder directly against mocked
Postgres/Redis, mirroring the mocking style already used by
test_dependency_graph_endpoint.py and test_critical_flags_endpoint.py.
"""

from __future__ import annotations

import pytest

from app.graph.builder import DEPGRAPH_KEY, DependencyGraphBuilder


class MockRedisPipeline:
    def __init__(self):
        self.zadd_calls: list[tuple[str, str, float]] = []

    def zadd(self, key, mapping, gt=True):
        for member, score in mapping.items():
            self.zadd_calls.append((key, member, score))
        return self

    def expire(self, key, ttl):
        return self

    async def execute(self):
        pass


class MockRedisClient:
    def __init__(self):
        self.pipe = MockRedisPipeline()

    def pipeline(self):
        return self.pipe


class MockDBPool:
    def __init__(self, rows):
        self._rows = rows
        self.fetch_calls: list[tuple[str, tuple]] = []

    async def fetch(self, query, *args):
        self.fetch_calls.append((query, args))
        return self._rows


@pytest.fixture
def builder():
    return DependencyGraphBuilder(db_url="postgresql://unused/unused")


class TestRebuildAllCrossTenantIsolation:
    @pytest.mark.asyncio
    async def test_does_not_pair_two_different_projects_close_in_time(self, builder):
        """
        The core bug: two DIFFERENT flags in two DIFFERENT projects, changed
        10 seconds apart (well inside the 300s coupling window), must NOT be
        recorded as a co-change edge -- pre-fix, project was never checked,
        so this exact scenario produced a cross-tenant edge_map entry and
        Redis writes.
        """
        rows = [
            {
                "flag_key": "checkout-v2",
                "project_id": "project-a",
                "ts": 1000,
                "event_type": "flag_environment_updated",
            },
            {
                "flag_key": "notifications",
                "project_id": "project-b",
                "ts": 1010,
                "event_type": "flag_environment_updated",
            },
        ]
        pool = MockDBPool(rows)
        redis_client = MockRedisClient()

        await builder.rebuild_all(pool, redis_client)

        assert redis_client.pipe.zadd_calls == []

    @pytest.mark.asyncio
    async def test_still_pairs_two_flags_in_the_same_project(self, builder):
        """Sanity check: the same scenario WITHIN one project must still work."""
        rows = [
            {
                "flag_key": "checkout-v2",
                "project_id": "project-a",
                "ts": 1000,
                "event_type": "flag_environment_updated",
            },
            {
                "flag_key": "fraud-check",
                "project_id": "project-a",
                "ts": 1010,
                "event_type": "flag_environment_updated",
            },
        ]
        pool = MockDBPool(rows)
        redis_client = MockRedisClient()

        await builder.rebuild_all(pool, redis_client)

        written_keys = {call[0] for call in redis_client.pipe.zadd_calls}
        assert (
            DEPGRAPH_KEY.format(project_id="project-a", flag_key="checkout-v2")
            in written_keys
        )
        assert (
            DEPGRAPH_KEY.format(project_id="project-a", flag_key="fraud-check")
            in written_keys
        )
        # And never under an unscoped or wrong-project key.
        assert all("project-a" in k or "fraud-check" not in k for k in written_keys)

    @pytest.mark.asyncio
    async def test_three_projects_only_pair_within_their_own_project(self, builder):
        """
        Multiple projects interleaved in time: only same-project pairs get
        an edge. Cross-project pairs (even same flag_key) must not.
        """
        rows = [
            {
                "flag_key": "x",
                "project_id": "p1",
                "ts": 1000,
                "event_type": "flag_created",
            },
            {
                "flag_key": "y",
                "project_id": "p1",
                "ts": 1005,
                "event_type": "flag_created",
            },
            {
                "flag_key": "x",
                "project_id": "p2",
                "ts": 1008,
                "event_type": "flag_created",
            },
            {
                "flag_key": "z",
                "project_id": "p2",
                "ts": 1012,
                "event_type": "flag_created",
            },
        ]
        pool = MockDBPool(rows)
        redis_client = MockRedisClient()

        await builder.rebuild_all(pool, redis_client)

        written_keys = {call[0] for call in redis_client.pipe.zadd_calls}
        assert DEPGRAPH_KEY.format(project_id="p1", flag_key="x") in written_keys
        assert DEPGRAPH_KEY.format(project_id="p1", flag_key="y") in written_keys
        assert DEPGRAPH_KEY.format(project_id="p2", flag_key="x") in written_keys
        assert DEPGRAPH_KEY.format(project_id="p2", flag_key="z") in written_keys
        # p1's "x" paired with p2's "x"/"z" would show up as extra keys beyond
        # the 4 expected above -- there are none.
        assert len(written_keys) == 4

    @pytest.mark.asyncio
    async def test_query_excludes_rows_with_null_project_id(self, builder):
        """
        Legacy pre-TEN-1a-2 audit_log rows with NULL project_id can't be
        safely attributed to any tenant -- rebuild_all's own SQL filters
        them out (matching the "legacy rows are unverifiable, not guessed
        at" precedent already established for AUD-1/TEN-1a-2).
        """
        pool = MockDBPool([])
        redis_client = MockRedisClient()

        await builder.rebuild_all(pool, redis_client)

        query, _args = pool.fetch_calls[0]
        assert "project_id IS NOT NULL" in query


class TestQueryScoping:
    """Every audit_log/flags query builder.py issues must filter by project_id."""

    @pytest.mark.asyncio
    async def test_build_filters_audit_log_and_flags_by_project_id(self, builder):
        pool = MockDBPool([])
        builder._pool = pool

        await builder.build("project-a", "production", 0, 100)

        query, args = pool.fetch_calls[0]
        assert "project_id" in query
        assert "project-a" in args

    @pytest.mark.asyncio
    async def test_get_impact_filters_both_ctes_by_project_id(self, builder):
        pool = MockDBPool([])
        builder._pool = pool

        await builder.get_impact("checkout-v2", "production", "project-a", days=30)

        query, args = pool.fetch_calls[0]
        assert query.count("project_id") >= 2  # flag_changes CTE + nearby CTE
        assert "project-a" in args

    @pytest.mark.asyncio
    async def test_update_on_flag_change_filters_by_project_id(self, builder):
        pool = MockDBPool([])
        builder._pool = pool
        redis_client = MockRedisClient()

        from datetime import datetime, timezone

        await builder.update_on_flag_change(
            "checkout-v2",
            "production",
            datetime.now(timezone.utc),
            redis_client,
            "project-a",
        )

        query, args = pool.fetch_calls[0]
        assert "project_id" in query
        assert "project-a" in args


class TestRedisKeyNamespacing:
    @pytest.mark.asyncio
    async def test_get_impact_fast_uses_project_scoped_key(self, builder):
        class RecordingRedis:
            def __init__(self):
                self.requested_key = None

            async def zrangebyscore(self, key, lo, hi, withscores=True):
                self.requested_key = key
                return []

        redis_client = RecordingRedis()
        await builder.get_impact_fast("checkout-v2", redis_client, "project-a")

        assert redis_client.requested_key == DEPGRAPH_KEY.format(
            project_id="project-a", flag_key="checkout-v2"
        )

    @pytest.mark.asyncio
    async def test_different_projects_get_different_redis_keys_for_the_same_flag(
        self, builder
    ):
        class RecordingRedis:
            def __init__(self):
                self.requested_keys = []

            async def zrangebyscore(self, key, lo, hi, withscores=True):
                self.requested_keys.append(key)
                return []

        redis_client = RecordingRedis()
        await builder.get_impact_fast("checkout-v2", redis_client, "project-a")
        await builder.get_impact_fast("checkout-v2", redis_client, "project-b")

        assert redis_client.requested_keys[0] != redis_client.requested_keys[1]
