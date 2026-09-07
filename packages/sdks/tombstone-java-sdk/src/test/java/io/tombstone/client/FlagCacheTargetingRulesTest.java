package io.tombstone.client;

import io.tombstone.types.FlagEnvironmentState;
import io.tombstone.types.FlagPrerequisite;
import io.tombstone.types.PropertyCondition;
import io.tombstone.types.TargetingRule;
import org.junit.jupiter.api.Test;

import java.util.List;

import static org.junit.jupiter.api.Assertions.*;

/** FlagCache -- direct unit tests for targetingRules-streaming consumption,
 *  mirroring FlagCachePrerequisitesTest.java exactly. Applied here
 *  PROACTIVELY from the start: the &gt;= tie-break, the one-shot provenance
 *  map, and the combined-CacheState/AtomicReference concurrency design were
 *  all discovered incrementally for prerequisites (across PRs #236/#240/
 *  #241, through several rounds of adversarial review) -- this file exists
 *  to prove the SAME lessons hold for targetingRules from the very first
 *  commit, not to rediscover them.
 *
 *  loadSnapshot's own logic always OVERWRITES an incoming flag's
 *  targetingRulesUpdatedAt with the snapshot's own ts (unless preserving a
 *  fresher existing live update) -- so flag()'s own updatedAt value is a
 *  placeholder here; the snapshot ts passed to loadSnapshot is the real
 *  source of truth for these tests. */
public class FlagCacheTargetingRulesTest {

    private static FlagEnvironmentState flag(String key, TargetingRule... rules) {
        return flag(key, 0L, rules);
    }

    private static FlagEnvironmentState flag(String key, long updatedAt, TargetingRule... rules) {
        return new FlagEnvironmentState(
            "id", key, "test", true, 100, "false", updatedAt,
            List.of(), List.of(rules), List.of(), 1, 0L, 0L
        );
    }

    private static TargetingRule rule(String id) {
        return new TargetingRule(
            id,
            List.of(new PropertyCondition("email", "eq", List.of("x@example.com"), false)),
            100.0, "true", 0
        );
    }

    @Test
    void applyTargetingRulesEventRejectsAStaleOlderTsDelivery() {
        var cache = new FlagCache();
        var current = rule("parent-rule");
        cache.loadSnapshot(List.of(flag("child-flag", current)), 5000);

        cache.applyTargetingRulesEvent("child-flag", List.of(rule("stale-rule")), 3000);

        var updated = cache.get("child-flag").orElseThrow();
        assertEquals(List.of(current), updated.targetingRules(),
            "a stale (older-ts) event must not overwrite the newer cached state");
        assertEquals(5000L, updated.targetingRulesUpdatedAt());
    }

    @Test
    void applyTargetingRulesEventWithTsEqualToCachedIsApplied() {
        var cache = new FlagCache();
        var current = rule("parent-rule");
        cache.loadSnapshot(List.of(flag("child-flag", current)), 5000);

        var incoming = rule("new-rule");
        cache.applyTargetingRulesEvent("child-flag", List.of(incoming), 5000);

        var updated = cache.get("child-flag").orElseThrow();
        assertEquals(List.of(incoming), updated.targetingRules(),
            "an equal-ts event must be applied, not dropped as stale");
        assertEquals(5000L, updated.targetingRulesUpdatedAt());
    }

    @Test
    void applyTargetingRulesEventForAnUnknownFlagIsANoOp() {
        var cache = new FlagCache();
        assertDoesNotThrow(() ->
            cache.applyTargetingRulesEvent("never-seen", List.of(), 1L));
        assertTrue(cache.get("never-seen").isEmpty());
    }

    @Test
    void loadSnapshotPreservesAFresherLiveTargetingRulesUpdate() {
        var cache = new FlagCache();
        var oldRule = rule("old-rule");
        cache.loadSnapshot(List.of(flag("child-flag", oldRule)), 1000);

        var newRule = rule("new-rule");
        cache.applyTargetingRulesEvent("child-flag", List.of(newRule), 2000);

        // The slower snapshot now resolves. Its ts=1500 is newer than the
        // cache's LAST SNAPSHOT ts (1000), so it passes the whole-snapshot
        // check -- but it still carries the OLD (pre-live-update) rules.
        cache.loadSnapshot(List.of(flag("child-flag", oldRule)), 1500);

        var updated = cache.get("child-flag").orElseThrow();
        assertEquals(List.of(newRule), updated.targetingRules(),
            "the already-fresher live update must survive a slower, stale-for-this-flag snapshot reload");
        assertEquals(2000L, updated.targetingRulesUpdatedAt(),
            "targetingRulesUpdatedAt must not regress from 2000 back to the snapshot's 1500");
    }

    @Test
    void loadSnapshotAppliesNormallyWhenNewerThanTheLiveUpdatesOwnTs() {
        var cache = new FlagCache();
        var oldRule = rule("old-rule");
        cache.loadSnapshot(List.of(flag("child-flag", oldRule)), 1000);

        var newRule = rule("new-rule");
        cache.applyTargetingRulesEvent("child-flag", List.of(newRule), 2000);

        var evenNewerRule = rule("even-newer-rule");
        cache.loadSnapshot(List.of(flag("child-flag", evenNewerRule)), 3000);

        var updated = cache.get("child-flag").orElseThrow();
        assertEquals(List.of(evenNewerRule), updated.targetingRules());
        assertEquals(3000L, updated.targetingRulesUpdatedAt());
    }

    @Test
    void loadSnapshotPreservesTheLiveUpdateOnAnExactTsTie() {
        var cache = new FlagCache();
        var oldRule = rule("old-rule");
        cache.loadSnapshot(List.of(flag("child-flag", oldRule)), 1000);

        var newRule = rule("new-rule");
        cache.applyTargetingRulesEvent("child-flag", List.of(newRule), 2000);

        // The in-flight snapshot resolves with the EXACT SAME ts as the
        // live update that already applied.
        cache.loadSnapshot(List.of(flag("child-flag", oldRule)), 2000);

        var updated = cache.get("child-flag").orElseThrow();
        assertEquals(List.of(newRule), updated.targetingRules(),
            "a tied ts must not let the snapshot silently overwrite the already-applied live update");
        assertEquals(2000L, updated.targetingRulesUpdatedAt());
    }

    @Test
    void aSecondSnapshotSharingTheExactSameTsAsAFirstSnapshotNoLiveEventStillAppliesItsOwnData() {
        var cache = new FlagCache();
        cache.loadSnapshot(List.of(flag("child-flag", rule("rule-a"))), 5000);

        // A second, completely independent snapshot fetch resolves with the
        // EXACT SAME ts but genuinely different data. No live event at all.
        cache.loadSnapshot(List.of(flag("child-flag", rule("rule-b"))), 5000);

        var updated = cache.get("child-flag").orElseThrow();
        assertEquals(List.of(rule("rule-b")), updated.targetingRules(),
            "a second snapshot's own targetingRules must apply even on a ts tie with the first snapshot, since no live event is involved");
        assertEquals(5000L, updated.targetingRulesUpdatedAt());
    }

    @Test
    void aThirdSnapshotTyingALiveEventsTsAfterASecondTiedSnapshotAlreadyResolvedTheRaceStillAppliesItsOwnData() {
        var cache = new FlagCache();
        cache.loadSnapshot(List.of(flag("child-flag", rule("old-rule"))), 1000);
        cache.applyTargetingRulesEvent("child-flag", List.of(rule("live-rule")), 2000);

        // First tied snapshot after the live event -- the ONE specific race
        // the live event's own protection exists to close. Must preserve.
        cache.loadSnapshot(List.of(flag("child-flag", rule("snap-b-rule"))), 2000);
        assertEquals(List.of(rule("live-rule")),
            cache.get("child-flag").orElseThrow().targetingRules(),
            "the first tied snapshot after the live event must still be blocked");

        // A SECOND, independent snapshot arrives, also tying ts=2000. No new
        // live event raced THIS one -- the protection was already consumed.
        cache.loadSnapshot(List.of(flag("child-flag", rule("snap-c-rule"))), 2000);

        var updated = cache.get("child-flag").orElseThrow();
        assertEquals(List.of(rule("snap-c-rule")), updated.targetingRules(),
            "a second, independent tied snapshot must apply its own data, not be vetoed by a protection already consumed by the first");
        assertEquals(2000L, updated.targetingRulesUpdatedAt());
    }

    @Test
    void applyTargetingRulesEventWithAnEmptyListClearsExistingRules() {
        var cache = new FlagCache();
        cache.loadSnapshot(List.of(flag("child-flag", rule("parent-rule"))), 1000);

        cache.applyTargetingRulesEvent("child-flag", List.of(), 2000);

        var updated = cache.get("child-flag").orElseThrow();
        assertEquals(List.of(), updated.targetingRules());
        assertEquals(2000L, updated.targetingRulesUpdatedAt());
    }

    @Test
    void prerequisitesAndTargetingRulesTieBreakingOperateIndependently_LivePrerequisitesDoesNotProtectTargetingRules() {
        // Found missing (for the analogous TS SDK feature) by adversarial
        // review of PR #246: a live PREREQUISITES event must never mark
        // targetingRules as live-sourced, or an unrelated snapshot tie
        // would incorrectly veto targetingRules' OWN genuinely new data.
        var cache = new FlagCache();
        var prereq = new FlagPrerequisite("old-parent", "true", true);
        cache.loadSnapshot(List.of(new FlagEnvironmentState(
            "id", "child-flag", "test", true, 100, "false", 1000L,
            List.of(prereq), List.of(rule("old-rule")), List.of(), 1, 0L, 0L
        )), 1000);

        // Only a live PREREQUISITES event fires -- targetingRules gets no
        // live event at all.
        var newPrereq = new FlagPrerequisite("new-parent", "true", true);
        cache.applyPrerequisitesEvent("child-flag", List.of(newPrereq), 2000);

        // A snapshot ties the live prerequisites event's ts, carrying stale
        // prerequisites (correctly preserved) but genuinely NEW
        // targetingRules (must NOT be blocked).
        var genuinelyNewRule = rule("genuinely-new-rule");
        cache.loadSnapshot(List.of(new FlagEnvironmentState(
            "id", "child-flag", "test", true, 100, "false", 2000L,
            List.of(prereq), List.of(genuinelyNewRule), List.of(), 1, 0L, 0L
        )), 2000);

        var state = cache.get("child-flag").orElseThrow();
        assertEquals(List.of(newPrereq), state.prerequisites(),
            "the live prerequisites update must still be preserved across the tie");
        assertEquals(List.of(genuinelyNewRule), state.targetingRules(),
            "targetingRules must NOT be blocked by an unrelated live PREREQUISITES event tying the same ts");
    }

    @Test
    void prerequisitesAndTargetingRulesTieBreakingOperateIndependently_LiveTargetingRulesDoesNotProtectPrerequisites() {
        // The mirror image of the test above -- a live TARGETINGRULES event
        // must never mark prerequisites as live-sourced either.
        var cache = new FlagCache();
        var oldRule = rule("old-rule");
        cache.loadSnapshot(List.of(new FlagEnvironmentState(
            "id", "child-flag", "test", true, 100, "false", 1000L,
            List.of(new FlagPrerequisite("old-parent", "true", true)), List.of(oldRule), List.of(), 1, 0L, 0L
        )), 1000);

        // Only a live TARGETINGRULES event fires -- prerequisites gets no
        // live event at all.
        var newRule = rule("new-rule");
        cache.applyTargetingRulesEvent("child-flag", List.of(newRule), 2000);

        // A snapshot ties the live targetingRules event's ts, carrying
        // stale targetingRules (correctly preserved) but genuinely NEW
        // prerequisites (must NOT be blocked).
        var genuinelyNewPrereq = new FlagPrerequisite("genuinely-new-parent", "true", true);
        cache.loadSnapshot(List.of(new FlagEnvironmentState(
            "id", "child-flag", "test", true, 100, "false", 2000L,
            List.of(genuinelyNewPrereq), List.of(oldRule), List.of(), 1, 0L, 0L
        )), 2000);

        var state = cache.get("child-flag").orElseThrow();
        assertEquals(List.of(newRule), state.targetingRules(),
            "the live targetingRules update must still be preserved across the tie");
        assertEquals(List.of(genuinelyNewPrereq), state.prerequisites(),
            "prerequisites must NOT be blocked by an unrelated live TARGETINGRULES event tying the same ts");
    }

    @Test
    void concurrentLoadSnapshotAndApplyTargetingRulesEventDoNotCorruptOrLoseState() throws InterruptedException {
        // Mirrors concurrentLoadSnapshotAndApplyPrerequisitesEventDoNotCorruptOrLoseState
        // exactly -- see that test's own doc comment for the full reasoning.
        // Deliberately does NOT assert a specific winning ts, for the
        // identical reason (the pre-existing, disclosed "lost update" race
        // is not fixed by the combined-CacheState design, only the
        // concurrency TEAR between the two fields is).
        var cache = new FlagCache();
        cache.loadSnapshot(List.of(flag("child-flag", rule("initial-rule"))), 1000);

        var threads = new java.util.ArrayList<Thread>();
        for (int i = 0; i < 20; i++) {
            final int n = i;
            threads.add(new Thread(() -> cache.loadSnapshot(
                List.of(flag("child-flag", rule("snap-rule-" + n))), 2000 + n)));
            threads.add(new Thread(() -> cache.applyTargetingRulesEvent(
                "child-flag", List.of(rule("live-rule-" + n)), 3000 + n)));
        }
        for (Thread t : threads) t.start();
        for (Thread t : threads) t.join();

        var finalState = cache.get("child-flag").orElseThrow();
        assertNotNull(finalState.targetingRules());
        assertEquals(1, finalState.targetingRules().size(),
            "the final targetingRules list must be a single, internally-consistent list from ONE update, never a corrupted mix");
    }
}
