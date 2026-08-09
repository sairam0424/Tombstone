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
