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

  describe ".padded_version" do
    it "orders numeric segments correctly" do
      expect(Tombstone::RuleMatcher.padded_version("1.9.0")).to be < Tombstone::RuleMatcher.padded_version("1.10.0")
    end

    it "prerelease sorts below release" do
      expect(Tombstone::RuleMatcher.padded_version("1.0.0-beta")).to be < Tombstone::RuleMatcher.padded_version("1.0.0")
    end

    it "strips v prefix and build metadata" do
      expect(Tombstone::RuleMatcher.padded_version("1.2.3")).to eq(Tombstone::RuleMatcher.padded_version("v1.2.3+build.5"))
    end
  end

  describe ".evaluate_condition semver/date" do
    it "semver_gte" do
      cond = Tombstone::PropertyCondition.new(attribute: "app_version", operator: "semver_gte", values: ["1.9.0"], negate: false)
      context = ctx("app_version" => "1.10.0")
      expect(Tombstone::RuleMatcher.evaluate_condition(cond, context)).to be true
    end

    it "semver prerelease ordering" do
      cond = Tombstone::PropertyCondition.new(attribute: "app_version", operator: "semver_gte", values: ["1.0.0"], negate: false)
      context = ctx("app_version" => "1.0.0-beta")
      expect(Tombstone::RuleMatcher.evaluate_condition(cond, context)).to be false
    end

    it "date_before" do
      cond = Tombstone::PropertyCondition.new(attribute: "signup_date", operator: "date_before", values: ["2026-01-01T00:00:00Z"], negate: false)
      context = ctx("signup_date" => "2025-06-01T00:00:00Z")
      expect(Tombstone::RuleMatcher.evaluate_condition(cond, context)).to be true
    end

    it "date malformed raises InconclusiveMatchError" do
      cond = Tombstone::PropertyCondition.new(attribute: "signup_date", operator: "date_before", values: ["2026-01-01T00:00:00Z"], negate: false)
      context = ctx("signup_date" => "not-a-date")
      expect {
        Tombstone::RuleMatcher.evaluate_condition(cond, context)
      }.to raise_error(Tombstone::InconclusiveMatchError)
    end

    it "date empty values raises InconclusiveMatchError" do
      cond = Tombstone::PropertyCondition.new(attribute: "signup_date", operator: "date_before", values: [], negate: false)
      context = ctx("signup_date" => "2025-06-01T00:00:00Z")
      expect {
        Tombstone::RuleMatcher.evaluate_condition(cond, context)
      }.to raise_error(Tombstone::InconclusiveMatchError)
    end

    it "semver empty values raises InconclusiveMatchError" do
      cond = Tombstone::PropertyCondition.new(attribute: "app_version", operator: "semver_gte", values: [], negate: false)
      context = ctx("app_version" => "1.0.0")
      expect {
        Tombstone::RuleMatcher.evaluate_condition(cond, context)
      }.to raise_error(Tombstone::InconclusiveMatchError)
    end
  end
end
