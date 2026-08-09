require "spec_helper"

RSpec.describe "Entrypoint loading" do
  it "can require 'tombstone' successfully" do
    # This test runs in an RSpec context where tombstone is already required by spec_helper,
    # but we verify the module is defined and accessible.
    expect(defined?(Tombstone)).to eq("constant")
    expect(Tombstone).to be_a(Module)
  end

  it "exposes Tombstone::Client" do
    expect(defined?(Tombstone::Client)).to eq("constant")
  end

  it "exposes Tombstone::EvaluationEngine" do
    expect(defined?(Tombstone::EvaluationEngine)).to eq("constant")
  end

  it "does not expose a Flagmind constant" do
    expect(defined?(Flagmind)).to be_nil
  end
end
