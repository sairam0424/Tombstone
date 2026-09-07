require "spec_helper"

# FlagCache -- direct unit tests for prerequisites-streaming consumption
# (mirrors the TypeScript SDK's cache.test.ts and the Java SDK's
# FlagCachePrerequisitesTest.java, added after PR #236's own adversarial
# review found three related races in the TypeScript port: a full-snapshot
# reload regressing an already-fresher live prerequisites update, two
# overlapping reloads applying out of order, and a staleness comparison
# that must be pinned down to strict "<" rather than "<="). Applied here
# proactively from the start rather than waiting for a review to find the
# same gaps.
#
# load_snapshot always OVERWRITES an incoming flag's prerequisites_updated_at
# with the snapshot's own ts (unless preserving a fresher existing live
# update), so flag()'s own prerequisites_updated_at is a placeholder here;
# the snapshot_ts passed to load_snapshot is the real source of truth.
RSpec.describe Tombstone::FlagCache do
  def flag(key, updated_at: 0, prerequisites: [])
    Tombstone::FlagEnvironmentState.new(
      flag_id: "id", flag_key: key, environment: "test",
      enabled: true, rollout_pct: 100, safe_default: "false", updated_at: updated_at,
      prerequisites: prerequisites
    )
  end

  def prereq(flag_key)
    Tombstone::FlagPrerequisite.new(flag_key: flag_key, required_variation: "true", gate: true)
  end

  describe "#apply_prerequisites_event" do
    it "rejects a stale (older-ts) delivery" do
      cache = described_class.new
      current = prereq("parent-flag")
      cache.load_snapshot([flag("child-flag", prerequisites: [current])], 5000)

      cache.apply_prerequisites_event("child-flag", [prereq("stale-parent")], 3000)

      updated = cache.get("child-flag")
      expect(updated.prerequisites).to eq([current])
      expect(updated.prerequisites_updated_at).to eq(5000)
    end

    it "applies an event with ts EQUAL to the cached value -- pins down strict '<', not '<='" do
      # A "clearly older" ts alone (the test above) cannot distinguish a
      # correct "<" comparison from a buggy "<=" regression -- both reject
      # that input identically. Only an equal-ts input tells them apart.
      cache = described_class.new
      current = prereq("parent-flag")
      cache.load_snapshot([flag("child-flag", prerequisites: [current])], 5000)

      incoming = prereq("new-parent")
      cache.apply_prerequisites_event("child-flag", [incoming], 5000)

      updated = cache.get("child-flag")
      expect(updated.prerequisites).to eq([incoming])
      expect(updated.prerequisites_updated_at).to eq(5000)
    end

    it "is a no-op for a flag it has never seen" do
      cache = described_class.new
      expect { cache.apply_prerequisites_event("never-seen", [], 1) }.not_to raise_error
      expect(cache.get("never-seen")).to be_nil
    end

    it "clears a flag's existing prerequisites when the incoming list is empty" do
      # apply_prerequisites_event is documented as a full replacement, not a
      # delta -- an empty incoming list must actually clear a flag's
      # existing (non-empty) gates, not be mistaken for "nothing to apply".
      cache = described_class.new
      cache.load_snapshot([flag("child-flag", prerequisites: [prereq("parent-flag")])], 1000)

      cache.apply_prerequisites_event("child-flag", [], 2000)

      updated = cache.get("child-flag")
      expect(updated.prerequisites).to eq([])
      expect(updated.prerequisites_updated_at).to eq(2000)
    end
  end

  describe "#load_snapshot monotonicity" do
    it "rejects an incoming snapshot older than the last one actually applied" do
      # Simulates two overlapping fetch_snapshot calls (e.g. a lag-triggered
      # refetch racing a reconnect-triggered one) resolving out of order.
      #
      # Asserts updated_at, NOT prerequisites_updated_at: the per-flag
      # prerequisites-preservation behavior (tested separately below) would
      # incidentally ALSO protect prerequisites_updated_at here, which would
      # let this test pass even with the monotonicity guard alone removed
      # (confirmed via the identical pitfall found -- and fixed -- while
      # writing the Java SDK's equivalent test). updated_at is a field the
      # per-flag logic never touches, so it isolates this guard specifically.
      cache = described_class.new
      cache.load_snapshot([flag("child-flag", updated_at: 111)], 5000)
      cache.load_snapshot([flag("child-flag", updated_at: 999)], 3000) # older -- rejected wholesale

      expect(cache.get("child-flag").updated_at).to eq(111)
    end

    it "still applies a genuinely newer snapshot after an older one was rejected" do
      cache = described_class.new
      cache.load_snapshot([flag("child-flag", updated_at: 111)], 5000)
      cache.load_snapshot([flag("child-flag", updated_at: 999)], 3000) # rejected
      cache.load_snapshot([flag("child-flag", updated_at: 777)], 7000) # genuinely newer -- applies

      expect(cache.get("child-flag").updated_at).to eq(777)
    end

    it "the very first load_snapshot call always applies regardless of ts" do
      cache = described_class.new
      cache.load_snapshot([flag("child-flag", updated_at: 1)], 1)
      expect(cache.get("child-flag").updated_at).to eq(1)
    end
  end

  describe "#load_snapshot preserves a fresher live prerequisites update" do
    it "does not regress prerequisites when a newer-but-still-behind-the-live-update snapshot reloads" do
      cache = described_class.new
      old_parent = prereq("old-parent")
      cache.load_snapshot([flag("child-flag", prerequisites: [old_parent])], 1000)

      new_parent = prereq("new-parent")
      cache.apply_prerequisites_event("child-flag", [new_parent], 2000)

      # The slower snapshot now resolves. Its ts=1500 is newer than the
      # cache's LAST SNAPSHOT ts (1000), so it passes the whole-snapshot
      # check -- but it still carries the OLD (pre-live-update)
      # prerequisites for this flag.
      cache.load_snapshot([flag("child-flag", prerequisites: [old_parent])], 1500)

      updated = cache.get("child-flag")
      expect(updated.prerequisites).to eq([new_parent])
      expect(updated.prerequisites_updated_at).to eq(2000)
    end

    it "applies normally when the snapshot is newer than the live update's own ts" do
      cache = described_class.new
      old_parent = prereq("old-parent")
      cache.load_snapshot([flag("child-flag", prerequisites: [old_parent])], 1000)

      new_parent = prereq("new-parent")
      cache.apply_prerequisites_event("child-flag", [new_parent], 2000)

      even_newer_parent = prereq("even-newer-parent")
      cache.load_snapshot([flag("child-flag", prerequisites: [even_newer_parent])], 3000)

      updated = cache.get("child-flag")
      expect(updated.prerequisites).to eq([even_newer_parent])
      expect(updated.prerequisites_updated_at).to eq(3000)
    end

    it "preserves the live update on an exact ts tie" do
      # flag-api's snapshot endpoint (environments.go: Ts: time.Now().Unix())
      # and its prerequisites-event publisher (prerequisites.go: ts :=
      # time.Now().Unix()) both use the SAME 1-second-resolution wall clock,
      # so a live event and a racing/in-flight snapshot fetch that land in
      # the same wall-clock second get an IDENTICAL ts.
      # apply_prerequisites_event's own staleness guard uses strict "<",
      # meaning it treats an equal ts as "fresh enough to apply" -- this
      # preservation check must treat the SAME tie as "fresh enough to
      # keep" (i.e. use ">=", not ">"), or the snapshot silently overwrites
      # the just-applied live update purely because of a tie.
      cache = described_class.new
      old_parent = prereq("old-parent")
      cache.load_snapshot([flag("child-flag", prerequisites: [old_parent])], 1000)

      new_parent = prereq("new-parent")
      cache.apply_prerequisites_event("child-flag", [new_parent], 2000)

      # The in-flight snapshot resolves with the EXACT SAME ts as the live
      # update that already applied.
      cache.load_snapshot([flag("child-flag", prerequisites: [old_parent])], 2000)

      updated = cache.get("child-flag")
      expect(updated.prerequisites).to eq([new_parent])
      expect(updated.prerequisites_updated_at).to eq(2000)
    end

    it "still applies a second snapshot's own data on a ts tie with a first snapshot, no live event involved" do
      # Regression test for a real bug the >= tie-break above introduced in
      # its own first draft: with only a ts comparison, this cache cannot
      # tell "existing.prerequisites_updated_at came from a live event that
      # must win a tie" from "existing.prerequisites_updated_at came from a
      # PRIOR SNAPSHOT LOAD that merely happens to share flag-api's coarse
      # 1-second-resolution ts with a SECOND, later snapshot". Without
      # @prerequisites_from_live_event tracking, this second snapshot's
      # genuinely different prerequisites would be silently discarded.
      cache = described_class.new
      cache.load_snapshot([flag("child-flag", prerequisites: [prereq("parent-a")])], 5000)

      # A second, completely independent snapshot fetch resolves with the
      # EXACT SAME ts but genuinely different data. No live event at all.
      cache.load_snapshot([flag("child-flag", prerequisites: [prereq("parent-b")])], 5000)

      updated = cache.get("child-flag")
      expect(updated.prerequisites).to eq([prereq("parent-b")])
      expect(updated.prerequisites_updated_at).to eq(5000)
    end

    it "still applies a third snapshot's own data after a second tied snapshot already resolved the race with a live event" do
      # Regression test found by a SECOND round of adversarial review of
      # this fix's own first draft: @prerequisites_from_live_event was
      # being re-set to true every time it was used to preserve a live
      # event across a tied snapshot, making the protection "sticky" --
      # EVERY subsequent snapshot at or before that ts would ALSO get
      # vetoed, not just the one snapshot that legitimately raced the live
      # event. The protection must be ONE-SHOT.
      cache = described_class.new
      cache.load_snapshot([flag("child-flag", prerequisites: [prereq("old-parent")])], 1000)
      cache.apply_prerequisites_event("child-flag", [prereq("live-parent")], 2000)

      # First tied snapshot after the live event -- the ONE specific race
      # the live event's own protection exists to close. Must preserve.
      cache.load_snapshot([flag("child-flag", prerequisites: [prereq("snap-b-parent")])], 2000)
      expect(cache.get("child-flag").prerequisites).to eq([prereq("live-parent")])

      # A SECOND, independent snapshot arrives, also tying ts=2000. No new
      # live event raced THIS one -- the protection was already consumed.
      cache.load_snapshot([flag("child-flag", prerequisites: [prereq("snap-c-parent")])], 2000)

      updated = cache.get("child-flag")
      expect(updated.prerequisites).to eq([prereq("snap-c-parent")])
      expect(updated.prerequisites_updated_at).to eq(2000)
    end
  end

  describe "concurrent access" do
    it "does not corrupt or lose an update under concurrent apply_event and apply_prerequisites_event calls" do
      # The PR's own rationale claims FlagCache is "genuinely mutex-guarded"
      # (unlike Java's disclosed non-atomic race) -- this test backs that
      # claim with a real concurrent-thread run instead of resting on code
      # inspection alone, mirroring client_lag_spec.rb's own Thread.new
      # convention for the debounce-timer thread.
      cache = described_class.new
      cache.load_snapshot([flag("child-flag", prerequisites: [prereq("initial-parent")])], 1000)

      threads = []
      20.times do |i|
        threads << Thread.new { cache.apply_event("child-flag", true, i, 2000 + i) }
        threads << Thread.new { cache.apply_prerequisites_event("child-flag", [prereq("parent-#{i}")], 3000 + i) }
      end
      threads.each(&:join)

      # No exceptions, no torn/nil state -- the cache entry must still be a
      # single, internally-consistent, fully-formed FlagEnvironmentState.
      final = cache.get("child-flag")
      expect(final).not_to be_nil
      expect(final.rollout_pct).to be_a(Integer)
      expect(final.prerequisites).to be_an(Array)
      expect(final.prerequisites_updated_at).to be >= 3000
    end
  end
end
