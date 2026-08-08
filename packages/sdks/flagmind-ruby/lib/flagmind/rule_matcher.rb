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
      when "semver_gt", "semver_gte", "semver_lt", "semver_lte", "semver_eq"
        evaluate_semver(op, attr_val, values, condition.attribute)
      when "date_before", "date_after"
        evaluate_date(op, attr_val, values, condition.attribute)
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
  end
end
