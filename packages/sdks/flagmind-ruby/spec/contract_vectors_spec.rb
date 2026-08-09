# packages/sdks/flagmind-ruby/spec/contract_vectors_spec.rb
require "spec_helper"
require "json"
require "set"

# Loads packages/sdks/test-contract/vectors.json and asserts the Ruby SDK's
# evaluation logic matches every vector. This is the executable definition
# of "parity" for this SDK — see docs/SDK_CONTRACT.md.
RSpec.describe "Contract Vectors" do
  let(:engine) { Tombstone::EvaluationEngine.new }
  let(:vectors_path) { File.expand_path("../../test-contract/vectors.json", __dir__) }
  let(:vectors) { JSON.parse(File.read(vectors_path)) }

  describe "hash vectors" do
    it "matches all hash vectors from vectors.json" do
      vectors["vectors"].each do |v|
        flag_key = v["flag_key"]
        user_id = v["user_id"]
        hash_version = v["hash_version"]
        rollout_pct = v["rollout_pct"]
        expected = v["expected_in_cohort"]

        flag = Tombstone::FlagEnvironmentState.new(
          flag_id: "id", flag_key: flag_key, environment: "test",
          enabled: true, rollout_pct: rollout_pct, safe_default: "false", updated_at: 0,
          prerequisites: [], targeting_rules: [], target_list: [], hash_version: hash_version
        )
        context = Tombstone::EvaluationContext.of(user_id)
        result = engine.evaluate(flag, context, false, flag_key)

        expect(result.value).to(eq(expected), "hash vector mismatch for #{flag_key}/#{user_id}")
      end
    end
  end

  describe "prerequisite vectors" do
    it "matches all prerequisite vectors from vectors.json" do
      vectors["prerequisite_vectors"].each do |v|
        id = v["id"]
        prereq_node = v["prerequisite"]
        prereq = Tombstone::FlagPrerequisite.new(
          flag_key: prereq_node["flag_key"],
          required_variation: prereq_node["required_variation"],
          gate: prereq_node["gate"]
        )
        expected_satisfied = v["expected_satisfied"]

        all_flags_node = v["all_flags"]

        seen_keys = Set.new
        if v["seen_keys"]
          v["seen_keys"].each { |k| seen_keys.add(k) }
        end

        # Lookup proc: each "all_flags" entry is {"enabled": bool, "variation": "true"|"false"}.
        lookup = lambda { |key|
          return nil unless all_flags_node.key?(key)
          fn = all_flags_node[key]
          enabled = fn["enabled"]
          variation = fn["variation"]
          rollout_pct = variation == "true" ? 100 : 0
          Tombstone::FlagEnvironmentState.new(
            flag_id: "id", flag_key: key, environment: "test",
            enabled: enabled, rollout_pct: rollout_pct, safe_default: "false", updated_at: 0,
            prerequisites: [], targeting_rules: [], target_list: [], hash_version: 1
          )
        }

        satisfied = Tombstone::PrerequisiteChecker.check_all(
          [prereq], lookup, {}, seen_keys, "parent-flag", engine,
          Tombstone::EvaluationContext.of("u1")
        )

        expect(satisfied).to(eq(expected_satisfied), "prerequisite vector mismatch for #{id}")
      end
    end
  end

  describe "rule vectors" do
    it "matches all rule vectors from vectors.json" do
      vectors["rule_vectors"].each do |v|
        id = v["id"]
        rules_node = v["rules"]
        rules = rules_node.map do |r|
          conditions = r["conditions"].map do |c|
            Tombstone::PropertyCondition.new(
              attribute: c["attribute"], operator: c["operator"],
              values: c["values"], negate: c["negate"]
            )
          end
          Tombstone::TargetingRule.new(
            id: r["id"], conditions: conditions, rollout_pct: r["rollout_pct"],
            variation: r["variation"], priority: r["priority"]
          )
        end

        attrs = v["attrs"]
        user_id = attrs["user_id"] || ""

        expected_node = v["expected_result"]

        context = Tombstone::EvaluationContext.new(user_id: user_id, org_id: "", attrs: attrs)
        result = Tombstone::RuleMatcher.match_rules(rules, context, "test-flag")

        if expected_node.nil?
          expect(result).to(be_nil, "expected no rule match for #{id}")
        else
          expect(result).not_to(be_nil, "expected a rule match for #{id}")
          expect(result).to eq(expected_node["variation"])
        end
      end
    end
  end

  describe "missing attribute vectors" do
    it "matches all missing_attribute vectors from vectors.json" do
      vectors["missing_attribute_vectors"].each do |v|
        id = v["id"]
        expected_node = v["expected_result"]

        condition = Tombstone::PropertyCondition.new(attribute: "missing_attr", operator: "eq", values: ["x"], negate: false)
        rule = Tombstone::TargetingRule.new(id: "r1", conditions: [condition], rollout_pct: 100, variation: "skipped", priority: 0)
        context = Tombstone::EvaluationContext.of("u1")
        result = Tombstone::RuleMatcher.match_rules([rule], context, "test-flag")

        expect(result.nil? == expected_node.nil?).to(be(true), "missing-attribute vector mismatch for #{id}")
      end
    end
  end
end
