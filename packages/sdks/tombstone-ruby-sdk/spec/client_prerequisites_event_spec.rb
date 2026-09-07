require "spec_helper"

# End-to-end regression suite for the SDK-4 prerequisites-streaming
# follow-up: a live "prerequisites_updated" SSE frame (services/flag-api/
# internal/api/v1/prerequisites.go's PrerequisitesEvent, relayed verbatim by
# the gateway) must actually change what evaluate() returns for the
# affected flag. Drives the real dispatch_sse_event(event_type, data)
# routing directly via client.send(...) (matching client_lag_spec.rb's
# established convention for private methods), so this suite exercises the
# ACTUAL if/elsif/else dispatch branch, not just the downstream handler in
# isolation.
#
# Also proactively closes the exact gap PR #235/#236's own adversarial
# reviews found: a staleness test that only uses a "clearly older" ts
# cannot distinguish a correct "<" comparison from a buggy "<=" regression,
# since both reject that input identically. The "ts equal to the cached
# value" spec below is the one that actually pins the "<" behavior down.
RSpec.describe Tombstone::Client do
  let(:client) { described_class.new(sdk_key: "sdk-test-key", environment: "test") }

  def load(ts)
    client.instance_variable_get(:@cache).load_snapshot(
      [
        Tombstone::FlagEnvironmentState.new(
          flag_id: "1", flag_key: "parent-flag", environment: "test",
          enabled: false, rollout_pct: 0, safe_default: "false", updated_at: 0
        ),
        Tombstone::FlagEnvironmentState.new(
          flag_id: "2", flag_key: "child-flag", environment: "test",
          enabled: true, rollout_pct: 100, safe_default: "false", updated_at: 0
        )
      ],
      ts
    )
  end

  it "a live event newer than the snapshot changes evaluate()'s outcome" do
    load(1000)

    before = client.evaluate("child-flag", Tombstone::EvaluationContext.of("u1"))
    expect(before.value).to eq(true)
    expect(before.reason).not_to eq(Tombstone::EvaluationReason::PREREQUISITE_FAILED)

    client.send(:dispatch_sse_event, "prerequisites_updated", <<~JSON)
      {"flag_key":"child-flag","environment":"test",
       "prerequisites":[{"flag_key":"parent-flag","required_variation":"true","gate":true}],
       "ts":2000}
    JSON

    after = client.evaluate("child-flag", Tombstone::EvaluationContext.of("u1"))
    expect(after.reason).to eq(Tombstone::EvaluationReason::PREREQUISITE_FAILED)
    expect(after.value).to eq(false)
  end

  it "an event older than the cached ts is rejected" do
    load(5000)

    client.send(:dispatch_sse_event, "prerequisites_updated", <<~JSON)
      {"flag_key":"child-flag","environment":"test",
       "prerequisites":[{"flag_key":"parent-flag","required_variation":"true","gate":true}],
       "ts":3000}
    JSON

    result = client.evaluate("child-flag", Tombstone::EvaluationContext.of("u1"))
    expect(result.reason).not_to eq(Tombstone::EvaluationReason::PREREQUISITE_FAILED)
    expect(result.value).to eq(true)
  end

  it "an event with ts equal to the cached ts is applied" do
    load(5000)

    client.send(:dispatch_sse_event, "prerequisites_updated", <<~JSON)
      {"flag_key":"child-flag","environment":"test",
       "prerequisites":[{"flag_key":"parent-flag","required_variation":"true","gate":true}],
       "ts":5000}
    JSON

    result = client.evaluate("child-flag", Tombstone::EvaluationContext.of("u1"))
    expect(result.reason).to eq(Tombstone::EvaluationReason::PREREQUISITE_FAILED)
    expect(result.value).to eq(false)
  end

  it "an update for a flag never seen is a no-op" do
    load(1000)

    expect do
      client.send(:dispatch_sse_event, "prerequisites_updated", <<~JSON)
        {"flag_key":"never-configured-flag","environment":"test",
         "prerequisites":[{"flag_key":"parent-flag","required_variation":"true"}],
         "ts":9999}
      JSON
    end.not_to raise_error

    result = client.evaluate("child-flag", Tombstone::EvaluationContext.of("u1"))
    expect(result.reason).not_to eq(Tombstone::EvaluationReason::PREREQUISITE_FAILED)
  end

  it "malformed JSON is swallowed, not raised" do
    load(1000)
    expect { client.send(:dispatch_sse_event, "prerequisites_updated", "not valid json{{{") }.not_to raise_error
  end

  it "a syntactically valid payload with no flag_key at all is swallowed, not raised" do
    # Distinct from the malformed-JSON case above: this payload IS valid
    # JSON, it simply omits flag_key entirely -- the exact case
    # Client#apply_prerequisites_event's `return unless flag_key` guard
    # exists to handle.
    load(1000)
    expect do
      client.send(:dispatch_sse_event, "prerequisites_updated", <<~JSON)
        {"environment":"test","prerequisites":[],"ts":9999}
      JSON
    end.not_to raise_error

    result = client.evaluate("child-flag", Tombstone::EvaluationContext.of("u1"))
    expect(result.reason).not_to eq(Tombstone::EvaluationReason::PREREQUISITE_FAILED)
  end

  it "gate omitted on the wire defaults to true, matching flag-api's own AddPrerequisite default" do
    load(1000)

    client.send(:dispatch_sse_event, "prerequisites_updated", <<~JSON)
      {"flag_key":"child-flag","environment":"test",
       "prerequisites":[{"flag_key":"parent-flag","required_variation":"true"}],
       "ts":2000}
    JSON

    result = client.evaluate("child-flag", Tombstone::EvaluationContext.of("u1"))
    expect(result.reason).to eq(Tombstone::EvaluationReason::PREREQUISITE_FAILED)
  end
end
