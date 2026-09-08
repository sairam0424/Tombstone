namespace Tombstone.Tests;
using System.Net;
using System.Text;
using Xunit;

/// <summary>
/// End-to-end regression suite for the SDK-4 prerequisites-streaming
/// follow-up: a live "prerequisites_updated" SSE frame (services/flag-api/
/// internal/api/v1/prerequisites.go's PrerequisitesEvent, relayed verbatim
/// by the gateway) must actually change what Evaluate() returns for the
/// affected flag. Drives the REAL SSE stream via a stubbed HttpMessageHandler
/// (mirroring LagRecoveryTests.cs's own SseStream/StubHandler convention),
/// so this suite exercises the ACTUAL if/elsif/else dispatch branch in
/// RunSseListenerAsync, not just the downstream handler in isolation.
///
/// Also proactively closes the exact gap PR #235/#236/#237's own
/// adversarial reviews found: a staleness test that only uses a "clearly
/// older" ts cannot distinguish a correct "&lt;" comparison from a buggy
/// "&lt;=" regression, since both reject that input identically. The
/// ts-equal-to-cached test below is the one that actually pins the "&lt;"
/// behavior down.
/// </summary>
public class PrerequisitesEventTests
{
    private const string SnapshotPath = "/api/v1/environments/snapshot";
    private const string StreamPath = "/api/v1/stream";

    private static string SnapshotJson(long ts) => $$"""
        {"environment":"test","flags":[
          {"flag_id":"1","flag_key":"parent-flag","environment":"test",
           "enabled":false,"rollout_pct":0,"safe_default":"false","updated_at":0},
          {"flag_id":"2","flag_key":"child-flag","environment":"test",
           "enabled":true,"rollout_pct":100,"safe_default":"false","updated_at":0}
        ],"hash":"h","ts":{{ts}}}
        """;

    private static string PrereqFrame(long ts) =>
        $"event: prerequisites_updated\ndata: {{\"flag_key\":\"child-flag\",\"environment\":\"test\"," +
        $"\"prerequisites\":[{{\"flag_key\":\"parent-flag\",\"required_variation\":\"true\",\"gate\":true}}],\"ts\":{ts}}}\n\n";

    private static async Task<EvaluationResult<bool>> WaitForReasonAsync(
        TombstoneClient client, EvaluationReason target, TimeSpan timeout)
    {
        var deadline = DateTime.UtcNow + timeout;
        EvaluationResult<bool> result;
        do
        {
            result = client.Evaluate<bool>("child-flag", EvaluationContext.Of("u1"));
            if (result.Reason == target) return result;
            await Task.Delay(10);
        } while (DateTime.UtcNow < deadline);
        return result;
    }

    [Fact]
    public async Task ALiveEventNewerThanTheSnapshot_ChangesEvaluatesOutcome()
    {
        var handler = new StubHandler(SnapshotJson(1000), PrereqFrame(2000));
        using var client = new TombstoneClient("sdk-key", "test", httpMessageHandler: handler);
        await client.ConnectAsync();

        var before = client.Evaluate<bool>("child-flag", EvaluationContext.Of("u1"));
        Assert.True(before.Value);
        Assert.NotEqual(EvaluationReason.PrerequisiteFailed, before.Reason);

        var after = await WaitForReasonAsync(client, EvaluationReason.PrerequisiteFailed, TimeSpan.FromSeconds(2));
        Assert.Equal(EvaluationReason.PrerequisiteFailed, after.Reason);
        Assert.False(after.Value);
    }

    [Fact]
    public async Task AnEventOlderThanTheCachedTs_IsRejected()
    {
        var handler = new StubHandler(SnapshotJson(5000), PrereqFrame(3000));
        using var client = new TombstoneClient("sdk-key", "test", httpMessageHandler: handler);
        await client.ConnectAsync();

        // Give the (incorrectly-expected-to-be-rejected) frame ample time to
        // have been processed if it were wrongly applied.
        await Task.Delay(300);

        var result = client.Evaluate<bool>("child-flag", EvaluationContext.Of("u1"));
        Assert.NotEqual(EvaluationReason.PrerequisiteFailed, result.Reason);
        Assert.True(result.Value);
    }

    [Fact]
    public async Task AnEventWithTsEqualToTheCachedTs_IsApplied()
    {
        var handler = new StubHandler(SnapshotJson(5000), PrereqFrame(5000));
        using var client = new TombstoneClient("sdk-key", "test", httpMessageHandler: handler);
        await client.ConnectAsync();

        var result = await WaitForReasonAsync(client, EvaluationReason.PrerequisiteFailed, TimeSpan.FromSeconds(2));
        Assert.Equal(EvaluationReason.PrerequisiteFailed, result.Reason);
        Assert.False(result.Value);
    }

    [Fact]
    public async Task AFrameWithNoFlagKeyAtAll_DoesNotThrowAndIsANoOp()
    {
        // Distinct from a syntactically-invalid JSON frame: this payload is
        // valid JSON but omits flag_key entirely, the exact case
        // ApplyPrerequisitesEvent's `string.IsNullOrEmpty(flagKey)` guard
        // exists to handle.
        var noFlagKeyFrame = "event: prerequisites_updated\ndata: {\"environment\":\"test\",\"prerequisites\":[],\"ts\":9999}\n\n";
        var handler = new StubHandler(SnapshotJson(1000), noFlagKeyFrame);
        using var client = new TombstoneClient("sdk-key", "test", httpMessageHandler: handler);
        await client.ConnectAsync();

        await Task.Delay(300);

        var result = client.Evaluate<bool>("child-flag", EvaluationContext.Of("u1"));
        Assert.NotEqual(EvaluationReason.PrerequisiteFailed, result.Reason);
    }

    // Stubs the transport: serves the given snapshot JSON and a stream that
    // sends one SSE frame then stays open (never returns EOF) -- mirrors
    // LagRecoveryTests.cs's StubHandler/SseStream convention exactly, for
    // the same reason: an EOF-returning stream would make the SSE listener's
    // while loop spin in a tight busy-retry loop instead of just blocking
    // harmlessly until this test's `using var client` disposal cancels it.
    private sealed class StubHandler : HttpMessageHandler
    {
        private readonly string _snapshotJson;
        private readonly string _sseFrame;

        public StubHandler(string snapshotJson, string sseFrame)
        {
            _snapshotJson = snapshotJson;
            _sseFrame = sseFrame;
        }

        protected override Task<HttpResponseMessage> SendAsync(
            HttpRequestMessage request, CancellationToken cancellationToken)
        {
            var path = request.RequestUri!.AbsolutePath;
            if (path == SnapshotPath)
            {
                return Task.FromResult(new HttpResponseMessage(HttpStatusCode.OK)
                {
                    Content = new StringContent(_snapshotJson, Encoding.UTF8, "application/json"),
                });
            }
            if (path == StreamPath)
            {
                return Task.FromResult(new HttpResponseMessage(HttpStatusCode.OK)
                {
                    Content = new StreamContent(new SseStream(_sseFrame)),
                });
            }
            return Task.FromResult(new HttpResponseMessage(HttpStatusCode.NotFound));
        }
    }

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
            await Task.Delay(Timeout.Infinite, cancellationToken);
            return 0;
        }
    }
}
