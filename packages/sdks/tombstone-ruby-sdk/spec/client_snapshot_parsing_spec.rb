require "spec_helper"

# Regression suite for a bug found while investigating SDK-4's
# prerequisites-streaming follow-up: fetch_snapshot's FlagEnvironmentState.new
# call never passed prerequisites: at all, so every flag defaulted to
# prerequisites: [] regardless of what the wire actually sent --
# PrerequisiteChecker.check_all's algorithm is otherwise correct, but was
# completely unreachable with real gating data. Exercises the real
# parse_snapshot_flags(Hash) parsing logic directly via `client.send(...)`
# (matching client_lag_spec.rb's existing convention for private methods),
# with a hand-built Hash shaped exactly like flag-api's real snapshot
# endpoint (services/flag-api/internal/api/v1/environments.go), rather than
# stubbing Net::HTTP.
RSpec.describe Tombstone::Client do
  let(:client) { described_class.new(sdk_key: "sdk-test-key", environment: "test") }

  it "parses top-level flag fields from a real snapshot response" do
    data = {
      "environment" => "production",
      "flags" => [
        {
          "flag_id" => "1", "flag_key" => "known-flag", "environment" => "production",
          "enabled" => true, "rollout_pct" => 100, "safe_default" => "false",
          "updated_at" => 1_700_000_000, "prerequisites" => []
        }
      ]
    }
    states = client.send(:parse_snapshot_flags, data)
    expect(states.length).to eq(1)
    s = states.first
    expect(s.flag_id).to eq("1")
    expect(s.flag_key).to eq("known-flag")
    expect(s.enabled).to eq(true)
    expect(s.rollout_pct).to eq(100)
    expect(s.safe_default).to eq("false")
    expect(s.updated_at).to eq(1_700_000_000)
  end

  it "parses real prerequisites using flag_key (not prereq_flag_key) and required_variation (not required_value)" do
    data = {
      "flags" => [
        {
          "flag_id" => "2", "flag_key" => "child-flag", "environment" => "production",
          "enabled" => true, "rollout_pct" => 100, "safe_default" => "false",
          "updated_at" => 1_700_000_000,
          "prerequisites" => [
            { "id" => "prereq-1", "flag_key" => "parent-flag", "required_variation" => "true", "gate" => true, "priority" => 0 }
          ]
        }
      ]
    }
    states = client.send(:parse_snapshot_flags, data)
    prereqs = states.first.prerequisites
    expect(prereqs.length).to eq(1)
    expect(prereqs.first).to be_a(Tombstone::FlagPrerequisite)
    expect(prereqs.first.flag_key).to eq("parent-flag")
    expect(prereqs.first.required_variation).to eq("true")
    expect(prereqs.first.gate).to eq(true)
  end

  it "defaults gate to true (hard-blocking) when the wire omits it, matching flag-api's own AddPrerequisite default" do
    data = {
      "flags" => [
        {
          "flag_key" => "child-flag", "flag_id" => "2", "environment" => "production",
          "enabled" => true, "rollout_pct" => 100, "safe_default" => "false", "updated_at" => 0,
          "prerequisites" => [{ "flag_key" => "parent-flag", "required_variation" => "true" }]
        }
      ]
    }
    states = client.send(:parse_snapshot_flags, data)
    expect(states.first.prerequisites.first.gate).to eq(true)
  end

  it "parses an explicit gate: false as soft" do
    data = {
      "flags" => [
        {
          "flag_key" => "child-flag", "flag_id" => "2", "environment" => "production",
          "enabled" => true, "rollout_pct" => 100, "safe_default" => "false", "updated_at" => 0,
          "prerequisites" => [{ "flag_key" => "parent-flag", "required_variation" => "true", "gate" => false }]
        }
      ]
    }
    states = client.send(:parse_snapshot_flags, data)
    expect(states.first.prerequisites.first.gate).to eq(false)
  end

  it "a flag with no prerequisites key at all parses as an empty array, not an error" do
    data = {
      "flags" => [
        {
          "flag_key" => "known-flag", "flag_id" => "1", "environment" => "production",
          "enabled" => true, "rollout_pct" => 100, "safe_default" => "false", "updated_at" => 0
        }
      ]
    }
    states = client.send(:parse_snapshot_flags, data)
    expect(states.first.prerequisites).to eq([])
  end

  # Regression suite for a SECOND bug: evaluate() called
  # EvaluationEngine#evaluate with only 4 positional args, so flag_lookup
  # defaulted to ->(k) { nil } -- documented there as being for callers with
  # no snapshot access. This client DOES have snapshot access via @cache,
  # but never threaded it through. Before real prerequisites existed, this
  # was dead code; once they're real (the fix above), a nil-returning lookup
  # makes EVERY hard-gated prerequisite permanently blocked regardless of the
  # real dependency's state -- found while verifying this SDK against the
  # identical bug an adversarial review found in the Java SDK's equivalent
  # fix (PR #231). Drives the real, public evaluate()/enabled? entry points
  # end to end (via @cache.load_snapshot, matching client_lag_spec.rb's
  # existing instance_variable_get convention), not
  # PrerequisiteChecker.check_all directly -- which bypasses Client's own
  # wiring entirely and would not have caught this.
  describe "evaluate() prerequisite lookup" do
    def load(*states)
      client.instance_variable_get(:@cache).load_snapshot(states)
    end

    it "resolves a real satisfied hard-gated prerequisite from its own cache" do
      load(
        Tombstone::FlagEnvironmentState.new(
          flag_id: "1", flag_key: "parent-flag", environment: "test",
          enabled: true, rollout_pct: 100, safe_default: "false", updated_at: 0
        ),
        Tombstone::FlagEnvironmentState.new(
          flag_id: "2", flag_key: "child-flag", environment: "test",
          enabled: true, rollout_pct: 100, safe_default: "false", updated_at: 0,
          prerequisites: [Tombstone::FlagPrerequisite.new(flag_key: "parent-flag", required_variation: "true", gate: true)]
        )
      )
      result = client.evaluate("child-flag", Tombstone::EvaluationContext.of("u1"))
      expect(result.value).to eq(true)
      expect(result.reason).not_to eq(Tombstone::EvaluationReason::PREREQUISITE_FAILED)
    end

    it "blocks on a genuinely unmet hard-gated prerequisite" do
      load(
        Tombstone::FlagEnvironmentState.new(
          flag_id: "1", flag_key: "parent-flag", environment: "test",
          enabled: false, rollout_pct: 0, safe_default: "false", updated_at: 0
        ),
        Tombstone::FlagEnvironmentState.new(
          flag_id: "2", flag_key: "child-flag", environment: "test",
          enabled: true, rollout_pct: 100, safe_default: "false", updated_at: 0,
          prerequisites: [Tombstone::FlagPrerequisite.new(flag_key: "parent-flag", required_variation: "true", gate: true)]
        )
      )
      result = client.evaluate("child-flag", Tombstone::EvaluationContext.of("u1"))
      expect(result.reason).to eq(Tombstone::EvaluationReason::PREREQUISITE_FAILED)
    end
  end
end
