require "set"
require "murmurhash3"

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
      # A blank attribute has nothing to resolve -- without this guard,
      # "".split(".") returns [], so the segments.each loop below never
      # runs and `current` falls through UNCHANGED from its initial value
      # of context.attrs (the WHOLE attrs Hash), not nil. evaluate_condition
      # only raises InconclusiveMatchError (the graceful "attribute not
      # present, skip this rule" path) when resolve_attribute returns nil,
      # so a blank attribute would otherwise silently stringify the entire
      # attrs Hash via #to_s and substring-match it against contains/
      # startswith/endswith, producing a spurious match instead of being
      # skipped. Found by adversarial review of PR #248 -- reachable via a
      # malformed/legacy targeting-rule row missing "attribute" on the wire
      # (client.rb's parse_targeting_rules defaults it to "").
      return nil if attribute.nil? || attribute.empty?

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
      raw_op = condition.operator.downcase
      op = normalize_operator(condition.operator)
      values = condition.values
      # is_geo is true whenever EITHER the attribute is a recognized geo path
      # OR the operator itself declares geo semantics (GEO_COUNTRY/
      # GEO_REGION) -- checking the attribute name ALONE would mean a rule
      # using a non-canonical attribute (e.g. "country" instead of
      # "geo.country") with a real GEO_COUNTRY operator silently falls back
      # to case-SENSITIVE matching, even though nothing (backend or SDK)
      # validates that operator=GEO_COUNTRY implies attribute=="geo.country".
      # Found and fixed in the Java SDK's equivalent evaluateCondition by
      # adversarial review of PR #247; checked here proactively.
      is_geo = GEO_ATTRIBUTES.include?(condition.attribute) ||
               raw_op == "geo_country" || raw_op == "geo_region"

      result = case op
      when "eq", "in"
        is_geo ? contains_ignore_case(values, attr_val) : values.include?(attr_val)
      when "neq", "nin"
        # An EMPTY values list must never match "neq"/"nin":
        # !values.include?(x) on an empty list is vacuously true (there is
        # nothing to find, so "not found" is trivially true), which would
        # make a rule with an empty/missing "values" list match EVERY
        # context unconditionally -- the opposite of "no exclusions
        # configured, so exclude nothing". Same bug class found and fixed in
        # the TypeScript SDK's NOT_IN operator (adversarial review of PR
        # #246) and the Java SDK's "neq"/"nin" branch (PR #247); fixed here
        # proactively per those findings' own explicit note that the
        # remaining SDKs likely share it.
        !values.empty? && (is_geo ? !contains_ignore_case(values, attr_val) : !values.include?(attr_val))
      when "contains"
        any_contains_ignore_case(values, attr_val)
      when "startswith"
        any_starts_with_ignore_case(values, attr_val)
      when "endswith"
        any_ends_with_ignore_case(values, attr_val)
      when "gt", "gte", "lt", "lte"
        evaluate_numeric(op, attr_val, values, condition.attribute)
      when "semver_gt", "semver_gte", "semver_lt", "semver_lte", "semver_eq"
        evaluate_semver(op, attr_val, values, condition.attribute)
      when "date_before", "date_after"
        evaluate_date(op, attr_val, values, condition.attribute)
      when "regex"
        # docs/SDK_CONTRACT.md:32 -- REGEX is declared (a real, distinct
        # operator value in flag-api's targeting_rules.operator CHECK
        # constraint) but deliberately NOT IMPLEMENTED in this release,
        # across all 5 SDKs (parity matrix: "No" for every language) --
        # matching TypeScript's existing behavior of "always returns false,
        # not inconclusive". Found by adversarial review of PR #248: this
        # SDK's own "else raise InconclusiveMatchError" fallback previously
        # caught "regex" too (normalize_operator passes it through
        # unchanged), which deviates from that documented contract in two
        # ways -- it's treated as skip-the-whole-rule rather than a
        # definite false, AND `negate: true` on a regex condition would
        # ALSO skip rather than the contract's literal "false, negated ->
        # true" outcome. This branch closes that narrow conformance gap
        # WITHOUT implementing real regex matching, which remains
        # deliberately deferred ("Future work") to keep this SDK consistent
        # with the other 4, not introduce Ruby-only regex support.
        false
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
      # flag-api's targeting_rules.operator CHECK constraint (schema.sql)
      # has GEO_COUNTRY/GEO_REGION as real, distinct operator VALUES (not
      # just an attribute-name convention) -- without this mapping, a
      # targeting rule using either would hit the else branch below and
      # raise InconclusiveMatchError on every evaluation, silently never
      # matching for any user. Mapped to "in" so it falls into the existing
      # "eq"/"in" branch above, whose is_geo case-insensitive comparison
      # already implements the real geo-matching semantics correctly. Found
      # while wiring the real backend wire format into this SDK for the
      # first time (targeting_rules had zero real snapshot data before this
      # change, so this gap was never previously reachable) -- the identical
      # gap the Java SDK's own normalizeOperator needed (PR #247).
      when "geo_country", "geo_region" then "in"
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
      raise InconclusiveMatchError, "date operator requires at least one value for '#{attribute}'" if values.empty?

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
  end
end
