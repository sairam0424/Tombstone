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

import uuid

import pytest

from app.graph.builder import DependencyGraphBuilder, depgraph_key


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
    """
    `rows` is either a single response reused for every fetch() call, or a
    list of per-call responses (one per successive fetch(), by call order) --
    needed to test build(), which only issues its second (flags-table) query
    when the first (audit_log) query returns non-empty rows.
    """

    def __init__(self, rows):
        self._responses = rows
        self.fetch_calls: list[tuple[str, tuple]] = []

    async def fetch(self, query, *args):
        self.fetch_calls.append((query, args))
        if self._responses and isinstance(self._responses[0], list):
            index = min(len(self.fetch_calls) - 1, len(self._responses) - 1)
            return self._responses[index]
        return self._responses


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
        assert depgraph_key("project-a", "checkout-v2") in written_keys
        assert depgraph_key("project-a", "fraud-check") in written_keys
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
        assert depgraph_key("p1", "x") in written_keys
        assert depgraph_key("p1", "y") in written_keys
        assert depgraph_key("p2", "x") in written_keys
        assert depgraph_key("p2", "z") in written_keys
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

    @pytest.mark.asyncio
    async def test_rebuild_all_handles_non_str_project_id_from_a_real_db_driver(
        self, builder
    ):
        """
        Regression test: asyncpg decodes a Postgres UUID column into a
        uuid.UUID object, not a str. audit_log.project_id is a UUID column
        (migration 017), so rebuild_all's row-derived project_id is a
        uuid.UUID in production -- MockDBPool's other tests all pass plain
        str project_id values and would never catch depgraph_key() calling
        urllib.parse.quote() directly on a non-str value (raises TypeError,
        silently swallowed by main.py's surrounding try/except, permanently
        no-op'ing every scheduled rebuild for any project with real UUIDs).
        """
        project_uuid = uuid.UUID("11111111-1111-1111-1111-111111111111")
        rows = [
            {
                "flag_key": "checkout-v2",
                "project_id": project_uuid,
                "ts": 1000,
                "event_type": "flag_environment_updated",
            },
            {
                "flag_key": "fraud-check",
                "project_id": project_uuid,
                "ts": 1010,
                "event_type": "flag_environment_updated",
            },
        ]
        pool = MockDBPool(rows)
        redis_client = MockRedisClient()

        await builder.rebuild_all(pool, redis_client)

        written_keys = {call[0] for call in redis_client.pipe.zadd_calls}
        assert depgraph_key(project_uuid, "checkout-v2") in written_keys
        assert depgraph_key(project_uuid, "fraud-check") in written_keys


class TestQueryScoping:
    """Every audit_log/flags query builder.py issues must filter by project_id."""

    @pytest.mark.asyncio
    async def test_build_filters_audit_log_and_flags_by_project_id(self, builder):
        """
        build() only issues its second (flags-table) query when the first
        (audit_log) query returns non-empty rows -- MockDBPool([]) alone
        would make this test never reach that second query at all (a real
        gap found by adversarial review). Uses two distinct per-call
        responses so both queries are actually exercised and asserted on.
        """
        audit_log_rows = [
            {
                "flag_key": "checkout-v2",
                "ts": 1000,
                "event_type": "flag_environment_updated",
            }
        ]
        flags_rows = [
            {
                "key": "checkout-v2",
                "owner_id": "alice",
                "state": "ACTIVE",
                "enabled": True,
                "rollout_pct": 50,
            }
        ]
        pool = MockDBPool([audit_log_rows, flags_rows])
        builder._pool = pool

        graph = await builder.build("project-a", "production", 0, 100)

        assert len(pool.fetch_calls) == 2

        audit_query, audit_args = pool.fetch_calls[0]
        assert "project_id = $" in audit_query
        assert "project-a" in audit_args

        flags_query, flags_args = pool.fetch_calls[1]
        assert "f.project_id = $" in flags_query
        assert "project-a" in flags_args

        assert len(graph.nodes) == 1
        assert graph.nodes[0].flag_key == "checkout-v2"

    @pytest.mark.asyncio
    async def test_get_impact_filters_both_ctes_by_project_id(self, builder):
        pool = MockDBPool([])
        builder._pool = pool

        await builder.get_impact("checkout-v2", "production", "project-a", days=30)

        query, args = pool.fetch_calls[0]
        # A real equality predicate on project_id, not just the word appearing
        # anywhere (e.g. in a comment) -- one in flag_changes, one in nearby.
        assert query.count("project_id = $") >= 2
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
        assert "project_id = $" in query
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

        assert redis_client.requested_key == depgraph_key("project-a", "checkout-v2")

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

    def test_colon_in_flag_key_does_not_collide_with_a_different_project_split(self):
        """
        Regression test for a real collision found by adversarial review:
        DEPGRAPH_KEY used to join project_id and flag_key with a bare ':',
        so project_id="X", flag_key="checkout:new-flow" formatted to the
        SAME string as project_id="X:checkout", flag_key="new-flow" --
        letting a crafted flag_key/project_id pair read another tenant's
        data. depgraph_key() must percent-encode both components so no
        two DIFFERENT (project_id, flag_key) pairs can ever collide.
        """
        key_a = depgraph_key(
            "11111111-1111-1111-1111-111111111111", "checkout:new-flow"
        )
        key_b = depgraph_key(
            "11111111-1111-1111-1111-111111111111:checkout", "new-flow"
        )

        assert key_a != key_b

    def test_depgraph_key_round_trips_a_variety_of_special_characters(self):
        for project_id, flag_key in [
            ("a:b", "c:d"),
            ("a", "b:c:d"),
            ("proj/1", "flag/2"),
            ("proj a", "flag b"),
        ]:
            key = depgraph_key(project_id, flag_key)
            # Every distinct input pair must produce a key that no OTHER
            # input pair in this list also produces.
            others = [
                depgraph_key(p, f)
                for p, f in [
                    ("a:b", "c:d"),
                    ("a", "b:c:d"),
                    ("proj/1", "flag/2"),
                    ("proj a", "flag b"),
                ]
                if (p, f) != (project_id, flag_key)
            ]
            assert key not in others
