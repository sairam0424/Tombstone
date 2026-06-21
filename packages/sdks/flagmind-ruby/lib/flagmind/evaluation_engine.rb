require "murmurhash3"

module Tombstone
  class EvaluationEngine
    # CRITICAL: Uses MurmurHash3 unsigned 32-bit to match TypeScript + Python + Java SDKs
    # TypeScript: murmurhash.v3(flagKey + userId) >>> 0 % 100
    # Python: mmh3.hash(flag_key + user_id, seed=0, signed=False) % 100
    def evaluate(flag_state, context, default_value, flag_key)
      return EvaluationResult.new(value: default_value, reason: EvaluationReason::ERROR, from_cache: false, flag_key: flag_key) if flag_state.nil?
      return EvaluationResult.new(value: default_value, reason: EvaluationReason::OFF, from_cache: true, flag_key: flag_key) unless flag_state.enabled
      return EvaluationResult.new(value: true, reason: EvaluationReason::FALLTHROUGH, from_cache: true, flag_key: flag_key) if flag_state.rollout_pct >= 100
      return EvaluationResult.new(value: default_value, reason: EvaluationReason::FALLTHROUGH, from_cache: true, flag_key: flag_key) if flag_state.rollout_pct <= 0
      if in_rollout?(flag_key, context.user_id, flag_state.rollout_pct)
        EvaluationResult.new(value: true, reason: EvaluationReason::FALLTHROUGH, from_cache: true, flag_key: flag_key)
      else
        EvaluationResult.new(value: default_value, reason: EvaluationReason::FALLTHROUGH, from_cache: true, flag_key: flag_key)
      end
    end

    private

    def in_rollout?(flag_key, user_id, rollout_pct)
      hash = MurmurHash3::V32.str_hash(flag_key + user_id, 0)
      bucket = hash % 100
      bucket < rollout_pct
    end
  end
end
