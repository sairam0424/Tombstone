require "spec_helper"

# FlagCache -- direct unit tests for targeting_rules-streaming consumption,
# mirroring flag_cache_prerequisites_spec.rb exactly. Applied here
# PROACTIVELY from the start: the >= tie-break, the one-shot live-provenance
# hash, and the cross-feature independence isolation technique were all
# discovered incrementally for prerequisites (and, for the independence
# tests specifically, only correctly isolated after a SECOND round of
# adversarial review of the Java SDK's PR #247) -- this file exists to prove
# the SAME lessons hold for targeting_rules from the very first commit, not
# to rediscover them.
#
# load_snapshot's own logic always OVERWRITES an incoming flag's
# targeting_rules_updated_at with the snapshot's own ts (unless preserving a
# fresher existing live update) -- so flag()'s own updated_at is a
# placeholder here; the snapshot_ts passed to load_snapshot is the real
# source of truth for these tests.
RSpec.describe Tombstone::FlagCache do
  def flag(key, updated_at: 0, targeting_rules: [], prerequisites: [])
    Tombstone::FlagEnvironmentState.new(
      flag_id: "id", flag_key: key, environment: "test",
      enabled: true, rollout_pct: 100, safe_default: "false", updated_at: updated_at,
      targeting_rules: targeting_rules, prerequisites: prerequisites
    )
  end

  def rule(id)
    condition = Tombstone::PropertyCondition.new(attribute: "email", operator: "eq", values: ["x@example.com"], negate: false)
    Tombstone::TargetingRule.new(id: id, conditions: [condition], rollout_pct: 100, variation: "true", priority: 0)
  end

  def prereq(flag_key)
    Tombstone::FlagPrerequisite.new(flag_key: flag_key, required_variation: "true", gate: true)
  end

  describe "#apply_targeting_rules_event" do
    it "rejects a stale (older-ts) delivery" do
      cache = described_class.new
      current = rule("parent-rule")
      cache.load_snapshot([flag("child-flag", targeting_rules: [current])], 5000)

      cache.apply_targeting_rules_event("child-flag", [rule("stale-rule")], 3000)

      updated = cache.get("child-flag")
      expect(updated.targeting_rules).to eq([current])
      expect(updated.targeting_rules_updated_at).to eq(5000)
    end

    it "applies an event with ts EQUAL to the cached value -- pins down strict '<', not '<='" do
      cache = described_class.new
      current = rule("parent-rule")
      cache.load_snapshot([flag("child-flag", targeting_rules: [current])], 5000)

      incoming = rule("new-rule")
      cache.apply_targeting_rules_event("child-flag", [incoming], 5000)

      updated = cache.get("child-flag")
      expect(updated.targeting_rules).to eq([incoming])
      expect(updated.targeting_rules_updated_at).to eq(5000)
    end

    it "is a no-op for a flag it has never seen" do
      cache = described_class.new
      expect { cache.apply_targeting_rules_event("never-seen", [], 1) }.not_to raise_error
      expect(cache.get("never-seen")).to be_nil
    end

    it "clears a flag's existing targeting_rules when the incoming list is empty" do
      cache = described_class.new
      cache.load_snapshot([flag("child-flag", targeting_rules: [rule("parent-rule")])], 1000)

      cache.apply_targeting_rules_event("child-flag", [], 2000)

      updated = cache.get("child-flag")
      expect(updated.targeting_rules).to eq([])
      expect(updated.targeting_rules_updated_at).to eq(2000)
    end
  end

  describe "#load_snapshot preserves a fresher live targeting_rules update" do
    it "does not regress targeting_rules when a newer-but-still-behind-the-live-update snapshot reloads" do
      cache = described_class.new
      old_rule = rule("old-rule")
      cache.load_snapshot([flag("child-flag", targeting_rules: [old_rule])], 1000)

      new_rule = rule("new-rule")
      cache.apply_targeting_rules_event("child-flag", [new_rule], 2000)

      cache.load_snapshot([flag("child-flag", targeting_rules: [old_rule])], 1500)

      updated = cache.get("child-flag")
      expect(updated.targeting_rules).to eq([new_rule])
      expect(updated.targeting_rules_updated_at).to eq(2000)
    end

    it "applies normally when the snapshot is newer than the live update's own ts" do
      cache = described_class.new
      old_rule = rule("old-rule")
      cache.load_snapshot([flag("child-flag", targeting_rules: [old_rule])], 1000)

      new_rule = rule("new-rule")
      cache.apply_targeting_rules_event("child-flag", [new_rule], 2000)

      even_newer_rule = rule("even-newer-rule")
      cache.load_snapshot([flag("child-flag", targeting_rules: [even_newer_rule])], 3000)

      updated = cache.get("child-flag")
      expect(updated.targeting_rules).to eq([even_newer_rule])
      expect(updated.targeting_rules_updated_at).to eq(3000)
    end

    it "preserves the live update on an exact ts tie" do
      cache = described_class.new
      old_rule = rule("old-rule")
      cache.load_snapshot([flag("child-flag", targeting_rules: [old_rule])], 1000)

      new_rule = rule("new-rule")
      cache.apply_targeting_rules_event("child-flag", [new_rule], 2000)

      cache.load_snapshot([flag("child-flag", targeting_rules: [old_rule])], 2000)

      updated = cache.get("child-flag")
      expect(updated.targeting_rules).to eq([new_rule])
      expect(updated.targeting_rules_updated_at).to eq(2000)
    end

    it "still applies a second snapshot's own data on a ts tie with a first snapshot, no live event involved" do
      cache = described_class.new
      cache.load_snapshot([flag("child-flag", targeting_rules: [rule("rule-a")])], 5000)
      cache.load_snapshot([flag("child-flag", targeting_rules: [rule("rule-b")])], 5000)

      updated = cache.get("child-flag")
      expect(updated.targeting_rules).to eq([rule("rule-b")])
      expect(updated.targeting_rules_updated_at).to eq(5000)
    end

    it "still applies a third snapshot's own data after a second tied snapshot already resolved the race with a live event" do
      cache = described_class.new
      cache.load_snapshot([flag("child-flag", targeting_rules: [rule("old-rule")])], 1000)
      cache.apply_targeting_rules_event("child-flag", [rule("live-rule")], 2000)

      cache.load_snapshot([flag("child-flag", targeting_rules: [rule("snap-b-rule")])], 2000)
      expect(cache.get("child-flag").targeting_rules).to eq([rule("live-rule")])

      cache.load_snapshot([flag("child-flag", targeting_rules: [rule("snap-c-rule")])], 2000)

      updated = cache.get("child-flag")
      expect(updated.targeting_rules).to eq([rule("snap-c-rule")])
      expect(updated.targeting_rules_updated_at).to eq(2000)
    end
  end

  describe "prerequisites and targeting_rules tie-breaking operate independently" do
    # Found missing (for the analogous TS SDK feature) by adversarial review
    # of PR #246, and only correctly ISOLATED after a second round of
    # adversarial review of the Java SDK's own first draft of this same test
    # (PR #247): a naive version where the untouched field's own ts is
    # strictly OLDER than the tying snapshot's ts lets the ts>=snapshot_ts
    # half of the AND condition alone force the correct outcome regardless
    # of what the live-provenance boolean is set to -- so it doesn't
    # actually test anything. This version ties BOTH the live event and the
    # final snapshot at the SAME ts as the very first load, so the boolean
    # is the ONLY variable that can distinguish a correct pass from a false
    # one.
    it "a live prerequisites event does not protect targeting_rules" do
      cache = described_class.new
      old_prereq = prereq("old-parent")
      old_rule = rule("old-rule")
      cache.load_snapshot([flag("child-flag", prerequisites: [old_prereq], targeting_rules: [old_rule])], 1000)

      # Only a live PREREQUISITES event fires, tying the SAME ts=1000 --
      # targeting_rules gets no live event at all.
      new_prereq = prereq("new-parent")
      cache.apply_prerequisites_event("child-flag", [new_prereq], 1000)

      # A second snapshot ties the SAME ts=1000 again. Both existing
      # *_updated_at fields are now >= 1000 -- carrying stale prerequisites
      # (correctly preserved) but genuinely NEW targeting_rules (must NOT
      # be blocked).
      genuinely_new_rule = rule("genuinely-new-rule")
      cache.load_snapshot([flag("child-flag", prerequisites: [old_prereq], targeting_rules: [genuinely_new_rule])], 1000)

      state = cache.get("child-flag")
      expect(state.prerequisites).to eq([new_prereq])
      expect(state.targeting_rules).to eq([genuinely_new_rule])
    end

    it "a live targeting_rules event does not protect prerequisites" do
      cache = described_class.new
      old_prereq = prereq("old-parent")
      old_rule = rule("old-rule")
      cache.load_snapshot([flag("child-flag", prerequisites: [old_prereq], targeting_rules: [old_rule])], 1000)

      new_rule = rule("new-rule")
      cache.apply_targeting_rules_event("child-flag", [new_rule], 1000)

      genuinely_new_prereq = prereq("genuinely-new-parent")
      cache.load_snapshot([flag("child-flag", prerequisites: [genuinely_new_prereq], targeting_rules: [old_rule])], 1000)

      state = cache.get("child-flag")
      expect(state.targeting_rules).to eq([new_rule])
      expect(state.prerequisites).to eq([genuinely_new_prereq])
    end
  end

  describe "#apply_targeting_rules_event staleness guard field isolation" do
    # Found by adversarial review of the Java SDK's equivalent test (PR
    # #247): every other test in this file only ever sets
    # prerequisites_updated_at and targeting_rules_updated_at to the SAME
    # value (both come from load_snapshot alone) -- so a copy-paste bug in
    # apply_targeting_rules_event's own staleness guard (comparing against
    # existing.prerequisites_updated_at instead of
    # existing.targeting_rules_updated_at) would go completely undetected.
    # This test deliberately DIVERGES the two fields first.
    it "compares against targeting_rules_updated_at, not prerequisites_updated_at" do
      cache = described_class.new
      cache.load_snapshot([flag("child-flag", targeting_rules: [rule("old-rule")])], 1000)
      # Both *_updated_at fields are 1000 here.

      # A live PREREQUISITES event bumps ONLY prerequisites_updated_at to
      # 5000 -- targeting_rules_updated_at must stay at 1000.
      cache.apply_prerequisites_event("child-flag", [prereq("new-parent")], 5000)
      expect(cache.get("child-flag").targeting_rules_updated_at).to eq(1000)

      # ts=2000 is NEWER than targeting_rules_updated_at (1000, the correct
      # field) but OLDER than prerequisites_updated_at (5000, the WRONG
      # field a copy-paste bug might compare against instead).
      new_rule = rule("new-rule")
      cache.apply_targeting_rules_event("child-flag", [new_rule], 2000)

      updated = cache.get("child-flag")
      expect(updated.targeting_rules).to eq([new_rule])
      expect(updated.targeting_rules_updated_at).to eq(2000)
    end
  end

  describe "concurrent access" do
    it "does not corrupt or lose an update under concurrent load_snapshot and apply_targeting_rules_event calls" do
      cache = described_class.new
      cache.load_snapshot([flag("child-flag", targeting_rules: [rule("initial-rule")])], 1000)

      threads = []
      20.times do |i|
        threads << Thread.new { cache.load_snapshot([flag("child-flag", targeting_rules: [rule("snap-rule-#{i}")])], 2000 + i) }
        threads << Thread.new { cache.apply_targeting_rules_event("child-flag", [rule("live-rule-#{i}")], 3000 + i) }
      end
      threads.each(&:join)

      final = cache.get("child-flag")
      expect(final).not_to be_nil
      expect(final.targeting_rules).to be_an(Array)
      expect(final.targeting_rules.size).to eq(1)
    end
  end
end
