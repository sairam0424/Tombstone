require "spec_helper"

# End-to-end regression suite for the Java-SDK-parity follow-up: a live
# "targeting_rules_updated" SSE frame (services/flag-api/internal/api/v1/
# targeting_rules.go's TargetingRulesEvent, relayed verbatim by the gateway)
# must actually change what evaluate() returns for the affected flag.
# Mirrors client_prerequisites_event_spec.rb exactly, drives the real
# dispatch_sse_event(event_type, data) routing directly via client.send(...).
RSpec.describe Tombstone::Client do
  let(:client) { described_class.new(sdk_key: "sdk-test-key", environment: "test", defaults: { "child-flag" => "off" }) }

  def load(ts)
    client.instance_variable_get(:@cache).load_snapshot(
      [
        Tombstone::FlagEnvironmentState.new(
          flag_id: "2", flag_key: "child-flag", environment: "test",
          enabled: true, rollout_pct: 0, safe_default: "off", updated_at: 0
        )
      ],
      ts
    )
  end

  it "a live event newer than the snapshot changes evaluate()'s outcome" do
    load(1000)

    before = client.evaluate("child-flag", Tombstone::EvaluationContext.new(user_id: "u1", org_id: "", attrs: { "email" => "x@example.com" }))
    expect(before.reason).not_to eq(Tombstone::EvaluationReason::RULE_MATCH)

    client.send(:dispatch_sse_event, "targeting_rules_updated", <<~JSON)
      {"flag_key":"child-flag","environment":"test",
       "targeting_rules":[{"id":"rule-1","rule_type":"USER","attribute":"email","operator":"EQ",
        "values":["x@example.com"],"variation":"matched","priority":0}],
       "ts":2000}
    JSON

    after = client.evaluate("child-flag", Tombstone::EvaluationContext.new(user_id: "u1", org_id: "", attrs: { "email" => "x@example.com" }))
    expect(after.reason).to eq(Tombstone::EvaluationReason::RULE_MATCH)
    expect(after.value).to eq("matched")
  end

  it "an event older than the cached ts is rejected" do
    load(5000)

    client.send(:dispatch_sse_event, "targeting_rules_updated", <<~JSON)
      {"flag_key":"child-flag","environment":"test",
       "targeting_rules":[{"id":"rule-1","rule_type":"USER","attribute":"email","operator":"EQ",
        "values":["x@example.com"],"variation":"matched","priority":0}],
       "ts":3000}
    JSON

    result = client.evaluate("child-flag", Tombstone::EvaluationContext.new(user_id: "u1", org_id: "", attrs: { "email" => "x@example.com" }))
    expect(result.reason).not_to eq(Tombstone::EvaluationReason::RULE_MATCH)
  end

  it "an event with ts equal to the cached ts is applied" do
    load(5000)

    client.send(:dispatch_sse_event, "targeting_rules_updated", <<~JSON)
      {"flag_key":"child-flag","environment":"test",
       "targeting_rules":[{"id":"rule-1","rule_type":"USER","attribute":"email","operator":"EQ",
        "values":["x@example.com"],"variation":"matched","priority":0}],
       "ts":5000}
    JSON

    result = client.evaluate("child-flag", Tombstone::EvaluationContext.new(user_id: "u1", org_id: "", attrs: { "email" => "x@example.com" }))
    expect(result.reason).to eq(Tombstone::EvaluationReason::RULE_MATCH)
  end

  it "an update for a flag never seen is a no-op" do
    load(1000)

    expect do
      client.send(:dispatch_sse_event, "targeting_rules_updated", <<~JSON)
        {"flag_key":"never-configured-flag","environment":"test","targeting_rules":[],"ts":9999}
      JSON
    end.not_to raise_error

    result = client.evaluate("child-flag", Tombstone::EvaluationContext.new(user_id: "u1", org_id: "", attrs: { "email" => "x@example.com" }))
    expect(result.reason).not_to eq(Tombstone::EvaluationReason::RULE_MATCH)
  end

  it "malformed JSON is swallowed, not raised" do
    load(1000)
    expect { client.send(:dispatch_sse_event, "targeting_rules_updated", "not valid json{{{") }.not_to raise_error
  end

  it "a syntactically valid payload with no flag_key at all is swallowed, not raised" do
    load(1000)
    expect do
      client.send(:dispatch_sse_event, "targeting_rules_updated", <<~JSON)
        {"environment":"test","targeting_rules":[],"ts":9999}
      JSON
    end.not_to raise_error

    result = client.evaluate("child-flag", Tombstone::EvaluationContext.new(user_id: "u1", org_id: "", attrs: { "email" => "x@example.com" }))
    expect(result.reason).not_to eq(Tombstone::EvaluationReason::RULE_MATCH)
  end

  it "an empty targeting_rules list clears existing rules" do
    load(1000)
    client.send(:dispatch_sse_event, "targeting_rules_updated", <<~JSON)
      {"flag_key":"child-flag","environment":"test",
       "targeting_rules":[{"id":"rule-1","rule_type":"USER","attribute":"email","operator":"EQ",
        "values":["x@example.com"],"variation":"matched","priority":0}],
       "ts":2000}
    JSON
    matched = client.evaluate("child-flag", Tombstone::EvaluationContext.new(user_id: "u1", org_id: "", attrs: { "email" => "x@example.com" }))
    expect(matched.reason).to eq(Tombstone::EvaluationReason::RULE_MATCH)

    client.send(:dispatch_sse_event, "targeting_rules_updated", <<~JSON)
      {"flag_key":"child-flag","environment":"test","targeting_rules":[],"ts":3000}
    JSON
    cleared = client.evaluate("child-flag", Tombstone::EvaluationContext.new(user_id: "u1", org_id: "", attrs: { "email" => "x@example.com" }))
    expect(cleared.reason).not_to eq(Tombstone::EvaluationReason::RULE_MATCH)
  end
end
