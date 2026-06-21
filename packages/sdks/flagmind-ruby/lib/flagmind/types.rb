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

  FlagEnvironmentState = Struct.new(
    :flag_id, :flag_key, :environment,
    :enabled, :rollout_pct, :safe_default, :updated_at,
    keyword_init: true
  )
end
