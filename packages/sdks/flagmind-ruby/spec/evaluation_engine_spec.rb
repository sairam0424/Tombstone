require "spec_helper"
require "tombstone"

RSpec.describe Tombstone::EvaluationEngine do
  let(:engine) { described_class.new }
  let(:ctx) { Tombstone::EvaluationContext.of("user-abc-123") }

  def flag(enabled:, pct:)
    Tombstone::FlagEnvironmentState.new(
      flag_id: "id1", flag_key: "test-flag", environment: "test",
      enabled: enabled, rollout_pct: pct, safe_default: "false", updated_at: 0
    )
  end

  it "returns OFF when disabled" do
    r = engine.evaluate(flag(enabled: false, pct: 100), ctx, false, "test-flag")
    expect(r.reason).to eq(Tombstone::EvaluationReason::OFF)
    expect(r.value).to be false
  end

  it "returns true at 100% rollout" do
    r = engine.evaluate(flag(enabled: true, pct: 100), ctx, false, "test-flag")
    expect(r.reason).to eq(Tombstone::EvaluationReason::FALLTHROUGH)
    expect(r.value).to be true
  end

  it "returns false at 0% rollout" do
    r = engine.evaluate(flag(enabled: true, pct: 0), ctx, false, "test-flag")
    expect(r.value).to be false
  end

  it "returns ERROR for nil flag state" do
    r = engine.evaluate(nil, ctx, false, "missing")
    expect(r.reason).to eq(Tombstone::EvaluationReason::ERROR)
  end

  it "is sticky — same user always gets same result" do
    f = flag(enabled: true, pct: 50)
    results = 20.times.map { engine.evaluate(f, ctx, false, "test-flag").value }.uniq
    expect(results.size).to eq(1)
  end

  describe "MurmurHash3 parity with TypeScript" do
    [
      ["checkout-v2", "user-abc-123", 100, true],
      ["checkout-v2", "user-abc-123", 0, false],
      ["checkout-v2", "user-xyz-789", 50, false],
    ].each do |flag_key, user_id, pct, expected|
      it "#{flag_key} #{user_id} #{pct}% → #{expected}" do
        f = flag(enabled: true, pct: pct)
        c = Tombstone::EvaluationContext.of(user_id)
        r = engine.evaluate(f, c, false, flag_key)
        expect(r.value).to eq(expected)
      end
    end
  end

  describe "full 5-step pipeline" do
    it "prerequisite hard gate blocks evaluation" do
      base_flag = flag(enabled: false, pct: 0)
      prereq = Tombstone::FlagPrerequisite.new(flag_key: "base-flag", required_variation: "true", gate: true)
      parent_flag = Tombstone::FlagEnvironmentState.new(
        flag_id: "id-1", flag_key: "parent-flag", environment: "test",
        enabled: true, rollout_pct: 100, safe_default: "false", updated_at: 0,
        prerequisites: [prereq], targeting_rules: [], target_list: [], hash_version: 1
      )
      lookup = ->(key) { key == "base-flag" ? base_flag : nil }

      result = engine.evaluate(parent_flag, ctx, false, "parent-flag", flag_lookup: lookup, prerequisite_cache: {}, seen_keys: Set.new)

      expect(result.reason).to eq(Tombstone::EvaluationReason::PREREQUISITE_FAILED)
      expect(result.value).to be false
    end

    it "target list match returns true" do
      target_flag = Tombstone::FlagEnvironmentState.new(
        flag_id: "id-1", flag_key: "test-flag", environment: "test",
        enabled: true, rollout_pct: 0, safe_default: "false", updated_at: 0,
        prerequisites: [], targeting_rules: [], target_list: ["user-abc-123"], hash_version: 1
      )

      result = engine.evaluate(target_flag, ctx, false, "test-flag")

      expect(result.reason).to eq(Tombstone::EvaluationReason::TARGET_MATCH)
      expect(result.value).to be true
    end

    it "rule match returns rule variation" do
      condition = Tombstone::PropertyCondition.new(attribute: "plan", operator: "eq", values: ["pro"], negate: false)
      rule = Tombstone::TargetingRule.new(id: "r1", conditions: [condition], rollout_pct: 100, variation: "matched-variation", priority: 0)
      rule_flag = Tombstone::FlagEnvironmentState.new(
        flag_id: "id-1", flag_key: "test-flag", environment: "test",
        enabled: true, rollout_pct: 0, safe_default: "false", updated_at: 0,
        prerequisites: [], targeting_rules: [rule], target_list: [], hash_version: 1
      )
      pro_context = Tombstone::EvaluationContext.new(user_id: "u1", org_id: "", attrs: { "plan" => "pro" })

      result = engine.evaluate(rule_flag, pro_context, "default-value", "test-flag")

      expect(result.reason).to eq(Tombstone::EvaluationReason::RULE_MATCH)
      expect(result.value).to eq("matched-variation")
    end

    it "hash version 2 uses FNV-1a" do
      # Vector from test-contract/vectors.json: checkout-v2/user-abc-123, v2, expected_bucket=0.343.
      # rollout_pct=30 -> bucket 0.343 >= 0.30 -> NOT in cohort -> default returned.
      v2_flag = Tombstone::FlagEnvironmentState.new(
        flag_id: "id-1", flag_key: "checkout-v2", environment: "test",
        enabled: true, rollout_pct: 30, safe_default: "false", updated_at: 0,
        prerequisites: [], targeting_rules: [], target_list: [], hash_version: 2
      )
      v2_context = Tombstone::EvaluationContext.of("user-abc-123")

      result = engine.evaluate(v2_flag, v2_context, false, "checkout-v2")

      expect(result.value).to be false
      expect(result.reason).to eq(Tombstone::EvaluationReason::FALLTHROUGH)
    end
  end
end
