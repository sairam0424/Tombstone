require "spec_helper"

# Found by adversarial review of PR #248: a syntactically valid JSON `data:`
# line that isn't a Hash at the top level (e.g. "null", "42", "[1,2,3]",
# a bare string) parses successfully via JSON.parse, so `rescue
# JSON::ParserError` alone does NOT catch the NoMethodError/TypeError that
# `data["flag_key"]` then raises. Before this fix, that exception propagated
# past apply_event/apply_prerequisites_event/apply_targeting_rules_event
# entirely and was only caught by start_sse_listener's own broad
# `rescue => e; sleep 3 if @connected`, which tears down and reconnects the
# WHOLE SSE connection for one malformed event instead of just skipping it --
# a stronger violation of the "malformed/missing SSE payloads must be
# swallowed, not raised" invariant than a per-event no-op. Covers all three
# dispatch_sse_event branches, since the identical bug existed in all three
# (apply_event predates this PR; the fix was applied to all three for
# consistency, not just the new targeting_rules handler).
RSpec.describe Tombstone::Client do
  let(:client) { described_class.new(sdk_key: "sdk-test-key", environment: "test") }

  def load_child_flag(ts)
    client.instance_variable_get(:@cache).load_snapshot(
      [
        Tombstone::FlagEnvironmentState.new(
          flag_id: "2", flag_key: "child-flag", environment: "test",
          enabled: true, rollout_pct: 100, safe_default: "false", updated_at: 0
        )
      ],
      ts
    )
  end

  ["null", "42", "[1,2,3]", '"a bare string"', "true"].each do |payload|
    it "a plain flag-update event with valid-JSON non-Hash payload #{payload.inspect} is swallowed, not raised" do
      load_child_flag(1000)
      expect { client.send(:dispatch_sse_event, nil, payload) }.not_to raise_error
      # The flag's cached state must be untouched -- a non-Hash payload has
      # no flag_key to act on at all.
      expect(client.evaluate("child-flag", Tombstone::EvaluationContext.of("u1")).value).to eq(true)
    end

    it "a prerequisites_updated event with valid-JSON non-Hash payload #{payload.inspect} is swallowed, not raised" do
      load_child_flag(1000)
      expect { client.send(:dispatch_sse_event, "prerequisites_updated", payload) }.not_to raise_error
    end

    it "a targeting_rules_updated event with valid-JSON non-Hash payload #{payload.inspect} is swallowed, not raised" do
      load_child_flag(1000)
      expect { client.send(:dispatch_sse_event, "targeting_rules_updated", payload) }.not_to raise_error
    end
  end
end
