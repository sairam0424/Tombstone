require "spec_helper"

RSpec.describe "Types" do
  describe "FlagEnvironmentState" do
    it "constructs with all fields including new parity fields" do
      prereq = Tombstone::FlagPrerequisite.new(
        flag_key: "base-flag", required_variation: "true", gate: true
      )
      condition = Tombstone::PropertyCondition.new(
        attribute: "plan", operator: "eq", values: ["pro"], negate: false
      )
      rule = Tombstone::TargetingRule.new(
        id: "r1", conditions: [condition], rollout_pct: 100, variation: "matched", priority: 0
      )

      state = Tombstone::FlagEnvironmentState.new(
        flag_id: "id-1", flag_key: "test-flag", environment: "test",
        enabled: true, rollout_pct: 50, safe_default: "false", updated_at: 0,
        prerequisites: [prereq], targeting_rules: [rule], target_list: ["user-1"], hash_version: 2
      )

      expect(state.prerequisites.size).to eq(1)
      expect(state.prerequisites.first.flag_key).to eq("base-flag")
      expect(state.targeting_rules.size).to eq(1)
      expect(state.targeting_rules.first.conditions.first.attribute).to eq("plan")
      expect(state.target_list).to eq(["user-1"])
      expect(state.hash_version).to eq(2)
    end

    it "defaults hash_version to 1 and other new fields to empty arrays" do
      state = Tombstone::FlagEnvironmentState.new(
        flag_id: "id-1", flag_key: "test-flag", environment: "test",
        enabled: true, rollout_pct: 50, safe_default: "false", updated_at: 0
      )

      expect(state.hash_version).to eq(1)
      expect(state.prerequisites).to eq([])
      expect(state.targeting_rules).to eq([])
      expect(state.target_list).to eq([])
    end
  end

  describe "FlagPrerequisite" do
    it "constructs with all fields" do
      prereq = Tombstone::FlagPrerequisite.new(
        flag_key: "dep", required_variation: "true", gate: false
      )
      expect(prereq.flag_key).to eq("dep")
      expect(prereq.required_variation).to eq("true")
      expect(prereq.gate).to be false
    end
  end

  describe "TargetingRule" do
    it "constructs with all fields" do
      condition = Tombstone::PropertyCondition.new(
        attribute: "age", operator: "gte", values: ["18"], negate: false
      )
      rule = Tombstone::TargetingRule.new(
        id: "r1", conditions: [condition], rollout_pct: 50, variation: "test", priority: 0
      )
      expect(rule.id).to eq("r1")
      expect(rule.conditions.size).to eq(1)
      expect(rule.rollout_pct).to eq(50)
      expect(rule.variation).to eq("test")
      expect(rule.priority).to eq(0)
    end
  end

  describe "PropertyCondition" do
    it "constructs with all fields" do
      cond = Tombstone::PropertyCondition.new(
        attribute: "plan", operator: "eq", values: ["pro", "enterprise"], negate: true
      )
      expect(cond.attribute).to eq("plan")
      expect(cond.operator).to eq("eq")
      expect(cond.values).to eq(["pro", "enterprise"])
      expect(cond.negate).to be true
    end
  end
end
