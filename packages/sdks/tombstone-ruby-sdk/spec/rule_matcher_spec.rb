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

    # Found by adversarial review of PR #248: "".split(".") is [] in Ruby, so
    # without an explicit guard the segments.each loop never runs and
    # `current` falls through UNCHANGED from context.attrs (the WHOLE attrs
    # Hash), not nil -- silently stringifying the entire Hash for contains/
    # startswith/endswith instead of being treated as "attribute not
    # present". Reachable via a malformed/legacy targeting-rule row missing
    # "attribute" on the wire (client.rb's parse_targeting_rules defaults it
    # to "").
    it "returns nil for an empty-string attribute, not the whole attrs hash" do
      context = ctx("email" => "x@y.com")
      expect(Tombstone::RuleMatcher.resolve_attribute("", context)).to be_nil
    end

    it "returns nil for a nil attribute" do
      context = ctx("email" => "x@y.com")
      expect(Tombstone::RuleMatcher.resolve_attribute(nil, context)).to be_nil
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

    # flag-api's targeting_rules.operator CHECK constraint (schema.sql) has
    # GEO_COUNTRY/GEO_REGION as real operator VALUES, not just an
    # attribute-name convention -- normalize_operator must map both to "in"
    # so they reach the case-insensitive is_geo branch above, or every
    # GEO_COUNTRY/GEO_REGION rule from a real backend response would raise
    # InconclusiveMatchError (unknown operator) and never match for any
    # user. Found while wiring the real backend wire format into this SDK
    # for the first time.
    it "GEO_COUNTRY operator is recognized" do
      cond = Tombstone::PropertyCondition.new(attribute: "geo.country", operator: "GEO_COUNTRY", values: ["US", "CA"], negate: false)
      expect(Tombstone::RuleMatcher.evaluate_condition(cond, ctx("geo" => { "country" => "us" }))).to be true
    end

    it "GEO_REGION operator is recognized" do
      cond = Tombstone::PropertyCondition.new(attribute: "geo.region", operator: "GEO_REGION", values: ["CA-ON"], negate: false)
      expect(Tombstone::RuleMatcher.evaluate_condition(cond, ctx("geo" => { "region" => "CA-QC" }))).to be false
    end

    # Found by adversarial review of the Java SDK's identical fix (PR #247):
    # is_geo was originally decided purely by attribute name
    # (GEO_ATTRIBUTES.include?(attribute)), so a GEO_COUNTRY rule using a
    # non-canonical attribute name (nothing validates that operator=
    # GEO_COUNTRY implies attribute=="geo.country") silently fell back to
    # case-SENSITIVE matching instead of the case-insensitive semantics the
    # operator itself declares. Checked here proactively.
    it "GEO_COUNTRY operator is case-insensitive even with a non-canonical attribute name" do
      cond = Tombstone::PropertyCondition.new(attribute: "country", operator: "GEO_COUNTRY", values: ["US"], negate: false)
      expect(Tombstone::RuleMatcher.evaluate_condition(cond, ctx("country" => "us"))).to be true
    end

    it "GEO_REGION operator is case-insensitive even with a non-canonical attribute name" do
      cond = Tombstone::PropertyCondition.new(attribute: "region", operator: "GEO_REGION", values: ["CA-ON"], negate: false)
      expect(Tombstone::RuleMatcher.evaluate_condition(cond, ctx("region" => "ca-on"))).to be true
    end

    # An EMPTY values list must never match "neq"/"nin": !values.include?(x)
    # on an empty list is vacuously true, which would make a rule with an
    # empty/missing "values" list match EVERY context unconditionally --
    # the same bug class found and fixed in the TypeScript SDK's NOT_IN
    # operator and the Java SDK's "neq"/"nin" branch (adversarial review of
    # PR #246/#247), which explicitly flagged this as likely present in the
    # other SDKs too. Confirmed here.
    it "NOT_IN with empty values never matches" do
      cond = Tombstone::PropertyCondition.new(attribute: "plan", operator: "not_in", values: [], negate: false)
      expect(Tombstone::RuleMatcher.evaluate_condition(cond, ctx("plan" => "anything"))).to be false
    end

    it "NOT_IN with empty values never matches for a geo attribute" do
      cond = Tombstone::PropertyCondition.new(attribute: "geo.country", operator: "not_in", values: [], negate: false)
      expect(Tombstone::RuleMatcher.evaluate_condition(cond, ctx("geo" => { "country" => "US" }))).to be false
    end

    it "NOT_IN with non-empty values still excludes correctly" do
      cond = Tombstone::PropertyCondition.new(attribute: "plan", operator: "not_in", values: ["banned", "suspended"], negate: false)
      expect(Tombstone::RuleMatcher.evaluate_condition(cond, ctx("plan" => "banned"))).to be false
      expect(Tombstone::RuleMatcher.evaluate_condition(cond, ctx("plan" => "pro"))).to be true
    end

    # End-to-end proof (not just resolve_attribute's own unit test above)
    # that a CONTAINS condition with an empty attribute is skipped as
    # inconclusive rather than spuriously matching against the stringified
    # attrs Hash. Found by adversarial review of PR #248.
    it "an empty attribute raises InconclusiveMatchError instead of spuriously matching the stringified attrs hash" do
      cond = Tombstone::PropertyCondition.new(attribute: "", operator: "contains", values: ["email"], negate: false)
      expect {
        Tombstone::RuleMatcher.evaluate_condition(cond, ctx("email" => "x@y.com"))
      }.to raise_error(Tombstone::InconclusiveMatchError)
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

    # docs/SDK_CONTRACT.md:32 -- REGEX is declared but deliberately NOT
    # implemented in this release, across all 5 SDKs. It must return a
    # definite `false` (matching TS's documented behavior), NOT raise
    # InconclusiveMatchError like a genuinely unknown operator would --
    # found by adversarial review of PR #248.
    it "REGEX returns false rather than raising, per the documented (not yet implemented) contract" do
      cond = Tombstone::PropertyCondition.new(attribute: "email", operator: "REGEX", values: ["^admin.*@corp\\.com$"], negate: false)
      expect(Tombstone::RuleMatcher.evaluate_condition(cond, ctx("email" => "admin1@corp.com"))).to be false
    end

    it "a negated REGEX condition returns true, per the contract's literal negate semantics" do
      cond = Tombstone::PropertyCondition.new(attribute: "email", operator: "REGEX", values: ["^admin.*@corp\\.com$"], negate: true)
      expect(Tombstone::RuleMatcher.evaluate_condition(cond, ctx("email" => "admin1@corp.com"))).to be true
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

  describe ".match_rules" do
    it "first priority wins" do
      cond = Tombstone::PropertyCondition.new(attribute: "plan", operator: "eq", values: ["pro"], negate: false)
      r1 = Tombstone::TargetingRule.new(id: "r1", conditions: [cond], rollout_pct: 100, variation: "variant-a", priority: 0)
      r2 = Tombstone::TargetingRule.new(id: "r2", conditions: [cond], rollout_pct: 100, variation: "variant-b", priority: 1)
      result = Tombstone::RuleMatcher.match_rules([r2, r1], ctx("plan" => "pro"), "test-flag")
      expect(result).to eq("variant-a")
    end

    it "multi-condition AND both match" do
      c1 = Tombstone::PropertyCondition.new(attribute: "plan", operator: "eq", values: ["pro"], negate: false)
      c2 = Tombstone::PropertyCondition.new(attribute: "region", operator: "eq", values: ["us"], negate: false)
      rule = Tombstone::TargetingRule.new(id: "r1", conditions: [c1, c2], rollout_pct: 100, variation: "match", priority: 0)
      result = Tombstone::RuleMatcher.match_rules([rule], ctx("plan" => "pro", "region" => "us"), "test-flag")
      expect(result).to eq("match")
    end

    it "multi-condition AND one fails" do
      c1 = Tombstone::PropertyCondition.new(attribute: "plan", operator: "eq", values: ["pro"], negate: false)
      c2 = Tombstone::PropertyCondition.new(attribute: "region", operator: "eq", values: ["us"], negate: false)
      rule = Tombstone::TargetingRule.new(id: "r1", conditions: [c1, c2], rollout_pct: 100, variation: "match", priority: 0)
      result = Tombstone::RuleMatcher.match_rules([rule], ctx("plan" => "pro", "region" => "eu"), "test-flag")
      expect(result).to be_nil
    end

    it "no match falls through" do
      cond = Tombstone::PropertyCondition.new(attribute: "plan", operator: "eq", values: ["enterprise"], negate: false)
      rule = Tombstone::TargetingRule.new(id: "r1", conditions: [cond], rollout_pct: 100, variation: "match", priority: 0)
      result = Tombstone::RuleMatcher.match_rules([rule], ctx("plan" => "free"), "test-flag")
      expect(result).to be_nil
    end

    it "inconclusive condition skips to next rule" do
      missing_cond = Tombstone::PropertyCondition.new(attribute: "missing_attr", operator: "eq", values: ["x"], negate: false)
      pro_cond = Tombstone::PropertyCondition.new(attribute: "plan", operator: "eq", values: ["pro"], negate: false)
      r1 = Tombstone::TargetingRule.new(id: "r1", conditions: [missing_cond], rollout_pct: 100, variation: "skipped", priority: 0)
      r2 = Tombstone::TargetingRule.new(id: "r2", conditions: [pro_cond], rollout_pct: 100, variation: "fallback-match", priority: 1)
      result = Tombstone::RuleMatcher.match_rules([r1, r2], ctx("plan" => "pro"), "test-flag")
      expect(result).to eq("fallback-match")
    end

    it "per-rule rollout sub-bucketing falls to next rule" do
      cond = Tombstone::PropertyCondition.new(attribute: "plan", operator: "eq", values: ["pro"], negate: false)
      r1 = Tombstone::TargetingRule.new(id: "r1", conditions: [cond], rollout_pct: 0, variation: "never", priority: 0)
      r2 = Tombstone::TargetingRule.new(id: "r2", conditions: [cond], rollout_pct: 100, variation: "fallback", priority: 1)
      result = Tombstone::RuleMatcher.match_rules([r1, r2], ctx("plan" => "pro"), "test-flag")
      expect(result).to eq("fallback")
    end
  end
end
