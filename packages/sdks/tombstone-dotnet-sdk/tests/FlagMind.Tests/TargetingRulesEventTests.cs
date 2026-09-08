namespace Tombstone.Tests;
using System.Net;
using System.Text;
using Xunit;

/// <summary>
/// End-to-end regression suite for the Java/Ruby-SDK-parity follow-up: a
/// live "targeting_rules_updated" SSE frame (services/flag-api/internal/
/// api/v1/targeting_rules.go's TargetingRulesEvent, relayed verbatim by the
/// gateway) must actually change what Evaluate() returns for the affected
/// flag. Mirrors PrerequisitesEventTests.cs exactly, drives the REAL SSE
/// stream via a stubbed HttpMessageHandler.
/// </summary>
public class TargetingRulesEventTests
{
    private const string SnapshotPath = "/api/v1/environments/snapshot";
    private const string StreamPath = "/api/v1/stream";

    private static string SnapshotJson(long ts) => $$"""
        {"environment":"test","flags":[
          {"flag_id":"2","flag_key":"child-flag","environment":"test",
           "enabled":true,"rollout_pct":0,"safe_default":"off","updated_at":0}
        ],"hash":"h","ts":{{ts}}}
        """;

    private static string RuleFrame(long ts) =>
        $"event: targeting_rules_updated\ndata: {{\"flag_key\":\"child-flag\",\"environment\":\"test\"," +
        $"\"targeting_rules\":[{{\"id\":\"rule-1\",\"rule_type\":\"USER\",\"attribute\":\"email\",\"operator\":\"EQ\"," +
        $"\"values\":[\"x@example.com\"],\"variation\":\"matched\",\"priority\":0}}],\"ts\":{ts}}}\n\n";

    private static readonly Dictionary<string, object?> Defaults = new() { ["child-flag"] = "off" };

    private static async Task<EvaluationResult<string>> WaitForReasonAsync(
        TombstoneClient client, EvaluationReason target, TimeSpan timeout)
    {
        var deadline = DateTime.UtcNow + timeout;
        EvaluationResult<string> result;
        do
        {
            result = client.Evaluate<string>("child-flag", new EvaluationContext("u1", "", new() { ["email"] = "x@example.com" }));
            if (result.Reason == target) return result;
            await Task.Delay(10);
        } while (DateTime.UtcNow < deadline);
        return result;
    }

    [Fact]
    public async Task ALiveEventNewerThanTheSnapshot_ChangesEvaluatesOutcome()
    {
        var handler = new StubHandler(SnapshotJson(1000), RuleFrame(2000));
        using var client = new TombstoneClient("sdk-key", "test", httpMessageHandler: handler, defaults: Defaults);
        await client.ConnectAsync();

        var before = client.Evaluate<string>("child-flag", new EvaluationContext("u1", "", new() { ["email"] = "x@example.com" }));
        Assert.NotEqual(EvaluationReason.RuleMatch, before.Reason);

        var after = await WaitForReasonAsync(client, EvaluationReason.RuleMatch, TimeSpan.FromSeconds(2));
        Assert.Equal(EvaluationReason.RuleMatch, after.Reason);
        Assert.Equal("matched", after.Value);
    }

    [Fact]
    public async Task AnEventOlderThanTheCachedTs_IsRejected()
    {
        var handler = new StubHandler(SnapshotJson(5000), RuleFrame(3000));
        using var client = new TombstoneClient("sdk-key", "test", httpMessageHandler: handler, defaults: Defaults);
        await client.ConnectAsync();

        await Task.Delay(300);

        var result = client.Evaluate<string>("child-flag", new EvaluationContext("u1", "", new() { ["email"] = "x@example.com" }));
        Assert.NotEqual(EvaluationReason.RuleMatch, result.Reason);
    }

    [Fact]
    public async Task AnEventWithTsEqualToTheCachedTs_IsApplied()
    {
        var handler = new StubHandler(SnapshotJson(5000), RuleFrame(5000));
        using var client = new TombstoneClient("sdk-key", "test", httpMessageHandler: handler, defaults: Defaults);
        await client.ConnectAsync();

        var result = await WaitForReasonAsync(client, EvaluationReason.RuleMatch, TimeSpan.FromSeconds(2));
        Assert.Equal(EvaluationReason.RuleMatch, result.Reason);
    }

    [Fact]
    public async Task AFrameWithNoFlagKeyAtAll_DoesNotThrowAndIsANoOp()
    {
        var noFlagKeyFrame = "event: targeting_rules_updated\ndata: {\"environment\":\"test\",\"targeting_rules\":[],\"ts\":9999}\n\n";
        var handler = new StubHandler(SnapshotJson(1000), noFlagKeyFrame);
        using var client = new TombstoneClient("sdk-key", "test", httpMessageHandler: handler, defaults: Defaults);
        await client.ConnectAsync();

        await Task.Delay(300);

        var result = client.Evaluate<string>("child-flag", new EvaluationContext("u1", "", new() { ["email"] = "x@example.com" }));
        Assert.NotEqual(EvaluationReason.RuleMatch, result.Reason);
    }

    // Found by adversarial review of PR #249: nothing in the suite proved
    // ApplyTargetingRulesEvent's bare `catch { }` actually swallows a
    // genuinely unparseable JSON payload -- the only malformed-payload test
    // above uses well-formed JSON that's merely missing "flag_key", which
    // never throws at all (TryGetProperty just returns false). A malformed
    // frame is served FIRST, immediately followed (same stream, no gap) by
    // a real rule frame -- if the malformed frame crashed instead of being
    // swallowed, the outer RunSseListenerAsync catch-all would trigger a
    // 3-second reconnect delay before replaying the SAME two frames from
    // the start, which the 2-second WaitForReasonAsync timeout below would
    // not survive, correctly failing this test.
    [Fact]
    public async Task MalformedJsonPayload_IsSwallowedNotRaised()
    {
        var malformedFrame = "event: targeting_rules_updated\ndata: not valid json{{{\n\n";
        var handler = new StubHandler(SnapshotJson(1000), malformedFrame + RuleFrame(2000));
        using var client = new TombstoneClient("sdk-key", "test", httpMessageHandler: handler, defaults: Defaults);
        await client.ConnectAsync();

        var result = await WaitForReasonAsync(client, EvaluationReason.RuleMatch, TimeSpan.FromSeconds(2));
        Assert.Equal(EvaluationReason.RuleMatch, result.Reason);
    }

    // A syntactically valid JSON payload that isn't an Object at the top
    // level (null/a number/an array) parses successfully via
    // JsonDocument.Parse, so the malformed-JSON test above does not
    // exercise this path -- JsonElement.TryGetProperty throws
    // InvalidOperationException for any non-Object ValueKind, which the
    // bare `catch { }` must also swallow. The identical bug class (a
    // narrower `catch (JsonException)` missing this) was found and fixed
    // in the Ruby SDK's own review (PR #248).
    [Theory]
    [InlineData("null")]
    [InlineData("42")]
    [InlineData("[1,2,3]")]
    public async Task ValidJsonNonObjectPayload_IsSwallowedNotRaised(string payload)
    {
        var nonObjectFrame = $"event: targeting_rules_updated\ndata: {payload}\n\n";
        var handler = new StubHandler(SnapshotJson(1000), nonObjectFrame + RuleFrame(2000));
        using var client = new TombstoneClient("sdk-key", "test", httpMessageHandler: handler, defaults: Defaults);
        await client.ConnectAsync();

        var result = await WaitForReasonAsync(client, EvaluationReason.RuleMatch, TimeSpan.FromSeconds(2));
        Assert.Equal(EvaluationReason.RuleMatch, result.Reason);
    }

    // The rule lives in the SNAPSHOT itself (not a live event) so the flag
    // already matches from ConnectAsync onward -- avoiding any race between
    // two back-to-back SSE frames served with no gap between them, which a
    // single WaitForReasonAsync(..., RuleMatch) poll could miss entirely if
    // both frames were already processed by its first check.
    private static string SnapshotJsonWithRule(long ts) => $$"""
        {"environment":"test","flags":[
          {"flag_id":"2","flag_key":"child-flag","environment":"test",
           "enabled":true,"rollout_pct":0,"safe_default":"off","updated_at":0,
           "targeting_rules":[{"id":"rule-1","rule_type":"USER","attribute":"email","operator":"EQ",
             "values":["x@example.com"],"variation":"matched","priority":0}]}
        ],"hash":"h","ts":{{ts}}}
        """;

    private static string ClearFrame(long ts) =>
        $"event: targeting_rules_updated\ndata: {{\"flag_key\":\"child-flag\",\"environment\":\"test\"," +
        $"\"targeting_rules\":[],\"ts\":{ts}}}\n\n";

    [Fact]
    public async Task AnEmptyTargetingRulesList_ClearsExistingRules()
    {
        var handler = new StubHandler(SnapshotJsonWithRule(1000), ClearFrame(2000));
        using var client = new TombstoneClient("sdk-key", "test", httpMessageHandler: handler, defaults: Defaults);
        await client.ConnectAsync();

        var matched = client.Evaluate<string>("child-flag", new EvaluationContext("u1", "", new() { ["email"] = "x@example.com" }));
        Assert.Equal(EvaluationReason.RuleMatch, matched.Reason);

        var cleared = await WaitForReasonAsync(client, EvaluationReason.Fallthrough, TimeSpan.FromSeconds(2));
        Assert.Equal(EvaluationReason.Fallthrough, cleared.Reason);
    }

    // Stubs the transport: serves the given snapshot JSON and a stream that
    // sends one SSE frame then stays open (never returns EOF) -- mirrors
    // PrerequisitesEventTests.cs's StubHandler/SseStream convention exactly.
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
