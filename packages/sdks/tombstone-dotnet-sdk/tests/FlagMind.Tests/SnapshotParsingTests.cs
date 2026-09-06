namespace Tombstone.Tests;
using System.Net;
using System.Text;
using Xunit;

/// <summary>
/// Regression suite for two bugs found while investigating SDK-4's
/// prerequisites-streaming follow-up (and confirmed identical to the Java/Ruby
/// SDKs' equivalent fixes, PR #231/#232):
///
/// 1. FetchSnapshotAsync built every real FlagEnvironmentState without ever
///    passing Prerequisites (defaulted to empty) and hardcoded UpdatedAt to
///    0L, regardless of what the wire actually sent.
/// 2. TombstoneClient.Evaluate called EvaluationEngine.Evaluate without a
///    flagLookup, which defaults to `_ => null` -- documented as being for
///    "callers [who have] no snapshot access". This client DOES have
///    snapshot access via its own cache, but never threaded it through.
///
/// The first 5 tests exercise ParseSnapshotResponse(string) directly (an
/// internal method, visible here via [assembly: InternalsVisibleTo(...)] in
/// FlagMindClient.cs) with hand-built JSON shaped exactly like flag-api's
/// real snapshot endpoint. The last 2 drive the real, public
/// ConnectAsync/Evaluate entry points end to end against a stubbed
/// HttpMessageHandler (matching LagRecoveryTests.cs's existing convention).
/// </summary>
public class SnapshotParsingTests
{
    [Fact]
    public void TopLevelFieldsParseCorrectlyFromRealWireJson()
    {
        var json = """
            {"environment":"production","flags":[
              {"flag_id":"1","flag_key":"known-flag","environment":"production",
               "enabled":true,"rollout_pct":100,"safe_default":"false","updated_at":1700000000,
               "prerequisites":[]}
            ],"hash":"h","ts":1700000000}
            """;
        var states = TombstoneClient.ParseSnapshotResponse(json);
        Assert.Single(states);
        var s = states[0];
        Assert.Equal("1", s.FlagId);
        Assert.Equal("known-flag", s.FlagKey);
        Assert.Equal("production", s.Environment);
        Assert.True(s.Enabled);
        Assert.Equal(100, s.RolloutPct);
        Assert.Equal("false", s.SafeDefault);
        Assert.Equal(1700000000L, s.UpdatedAt);
    }

    [Fact]
    public void PrerequisitesParseCorrectlyFromRealWireJson()
    {
        // flag-api's real per-prerequisite wire shape: "flag_key" (NOT
        // "prereq_flag_key" -- that's only the DB column name), plus
        // "required_variation"/"gate"/"priority".
        var json = """
            {"environment":"production","flags":[
              {"flag_id":"2","flag_key":"child-flag","environment":"production",
               "enabled":true,"rollout_pct":100,"safe_default":"false","updated_at":1700000000,
               "prerequisites":[
                 {"id":"prereq-1","flag_key":"parent-flag","required_variation":"true","gate":true,"priority":0}
               ]}
            ],"hash":"h","ts":1700000000}
            """;
        var states = TombstoneClient.ParseSnapshotResponse(json);
        var prereqs = states[0].Prerequisites;
        Assert.Single(prereqs);
        Assert.Equal("parent-flag", prereqs[0].FlagKey);
        Assert.Equal("true", prereqs[0].RequiredVariation);
        Assert.True(prereqs[0].Gate);
    }

    [Fact]
    public void GateOmittedOnTheWireDefaultsToTrue()
    {
        var json = """
            {"environment":"production","flags":[
              {"flag_id":"2","flag_key":"child-flag","environment":"production",
               "enabled":true,"rollout_pct":100,"safe_default":"false","updated_at":1700000000,
               "prerequisites":[
                 {"flag_key":"parent-flag","required_variation":"true"}
               ]}
            ],"hash":"h","ts":1700000000}
            """;
        var states = TombstoneClient.ParseSnapshotResponse(json);
        Assert.True(states[0].Prerequisites[0].Gate,
            "gate must default to true (hard-blocking) when the wire omits it, matching flag-api's own AddPrerequisite default");
    }

    [Fact]
    public void ExplicitGateFalseParsesAsSoft()
    {
        var json = """
            {"environment":"production","flags":[
              {"flag_id":"2","flag_key":"child-flag","environment":"production",
               "enabled":true,"rollout_pct":100,"safe_default":"false","updated_at":1700000000,
               "prerequisites":[
                 {"flag_key":"parent-flag","required_variation":"true","gate":false}
               ]}
            ],"hash":"h","ts":1700000000}
            """;
        var states = TombstoneClient.ParseSnapshotResponse(json);
        Assert.False(states[0].Prerequisites[0].Gate);
    }

    [Fact]
    public void FlagWithNoPrerequisitesFieldAtAllParsesAsEmptyNotAnError()
    {
        var json = """
            {"environment":"production","flags":[
              {"flag_id":"1","flag_key":"known-flag","environment":"production",
               "enabled":true,"rollout_pct":100,"safe_default":"false","updated_at":1700000000}
            ],"hash":"h","ts":1700000000}
            """;
        var states = TombstoneClient.ParseSnapshotResponse(json);
        Assert.Empty(states[0].Prerequisites);
    }

    [Fact]
    public async Task EvaluateResolvesARealSatisfiedHardGatedPrerequisiteFromItsOwnCache()
    {
        var json = """
            {"flags":[
              {"flag_id":"1","flag_key":"parent-flag","environment":"test",
               "enabled":true,"rollout_pct":100,"safe_default":"false","updated_at":0,"prerequisites":[]},
              {"flag_id":"2","flag_key":"child-flag","environment":"test",
               "enabled":true,"rollout_pct":100,"safe_default":"false","updated_at":0,
               "prerequisites":[{"flag_key":"parent-flag","required_variation":"true","gate":true}]}
            ]}
            """;
        using var client = new TombstoneClient("sdk-key", "test", httpMessageHandler: new SnapshotOnlyHandler(json));
        await client.ConnectAsync();

        var result = client.Evaluate<bool>("child-flag", EvaluationContext.Of("u1"));
        Assert.True(result.Value);
        Assert.NotEqual(EvaluationReason.PrerequisiteFailed, result.Reason);
    }

    [Fact]
    public async Task EvaluateBlocksOnAGenuinelyUnmetHardGatedPrerequisite()
    {
        var json = """
            {"flags":[
              {"flag_id":"1","flag_key":"parent-flag","environment":"test",
               "enabled":false,"rollout_pct":0,"safe_default":"false","updated_at":0,"prerequisites":[]},
              {"flag_id":"2","flag_key":"child-flag","environment":"test",
               "enabled":true,"rollout_pct":100,"safe_default":"false","updated_at":0,
               "prerequisites":[{"flag_key":"parent-flag","required_variation":"true","gate":true}]}
            ]}
            """;
        using var client = new TombstoneClient("sdk-key", "test", httpMessageHandler: new SnapshotOnlyHandler(json));
        await client.ConnectAsync();

        var result = client.Evaluate<bool>("child-flag", EvaluationContext.Of("u1"));
        Assert.Equal(EvaluationReason.PrerequisiteFailed, result.Reason);
    }

    // Serves the given JSON on the snapshot endpoint and a stream that stays
    // open (never returns EOF) on the SSE endpoint -- matching
    // LagRecoveryTests.cs's own SseStream convention exactly, for the same
    // reason documented there: an EOF-returning stream would make the SSE
    // listener's while loop spin in a tight busy-retry loop with no delay,
    // rather than just blocking harmlessly until this test's `using var
    // client` disposal cancels it.
    private sealed class SnapshotOnlyHandler : HttpMessageHandler
    {
        private readonly string _json;
        public SnapshotOnlyHandler(string json) => _json = json;

        protected override Task<HttpResponseMessage> SendAsync(
            HttpRequestMessage request, CancellationToken cancellationToken)
        {
            var path = request.RequestUri!.AbsolutePath;
            if (path == "/api/v1/environments/snapshot")
            {
                return Task.FromResult(new HttpResponseMessage(HttpStatusCode.OK)
                {
                    Content = new StringContent(_json, Encoding.UTF8, "application/json"),
                });
            }
            return Task.FromResult(new HttpResponseMessage(HttpStatusCode.OK)
            {
                Content = new StreamContent(new NeverEndingStream()),
            });
        }
    }

    // A read-only stream with no data that blocks forever instead of
    // returning EOF -- see SnapshotOnlyHandler's own comment for why.
    private sealed class NeverEndingStream : Stream
    {
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
            await Task.Delay(Timeout.Infinite, cancellationToken);
            return 0;
        }
    }
}
