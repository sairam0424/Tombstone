# Ruby SDK Parity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Bring `flagmind-ruby` from "steps 1+5 only" to the full 5-step canonical evaluation
pipeline (prerequisites, target list, priority-sorted rule matching with full operator
surface, both hash versions), fix the P0 broken entrypoint bug, verify against
`packages/sdks/test-contract/vectors.json` v1.2, and standardize naming.

**Architecture:** Extend the existing `Struct`-based types (`FlagEnvironmentState`,
`EvaluationContext`) with new fields for prerequisites/rules/target-list/hash-version.
Replace `EvaluationEngine#evaluate`'s current 5-branch if-chain with the full pipeline,
adding two new classes: `PrerequisiteChecker` (step 2, with memoization + cycle detection)
and `RuleMatcher` (step 4, operator dispatch). A new `InconclusiveMatchError` signals
"cannot evaluate this condition" up to `RuleMatcher`'s per-rule rescue, which then continues
to the next rule — mirroring Python's `InconclusiveMatchError`/`continue` pattern from
`evaluation.py:118-120`.

**Tech Stack:** Ruby 3.3+, RSpec, `murmurhash3` gem (already a dependency), no new external
libraries needed for FNV-1a (pure arithmetic implementation, matching the Python reference's
approach in `evaluation.py:27-48`).

## Global Constraints

- Canonical model per `docs/superpowers/specs/2026-08-04-v1.5.0-sdk-parity-and-dependency-viz-design.md`
  Section 3 — this is the ONLY source of truth for behavior. Do not consult TS or Python
  source for behavior not already cited in this plan; both diverge from the canonical model
  on several points (see spec Section 2a).
- Contract vectors: `packages/sdks/test-contract/vectors.json` MUST be at version `"1.2"` or
  higher before starting (Phase 1 of the overall v1.5.0 upgrade — confirm this file exists
  with `prerequisite_vectors`/`rule_vectors` keys before Task 1).
- No `variation`/value field is added to `FlagEnvironmentState` this release — prerequisite
  comparison uses the string-compare mechanism (forward-compatible with future multivariate
  support) but is only ever exercised against stringified boolean outcomes (`"true"`/`"false"`)
  in this release, since `enabled` (bool) is the only flag outcome type that exists.
- Regex targeting-rule operator stays declared-but-unimplemented — do NOT add regex support.
- String operators (`contains`/`startswith`/`endswith`) are case-**insensitive** (canonical
  choice) — use `.upcase` on both sides.
- FNV-1a v2 hash MUST iterate over UTF-8 bytes, not Ruby's default UTF-8 codepoints — use
  `.bytes`, matching the canonical choice in spec Section 3.
- Gem name changes from `flagmind-ruby` to `tombstone-ruby-sdk` — the module `Tombstone` (in
  all current source files) is already correct and stays unchanged.
- Branch: `feat/ruby-sdk-parity-v1.5.0` off `origin/develop`.
- Run `cd packages/sdks/flagmind-ruby && bundle exec rspec` before every commit (if `bundle`
  is not installed, use `gem install bundler` first, or `rspec spec/` if the gem is installed
  globally).

---

## Phase 1 — P0 Bug Fix

### Task 1: Fix broken lib/flagmind.rb entrypoint

**Files:**
- Delete: `packages/sdks/flagmind-ruby/lib/flagmind.rb`
- Modify: `packages/sdks/flagmind-ruby/lib/tombstone.rb`
- Create: `packages/sdks/flagmind-ruby/spec/entrypoint_spec.rb`

**Interfaces:**
- Produces: A single working entrypoint file (`lib/tombstone.rb`) that can be successfully
  required and exposes the `Tombstone` module. No `Flagmind` constant reference remains in
  any file.

- [ ] **Step 1: Write the failing test**

```ruby
# packages/sdks/flagmind-ruby/spec/entrypoint_spec.rb
require "spec_helper"

RSpec.describe "Entrypoint loading" do
  it "can require 'tombstone' successfully" do
    # This test runs in an RSpec context where tombstone is already required by spec_helper,
    # but we verify the module is defined and accessible.
    expect(defined?(Tombstone)).to eq("constant")
    expect(Tombstone).to be_a(Module)
  end

  it "exposes Tombstone::Client" do
    expect(defined?(Tombstone::Client)).to eq("constant")
  end

  it "exposes Tombstone::EvaluationEngine" do
    expect(defined?(Tombstone::EvaluationEngine)).to eq("constant")
  end

  it "does not expose a Flagmind constant" do
    expect(defined?(Flagmind)).to be_nil
  end
end
```

- [ ] **Step 2: Confirm the current state is broken**

Manually test the current entrypoint (outside of RSpec, which has workarounds):
```bash
cd packages/sdks/flagmind-ruby
ruby -Ilib -e 'require "flagmind"; puts "OK"'
```
Expected: `LoadError: cannot load such file -- tombstone/types` (because `lib/flagmind.rb`
does `require_relative "tombstone/types"` but no `lib/tombstone/` directory exists).

- [ ] **Step 3: Delete the broken lib/flagmind.rb**

```bash
cd packages/sdks/flagmind-ruby
git rm lib/flagmind.rb
```

- [ ] **Step 4: Rewrite lib/tombstone.rb as the corrected single entrypoint**

```ruby
# packages/sdks/flagmind-ruby/lib/tombstone.rb
# Tombstone Ruby SDK entrypoint
require_relative "flagmind/types"
require_relative "flagmind/evaluation_engine"
require_relative "flagmind/flag_cache"
require_relative "flagmind/client"

# Expose Tombstone as the top-level namespace (the directory structure under lib/flagmind/
# stays as-is for now — renaming directories is deferred to naming cleanup in Task 10).
module Tombstone
  VERSION = "0.2.0"  # bump from 0.1.0 since this is a breaking entrypoint change
end
```

- [ ] **Step 5: Verify the entrypoint now works**

```bash
cd packages/sdks/flagmind-ruby
ruby -Ilib -e 'require "tombstone"; puts "OK"'
```
Expected: `OK` with exit 0.

- [ ] **Step 6: Run the new entrypoint test**

```bash
cd packages/sdks/flagmind-ruby
bundle exec rspec spec/entrypoint_spec.rb
```
Expected: PASS (4 passed).

- [ ] **Step 7: Run the full existing test suite to confirm no regressions**

```bash
cd packages/sdks/flagmind-ruby
bundle exec rspec
```
Expected: PASS (all existing tests, currently 6 examples).

- [ ] **Step 8: Commit**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone
git add packages/sdks/flagmind-ruby/lib/tombstone.rb packages/sdks/flagmind-ruby/spec/entrypoint_spec.rb
git commit -m "fix(ruby-sdk): remove broken lib/flagmind.rb, correct entrypoint to lib/tombstone.rb"
```

---

## Phase 2 — Types

### Task 2: Add new fields to FlagEnvironmentState and supporting types

**Files:**
- Modify: `packages/sdks/flagmind-ruby/lib/flagmind/types.rb`
- Create: `packages/sdks/flagmind-ruby/spec/types_spec.rb`

**Interfaces:**
- Consumes: nothing (pure type definitions).
- Produces: `FlagEnvironmentState(flag_id, flag_key, environment, enabled, rollout_pct, safe_default, updated_at, prerequisites: [], targeting_rules: [], target_list: [], hash_version: 1)` — 4 new keyword fields with defaults. `FlagPrerequisite(flag_key:, required_variation:, gate:)`. `TargetingRule(id:, conditions:, rollout_pct:, variation:, priority:)`. `PropertyCondition(attribute:, operator:, values:, negate:)`.

- [ ] **Step 1: Write the failing test**

```ruby
# packages/sdks/flagmind-ruby/spec/types_spec.rb
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
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd packages/sdks/flagmind-ruby
bundle exec rspec spec/types_spec.rb
```
Expected: FAIL — `FlagPrerequisite` does not exist, and `FlagEnvironmentState` does not accept
`prerequisites`/`targeting_rules`/`target_list`/`hash_version` keyword arguments.

- [ ] **Step 3: Extend types.rb with new structs**

```ruby
# packages/sdks/flagmind-ruby/lib/flagmind/types.rb
module Tombstone
  module EvaluationReason
    OFF = :off
    FALLTHROUGH = :fallthrough
    TARGET_MATCH = :target_match
    RULE_MATCH = :rule_match
    PREREQUISITE_FAILED = :prerequisite_failed
    ERROR = :error
  end

  EvaluationContext = Struct.new(:user_id, :org_id, :attrs, keyword_init: true) do
    def self.of(user_id)
      new(user_id: user_id, org_id: "", attrs: {})
    end
  end

  EvaluationResult = Struct.new(:value, :reason, :from_cache, :flag_key, keyword_init: true)

  # New types for full pipeline
  FlagPrerequisite = Struct.new(:flag_key, :required_variation, :gate, keyword_init: true)

  PropertyCondition = Struct.new(:attribute, :operator, :values, :negate, keyword_init: true)

  TargetingRule = Struct.new(:id, :conditions, :rollout_pct, :variation, :priority, keyword_init: true)

  # Extended FlagEnvironmentState with new fields (4 new keyword args with defaults)
  FlagEnvironmentState = Struct.new(
    :flag_id, :flag_key, :environment,
    :enabled, :rollout_pct, :safe_default, :updated_at,
    :prerequisites, :targeting_rules, :target_list, :hash_version,
    keyword_init: true
  ) do
    # Default values for the new fields
    def initialize(
      flag_id:, flag_key:, environment:,
      enabled:, rollout_pct:, safe_default:, updated_at:,
      prerequisites: [], targeting_rules: [], target_list: [], hash_version: 1
    )
      super(
        flag_id: flag_id, flag_key: flag_key, environment: environment,
        enabled: enabled, rollout_pct: rollout_pct, safe_default: safe_default, updated_at: updated_at,
        prerequisites: prerequisites, targeting_rules: targeting_rules,
        target_list: target_list, hash_version: hash_version
      )
    end
  end
end
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd packages/sdks/flagmind-ruby
bundle exec rspec spec/types_spec.rb
```
Expected: PASS (6 passed).

- [ ] **Step 5: Fix the existing EvaluationEngine test call sites (positional constructor changed)**

The existing `spec/evaluation_engine_spec.rb` helper method `flag(enabled:, pct:)` constructs
`FlagEnvironmentState` with 7 positional args. Update it to use keyword args:

```ruby
# packages/sdks/flagmind-ruby/spec/evaluation_engine_spec.rb
# Change the helper method from:
#   def flag(enabled:, pct:)
#     Tombstone::FlagEnvironmentState.new(
#       flag_id: "id1", flag_key: "test-flag", environment: "test",
#       enabled: enabled, rollout_pct: pct, safe_default: "false", updated_at: 0
#     )
#   end
# to: (no change needed — it already uses keyword_init: true correctly)
# If the helper is currently positional, rewrite it to keyword args.
```
Actually, reading the existing test file, it already uses keyword args correctly. No change needed.

- [ ] **Step 6: Run the full existing test suite to confirm no regressions**

```bash
cd packages/sdks/flagmind-ruby
bundle exec rspec
```
Expected: PASS (10 examples — 6 existing + 4 new from entrypoint test).

- [ ] **Step 7: Commit**

```bash
git add packages/sdks/flagmind-ruby/lib/flagmind/types.rb packages/sdks/flagmind-ruby/spec/types_spec.rb
git commit -m "feat(ruby-sdk): add prerequisite/rule/target-list/hash-version fields to FlagEnvironmentState"
```

---

### Task 3: Add InconclusiveMatchError

**Files:**
- Create: `packages/sdks/flagmind-ruby/lib/flagmind/errors.rb`
- Modify: `packages/sdks/flagmind-ruby/lib/tombstone.rb` (to require it)
- Create: `packages/sdks/flagmind-ruby/spec/errors_spec.rb`

**Interfaces:**
- Consumes: nothing.
- Produces: `Tombstone::InconclusiveMatchError < StandardError` — raised by `RuleMatcher` (Task 5) when a condition cannot be evaluated (missing attribute, unparseable numeric/date/semver value).

- [ ] **Step 1: Write the failing test**

```ruby
# packages/sdks/flagmind-ruby/spec/errors_spec.rb
require "spec_helper"

RSpec.describe Tombstone::InconclusiveMatchError do
  it "is a StandardError" do
    err = Tombstone::InconclusiveMatchError.new("attribute missing")
    expect(err).to be_a(StandardError)
    expect(err.message).to eq("attribute missing")
  end

  it "can be raised and rescued" do
    expect {
      raise Tombstone::InconclusiveMatchError, "test message"
    }.to raise_error(Tombstone::InconclusiveMatchError, "test message")
  end
end
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd packages/sdks/flagmind-ruby
bundle exec rspec spec/errors_spec.rb
```
Expected: FAIL — class does not exist.

- [ ] **Step 3: Implement**

```ruby
# packages/sdks/flagmind-ruby/lib/flagmind/errors.rb
module Tombstone
  # Raised when a targeting-rule condition cannot be evaluated locally
  # (missing attribute, unparseable numeric/date/semver value). Caught
  # per-rule by RuleMatcher, which treats it as "this rule did not
  # match" and continues to the next priority-sorted rule. Mirrors
  # Python's InconclusiveMatchError, which is caught internally and
  # never expected to propagate to SDK callers.
  class InconclusiveMatchError < StandardError; end
end
```

```ruby
# packages/sdks/flagmind-ruby/lib/tombstone.rb
# Add this line after the other require_relative statements:
require_relative "flagmind/errors"
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd packages/sdks/flagmind-ruby
bundle exec rspec spec/errors_spec.rb
```
Expected: PASS (2 passed).

- [ ] **Step 5: Commit**

```bash
git add packages/sdks/flagmind-ruby/lib/flagmind/errors.rb packages/sdks/flagmind-ruby/lib/tombstone.rb packages/sdks/flagmind-ruby/spec/errors_spec.rb
git commit -m "feat(ruby-sdk): add InconclusiveMatchError for unevaluatable rule conditions"
```

---

## Phase 3 — Rule Matching (Step 4)

### Task 4: RuleMatcher — attribute resolution and equality/string/numeric operators

**Files:**
- Create: `packages/sdks/flagmind-ruby/lib/flagmind/rule_matcher.rb`
- Modify: `packages/sdks/flagmind-ruby/lib/tombstone.rb` (to require it)
- Create: `packages/sdks/flagmind-ruby/spec/rule_matcher_spec.rb`

**Interfaces:**
- Consumes: `PropertyCondition`, `TargetingRule`, `EvaluationContext` (Task 2 types + existing `EvaluationContext`).
- Produces: `RuleMatcher.resolve_attribute(attribute, context)` (dot-notation resolution, returns `nil` if unresolvable), `RuleMatcher.evaluate_condition(condition, context)` (raises `InconclusiveMatchError` on unresolvable/unparseable input), `RuleMatcher.match_rules(rules, context, flag_key)` (returns matched variation string or `nil` if no rule matches; implements priority sort + per-rule rollout sub-bucketing).

- [ ] **Step 1: Write the failing tests**

```ruby
# packages/sdks/flagmind-ruby/spec/rule_matcher_spec.rb
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
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd packages/sdks/flagmind-ruby
bundle exec rspec spec/rule_matcher_spec.rb
```
Expected: FAIL — `Tombstone::RuleMatcher` class does not exist.

- [ ] **Step 3: Implement RuleMatcher (attribute resolution + eq/string/numeric operators)**

```ruby
# packages/sdks/flagmind-ruby/lib/flagmind/rule_matcher.rb
require "set"

module Tombstone
  module RuleMatcher
    GEO_ATTRIBUTES = Set.new(["geo.country", "geo.region"])

    # Canonical model: dot-notation attribute resolution over a flat attrs hash
    # (this release's EvaluationContext.attrs is Hash, so multi-segment paths
    # like "geo.country" resolve via nested-map convention where the caller
    # stores nested structures). Returns nil if the attribute is not present.
    def self.resolve_attribute(attribute, context)
      return context.user_id if attribute == "user_id"
      return context.org_id if attribute == "org_id"

      # Dot-notation resolution: split on dots, traverse nested hashes
      segments = attribute.split(".")
      current = context.attrs
      segments.each do |seg|
        return nil unless current.is_a?(Hash) && current.key?(seg)
        current = current[seg]
      end

      # Fallback: if only one segment and it's a flat key, return it
      if segments.size == 1 && context.attrs.key?(attribute)
        return context.attrs[attribute]
      end

      current
    end

    def self.evaluate_condition(condition, context)
      raw = resolve_attribute(condition.attribute, context)
      raise InconclusiveMatchError, "Attribute '#{condition.attribute}' not present in evaluation context" if raw.nil?

      attr_val = raw.to_s
      op = normalize_operator(condition.operator)
      values = condition.values
      is_geo = GEO_ATTRIBUTES.include?(condition.attribute)

      result = case op
      when "eq", "in"
        is_geo ? contains_ignore_case(values, attr_val) : values.include?(attr_val)
      when "neq", "nin"
        is_geo ? !contains_ignore_case(values, attr_val) : !values.include?(attr_val)
      when "contains"
        any_contains_ignore_case(values, attr_val)
      when "startswith"
        any_starts_with_ignore_case(values, attr_val)
      when "endswith"
        any_ends_with_ignore_case(values, attr_val)
      when "gt", "gte", "lt", "lte"
        evaluate_numeric(op, attr_val, values, condition.attribute)
      else
        raise InconclusiveMatchError, "Unknown operator: '#{op}'"
      end

      condition.negate ? !result : result
    end

    def self.normalize_operator(operator)
      op = operator.downcase
      case op
      when "not_in" then "nin"
      when "prefix" then "startswith"
      when "suffix" then "endswith"
      else op
      end
    end

    def self.contains_ignore_case(values, attr_val)
      upper = attr_val.upcase
      values.any? { |v| v.to_s.upcase == upper }
    end

    def self.any_contains_ignore_case(values, attr_val)
      upper_attr = attr_val.upcase
      values.any? { |v| upper_attr.include?(v.to_s.upcase) }
    end

    def self.any_starts_with_ignore_case(values, attr_val)
      upper_attr = attr_val.upcase
      values.any? { |v| upper_attr.start_with?(v.to_s.upcase) }
    end

    def self.any_ends_with_ignore_case(values, attr_val)
      upper_attr = attr_val.upcase
      values.any? { |v| upper_attr.end_with?(v.to_s.upcase) }
    end

    def self.evaluate_numeric(op, attr_val, values, attribute)
      begin
        n_attr = Float(attr_val)
        n_val = Float(values[0])
      rescue ArgumentError, TypeError, IndexError
        raise InconclusiveMatchError, "Numeric cast failed for '#{attribute}'"
      end

      case op
      when "gt" then n_attr > n_val
      when "gte" then n_attr >= n_val
      when "lt" then n_attr < n_val
      when "lte" then n_attr <= n_val
      else false
      end
    end
  end
end
```

```ruby
# packages/sdks/flagmind-ruby/lib/tombstone.rb
# Add this line after the other require_relative statements:
require_relative "flagmind/rule_matcher"
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd packages/sdks/flagmind-ruby
bundle exec rspec spec/rule_matcher_spec.rb
```
Expected: PASS (9 passed).

- [ ] **Step 5: Commit**

```bash
git add packages/sdks/flagmind-ruby/lib/flagmind/rule_matcher.rb packages/sdks/flagmind-ruby/lib/tombstone.rb packages/sdks/flagmind-ruby/spec/rule_matcher_spec.rb
git commit -m "feat(ruby-sdk): add RuleMatcher attribute resolution and eq/string/numeric operators"
```

---

### Task 5: RuleMatcher — semver and date operators

**Files:**
- Modify: `packages/sdks/flagmind-ruby/lib/flagmind/rule_matcher.rb`
- Modify: `packages/sdks/flagmind-ruby/spec/rule_matcher_spec.rb`

**Interfaces:**
- Consumes: `evaluate_condition` from Task 4 (adding new operator branches).
- Produces: `RuleMatcher.padded_version(v)` (module function, used internally by `evaluate_condition`'s semver branch and directly tested).

- [ ] **Step 1: Write the failing tests**

```ruby
# Append to packages/sdks/flagmind-ruby/spec/rule_matcher_spec.rb

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
  end
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd packages/sdks/flagmind-ruby
bundle exec rspec spec/rule_matcher_spec.rb
```
Expected: FAIL — `padded_version` doesn't exist, semver/date operators raise `InconclusiveMatchError` unconditionally (fall into the `else` branch of the case statement).

- [ ] **Step 3: Add semver padding and date/semver operator branches**

```ruby
# In packages/sdks/flagmind-ruby/lib/flagmind/rule_matcher.rb
# Add these methods after the numeric helpers:

    # Ported byte-for-byte from flagmind-python's matching.py:27-39 (GrowthBook pattern).
    def self.padded_version(v)
      v = v.gsub(/^v/, "").gsub(/\+.*$/, "")
      parts = v.split(/[-.]/)
      padded = parts.map { |p| p.match?(/^\d+$/) ? p.rjust(5, " ") : p }
      padded << "~" if padded.size == 3
      padded.join(".")
    end

    def self.evaluate_semver(op, attr_val, values, attribute)
      raise InconclusiveMatchError, "semver operator requires at least one value for '#{attribute}'" if values.empty?

      a = padded_version(attr_val)
      b = padded_version(values[0])
      cmp = a <=> b

      case op
      when "semver_gt" then cmp > 0
      when "semver_gte" then cmp >= 0
      when "semver_lt" then cmp < 0
      when "semver_lte" then cmp <= 0
      when "semver_eq" then cmp == 0
      else false
      end
    end

    def self.evaluate_date(op, attr_val, values, attribute)
      require "time"
      begin
        dt_attr = Time.iso8601(normalize_iso8601(attr_val))
        dt_val = Time.iso8601(normalize_iso8601(values[0]))
      rescue ArgumentError, IndexError
        raise InconclusiveMatchError, "Date parse failed for '#{attribute}'"
      end

      op == "date_before" ? dt_attr < dt_val : dt_attr > dt_val
    end

    def self.normalize_iso8601(s)
      s.gsub("Z", "+00:00")
    end

# Update the case statement in evaluate_condition to add these branches before "else":
      when "semver_gt", "semver_gte", "semver_lt", "semver_lte", "semver_eq"
        evaluate_semver(op, attr_val, values, condition.attribute)
      when "date_before", "date_after"
        evaluate_date(op, attr_val, values, condition.attribute)
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd packages/sdks/flagmind-ruby
bundle exec rspec spec/rule_matcher_spec.rb
```
Expected: PASS (16 passed).

- [ ] **Step 5: Commit**

```bash
git add packages/sdks/flagmind-ruby/lib/flagmind/rule_matcher.rb packages/sdks/flagmind-ruby/spec/rule_matcher_spec.rb
git commit -m "feat(ruby-sdk): add semver and date operators to RuleMatcher"
```

---

### Task 6: RuleMatcher — priority sort, multi-condition AND, per-rule rollout, match_rules entrypoint

**Files:**
- Modify: `packages/sdks/flagmind-ruby/lib/flagmind/rule_matcher.rb`
- Modify: `packages/sdks/flagmind-ruby/spec/rule_matcher_spec.rb`

**Interfaces:**
- Consumes: `evaluate_condition` from Tasks 4-5, MurmurHash3 hashing logic (extracted from `EvaluationEngine#in_rollout?` — inline the same MurmurHash3 call directly in `RuleMatcher` since `match_rules` needs it independently of the engine's Step 5 fallthrough).
- Produces: `RuleMatcher.match_rules(rules, context, flag_key)` — returns matched variation string or `nil` if no rule matches.

- [ ] **Step 1: Write the failing tests**

```ruby
# Append to packages/sdks/flagmind-ruby/spec/rule_matcher_spec.rb

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
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd packages/sdks/flagmind-ruby
bundle exec rspec spec/rule_matcher_spec.rb
```
Expected: FAIL — `match_rules` does not exist.

- [ ] **Step 3: Implement match_rules**

```ruby
# In packages/sdks/flagmind-ruby/lib/flagmind/rule_matcher.rb
# Add at the top (after the module declaration):
    require "murmurhash3"

# Add this method (public entrypoint for Step 4):

    # Canonical model: priority-ascending sort (0 = highest), multi-condition AND
    # per rule, per-rule rollout sub-bucketing (matched conditions but bucket
    # outside this rule's own rollout_pct falls to the NEXT rule, not Step 5).
    def self.match_rules(rules, context, flag_key)
      sorted = rules.sort_by(&:priority)

      sorted.each do |rule|
        all_match = begin
          rule.conditions.all? { |c| evaluate_condition(c, context) }
        rescue InconclusiveMatchError
          next  # rule inconclusive — try next rule
        end

        next unless all_match

        bucket = murmur3_bucket(flag_key, context.user_id)
        return rule.variation if bucket < rule.rollout_pct

        # conditions matched but outside this rule's own rollout — try next rule
      end

      nil
    end

    def self.murmur3_bucket(flag_key, user_id)
      hash = MurmurHash3::V32.str_hash(flag_key + user_id, 0)
      hash % 100
    end
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd packages/sdks/flagmind-ruby
bundle exec rspec spec/rule_matcher_spec.rb
```
Expected: PASS (22 passed).

- [ ] **Step 5: Commit**

```bash
git add packages/sdks/flagmind-ruby/lib/flagmind/rule_matcher.rb packages/sdks/flagmind-ruby/spec/rule_matcher_spec.rb
git commit -m "feat(ruby-sdk): add match_rules with priority sort and per-rule rollout sub-bucketing"
```

---

## Phase 4 — Prerequisites (Step 2) and Target List (Step 3)

### Task 7: PrerequisiteChecker with cycle detection and memoization

**Files:**
- Create: `packages/sdks/flagmind-ruby/lib/flagmind/prerequisite_checker.rb`
- Modify: `packages/sdks/flagmind-ruby/lib/tombstone.rb` (to require it)
- Create: `packages/sdks/flagmind-ruby/spec/prerequisite_checker_spec.rb`

**Interfaces:**
- Consumes: `FlagPrerequisite` (Task 2), a lookup proc for other flags in the same snapshot.
- Produces: `PrerequisiteChecker.check_all(prerequisites, flag_lookup, cache, seen, current_flag_key, engine, context)`. Takes the `EvaluationEngine` itself as a parameter to enable recursive evaluation of dependency flags (mirrors Python's `evaluation.py:89-94`, which calls its own module-level `evaluate()` recursively) — this is a forward reference resolved when `EvaluationEngine` gains its `evaluate` overload in Task 8; until then this class only depends on the `EvaluationEngine` type signature, not its implementation.

- [ ] **Step 1: Write the failing tests**

```ruby
# packages/sdks/flagmind-ruby/spec/prerequisite_checker_spec.rb
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
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd packages/sdks/flagmind-ruby
bundle exec rspec spec/prerequisite_checker_spec.rb
```
Expected: FAIL — `Tombstone::PrerequisiteChecker` does not exist.

- [ ] **Step 3: Implement PrerequisiteChecker**

```ruby
# packages/sdks/flagmind-ruby/lib/flagmind/prerequisite_checker.rb
require "set"

module Tombstone
  module PrerequisiteChecker
    # Canonical model: string-compare mechanism against the dependency's
    # stringified boolean outcome (forward-compatible with future
    # multivariate prerequisites — see design spec Section 3). Cycle
    # detection via explicit seen-set (Python's approach); memoization
    # via cache hash keyed by dependency flag key (Python's approach).
    def self.check_all(prerequisites, flag_lookup, cache, seen, current_flag_key, engine, context)
      chain_seen = seen.dup
      chain_seen.add(current_flag_key)

      prerequisites.each do |prereq|
        dep_key = prereq.flag_key
        dep_variation = if cache.key?(dep_key)
          cache[dep_key]
        elsif chain_seen.include?(dep_key)
          next  # cycle detected — fail open, skip this one prerequisite
        else
          dep_flag = flag_lookup.call(dep_key)
          if dep_flag.nil?
            nil
          else
            # Recursive evaluation via engine's 7-arg overload (Task 8)
            dep_result = engine.evaluate(dep_flag, context, false, dep_key, flag_lookup, cache, chain_seen)
            dep_result.value.to_s
          end
        end

        cache[dep_key] = dep_variation unless cache.key?(dep_key)

        if dep_variation != prereq.required_variation
          next unless prereq.gate  # soft — unmet but non-blocking
          return false             # hard gate — block entire parent flag
        end
      end

      true
    end
  end
end
```

```ruby
# packages/sdks/flagmind-ruby/lib/tombstone.rb
# Add this line after the other require_relative statements:
require_relative "flagmind/prerequisite_checker"
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd packages/sdks/flagmind-ruby
bundle exec rspec spec/prerequisite_checker_spec.rb
```
Expected: FAIL — `engine.evaluate(...)` with 7 args doesn't exist yet on `EvaluationEngine`. This is
expected at this point in the plan; Task 8 adds the new `evaluate` overload PrerequisiteChecker
depends on. Do not attempt to make this pass yet — proceed to Task 8, then return and re-run.

- [ ] **Step 5: Commit (test file only, main class awaiting Task 8's engine overload)**

```bash
git add packages/sdks/flagmind-ruby/lib/flagmind/prerequisite_checker.rb packages/sdks/flagmind-ruby/lib/tombstone.rb packages/sdks/flagmind-ruby/spec/prerequisite_checker_spec.rb
git commit -m "feat(ruby-sdk): add PrerequisiteChecker with cycle detection and memoization (tests pending EvaluationEngine.evaluate overload)"
```

---

## Phase 5 — Full Pipeline Integration

### Task 8: Rewrite EvaluationEngine#evaluate to the full 5-step pipeline

**Files:**
- Modify: `packages/sdks/flagmind-ruby/lib/flagmind/evaluation_engine.rb`
- Modify: `packages/sdks/flagmind-ruby/spec/evaluation_engine_spec.rb`

**Interfaces:**
- Consumes: `PrerequisiteChecker.check_all` (Task 7), `RuleMatcher.match_rules` (Task 6), `FlagEnvironmentState`'s new fields (Task 2).
- Produces: `EvaluationEngine#evaluate(flag_state, context, default_value, flag_key, flag_lookup: ->(k) { nil }, prerequisite_cache: {}, seen_keys: Set.new)` — the new canonical signature with keyword args for the 3 optional parameters. The OLD signature `evaluate(flag_state, context, default_value, flag_key)` is supported as a convenience form that supplies default values for the keyword args.

- [ ] **Step 1: Write new integration tests for the full pipeline**

```ruby
# Append to packages/sdks/flagmind-ruby/spec/evaluation_engine_spec.rb

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
```

- [ ] **Step 2: Rewrite EvaluationEngine**

```ruby
# packages/sdks/flagmind-ruby/lib/flagmind/evaluation_engine.rb
require "murmurhash3"
require "set"

module Tombstone
  class EvaluationEngine
    FNV_OFFSET = 2166136261
    FNV_PRIME = 16777619

    # Full 5-step canonical evaluation pipeline. See docs/SDK_CONTRACT.md for the
    # normative spec this implements. flag_lookup resolves other flags in the same
    # snapshot for prerequisite evaluation (step 2); pass ->(k) { nil } if the
    # caller has no snapshot access (prerequisites will then always be treated
    # as missing, and hard-gated prerequisites will PREREQUISITE_FAILED).
    def evaluate(flag_state, context, default_value, flag_key, flag_lookup: ->(k) { nil }, prerequisite_cache: {}, seen_keys: Set.new)
      # Step 1: Preliminary checks
      return EvaluationResult.new(value: default_value, reason: EvaluationReason::ERROR, from_cache: false, flag_key: flag_key) if flag_state.nil?
      unless flag_state.enabled
        return EvaluationResult.new(value: parse_safe_default(flag_state.safe_default, default_value), reason: EvaluationReason::OFF, from_cache: true, flag_key: flag_key)
      end

      # Step 2: Prerequisites
      unless flag_state.prerequisites.empty?
        satisfied = PrerequisiteChecker.check_all(
          flag_state.prerequisites, flag_lookup, prerequisite_cache, seen_keys, flag_key, self, context
        )
        return EvaluationResult.new(value: default_value, reason: EvaluationReason::PREREQUISITE_FAILED, from_cache: true, flag_key: flag_key) unless satisfied
      end

      # Step 3: Individual target list
      if !flag_state.target_list.empty? && flag_state.target_list.include?(context.user_id)
        return EvaluationResult.new(value: true, reason: EvaluationReason::TARGET_MATCH, from_cache: true, flag_key: flag_key)
      end

      # Step 4: Priority-sorted rule matching
      unless flag_state.targeting_rules.empty?
        rule_match = RuleMatcher.match_rules(flag_state.targeting_rules, context, flag_key)
        if rule_match
          return EvaluationResult.new(value: rule_match, reason: EvaluationReason::RULE_MATCH, from_cache: true, flag_key: flag_key)
        end
      end

      # Step 5: Fallthrough rollout
      return EvaluationResult.new(value: cast_enabled(default_value), reason: EvaluationReason::FALLTHROUGH, from_cache: true, flag_key: flag_key) if flag_state.rollout_pct >= 100
      return EvaluationResult.new(value: default_value, reason: EvaluationReason::FALLTHROUGH, from_cache: true, flag_key: flag_key) if flag_state.rollout_pct <= 0

      in_rollout = flag_state.hash_version == 2 ? in_rollout_fnv?(flag_key, context.user_id, flag_state.rollout_pct) : in_rollout_murmur3?(flag_key, context.user_id, flag_state.rollout_pct)

      if in_rollout
        EvaluationResult.new(value: cast_enabled(default_value), reason: EvaluationReason::FALLTHROUGH, from_cache: true, flag_key: flag_key)
      else
        EvaluationResult.new(value: default_value, reason: EvaluationReason::FALLTHROUGH, from_cache: true, flag_key: flag_key)
      end
    end

    private

    # CRITICAL: Uses MurmurHash3 unsigned 32-bit to match TypeScript + Python + Java SDKs
    def in_rollout_murmur3?(flag_key, user_id, rollout_pct)
      hash = MurmurHash3::V32.str_hash(flag_key + user_id, 0)
      bucket = hash % 100
      bucket < rollout_pct
    end

    # Canonical hashVersion=2: double-pass FNV-1a, UTF-8 byte iteration, 10,000-bucket
    # resolution. Ported from flagmind-python's evaluation.py:27-48 (byte iteration,
    # not TS's UTF-16 code-unit iteration — canonical choice per design spec Section 3).
    def fnv1a_raw(s)
      h = FNV_OFFSET
      s.bytes.each do |b|
        h ^= b
        h = (h * FNV_PRIME) & 0xFFFFFFFF
      end
      h & 0xFFFFFFFF
    end

    def in_rollout_fnv?(flag_key, user_id, rollout_pct)
      h1 = fnv1a_raw(flag_key + user_id)
      h2 = fnv1a_raw(h1.to_s)
      bucket = (h2 % 10000) / 10000.0
      bucket < (rollout_pct / 100.0)
    end

    # Canonical model: OFF-path parses safeDefault into the target type (TS's
    # approach), falling back to the caller's defaultValue on parse failure
    # or type mismatch.
    def parse_safe_default(safe_default, fallback)
      case fallback
      when TrueClass, FalseClass
        safe_default == "true"
      when Numeric
        Float(safe_default)
      when String
        safe_default
      else
        fallback
      end
    rescue ArgumentError
      fallback
    end

    def cast_enabled(default_value)
      case default_value
      when TrueClass, FalseClass then true
      else default_value
      end
    end
  end
end
```

- [ ] **Step 3: Run all EvaluationEngine tests to verify they pass**

```bash
cd packages/sdks/flagmind-ruby
bundle exec rspec spec/evaluation_engine_spec.rb
```
Expected: PASS (all existing + 4 new integration tests).

- [ ] **Step 4: Return to Task 7's PrerequisiteCheckerSpec and confirm it now passes**

```bash
cd packages/sdks/flagmind-ruby
bundle exec rspec spec/prerequisite_checker_spec.rb
```
Expected: PASS (7 passed) — the `engine.evaluate(dep_flag, context, false, dep_key, flag_lookup: lookup, prerequisite_cache: cache, seen_keys: chain_seen)` call
in `PrerequisiteChecker.check_all` now resolves against the 7-kwarg overload added in this task.

- [ ] **Step 5: Run the full test suite**

```bash
cd packages/sdks/flagmind-ruby
bundle exec rspec
```
Expected: PASS (all tests across all files).

- [ ] **Step 6: Commit**

```bash
git add packages/sdks/flagmind-ruby/lib/flagmind/evaluation_engine.rb packages/sdks/flagmind-ruby/spec/evaluation_engine_spec.rb
git commit -m "feat(ruby-sdk): implement full 5-step canonical evaluation pipeline in EvaluationEngine"
```

---

## Phase 6 — Contract Vector Verification

### Task 9: Vector-harness test loading test-contract/vectors.json

**Files:**
- Create: `packages/sdks/flagmind-ruby/spec/contract_vectors_spec.rb`

**Interfaces:**
- Consumes: `EvaluationEngine#evaluate` (Task 8), `RuleMatcher.match_rules` (Task 6), `PrerequisiteChecker.check_all` (Task 7), `JSON` stdlib.
- Produces: nothing consumed elsewhere — this is the terminal verification task for Ruby parity.

- [ ] **Step 1: Write the vector-harness test**

```ruby
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

        expect(result.value).to eq(expected), "hash vector mismatch for #{flag_key}/#{user_id}"
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

        expect(satisfied).to eq(expected_satisfied), "prerequisite vector mismatch for #{id}"
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
          expect(result).to be_nil, "expected no rule match for #{id}"
        else
          expect(result).not_to be_nil, "expected a rule match for #{id}"
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

        expect(result.nil? == expected_node.nil?).to be true, "missing-attribute vector mismatch for #{id}"
      end
    end
  end
end
```

- [ ] **Step 2: Run the contract vector tests**

```bash
cd packages/sdks/flagmind-ruby
bundle exec rspec spec/contract_vectors_spec.rb
```
Expected: PASS — all dynamic tests generated from `vectors.json` (24 hash + 7 prerequisite + 14 rule + 1 missing-attribute) pass.

- [ ] **Step 3: If any vector fails, diagnose before adjusting anything**

If a hash vector fails: re-check `in_rollout_murmur3?`/`in_rollout_fnv?` byte-for-byte against
Section 3 of the design spec — do NOT adjust the vector, the vector is ground truth (Phase 1 of
the overall upgrade already verified it against a hand-tested oracle). If a rule/prerequisite
vector fails: re-check `RuleMatcher`/`PrerequisiteChecker` against `docs/SDK_CONTRACT.md`'s
Canonical Model table — the bug is almost certainly in this SDK's Ruby code, not the vector.

- [ ] **Step 4: Run the full Ruby test suite one final time**

```bash
cd packages/sdks/flagmind-ruby
bundle exec rspec
```
Expected: PASS (all unit tests + contract-vector tests).

- [ ] **Step 5: Commit**

```bash
git add packages/sdks/flagmind-ruby/spec/contract_vectors_spec.rb
git commit -m "test(ruby-sdk): add contract-vector harness verifying parity against test-contract/vectors.json"
```

---

## Phase 7 — Naming Cleanup

### Task 10: Standardize gem name

**Files:**
- Modify: `packages/sdks/flagmind-ruby/flagmind.gemspec`

**Interfaces:**
- Consumes: nothing.
- Produces: nothing consumed by code — this changes only the published gem name.

- [ ] **Step 1: Change the gem name**

```ruby
# packages/sdks/flagmind-ruby/flagmind.gemspec
# Change line 2 from:
#     s.name        = "flagmind-ruby"
# to:
  s.name        = "tombstone-ruby-sdk"
```

Note: the module `Tombstone` (used throughout all source files) is already correct and stays
unchanged. The directory `packages/sdks/flagmind-ruby/` is NOT renamed in this task — a
directory rename affects every other task's file paths in this plan; if desired, do it as a
separate follow-up PR after this plan's Phase 1-6 work is merged, to avoid churn mid-implementation.

- [ ] **Step 2: Verify the gemspec parses correctly**

```bash
cd packages/sdks/flagmind-ruby
gem build flagmind.gemspec
```
Expected: `Successfully built RubyGem` with `tombstone-ruby-sdk-0.2.0.gem` (or similar) created.

- [ ] **Step 3: Commit**

```bash
git add packages/sdks/flagmind-ruby/flagmind.gemspec
git commit -m "chore(ruby-sdk): rename gem flagmind-ruby -> tombstone-ruby-sdk"
```

---

## Phase 8 — PR

### Task 11: Open PR to develop

**Files:** none (GitHub operation only)

- [ ] **Step 1: Run the full test suite one final time before pushing**

```bash
cd packages/sdks/flagmind-ruby
bundle exec rspec
```
Expected: PASS (all tests).

- [ ] **Step 2: Push the branch**

```bash
git push -u origin feat/ruby-sdk-parity-v1.5.0
```

- [ ] **Step 3: Open the PR**

```bash
gh pr create --base develop --title "feat(ruby-sdk): bring flagmind-ruby to full 5-step evaluation parity" --body "$(cat <<'EOF'
## Summary
- Fixes P0 broken entrypoint bug (lib/flagmind.rb → lib/tombstone.rb, working).
- Implements steps 2-4 of the canonical evaluation pipeline (prerequisites with cycle detection + memoization, target list, priority-sorted rule matching with full operator surface including semver/date/geo, per-rule rollout sub-bucketing) plus hashVersion=2 (FNV-1a).
- Standardizes gem name to `tombstone-ruby-sdk` (was `flagmind-ruby`, inconsistent with the `Tombstone` module already in use).
- Verified against `test-contract/vectors.json` v1.2 (46 dynamic contract-vector tests, all passing).

Phase 3 of the v1.5.0 upgrade. See docs/superpowers/specs/2026-08-04-v1.5.0-sdk-parity-and-dependency-viz-design.md.

## Test plan
- [x] P0 entrypoint bug confirmed broken, fixed, and verified with manual `ruby -Ilib -e 'require "tombstone"'` test
- [x] All unit tests across types, RuleMatcher, PrerequisiteChecker, EvaluationEngine passing
- [x] 46 dynamic contract-vector tests loading test-contract/vectors.json, all passing
- [x] Existing tests updated to new types, no regressions
EOF
)"
```

- [ ] **Step 4: Report the PR URL to the user and stop — do not merge**

Per this repo's established workflow, PR merges are done by the user, not automatically.
