namespace Tombstone.Tests;
using System.Net;
using System.Text;
using Xunit;

/// <summary>
/// Verifies the SSE client recovers from a gateway "lag" event by re-running the
/// SAME snapshot fetch ConnectAsync uses, and that a burst of lag frames collapses
/// into a single debounced refetch. The gateway emits an `event: lag` frame right
/// before it DROPS a real flag-update event when the client falls behind (see
/// services/gateway/internal/hub/hub.go); without recovery the cache goes stale.
/// </summary>
public class LagRecoveryTests
{
    private const string SnapshotPath = "/api/v1/environments/snapshot";
    private const string StreamPath = "/api/v1/stream";
    private const int DebounceMs = 120;

    // One SSE "lag" frame exactly as the gateway writes it: an `event: lag` line,
    // a `data:` line, then a blank line terminating the frame.
    private static string LagFrame(int lagMs) => $"event: lag\ndata: {{\"lag_ms\":{lagMs}}}\n\n";

    [Fact]
    public async Task SingleLagEvent_TriggersExactlyOneRefetch()
    {
        var handler = new StubHandler(LagFrame(10));
        using var client = new TombstoneClient(
            "sdk-key", "test",
            lagRefetchDebounceMs: DebounceMs,
            httpMessageHandler: handler);

        await client.ConnectAsync();
        // ConnectAsync fetches exactly one snapshot before opening the stream.
        Assert.Equal(1, handler.SnapshotRequests);

        await handler.WaitForSnapshotRequestsAsync(2, TimeSpan.FromSeconds(2));
        // Give any (unexpected) additional refetch time to fire before asserting.
        await Task.Delay(DebounceMs * 3);

        // connect() snapshot + exactly one lag-triggered refetch.
        Assert.Equal(2, handler.SnapshotRequests);
    }

    [Fact]
    public async Task BurstOfLagEvents_CoalescesIntoOneRefetch()
    {
        var burst = new StringBuilder();
        for (var i = 0; i < 5; i++) burst.Append(LagFrame(i));

        var handler = new StubHandler(burst.ToString());
        using var client = new TombstoneClient(
            "sdk-key", "test",
            lagRefetchDebounceMs: DebounceMs,
            httpMessageHandler: handler);

        await client.ConnectAsync();
        Assert.Equal(1, handler.SnapshotRequests);

        await handler.WaitForSnapshotRequestsAsync(2, TimeSpan.FromSeconds(2));
        // A non-coalescing implementation would fire one refetch per frame inside
        // this window; wait it out so such a regression would push the count past 2.
        await Task.Delay(DebounceMs * 3);

        // Five lag frames within the debounce window -> a single coalesced refetch.
        Assert.Equal(2, handler.SnapshotRequests);
    }

    // Stubs the transport: counts snapshot requests and serves the given SSE
    // payload on the stream endpoint, mirroring the real gateway/flag-api routes.
    private sealed class StubHandler : HttpMessageHandler
    {
        private readonly string _sse;
        private int _snapshotRequests;

        public StubHandler(string sse) => _sse = sse;

        public int SnapshotRequests => Volatile.Read(ref _snapshotRequests);

        protected override Task<HttpResponseMessage> SendAsync(
            HttpRequestMessage request, CancellationToken cancellationToken)
        {
            var path = request.RequestUri!.AbsolutePath;
            if (path == SnapshotPath)
            {
                Interlocked.Increment(ref _snapshotRequests);
                return Task.FromResult(new HttpResponseMessage(HttpStatusCode.OK)
                {
                    Content = new StringContent("{\"flags\":[]}", Encoding.UTF8, "application/json"),
                });
            }
            if (path == StreamPath)
            {
                return Task.FromResult(new HttpResponseMessage(HttpStatusCode.OK)
                {
                    Content = new StreamContent(new SseStream(_sse)),
                });
            }
            return Task.FromResult(new HttpResponseMessage(HttpStatusCode.NotFound));
        }

        public async Task WaitForSnapshotRequestsAsync(int target, TimeSpan timeout)
        {
            var deadline = DateTime.UtcNow + timeout;
            while (SnapshotRequests < target && DateTime.UtcNow < deadline)
                await Task.Delay(10);
        }
    }

    // A read-only stream that yields the SSE payload once, then blocks until the
    // reader's token is cancelled — emulating a long-lived open SSE connection so
    // the client processes the frames exactly once and never reconnects to re-read
    // them (which would inflate the refetch count and break the assertions).
    private sealed class SseStream : Stream
    {
        private readonly byte[] _data;
        private int _pos;

        public SseStream(string sse) => _data = Encoding.UTF8.GetBytes(sse);

        public override bool CanRead => true;
        public override bool CanSeek => false;
        public override bool CanWrite => false;
        public override long Length => throw new NotSupportedException();
        public override long Position { get => throw new NotSupportedException(); set => throw new NotSupportedException(); }
        public override void Flush() { }
        public override long Seek(long offset, SeekOrigin origin) => throw new NotSupportedException();
        public override void SetLength(long value) => throw new NotSupportedException();
        public override void Write(byte[] buffer, int offset, int count) => throw new NotSupportedException();

        public override int Read(byte[] buffer, int offset, int count) =>
            ReadAsync(buffer.AsMemory(offset, count), CancellationToken.None).AsTask().GetAwaiter().GetResult();

        public override Task<int> ReadAsync(byte[] buffer, int offset, int count, CancellationToken cancellationToken) =>
            ReadAsync(buffer.AsMemory(offset, count), cancellationToken).AsTask();

        public override async ValueTask<int> ReadAsync(Memory<byte> buffer, CancellationToken cancellationToken = default)
        {
            if (_pos < _data.Length)
            {
                var n = Math.Min(buffer.Length, _data.Length - _pos);
                _data.AsMemory(_pos, n).CopyTo(buffer);
                _pos += n;
                return n;
            }
            // Payload exhausted: stay open (like a live SSE stream) until cancelled.
            await Task.Delay(Timeout.Infinite, cancellationToken);
            return 0;
        }
    }
}
