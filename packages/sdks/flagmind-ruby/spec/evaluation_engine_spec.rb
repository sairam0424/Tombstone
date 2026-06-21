require "spec_helper"
require "tombstone"

RSpec.describe Tombstone::EvaluationEngine do
  let(:engine) { described_class.new }
  let(:ctx) { Tombstone::EvaluationContext.of("user-abc-123") }

  def flag(enabled:, pct:)
    Tombstone::FlagEnvironmentState.new(
      flag_id: "id1", flag_key: "test-flag", environment: "test",
      enabled: enabled, rollout_pct: pct, safe_default: "false", updated_at: 0
    )
  end

  it "returns OFF when disabled" do
    r = engine.evaluate(flag(enabled: false, pct: 100), ctx, false, "test-flag")
    expect(r.reason).to eq(Tombstone::EvaluationReason::OFF)
    expect(r.value).to be false
  end

  it "returns true at 100% rollout" do
    r = engine.evaluate(flag(enabled: true, pct: 100), ctx, false, "test-flag")
    expect(r.reason).to eq(Tombstone::EvaluationReason::FALLTHROUGH)
    expect(r.value).to be true
  end

  it "returns false at 0% rollout" do
    r = engine.evaluate(flag(enabled: true, pct: 0), ctx, false, "test-flag")
    expect(r.value).to be false
  end

  it "returns ERROR for nil flag state" do
    r = engine.evaluate(nil, ctx, false, "missing")
    expect(r.reason).to eq(Tombstone::EvaluationReason::ERROR)
  end

  it "is sticky — same user always gets same result" do
    f = flag(enabled: true, pct: 50)
    results = 20.times.map { engine.evaluate(f, ctx, false, "test-flag").value }.uniq
    expect(results.size).to eq(1)
  end

  describe "MurmurHash3 parity with TypeScript" do
    [
      ["checkout-v2", "user-abc-123", 100, true],
      ["checkout-v2", "user-abc-123", 0, false],
      ["checkout-v2", "user-xyz-789", 50, false],
    ].each do |flag_key, user_id, pct, expected|
      it "#{flag_key} #{user_id} #{pct}% → #{expected}" do
        f = flag(enabled: true, pct: pct)
        c = Tombstone::EvaluationContext.of(user_id)
        r = engine.evaluate(f, c, false, flag_key)
        expect(r.value).to eq(expected)
      end
    end
  end
end
