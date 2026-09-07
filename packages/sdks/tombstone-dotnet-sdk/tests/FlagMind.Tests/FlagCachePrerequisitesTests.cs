namespace Tombstone.Tests;
using Xunit;

/// <summary>
/// FlagCache -- direct unit tests for prerequisites-streaming consumption
/// (mirrors the TypeScript/Java/Ruby SDKs' equivalent test files, added
/// after PR #236's own adversarial review found three related races in
/// the TypeScript port: a full-snapshot reload regressing an already-
/// fresher live prerequisites update, two overlapping reloads applying out
/// of order, and a staleness comparison that must be pinned down to strict
/// "&lt;" rather than "&lt;="). Applied here proactively from the start.
///
/// LoadSnapshot always OVERWRITES an incoming flag's PrerequisitesUpdatedAt
/// with the snapshot's own ts (unless preserving a fresher existing live
/// update), so Flag()'s own prerequisitesUpdatedAt argument is a
/// placeholder in every test below; the snapshotTs passed to LoadSnapshot
/// is the real source of truth.
/// </summary>
public class FlagCachePrerequisitesTests
{
    private static FlagEnvironmentState Flag(string key, long updatedAt = 0, List<FlagPrerequisite>? prerequisites = null) =>
        new("id", key, "test", true, 100, "false", updatedAt, prerequisites ?? new());

    private static FlagPrerequisite Prereq(string flagKey) => new(flagKey, "true", true);

    [Fact]
    public void ApplyPrerequisitesEvent_RejectsAStaleOlderTsDelivery()
    {
        var cache = new FlagCache();
        var current = Prereq("parent-flag");
        cache.LoadSnapshot(new[] { Flag("child-flag", prerequisites: new() { current }) }, 5000);

        cache.ApplyPrerequisitesEvent("child-flag", new() { Prereq("stale-parent") }, 3000);

        var updated = cache.Get("child-flag")!;
        Assert.Equal(new List<FlagPrerequisite> { current }, updated.Prerequisites);
        Assert.Equal(5000L, updated.PrerequisitesUpdatedAt);
    }

    [Fact]
    public void ApplyPrerequisitesEvent_WithTsEqualToCached_IsApplied()
    {
        // Pins down the strict "<" comparison, not a "<=" regression. A
        // "clearly older" ts alone (the test above) cannot distinguish the
        // two -- both correctly reject that input. Only an equal-ts input
        // tells them apart -- "<=" would incorrectly reject this one too.
        var cache = new FlagCache();
        var current = Prereq("parent-flag");
        cache.LoadSnapshot(new[] { Flag("child-flag", prerequisites: new() { current }) }, 5000);

        var incoming = Prereq("new-parent");
        cache.ApplyPrerequisitesEvent("child-flag", new() { incoming }, 5000);

        var updated = cache.Get("child-flag")!;
        Assert.Equal(new List<FlagPrerequisite> { incoming }, updated.Prerequisites);
        Assert.Equal(5000L, updated.PrerequisitesUpdatedAt);
    }

    [Fact]
    public void ApplyPrerequisitesEvent_ForAnUnknownFlag_IsANoOp()
    {
        var cache = new FlagCache();
        cache.ApplyPrerequisitesEvent("never-seen", new(), 1);
        Assert.Null(cache.Get("never-seen"));
    }

    [Fact]
    public void LoadSnapshot_RejectsAnIncomingSnapshotOlderThanTheLastOneApplied()
    {
        // Simulates two overlapping FetchSnapshotAsync calls (e.g. a
        // lag-triggered refetch racing a reconnect-triggered one) resolving
        // out of order: the slower one has an OLDER ts but resolves SECOND.
        //
        // Asserts UpdatedAt, NOT PrerequisitesUpdatedAt: the per-flag
        // prerequisites-preservation logic (tested separately below) would
        // incidentally ALSO protect PrerequisitesUpdatedAt here, which
        // would let this test pass even with the GLOBAL monotonicity guard
        // removed -- this exact pitfall was found (and fixed) while writing
        // the Java SDK's equivalent test. UpdatedAt is a field the per-flag
        // logic never touches, so it isolates this guard specifically.
        var cache = new FlagCache();
        cache.LoadSnapshot(new[] { Flag("child-flag", updatedAt: 111) }, 5000);
        cache.LoadSnapshot(new[] { Flag("child-flag", updatedAt: 999) }, 3000); // older -- rejected wholesale

        Assert.Equal(111L, cache.Get("child-flag")!.UpdatedAt);
    }

    [Fact]
    public void LoadSnapshot_StillAppliesAGenuinelyNewerSnapshotAfterAnOlderOneWasRejected()
    {
        var cache = new FlagCache();
        cache.LoadSnapshot(new[] { Flag("child-flag", updatedAt: 111) }, 5000);
        cache.LoadSnapshot(new[] { Flag("child-flag", updatedAt: 999) }, 3000); // rejected
        cache.LoadSnapshot(new[] { Flag("child-flag", updatedAt: 777) }, 7000); // genuinely newer -- applies

        Assert.Equal(777L, cache.Get("child-flag")!.UpdatedAt);
    }

    [Fact]
    public void LoadSnapshot_VeryFirstCall_AlwaysAppliesRegardlessOfTs()
    {
        var cache = new FlagCache();
        cache.LoadSnapshot(new[] { Flag("child-flag", updatedAt: 1) }, 1);
        Assert.Equal(1L, cache.Get("child-flag")!.UpdatedAt);
    }

    [Fact]
    public void LoadSnapshot_PreservesAFresherLivePrerequisitesUpdate()
    {
        // flag-api's snapshot ts is just time.Now().Unix() at response
        // generation, not a per-flag "last changed" value -- so even a
        // snapshot that passes the whole-snapshot monotonicity check can
        // still carry STALE prerequisite data for one specific flag, if
        // that flag's prerequisites were advanced by a live event that
        // arrived while this snapshot's own (slower) fetch was in flight.
        var cache = new FlagCache();
        var oldParent = Prereq("old-parent");
        cache.LoadSnapshot(new[] { Flag("child-flag", prerequisites: new() { oldParent }) }, 1000);

        var newParent = Prereq("new-parent");
        cache.ApplyPrerequisitesEvent("child-flag", new() { newParent }, 2000);

        // The slower snapshot now resolves. Its ts=1500 is newer than the
        // cache's LAST SNAPSHOT ts (1000), so it passes the whole-snapshot
        // check -- but it still carries the OLD (pre-live-update)
        // prerequisites for this flag.
        cache.LoadSnapshot(new[] { Flag("child-flag", prerequisites: new() { oldParent }) }, 1500);

        var updated = cache.Get("child-flag")!;
        Assert.Equal(new List<FlagPrerequisite> { newParent }, updated.Prerequisites);
        Assert.Equal(2000L, updated.PrerequisitesUpdatedAt);
    }

    [Fact]
    public void LoadSnapshot_AppliesNormallyWhenNewerThanTheLiveUpdatesOwnTs()
    {
        var cache = new FlagCache();
        var oldParent = Prereq("old-parent");
        cache.LoadSnapshot(new[] { Flag("child-flag", prerequisites: new() { oldParent }) }, 1000);

        var newParent = Prereq("new-parent");
        cache.ApplyPrerequisitesEvent("child-flag", new() { newParent }, 2000);

        var evenNewerParent = Prereq("even-newer-parent");
        cache.LoadSnapshot(new[] { Flag("child-flag", prerequisites: new() { evenNewerParent }) }, 3000);

        var updated = cache.Get("child-flag")!;
        Assert.Equal(new List<FlagPrerequisite> { evenNewerParent }, updated.Prerequisites);
        Assert.Equal(3000L, updated.PrerequisitesUpdatedAt);
    }

    [Fact]
    public void LoadSnapshot_PreservesTheLiveUpdateOnAnExactTsTie()
    {
        // flag-api's snapshot endpoint (environments.go: Ts: time.Now().Unix())
        // and its prerequisites-event publisher (prerequisites.go: ts :=
        // time.Now().Unix()) both use the SAME 1-second-resolution wall
        // clock, so a live event and a racing/in-flight snapshot fetch that
        // land in the same wall-clock second get an IDENTICAL ts.
        // ApplyPrerequisitesEvent's own staleness guard uses strict "<",
        // meaning it treats an equal ts as "fresh enough to apply" -- this
        // preservation check must treat the SAME tie as "fresh enough to
        // keep" (i.e. use ">=", not ">"), or the snapshot silently overwrites
        // the just-applied live update purely because of a tie.
        var cache = new FlagCache();
        var oldParent = Prereq("old-parent");
        cache.LoadSnapshot(new[] { Flag("child-flag", prerequisites: new() { oldParent }) }, 1000);

        var newParent = Prereq("new-parent");
        cache.ApplyPrerequisitesEvent("child-flag", new() { newParent }, 2000);

        // The in-flight snapshot resolves with the EXACT SAME ts as the
        // live update that already applied.
        cache.LoadSnapshot(new[] { Flag("child-flag", prerequisites: new() { oldParent }) }, 2000);

        // A tied ts must not let the snapshot silently overwrite the
        // already-applied live update.
        var updated = cache.Get("child-flag")!;
        Assert.Equal(new List<FlagPrerequisite> { newParent }, updated.Prerequisites);
        Assert.Equal(2000L, updated.PrerequisitesUpdatedAt);
    }

    [Fact]
    public void LoadSnapshot_SecondSnapshotSharingTheExactSameTsAsFirst_NoLiveEvent_StillAppliesItsOwnData()
    {
        // Regression test for a real bug the >= tie-break above introduced
        // in its own first draft: with only a ts comparison, this cache
        // cannot tell "existing.PrerequisitesUpdatedAt came from a live
        // event that must win a tie" from "existing.PrerequisitesUpdatedAt
        // came from a PRIOR SNAPSHOT LOAD that merely happens to share
        // flag-api's coarse 1-second-resolution ts with a SECOND, later
        // snapshot". Without _prerequisitesFromLiveEvent tracking, this
        // second snapshot's genuinely different prerequisites would be
        // silently discarded.
        var cache = new FlagCache();
        cache.LoadSnapshot(new[] { Flag("child-flag", prerequisites: new() { Prereq("parent-a") }) }, 5000);

        // A second, completely independent snapshot fetch resolves with the
        // EXACT SAME ts but genuinely different data. No live event at all.
        cache.LoadSnapshot(new[] { Flag("child-flag", prerequisites: new() { Prereq("parent-b") }) }, 5000);

        var updated = cache.Get("child-flag")!;
        Assert.Equal(new List<FlagPrerequisite> { Prereq("parent-b") }, updated.Prerequisites);
        Assert.Equal(5000L, updated.PrerequisitesUpdatedAt);
    }

    [Fact]
    public void LoadSnapshot_ThirdSnapshotTyingALiveEventsTs_AfterASecondTiedSnapshotAlreadyResolvedTheRace_StillAppliesItsOwnData()
    {
        // Regression test found by a SECOND round of adversarial review of
        // this fix's own first draft: _prerequisitesFromLiveEvent was being
        // re-set to true every time it was used to preserve a live event
        // across a tied snapshot, making the protection "sticky" -- EVERY
        // subsequent snapshot at or before that ts would ALSO get vetoed,
        // not just the one snapshot that legitimately raced the live event.
        // The protection must be ONE-SHOT.
        var cache = new FlagCache();
        cache.LoadSnapshot(new[] { Flag("child-flag", prerequisites: new() { Prereq("old-parent") }) }, 1000);
        cache.ApplyPrerequisitesEvent("child-flag", new() { Prereq("live-parent") }, 2000);

        // First tied snapshot after the live event -- the ONE specific race
        // the live event's own protection exists to close. Must preserve.
        cache.LoadSnapshot(new[] { Flag("child-flag", prerequisites: new() { Prereq("snap-b-parent") }) }, 2000);
        Assert.Equal(new List<FlagPrerequisite> { Prereq("live-parent") }, cache.Get("child-flag")!.Prerequisites);

        // A SECOND, independent snapshot arrives, also tying ts=2000. No new
        // live event raced THIS one -- the protection was already consumed.
        cache.LoadSnapshot(new[] { Flag("child-flag", prerequisites: new() { Prereq("snap-c-parent") }) }, 2000);

        var updated = cache.Get("child-flag")!;
        Assert.Equal(new List<FlagPrerequisite> { Prereq("snap-c-parent") }, updated.Prerequisites);
        Assert.Equal(2000L, updated.PrerequisitesUpdatedAt);
    }

    [Fact]
    public void ApplyPrerequisitesEvent_WithAnEmptyList_ClearsExistingPrerequisites()
    {
        // ApplyPrerequisitesEvent is documented as a full replacement, not a
        // delta -- an empty incoming list must actually clear a flag's
        // existing (non-empty) gates, not be mistaken for "nothing to apply".
        var cache = new FlagCache();
        cache.LoadSnapshot(new[] { Flag("child-flag", prerequisites: new() { Prereq("parent-flag") }) }, 1000);

        cache.ApplyPrerequisitesEvent("child-flag", new(), 2000);

        var updated = cache.Get("child-flag")!;
        Assert.Empty(updated.Prerequisites);
        Assert.Equal(2000L, updated.PrerequisitesUpdatedAt);
    }

    [Fact]
    public void LoadSnapshot_GlobalMonotonicityGuard_AppliesOnAnExactTsTie()
    {
        // Pins down that the monotonicity guard (snapshotTs < _lastSnapshotTs)
        // rejects only STRICTLY older snapshots -- a retried/duplicate fetch
        // arriving with the exact same ts as the last applied snapshot must
        // still be accepted (idempotent re-apply), not silently dropped by an
        // overly-strict "<=" regression.
        var cache = new FlagCache();
        cache.LoadSnapshot(new[] { Flag("child-flag", updatedAt: 111) }, 5000);
        cache.LoadSnapshot(new[] { Flag("child-flag", updatedAt: 222) }, 5000); // exact tie -- must still apply

        Assert.Equal(222L, cache.Get("child-flag")!.UpdatedAt);
    }
}
