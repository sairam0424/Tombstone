require "spec_helper"

RSpec.describe Tombstone::Client do
  # A short debounce window keeps the coalescing behaviour fast and deterministic
  # to assert; production defaults to ~500ms.
  let(:client) do
    described_class.new(sdk_key: "sdk-test-key", environment: "test", lag_debounce: 0.1)
  end

  # Stub the snapshot refetch (the same method connect() uses) and count how many
  # times it fires. The count is mutex-guarded because the debounce timer invokes
  # it from a background thread.
  def stub_snapshot_counter
    count = 0
    mtx = Mutex.new
    allow(client).to receive(:fetch_snapshot) { mtx.synchronize { count += 1 } }
    -> { mtx.synchronize { count } }
  end

  before do
    # The debounced refetch only fires while the client considers itself connected.
    client.instance_variable_set(:@connected, true)
  end

  describe "gateway lag recovery" do
    it "refetches the snapshot exactly once for a single lag event" do
      refetch_count = stub_snapshot_counter

      client.send(:dispatch_sse_event, "lag", '{"lag_ms":0}')
      client.instance_variable_get(:@lag_timer)&.join

      expect(refetch_count.call).to eq(1)
    end

    it "coalesces a burst of lag events within the window into one refetch" do
      refetch_count = stub_snapshot_counter

      5.times { client.send(:dispatch_sse_event, "lag", '{"lag_ms":0}') }
      client.instance_variable_get(:@lag_timer)&.join

      expect(refetch_count.call).to eq(1)
    end
  end
end
