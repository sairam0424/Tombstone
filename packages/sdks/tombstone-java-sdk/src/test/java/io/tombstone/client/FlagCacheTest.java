package io.tombstone.client;

import io.tombstone.types.FlagEnvironmentState;
import io.tombstone.types.FlagPrerequisite;
import io.tombstone.types.PropertyCondition;
import io.tombstone.types.TargetingRule;
import org.junit.jupiter.api.Test;

import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.CyclicBarrier;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.TimeUnit;

import static org.junit.jupiter.api.Assertions.*;

/** Regression tests for a real, live bug (SDK-4 investigation): flag-api's
 *  real FlagEvent (services/flag-api/internal/api/v1/flags.go) never
 *  carries prerequisites/targetingRules/targetList/hashVersion, so
 *  FlagCache.applyEvent previously rebuilt the cached entry via
 *  FlagEnvironmentState.simple(...) -- its OWN doc comment says "no
 *  prerequisites/rules/target-list" -- wiping all four to empty/default on
 *  EVERY event for a flag (a kill-switch, a rollback step, literally any
 *  enabled/rollout_pct change), silently disabling prerequisite-gating and
 *  rule-matching client-side until the next full snapshot refetch. The
 *  same bug class already found and fixed in the Python SDK's
 *  client.py _apply_event. */
public class FlagCacheTest {

    @Test
    void applyEventPreservesPrerequisitesTargetingRulesTargetListAndHashVersion() {
        var cache = new FlagCache();
        var rule = new TargetingRule(
            "r1",
            List.of(new PropertyCondition("country", "eq", List.of("US"), false)),
            100.0, "variant-a", 0
        );
        var prereq = new FlagPrerequisite("parent-flag", "true", true);
        var seeded = new FlagEnvironmentState(
            "flag-id", "my-flag", "prod", true, 50, "false", 0L,
            List.of(prereq), List.of(rule), List.of("vip-user"), 2, 0L, 0L
        );
        cache.loadSnapshot(List.of(seeded));

        // A real SSE event as flag-api actually publishes it -- just an
        // enabled/rollout_pct/ts change, nothing else.
        cache.applyEvent("my-flag", true, 75, 12345L);

        var updated = cache.get("my-flag").orElseThrow();
        assertEquals(75, updated.rolloutPct(), "the event's own field must still apply");
        assertEquals(12345L, updated.updatedAt(), "the event's own field must still apply");
        assertEquals(List.of(prereq), updated.prerequisites(), "prerequisites must survive an unrelated event");
        assertEquals(List.of(rule), updated.targetingRules(), "targetingRules must survive an unrelated event");
        assertEquals(List.of("vip-user"), updated.targetList(), "targetList must survive an unrelated event");
        assertEquals(2, updated.hashVersion(), "hashVersion must survive an unrelated event");
    }

    @Test
    void applyEventForAnUnknownFlagIsANoOp() {
        var cache = new FlagCache();
        cache.applyEvent("never-seen", true, 100, 1L);
        assertTrue(cache.get("never-seen").isEmpty());
    }

    // Regression test for the "lost update" race disclosed at the top of
    // FlagCache.java: a plain get()-then-set() read-modify-write lets two
    // concurrent mutations both read the same `current` state, and
    // whichever set() lands second silently discards the OTHER's entire
    // update. Seeds N distinct flags, then fires N threads that each call
    // applyEvent for its OWN distinct flag key all at once (a
    // CyclicBarrier forces every thread to start its get()-modify-set()
    // in the same instant, maximizing real contention on the shared
    // AtomicReference rather than hoping for it). With the old plain
    // get()/set() code this reliably lost several of the N updates every
    // run; with state.updateAndGet's CAS retry loop every single update
    // must survive regardless of interleaving -- this is a hard
    // correctness guarantee, not a probabilistic one, so the assertion
    // below must pass 100% of the time post-fix.
    @Test
    void concurrentApplyEventsOnDistinctKeysNeverLoseAnUpdate() throws InterruptedException {
        var cache = new FlagCache();
        int n = 200;
        var seeded = new ArrayList<FlagEnvironmentState>();
        for (int i = 0; i < n; i++) {
            seeded.add(new FlagEnvironmentState(
                "id-" + i, "flag-" + i, "prod", false, 0, "false", 0L,
                List.of(), List.of(), List.of(), 0, 0L, 0L
            ));
        }
        cache.loadSnapshot(seeded);

        // The barrier's party count (n) must equal the pool's thread count:
        // a CyclicBarrier only releases once EVERY party has called
        // await(), so a smaller pool would deadlock forever waiting for
        // threads that don't exist to ever reach the barrier.
        var barrier = new CyclicBarrier(n);
        ExecutorService pool = Executors.newFixedThreadPool(n);
        try {
            for (int i = 0; i < n; i++) {
                int idx = i;
                pool.submit(() -> {
                    try {
                        barrier.await();
                    } catch (Exception e) {
                        throw new RuntimeException(e);
                    }
                    cache.applyEvent("flag-" + idx, true, 100, 999L);
                });
            }
        } finally {
            pool.shutdown();
            assertTrue(pool.awaitTermination(30, TimeUnit.SECONDS), "all applyEvent calls must finish");
        }

        for (int i = 0; i < n; i++) {
            String key = "flag-" + i;
            var flag = cache.get(key).orElseThrow(
                () -> new AssertionError(key + " went missing from the cache entirely"));
            assertTrue(flag.enabled(), key + "'s concurrent applyEvent was lost -- enabled still false");
            assertEquals(100, flag.rolloutPct(), key + "'s concurrent applyEvent was lost -- rolloutPct unchanged");
        }
    }
}
