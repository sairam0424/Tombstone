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
  end
end
