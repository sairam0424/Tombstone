namespace Tombstone.Tests;
using Xunit;

/// <summary>
/// FlagCache -- direct unit tests for targeting_rules-streaming consumption,
/// mirroring FlagCachePrerequisitesTests.cs exactly. Applied here
/// PROACTIVELY from the start: the &gt;= tie-break, the one-shot live-
/// provenance map, and the cross-feature independence isolation technique
/// were all discovered incrementally for prerequisites (and, for the
/// independence tests specifically, only correctly isolated after a SECOND
/// round of adversarial review of the Java SDK's PR #247) -- this file
/// exists to prove the SAME lessons hold for targeting_rules from the very
/// first commit, not to rediscover them.
///
/// LoadSnapshot's own logic always OVERWRITES an incoming flag's
/// TargetingRulesUpdatedAt with the snapshot's own ts (unless preserving a
/// fresher existing live update) -- so Flag()'s own updatedAt argument is a
/// placeholder in every test below; the snapshotTs passed to LoadSnapshot is
/// the real source of truth.
/// </summary>
public class FlagCacheTargetingRulesTests
{
    private static FlagEnvironmentState Flag(
        string key, long updatedAt = 0,
        List<TargetingRule>? targetingRules = null, List<FlagPrerequisite>? prerequisites = null) =>
        new("id", key, "test", true, 100, "false", updatedAt,
            prerequisites ?? new(), targetingRules ?? new());

    private static TargetingRule Rule(string id) => new(
        id, new List<PropertyCondition> { new("email", "eq", new List<string> { "x@example.com" }, false) },
        100.0, "true", 0);

    private static FlagPrerequisite Prereq(string flagKey) => new(flagKey, "true", true);

    // TargetingRule is a record whose Conditions property is a
    // List<PropertyCondition> -- unlike Java's List.equals() or Ruby's
    // Array#==, C#'s List<T> has NO structural equality (it falls back to
    // reference equality), so a record's auto-generated Equals is silently
    // broken for any property typed List<T>: two separately-constructed
    // TargetingRule instances with byte-identical content compare as
    // UNEQUAL. Comparing by Id (a plain string) sidesteps this entirely --
    // mirrors how RuleMatcher's own tests already compare MatchRules'
    // string return value, never a whole TargetingRule object. Found via
    // this PR's own CI run: two tests that built the "expected" rule via a
    // SEPARATE Rule(...) call (not reusing the same object reference)
    // failed with visually-identical Expected/Actual output.
    private static List<string> Ids(IEnumerable<TargetingRule> rules) => rules.Select(r => r.Id).ToList();

    [Fact]
    public void ApplyTargetingRulesEvent_RejectsAStaleOlderTsDelivery()
    {
        var cache = new FlagCache();
        var current = Rule("parent-rule");
        cache.LoadSnapshot(new[] { Flag("child-flag", targetingRules: new() { current }) }, 5000);

        cache.ApplyTargetingRulesEvent("child-flag", new() { Rule("stale-rule") }, 3000);

        var updated = cache.Get("child-flag")!;
        Assert.Equal(new List<string> { "parent-rule" }, Ids(updated.TargetingRules));
        Assert.Equal(5000L, updated.TargetingRulesUpdatedAt);
    }

    [Fact]
    public void ApplyTargetingRulesEvent_WithTsEqualToCached_IsApplied()
    {
        var cache = new FlagCache();
        var current = Rule("parent-rule");
        cache.LoadSnapshot(new[] { Flag("child-flag", targetingRules: new() { current }) }, 5000);

        var incoming = Rule("new-rule");
        cache.ApplyTargetingRulesEvent("child-flag", new() { incoming }, 5000);

        var updated = cache.Get("child-flag")!;
        Assert.Equal(new List<string> { "new-rule" }, Ids(updated.TargetingRules));
        Assert.Equal(5000L, updated.TargetingRulesUpdatedAt);
    }

    [Fact]
    public void ApplyTargetingRulesEvent_ForAnUnknownFlag_IsANoOp()
    {
        var cache = new FlagCache();
        cache.ApplyTargetingRulesEvent("never-seen", new(), 1);
        Assert.Null(cache.Get("never-seen"));
    }

    [Fact]
    public void LoadSnapshot_PreservesAFresherLiveTargetingRulesUpdate()
    {
        var cache = new FlagCache();
        var oldRule = Rule("old-rule");
        cache.LoadSnapshot(new[] { Flag("child-flag", targetingRules: new() { oldRule }) }, 1000);

        var newRule = Rule("new-rule");
        cache.ApplyTargetingRulesEvent("child-flag", new() { newRule }, 2000);

        cache.LoadSnapshot(new[] { Flag("child-flag", targetingRules: new() { oldRule }) }, 1500);

        var updated = cache.Get("child-flag")!;
        Assert.Equal(new List<string> { "new-rule" }, Ids(updated.TargetingRules));
        Assert.Equal(2000L, updated.TargetingRulesUpdatedAt);
    }

    [Fact]
    public void LoadSnapshot_AppliesNormallyWhenNewerThanTheLiveUpdatesOwnTs()
    {
        var cache = new FlagCache();
        var oldRule = Rule("old-rule");
        cache.LoadSnapshot(new[] { Flag("child-flag", targetingRules: new() { oldRule }) }, 1000);

        var newRule = Rule("new-rule");
        cache.ApplyTargetingRulesEvent("child-flag", new() { newRule }, 2000);

        var evenNewerRule = Rule("even-newer-rule");
        cache.LoadSnapshot(new[] { Flag("child-flag", targetingRules: new() { evenNewerRule }) }, 3000);

        var updated = cache.Get("child-flag")!;
        Assert.Equal(new List<string> { "even-newer-rule" }, Ids(updated.TargetingRules));
        Assert.Equal(3000L, updated.TargetingRulesUpdatedAt);
    }

    [Fact]
    public void LoadSnapshot_PreservesTheLiveUpdateOnAnExactTsTie()
    {
        var cache = new FlagCache();
        var oldRule = Rule("old-rule");
        cache.LoadSnapshot(new[] { Flag("child-flag", targetingRules: new() { oldRule }) }, 1000);

        var newRule = Rule("new-rule");
        cache.ApplyTargetingRulesEvent("child-flag", new() { newRule }, 2000);

        cache.LoadSnapshot(new[] { Flag("child-flag", targetingRules: new() { oldRule }) }, 2000);

        var updated = cache.Get("child-flag")!;
        Assert.Equal(new List<string> { "new-rule" }, Ids(updated.TargetingRules));
        Assert.Equal(2000L, updated.TargetingRulesUpdatedAt);
    }

    [Fact]
    public void LoadSnapshot_SecondSnapshotSharingTheExactSameTsAsFirst_NoLiveEvent_StillAppliesItsOwnData()
    {
        var cache = new FlagCache();
        cache.LoadSnapshot(new[] { Flag("child-flag", targetingRules: new() { Rule("rule-a") }) }, 5000);
        cache.LoadSnapshot(new[] { Flag("child-flag", targetingRules: new() { Rule("rule-b") }) }, 5000);

        var updated = cache.Get("child-flag")!;
        Assert.Equal(new List<string> { "rule-b" }, Ids(updated.TargetingRules));
        Assert.Equal(5000L, updated.TargetingRulesUpdatedAt);
    }

    [Fact]
    public void LoadSnapshot_ThirdSnapshotTyingALiveEventsTs_AfterASecondTiedSnapshotAlreadyResolvedTheRace_StillAppliesItsOwnData()
    {
        var cache = new FlagCache();
        cache.LoadSnapshot(new[] { Flag("child-flag", targetingRules: new() { Rule("old-rule") }) }, 1000);
        cache.ApplyTargetingRulesEvent("child-flag", new() { Rule("live-rule") }, 2000);

        cache.LoadSnapshot(new[] { Flag("child-flag", targetingRules: new() { Rule("snap-b-rule") }) }, 2000);
        Assert.Equal(new List<string> { "live-rule" }, Ids(cache.Get("child-flag")!.TargetingRules));

        cache.LoadSnapshot(new[] { Flag("child-flag", targetingRules: new() { Rule("snap-c-rule") }) }, 2000);

        var updated = cache.Get("child-flag")!;
        Assert.Equal(new List<string> { "snap-c-rule" }, Ids(updated.TargetingRules));
        Assert.Equal(2000L, updated.TargetingRulesUpdatedAt);
    }

    [Fact]
    public void ApplyTargetingRulesEvent_WithAnEmptyList_ClearsExistingRules()
    {
        var cache = new FlagCache();
        cache.LoadSnapshot(new[] { Flag("child-flag", targetingRules: new() { Rule("parent-rule") }) }, 1000);

        cache.ApplyTargetingRulesEvent("child-flag", new(), 2000);

        var updated = cache.Get("child-flag")!;
        Assert.Empty(updated.TargetingRules);
        Assert.Equal(2000L, updated.TargetingRulesUpdatedAt);
    }

    // Found missing (for the analogous TS SDK feature) by adversarial
    // review of PR #246, and only correctly ISOLATED after a second round
    // of adversarial review of the Java SDK's own first draft of this same
    // test (PR #247): a naive version where the untouched field's own ts is
    // strictly OLDER than the tying snapshot's ts lets the ts>=snapshotTs
    // half of the AND condition alone force the correct outcome regardless
    // of what the live-provenance boolean is set to -- so it doesn't
    // actually test anything. This version ties BOTH the live event and the
    // final snapshot at the SAME ts as the very first load, so the boolean
    // is the ONLY variable that can distinguish a correct pass from a false
    // one.
    [Fact]
    public void ALivePrerequisitesEvent_DoesNotProtectTargetingRules()
    {
        var cache = new FlagCache();
        var oldPrereq = Prereq("old-parent");
        var oldRule = Rule("old-rule");
        cache.LoadSnapshot(new[] { Flag("child-flag", prerequisites: new() { oldPrereq }, targetingRules: new() { oldRule }) }, 1000);

        // Only a live PREREQUISITES event fires, tying the SAME ts=1000 --
        // TargetingRules gets no live event at all.
        var newPrereq = Prereq("new-parent");
        cache.ApplyPrerequisitesEvent("child-flag", new() { newPrereq }, 1000);

        // A second snapshot ties the SAME ts=1000 again. Both existing
        // *UpdatedAt fields are now >= 1000 -- carrying stale prerequisites
        // (correctly preserved) but genuinely NEW targeting_rules (must NOT
        // be blocked).
        var genuinelyNewRule = Rule("genuinely-new-rule");
        cache.LoadSnapshot(new[] { Flag("child-flag", prerequisites: new() { oldPrereq }, targetingRules: new() { genuinelyNewRule }) }, 1000);

        var state = cache.Get("child-flag")!;
        Assert.Equal(new List<FlagPrerequisite> { newPrereq }, state.Prerequisites);
        Assert.Equal(new List<string> { "genuinely-new-rule" }, Ids(state.TargetingRules));
    }

    [Fact]
    public void ALiveTargetingRulesEvent_DoesNotProtectPrerequisites()
    {
        var cache = new FlagCache();
        var oldPrereq = Prereq("old-parent");
        var oldRule = Rule("old-rule");
        cache.LoadSnapshot(new[] { Flag("child-flag", prerequisites: new() { oldPrereq }, targetingRules: new() { oldRule }) }, 1000);

        var newRule = Rule("new-rule");
        cache.ApplyTargetingRulesEvent("child-flag", new() { newRule }, 1000);

        var genuinelyNewPrereq = Prereq("genuinely-new-parent");
        cache.LoadSnapshot(new[] { Flag("child-flag", prerequisites: new() { genuinelyNewPrereq }, targetingRules: new() { oldRule }) }, 1000);

        var state = cache.Get("child-flag")!;
        Assert.Equal(new List<string> { "new-rule" }, Ids(state.TargetingRules));
        Assert.Equal(new List<FlagPrerequisite> { genuinelyNewPrereq }, state.Prerequisites);
    }

    // Found by adversarial review of the Java/Ruby SDKs' own equivalent
    // tests (PRs #247/#248): every other test in this file only ever sets
    // PrerequisitesUpdatedAt and TargetingRulesUpdatedAt to the SAME value
    // (both come from LoadSnapshot alone) -- so a copy-paste bug in
    // ApplyTargetingRulesEvent's own staleness guard (comparing against
    // existing.PrerequisitesUpdatedAt instead of
    // existing.TargetingRulesUpdatedAt) would go completely undetected.
    // This test deliberately DIVERGES the two fields first.
    [Fact]
    public void ApplyTargetingRulesEvent_ComparesAgainstTargetingRulesUpdatedAt_NotPrerequisitesUpdatedAt()
    {
        var cache = new FlagCache();
        cache.LoadSnapshot(new[] { Flag("child-flag", targetingRules: new() { Rule("old-rule") }) }, 1000);
        // Both *UpdatedAt fields are 1000 here.

        // A live PREREQUISITES event bumps ONLY PrerequisitesUpdatedAt to
        // 5000 -- TargetingRulesUpdatedAt must stay at 1000.
        cache.ApplyPrerequisitesEvent("child-flag", new() { Prereq("new-parent") }, 5000);
        Assert.Equal(1000L, cache.Get("child-flag")!.TargetingRulesUpdatedAt);

        // ts=2000 is NEWER than TargetingRulesUpdatedAt (1000, the correct
        // field) but OLDER than PrerequisitesUpdatedAt (5000, the WRONG
        // field a copy-paste bug might compare against instead).
        var newRule = Rule("new-rule");
        cache.ApplyTargetingRulesEvent("child-flag", new() { newRule }, 2000);

        var updated = cache.Get("child-flag")!;
        Assert.Equal(new List<string> { "new-rule" }, Ids(updated.TargetingRules));
        Assert.Equal(2000L, updated.TargetingRulesUpdatedAt);
    }

    [Fact]
    public async Task ConcurrentLoadSnapshotAndApplyTargetingRulesEvent_DoNotCorruptOrLoseState()
    {
        // Mirrors ConcurrentLoadSnapshotAndApplyPrerequisitesEvent_DoNotCorruptOrLoseState
        // exactly -- see that test's own doc comment for the full reasoning.
        var cache = new FlagCache();
        cache.LoadSnapshot(new[] { Flag("child-flag", targetingRules: new() { Rule("initial-rule") }) }, 1000);

        var tasks = new List<Task>();
        for (var i = 0; i < 20; i++)
        {
            var n = i;
            tasks.Add(Task.Run(() => cache.LoadSnapshot(
                new[] { Flag("child-flag", targetingRules: new() { Rule($"snap-rule-{n}") }) }, 2000 + n)));
            tasks.Add(Task.Run(() => cache.ApplyTargetingRulesEvent(
                "child-flag", new() { Rule($"live-rule-{n}") }, 3000 + n)));
        }
        await Task.WhenAll(tasks);

        var finalState = cache.Get("child-flag");
        Assert.NotNull(finalState);
        Assert.NotNull(finalState!.TargetingRules);
        Assert.Equal(1, finalState.TargetingRules.Count);
    }
}
