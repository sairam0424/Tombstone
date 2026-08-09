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
    # Supports both keyword args (new style) and positional args (Task 7 compatibility).
    def evaluate(flag_state, context, default_value, flag_key, flag_lookup_or_nil = nil, prerequisite_cache_or_nil = nil, seen_keys_or_nil = nil, flag_lookup: nil, prerequisite_cache: nil, seen_keys: nil)
      # Handle both styles: positional and keyword arguments
      flag_lookup = flag_lookup || flag_lookup_or_nil || ->(k) { nil }
      prerequisite_cache = prerequisite_cache || prerequisite_cache_or_nil || {}
      seen_keys = seen_keys || seen_keys_or_nil || Set.new
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
