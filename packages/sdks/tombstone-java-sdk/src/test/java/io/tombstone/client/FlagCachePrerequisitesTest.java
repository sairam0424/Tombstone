package io.tombstone.client;

import io.tombstone.types.FlagEnvironmentState;
import io.tombstone.types.FlagPrerequisite;
import org.junit.jupiter.api.Test;

import java.util.List;

import static org.junit.jupiter.api.Assertions.*;

/** FlagCache -- direct unit tests for prerequisites-streaming consumption
 *  (mirrors the TypeScript SDK's cache.test.ts, added after PR #236's own
 *  adversarial review found three related races: a full-snapshot reload
 *  regressing an already-fresher live prerequisites update, two overlapping
 *  reloads applying out of order, and a staleness comparison that must be
 *  pinned down to strict "&lt;" rather than "&lt;="). Applied here proactively
 *  from the start rather than waiting for a review to find the same gaps.
 *
 *  loadSnapshot's own logic always OVERWRITES an incoming flag's
 *  prerequisitesUpdatedAt with the snapshot's own ts (unless preserving a
 *  fresher existing live update) -- so flag()'s own prerequisitesUpdatedAt
 *  value is a placeholder here; the snapshot ts passed to loadSnapshot is
 *  the real source of truth for these tests. */
public class FlagCachePrerequisitesTest {

    private static FlagEnvironmentState flag(String key, FlagPrerequisite... prereqs) {
        return flag(key, 0L, prereqs);
    }

    private static FlagEnvironmentState flag(String key, long updatedAt, FlagPrerequisite... prereqs) {
        return new FlagEnvironmentState(
            "id", key, "test", true, 100, "false", updatedAt,
            List.of(prereqs), List.of(), List.of(), 1, 0L
        );
    }

    @Test
    void applyPrerequisitesEventRejectsAStaleOlderTsDelivery() {
        var cache = new FlagCache();
        var current = new FlagPrerequisite("parent-flag", "true", true);
        cache.loadSnapshot(List.of(flag("child-flag", current)), 5000);

        var stale = new FlagPrerequisite("stale-parent", "true", true);
        cache.applyPrerequisitesEvent("child-flag", List.of(stale), 3000);

        var updated = cache.get("child-flag").orElseThrow();
        assertEquals(List.of(current), updated.prerequisites(),
            "a stale (older-ts) event must not overwrite the newer cached state");
        assertEquals(5000L, updated.prerequisitesUpdatedAt());
    }

    @Test
    void applyPrerequisitesEventWithTsEqualToCachedIsApplied() {
        /** Pins down the strict "&lt;" comparison, not a "&lt;=" regression. A
         *  "clearly older" ts alone (the test above) cannot distinguish the
         *  two -- both correctly reject that input. Only an equal-ts input
         *  tells them apart: "&lt;=" would incorrectly reject this one too. */
        var cache = new FlagCache();
        var current = new FlagPrerequisite("parent-flag", "true", true);
        cache.loadSnapshot(List.of(flag("child-flag", current)), 5000);

        var incoming = new FlagPrerequisite("new-parent", "true", true);
        cache.applyPrerequisitesEvent("child-flag", List.of(incoming), 5000);

        var updated = cache.get("child-flag").orElseThrow();
        assertEquals(List.of(incoming), updated.prerequisites(),
            "an equal-ts event must be applied, not dropped as stale");
        assertEquals(5000L, updated.prerequisitesUpdatedAt());
    }

    @Test
    void applyPrerequisitesEventForAnUnknownFlagIsANoOp() {
        var cache = new FlagCache();
        assertDoesNotThrow(() ->
            cache.applyPrerequisitesEvent("never-seen", List.of(), 1L));
        assertTrue(cache.get("never-seen").isEmpty());
    }

    @Test
    void loadSnapshotRejectsAnIncomingSnapshotOlderThanTheLastOneApplied() {
        /** Simulates two overlapping fetchSnapshot() calls (e.g. a
         *  lag-triggered refetch racing a reconnect-triggered one) resolving
         *  out of order: the slower one has an OLDER ts but resolves SECOND.
         *
         *  Asserts updatedAt, NOT prerequisitesUpdatedAt: the per-flag
         *  prerequisites-preservation fix (tested separately below) would
         *  incidentally ALSO protect prerequisitesUpdatedAt here (since the
         *  existing entry's value already exceeds the incoming snapshot's
         *  ts), which would let this test pass even with the GLOBAL
         *  monotonicity guard removed -- confirmed empirically via
         *  revert-confirms-fails, which is why updatedAt (a field the
         *  per-flag logic never touches) is what actually isolates this
         *  fix. */
        var cache = new FlagCache();
        cache.loadSnapshot(List.of(flag("child-flag", 111L)), 5000);
        cache.loadSnapshot(List.of(flag("child-flag", 999L)), 3000); // older -- rejected wholesale

        assertEquals(111L, cache.get("child-flag").orElseThrow().updatedAt(),
            "an older snapshot must not overwrite already-applied newer data, even for a field unrelated to prerequisites");
    }

    @Test
    void loadSnapshotStillAppliesAGenuinelyNewerSnapshotAfterAnOlderOneWasRejected() {
        var cache = new FlagCache();
        cache.loadSnapshot(List.of(flag("child-flag", 111L)), 5000);
        cache.loadSnapshot(List.of(flag("child-flag", 999L)), 3000); // rejected
        cache.loadSnapshot(List.of(flag("child-flag", 777L)), 7000); // genuinely newer -- applies

        assertEquals(777L, cache.get("child-flag").orElseThrow().updatedAt());
    }

    @Test
    void loadSnapshotPreservesAFresherLivePrerequisitesUpdate() {
        /** flag-api's snapshot ts is just time.Now() at response generation,
         *  not a per-flag "last changed" value -- so even a snapshot that
         *  passes the whole-snapshot monotonicity check can still carry
         *  STALE prerequisite data for one specific flag, if that flag's
         *  prerequisites were advanced by a live event that arrived while
         *  this snapshot's own (slower) fetch was still in flight. */
        var cache = new FlagCache();
        var oldParent = new FlagPrerequisite("old-parent", "true", true);
        cache.loadSnapshot(List.of(flag("child-flag", oldParent)), 1000);

        var newParent = new FlagPrerequisite("new-parent", "true", true);
        cache.applyPrerequisitesEvent("child-flag", List.of(newParent), 2000);

        // The slower snapshot now resolves. Its ts=1500 is newer than the
        // cache's LAST SNAPSHOT ts (1000), so it passes the whole-snapshot
        // check -- but it still carries the OLD (pre-live-update)
        // prerequisites for this flag.
        cache.loadSnapshot(List.of(flag("child-flag", oldParent)), 1500);

        var updated = cache.get("child-flag").orElseThrow();
        assertEquals(List.of(newParent), updated.prerequisites(),
            "the already-fresher live update must survive a slower, stale-for-this-flag snapshot reload");
        assertEquals(2000L, updated.prerequisitesUpdatedAt(),
            "prerequisitesUpdatedAt must not regress from 2000 back to the snapshot's 1500");
    }

    @Test
    void loadSnapshotAppliesNormallyWhenNewerThanTheLiveUpdatesOwnTs() {
        var cache = new FlagCache();
        var oldParent = new FlagPrerequisite("old-parent", "true", true);
        cache.loadSnapshot(List.of(flag("child-flag", oldParent)), 1000);

        var newParent = new FlagPrerequisite("new-parent", "true", true);
        cache.applyPrerequisitesEvent("child-flag", List.of(newParent), 2000);

        var evenNewerParent = new FlagPrerequisite("even-newer-parent", "true", true);
        cache.loadSnapshot(List.of(flag("child-flag", evenNewerParent)), 3000);

        var updated = cache.get("child-flag").orElseThrow();
        assertEquals(List.of(evenNewerParent), updated.prerequisites());
        assertEquals(3000L, updated.prerequisitesUpdatedAt());
    }

    @Test
    void loadSnapshotPreservesTheLiveUpdateOnAnExactTsTie() {
        /** flag-api's snapshot endpoint (environments.go: Ts: time.Now().Unix())
         *  and its prerequisites-event publisher (prerequisites.go: ts :=
         *  time.Now().Unix()) both use the SAME 1-second-resolution wall
         *  clock, so a live event and a racing/in-flight snapshot fetch that
         *  land in the same wall-clock second get an IDENTICAL ts.
         *  applyPrerequisitesEvent's own staleness guard uses strict "&lt;",
         *  meaning it treats an equal ts as "fresh enough to apply" -- this
         *  preservation check must treat the SAME tie as "fresh enough to
         *  keep" (i.e. use "&gt;=", not "&gt;"), or the snapshot silently
         *  overwrites the just-applied live update purely because of a tie.
         *  Found by adversarial review of the Ruby SDK's identical bug,
         *  PR #238. */
        var cache = new FlagCache();
        var oldParent = new FlagPrerequisite("old-parent", "true", true);
        cache.loadSnapshot(List.of(flag("child-flag", oldParent)), 1000);

        var newParent = new FlagPrerequisite("new-parent", "true", true);
        cache.applyPrerequisitesEvent("child-flag", List.of(newParent), 2000);

        // The in-flight snapshot resolves with the EXACT SAME ts as the
        // live update that already applied.
        cache.loadSnapshot(List.of(flag("child-flag", oldParent)), 2000);

        var updated = cache.get("child-flag").orElseThrow();
        assertEquals(List.of(newParent), updated.prerequisites(),
            "a tied ts must not let the snapshot silently overwrite the already-applied live update");
        assertEquals(2000L, updated.prerequisitesUpdatedAt());
    }

    @Test
    void aSecondSnapshotSharingTheExactSameTsAsAFirstSnapshotNoLiveEventStillAppliesItsOwnData() {
        /** Regression test for a real bug the &gt;= tie-break above introduced
         *  in its own first draft (found by adversarial review of that fix):
         *  with only a ts comparison, this cache cannot tell "existing.
         *  prerequisitesUpdatedAt came from a live event that must win a
         *  tie" from "existing.prerequisitesUpdatedAt came from a PRIOR
         *  SNAPSHOT LOAD that merely happens to share flag-api's coarse
         *  1-second-resolution ts with a SECOND, later snapshot". Without
         *  prerequisitesFromLiveEvent tracking, this second snapshot's
         *  genuinely different prerequisites would be silently discarded. */
        var cache = new FlagCache();
        cache.loadSnapshot(List.of(flag("child-flag", new FlagPrerequisite("parent-a", "true", true))), 5000);

        // A second, completely independent snapshot fetch resolves with the
        // EXACT SAME ts but genuinely different data. No live event at all.
        cache.loadSnapshot(List.of(flag("child-flag", new FlagPrerequisite("parent-b", "true", true))), 5000);

        var updated = cache.get("child-flag").orElseThrow();
        assertEquals(List.of(new FlagPrerequisite("parent-b", "true", true)), updated.prerequisites(),
            "a second snapshot's own prerequisites must apply even on a ts tie with the first snapshot, since no live event is involved");
        assertEquals(5000L, updated.prerequisitesUpdatedAt());
    }

    @Test
    void aThirdSnapshotTyingALiveEventsTsAfterASecondTiedSnapshotAlreadyResolvedTheRaceStillAppliesItsOwnData() {
        /** Regression test found by a SECOND round of adversarial review of
         *  this fix's own first draft: prerequisitesFromLiveEvent was being
         *  re-set to true every time it was used to preserve a live event
         *  across a tied snapshot, making the protection "sticky" -- EVERY
         *  subsequent snapshot at or before that ts would ALSO get vetoed,
         *  not just the one snapshot that legitimately raced the live
         *  event. The protection must be ONE-SHOT: consumed the first time
         *  a loadSnapshot call resolves the race, so an independent, LATER
         *  snapshot that merely happens to tie the same coarse-resolution
         *  ts is trusted normally. */
        var cache = new FlagCache();
        cache.loadSnapshot(List.of(flag("child-flag", new FlagPrerequisite("old-parent", "true", true))), 1000);
        cache.applyPrerequisitesEvent("child-flag", List.of(new FlagPrerequisite("live-parent", "true", true)), 2000);

        // First tied snapshot after the live event -- the ONE specific race
        // the live event's own protection exists to close. Must preserve.
        cache.loadSnapshot(List.of(flag("child-flag", new FlagPrerequisite("snap-b-parent", "true", true))), 2000);
        assertEquals(List.of(new FlagPrerequisite("live-parent", "true", true)),
            cache.get("child-flag").orElseThrow().prerequisites(),
            "the first tied snapshot after the live event must still be blocked");

        // A SECOND, independent snapshot arrives, also tying ts=2000. No new
        // live event raced THIS one -- the protection was already consumed.
        cache.loadSnapshot(List.of(flag("child-flag", new FlagPrerequisite("snap-c-parent", "true", true))), 2000);

        var updated = cache.get("child-flag").orElseThrow();
        assertEquals(List.of(new FlagPrerequisite("snap-c-parent", "true", true)), updated.prerequisites(),
            "a second, independent tied snapshot must apply its own data, not be vetoed by a protection already consumed by the first");
        assertEquals(2000L, updated.prerequisitesUpdatedAt());
    }

    @Test
    void applyPrerequisitesEventWithAnEmptyListClearsExistingPrerequisites() {
        /** applyPrerequisitesEvent is documented as a full replacement, not a
         *  delta -- an empty incoming list must actually clear a flag's
         *  existing (non-empty) gates, not be mistaken for "nothing to apply". */
        var cache = new FlagCache();
        cache.loadSnapshot(List.of(flag("child-flag", new FlagPrerequisite("parent-flag", "true", true))), 1000);

        cache.applyPrerequisitesEvent("child-flag", List.of(), 2000);

        var updated = cache.get("child-flag").orElseThrow();
        assertEquals(List.of(), updated.prerequisites());
        assertEquals(2000L, updated.prerequisitesUpdatedAt());
    }

    @Test
    void loadSnapshotGlobalMonotonicityGuardAppliesOnAnExactTsTie() {
        /** Pins down that the monotonicity guard (snapshotTs &lt; lastSnapshotTs)
         *  rejects only STRICTLY older snapshots -- a retried/duplicate fetch
         *  arriving with the exact same ts as the last applied snapshot must
         *  still be accepted (idempotent re-apply), not silently dropped by an
         *  overly-strict "&lt;=" regression. Found by adversarial review of this
         *  PR: the analogous "&lt;" vs "&lt;=" boundary IS tested for
         *  applyPrerequisitesEvent's per-flag staleness check, but was missing
         *  here for loadSnapshot's own global guard. */
        var cache = new FlagCache();
        cache.loadSnapshot(List.of(flag("child-flag", 111L)), 5000);
        cache.loadSnapshot(List.of(flag("child-flag", 222L)), 5000); // exact tie -- must still apply

        assertEquals(222L, cache.get("child-flag").orElseThrow().updatedAt());
    }

    @Test
    void concurrentLoadSnapshotAndApplyPrerequisitesEventDoNotCorruptOrLoseState() throws InterruptedException {
        /** Found by adversarial review of this fix's own first draft: the
         *  cache map and the prerequisitesFromLiveEvent map used to be two
         *  SEPARATE fields, updated by separate statements -- a real JVM
         *  thread (e.g. the SSE listener calling applyPrerequisitesEvent
         *  concurrently with a lag-recovery loadSnapshot, exactly the
         *  scenario this class's own top-of-file comment names) could
         *  observe the two fields in an INCONSISTENT combination, silently
         *  defeating the tie-break protection itself. Fixed by combining
         *  both into one CacheState swapped via a single AtomicReference,
         *  so any read is always mutually consistent. This test backs that
         *  guarantee with a real concurrent-thread run (mirroring the Ruby
         *  SDK's own "concurrent access" spec), asserting no exceptions and
         *  a fully-formed final state -- not a proof of the specific race
         *  (which is now structurally impossible, not merely improbable),
         *  but a smoke test against any future regression that reintroduces
         *  a second independently-updated field.
         *
         *  Deliberately does NOT assert a specific winning ts: the
         *  pre-existing, disclosed "lost update" race (see this class's own
         *  top-of-file comment) means the LAST state.set() to land wins
         *  outright regardless of which ts it carries, so under adversarial
         *  scheduling the final prerequisitesUpdatedAt could legitimately be
         *  any of the values raced here -- that risk is accepted/deferred,
         *  not what this test exists to catch. */
        var cache = new FlagCache();
        cache.loadSnapshot(List.of(flag("child-flag", new FlagPrerequisite("initial-parent", "true", true))), 1000);

        var threads = new java.util.ArrayList<Thread>();
        for (int i = 0; i < 20; i++) {
            final int n = i;
            threads.add(new Thread(() -> cache.loadSnapshot(
                List.of(flag("child-flag", new FlagPrerequisite("snap-parent-" + n, "true", true))), 2000 + n)));
            threads.add(new Thread(() -> cache.applyPrerequisitesEvent(
                "child-flag", List.of(new FlagPrerequisite("live-parent-" + n, "true", true)), 3000 + n)));
        }
        for (Thread t : threads) t.start();
        for (Thread t : threads) t.join();

        var finalState = cache.get("child-flag").orElseThrow();
        assertNotNull(finalState.prerequisites());
        assertEquals(1, finalState.prerequisites().size(),
            "the final prerequisites list must be a single, internally-consistent list from ONE update, never a corrupted mix");
    }
}
