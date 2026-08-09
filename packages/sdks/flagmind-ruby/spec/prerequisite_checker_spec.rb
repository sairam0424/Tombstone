require "spec_helper"
require "set"

RSpec.describe Tombstone::PrerequisiteChecker do
  let(:engine) { Tombstone::EvaluationEngine.new }
  let(:ctx) { Tombstone::EvaluationContext.of("u1") }

  def simple_flag(key, enabled, pct)
    Tombstone::FlagEnvironmentState.new(
      flag_id: "id", flag_key: key, environment: "test",
      enabled: enabled, rollout_pct: pct, safe_default: "false", updated_at: 0
    )
  end

  it "hard gate unmet blocks" do
    base_flag = simple_flag("base-flag", false, 0)
    lookup = ->(key) { key == "base-flag" ? base_flag : nil }
    prereq = Tombstone::FlagPrerequisite.new(flag_key: "base-flag", required_variation: "true", gate: true)

    satisfied = Tombstone::PrerequisiteChecker.check_all(
      [prereq], lookup, {}, Set.new, "parent-flag", engine, ctx
    )

    expect(satisfied).to be false
  end

  it "hard gate met passes" do
    base_flag = simple_flag("base-flag", true, 100)
    lookup = ->(key) { key == "base-flag" ? base_flag : nil }
    prereq = Tombstone::FlagPrerequisite.new(flag_key: "base-flag", required_variation: "true", gate: true)

    satisfied = Tombstone::PrerequisiteChecker.check_all(
      [prereq], lookup, {}, Set.new, "parent-flag", engine, ctx
    )

    expect(satisfied).to be true
  end

  it "soft gate unmet still passes" do
    base_flag = simple_flag("base-flag", false, 0)
    lookup = ->(key) { key == "base-flag" ? base_flag : nil }
    prereq = Tombstone::FlagPrerequisite.new(flag_key: "base-flag", required_variation: "true", gate: false)

    satisfied = Tombstone::PrerequisiteChecker.check_all(
      [prereq], lookup, {}, Set.new, "parent-flag", engine, ctx
    )

    expect(satisfied).to be true
  end

  it "cycle detected fails open" do
    lookup = ->(_key) { nil }  # unreachable — cycle short-circuits before lookup
    prereq = Tombstone::FlagPrerequisite.new(flag_key: "self-ref", required_variation: "true", gate: true)
    seen = Set.new(["self-ref"])

    satisfied = Tombstone::PrerequisiteChecker.check_all(
      [prereq], lookup, {}, seen, "self-ref", engine, ctx
    )

    expect(satisfied).to be true
  end

  it "missing prerequisite flag with hard gate blocks" do
    lookup = ->(_key) { nil }
    prereq = Tombstone::FlagPrerequisite.new(flag_key: "nonexistent", required_variation: "true", gate: true)

    satisfied = Tombstone::PrerequisiteChecker.check_all(
      [prereq], lookup, {}, Set.new, "parent-flag", engine, ctx
    )

    expect(satisfied).to be false
  end

  it "missing prerequisite flag with soft gate passes" do
    lookup = ->(_key) { nil }
    prereq = Tombstone::FlagPrerequisite.new(flag_key: "nonexistent", required_variation: "true", gate: false)

    satisfied = Tombstone::PrerequisiteChecker.check_all(
      [prereq], lookup, {}, Set.new, "parent-flag", engine, ctx
    )

    expect(satisfied).to be true
  end

  it "memoization prevents redundant evaluation" do
    call_count = 0
    base_flag = simple_flag("base-flag", true, 100)
    lookup = lambda { |key|
      call_count += 1 if key == "base-flag"
      key == "base-flag" ? base_flag : nil
    }
    prereq1 = Tombstone::FlagPrerequisite.new(flag_key: "base-flag", required_variation: "true", gate: true)
    prereq2 = Tombstone::FlagPrerequisite.new(flag_key: "base-flag", required_variation: "true", gate: true)
    cache = {}

    Tombstone::PrerequisiteChecker.check_all(
      [prereq1, prereq2], lookup, cache, Set.new, "parent-flag", engine, ctx
    )

    expect(call_count).to eq(1), "base-flag should be looked up and evaluated only once, memoized for the second reference"
  end
end
