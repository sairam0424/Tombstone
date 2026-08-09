require "spec_helper"

RSpec.describe Tombstone::InconclusiveMatchError do
  it "is a StandardError" do
    err = Tombstone::InconclusiveMatchError.new("attribute missing")
    expect(err).to be_a(StandardError)
    expect(err.message).to eq("attribute missing")
  end

  it "can be raised and rescued" do
    expect {
      raise Tombstone::InconclusiveMatchError, "test message"
    }.to raise_error(Tombstone::InconclusiveMatchError, "test message")
  end
end
