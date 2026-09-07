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

  # Mirrors the prerequisites-parsing tests above exactly, for
  # targeting_rules (added by flag-api PR #245). flag-api's real per-rule
  # wire shape: "id"/"rule_type"/"attribute"/"operator"/"values"/
  # "variation"/"priority" -- ONE condition per rule row, adapted into
  # this SDK's own richer TargetingRule(conditions, rollout_pct, variation,
  # priority) model by parse_targeting_rules (see that method's own comment
  # for why rollout_pct is always 100 for a wire-parsed rule).
  it "parses real targeting_rules using the flat wire shape into the richer TargetingRule model" do
    data = {
      "flags" => [
        {
          "flag_id" => "2", "flag_key" => "child-flag", "environment" => "production",
          "enabled" => true, "rollout_pct" => 100, "safe_default" => "false", "updated_at" => 1_700_000_000,
          "targeting_rules" => [
            { "id" => "rule-1", "rule_type" => "USER", "attribute" => "email", "operator" => "CONTAINS",
              "values" => ["@acme.com"], "variation" => "true", "priority" => 3 }
          ]
        }
      ]
    }
    states = client.send(:parse_snapshot_flags, data)
    rules = states.first.targeting_rules
    expect(rules.length).to eq(1)
    expect(rules.first).to be_a(Tombstone::TargetingRule)
    expect(rules.first.id).to eq("rule-1")
    expect(rules.first.variation).to eq("true")
    expect(rules.first.priority).to eq(3)
    expect(rules.first.rollout_pct).to eq(100)
    expect(rules.first.conditions.length).to eq(1)
    expect(rules.first.conditions.first.attribute).to eq("email")
    expect(rules.first.conditions.first.operator).to eq("CONTAINS")
    expect(rules.first.conditions.first.values).to eq(["@acme.com"])
  end

  it "targeting rule numeric values parse as their string representation" do
    # "values" can carry JSON numbers (e.g. for GT/GTE/LT/LTE operators) --
    # must round-trip as the plain string form Float() can consume, not
    # e.g. "18.0" for an integer 18.
    data = {
      "flags" => [
        {
          "flag_id" => "2", "flag_key" => "child-flag", "environment" => "production",
          "enabled" => true, "rollout_pct" => 100, "safe_default" => "false", "updated_at" => 1_700_000_000,
          "targeting_rules" => [
            { "id" => "rule-1", "rule_type" => "CUSTOM", "attribute" => "age", "operator" => "GTE",
              "values" => [18], "variation" => "true", "priority" => 0 }
          ]
        }
      ]
    }
    states = client.send(:parse_snapshot_flags, data)
    condition = states.first.targeting_rules.first.conditions.first
    expect(condition.values).to eq(["18"])
    expect(Tombstone::RuleMatcher.evaluate_condition(
      condition, Tombstone::EvaluationContext.new(user_id: "u1", org_id: "", attrs: { "age" => "21" })
    )).to be true
  end

  # Found by adversarial review of PR #248: a JSON float that happens to be
  # a whole number (e.g. flag-api's JSONB "values" column round-tripping
  # [21.0, 65.0]) must render as "21", not "21.0", or an EQ/IN/NEQ/NIN
  # condition silently fails to match/exclude a context attribute supplied
  # as a plain Integer (21) or bare numeric string ("21") -- the natural way
  # an app would populate EvaluationContext.attrs.
  it "a whole-number JSON float in targeting rule values round-trips without a trailing .0" do
    data = {
      "flags" => [
        {
          "flag_id" => "2", "flag_key" => "child-flag", "environment" => "production",
          "enabled" => true, "rollout_pct" => 100, "safe_default" => "false", "updated_at" => 1_700_000_000,
          "targeting_rules" => [
            { "id" => "rule-1", "rule_type" => "CUSTOM", "attribute" => "age_bracket", "operator" => "IN",
              "values" => [21.0, 65.0], "variation" => "on", "priority" => 0 }
          ]
        }
      ]
    }
    states = client.send(:parse_snapshot_flags, data)
    condition = states.first.targeting_rules.first.conditions.first
    expect(condition.values).to eq(["21", "65"])
    expect(Tombstone::RuleMatcher.evaluate_condition(
      condition, Tombstone::EvaluationContext.new(user_id: "u1", org_id: "", attrs: { "age_bracket" => 21 })
    )).to be true
  end

  it "a non-whole JSON float in targeting rule values preserves its fractional part" do
    data = {
      "flags" => [
        {
          "flag_id" => "2", "flag_key" => "child-flag", "environment" => "production",
          "enabled" => true, "rollout_pct" => 100, "safe_default" => "false", "updated_at" => 1_700_000_000,
          "targeting_rules" => [
            { "id" => "rule-1", "rule_type" => "CUSTOM", "attribute" => "score", "operator" => "EQ",
              "values" => [21.5], "variation" => "on", "priority" => 0 }
          ]
        }
      ]
    }
    states = client.send(:parse_snapshot_flags, data)
    condition = states.first.targeting_rules.first.conditions.first
    expect(condition.values).to eq(["21.5"])
  end

  it "a flag with no targeting_rules key at all parses as an empty array, not an error" do
    data = {
      "flags" => [
        {
          "flag_key" => "known-flag", "flag_id" => "1", "environment" => "production",
          "enabled" => true, "rollout_pct" => 100, "safe_default" => "false", "updated_at" => 0
        }
      ]
    }
    states = client.send(:parse_snapshot_flags, data)
    expect(states.first.targeting_rules).to eq([])
  end

  # Found by adversarial review of PR #248: parse_targeting_rules' own
  # `next unless r.is_a?(Hash)` guard (inside a filter_map over the
  # "targeting_rules" array) had zero test coverage -- deleting that single
  # line left all existing specs green, since none of them ever fed a
  # non-Hash entry (e.g. a stray JSON null) into the array.
  it "a malformed non-Hash entry inside targeting_rules is skipped, not raised" do
    data = {
      "flags" => [
        {
          "flag_id" => "2", "flag_key" => "child-flag", "environment" => "production",
          "enabled" => true, "rollout_pct" => 100, "safe_default" => "false", "updated_at" => 1_700_000_000,
          "targeting_rules" => [
            nil,
            { "id" => "rule-1", "attribute" => "email", "operator" => "EQ",
              "values" => ["x@example.com"], "variation" => "matched", "priority" => 0 }
          ]
        }
      ]
    }
    states = client.send(:parse_snapshot_flags, data)
    rules = states.first.targeting_rules
    expect(rules.length).to eq(1)
    expect(rules.first.id).to eq("rule-1")
  end

  # Found by adversarial review of PR #248: the `r["values"].is_a?(Array)`
  # guard inside parse_targeting_rules had zero test coverage -- a wire row
  # that omits "values" entirely (or sends it as null) had never been
  # exercised, even though the guard exists specifically to handle it.
  it "a targeting rule missing the values key entirely parses with empty values, not an error" do
    data = {
      "flags" => [
        {
          "flag_id" => "2", "flag_key" => "child-flag", "environment" => "production",
          "enabled" => true, "rollout_pct" => 100, "safe_default" => "false", "updated_at" => 1_700_000_000,
          "targeting_rules" => [
            { "id" => "rule-1", "attribute" => "email", "operator" => "EQ", "variation" => "matched", "priority" => 0 }
          ]
        }
      ]
    }
    states = client.send(:parse_snapshot_flags, data)
    rules = states.first.targeting_rules
    expect(rules.length).to eq(1)
    expect(rules.first.conditions.first.values).to eq([])
  end

  # End-to-end proof that a targeting rule parsed from a REAL snapshot
  # response (via parse_snapshot_flags, not a hand-built
  # FlagEnvironmentState) actually reaches evaluate() and changes its
  # outcome -- mirrors the two prerequisite tests below exactly, closing the
  # identical gap for targeting_rules.
  it "evaluate resolves a real rule match from a snapshot parsed by the real wire parser" do
    client_with_default = described_class.new(sdk_key: "sdk-test-key", environment: "test", defaults: { "child-flag" => "off" })
    data = {
      "flags" => [
        {
          "flag_id" => "2", "flag_key" => "child-flag", "environment" => "production",
          "enabled" => true, "rollout_pct" => 0, "safe_default" => "off", "updated_at" => 1_700_000_000,
          "targeting_rules" => [
            { "id" => "rule-1", "rule_type" => "USER", "attribute" => "email", "operator" => "EQ",
              "values" => ["x@example.com"], "variation" => "matched", "priority" => 0 }
          ]
        }
      ]
    }
    states = client_with_default.send(:parse_snapshot_flags, data)
    client_with_default.instance_variable_get(:@cache).load_snapshot(states, 1_700_000_000)

    result = client_with_default.evaluate(
      "child-flag", Tombstone::EvaluationContext.new(user_id: "u1", org_id: "", attrs: { "email" => "x@example.com" })
    )
    expect(result.reason).to eq(Tombstone::EvaluationReason::RULE_MATCH)
    expect(result.value).to eq("matched")
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
