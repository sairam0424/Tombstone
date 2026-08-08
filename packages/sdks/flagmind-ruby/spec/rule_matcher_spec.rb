require "spec_helper"

RSpec.describe Tombstone::RuleMatcher do
  def ctx(attrs)
    Tombstone::EvaluationContext.new(user_id: "u1", org_id: "", attrs: attrs)
  end

  describe ".resolve_attribute" do
    it "resolves flat key" do
      context = ctx("plan" => "pro")
      expect(Tombstone::RuleMatcher.resolve_attribute("plan", context)).to eq("pro")
    end

    it "returns nil for missing attribute" do
      context = ctx({})
      expect(Tombstone::RuleMatcher.resolve_attribute("missing", context)).to be_nil
    end

    it "resolves dot-notation nested paths" do
      context = ctx("geo" => { "country" => "us" })
      expect(Tombstone::RuleMatcher.resolve_attribute("geo.country", context)).to eq("us")
    end
  end

  describe ".evaluate_condition" do
    it "eq match" do
      cond = Tombstone::PropertyCondition.new(attribute: "plan", operator: "eq", values: ["pro"], negate: false)
      expect(Tombstone::RuleMatcher.evaluate_condition(cond, ctx("plan" => "pro"))).to be true
    end

    it "eq no match" do
      cond = Tombstone::PropertyCondition.new(attribute: "plan", operator: "eq", values: ["pro"], negate: false)
      expect(Tombstone::RuleMatcher.evaluate_condition(cond, ctx("plan" => "free"))).to be false
    end

    it "contains case-insensitive" do
      cond = Tombstone::PropertyCondition.new(attribute: "email", operator: "contains", values: ["ACME"], negate: false)
      expect(Tombstone::RuleMatcher.evaluate_condition(cond, ctx("email" => "user@acme.com"))).to be true
    end

    it "numeric gt" do
      cond = Tombstone::PropertyCondition.new(attribute: "age", operator: "gt", values: ["18"], negate: false)
      expect(Tombstone::RuleMatcher.evaluate_condition(cond, ctx("age" => "21"))).to be true
    end

    it "numeric non-numeric raises InconclusiveMatchError" do
      cond = Tombstone::PropertyCondition.new(attribute: "age", operator: "gt", values: ["18"], negate: false)
      expect {
        Tombstone::RuleMatcher.evaluate_condition(cond, ctx("age" => "not-a-number"))
      }.to raise_error(Tombstone::InconclusiveMatchError)
    end

    it "missing attribute raises InconclusiveMatchError" do
      cond = Tombstone::PropertyCondition.new(attribute: "missing_attr", operator: "eq", values: ["x"], negate: false)
      expect {
        Tombstone::RuleMatcher.evaluate_condition(cond, ctx({}))
      }.to raise_error(Tombstone::InconclusiveMatchError)
    end

    it "negate inverts result" do
      cond = Tombstone::PropertyCondition.new(attribute: "plan", operator: "eq", values: ["pro"], negate: true)
      expect(Tombstone::RuleMatcher.evaluate_condition(cond, ctx("plan" => "pro"))).to be false
    end

    it "geo case-insensitive" do
      # Canonical model resolves "geo.country" via dot-notation nesting
      # (attrs["geo"]["country"]), not as a flat literal key.
      cond = Tombstone::PropertyCondition.new(attribute: "geo.country", operator: "in", values: ["US", "CA"], negate: false)
      expect(Tombstone::RuleMatcher.evaluate_condition(cond, ctx("geo" => { "country" => "us" }))).to be true
    end
  end
end
