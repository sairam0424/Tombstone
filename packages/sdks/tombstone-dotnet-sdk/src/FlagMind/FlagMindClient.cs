using System.Globalization;
using System.Net.Http.Headers;
using System.Runtime.CompilerServices;
using System.Text.Json;

// Lets FlagMind.Tests exercise ParseSnapshotResponse/ParsePrerequisites
// directly with hand-built JSON, without standing up a mock HTTP server --
// mirrors the Java SDK's identical package-private-visibility rationale for
// its own equivalent parseSnapshotResponse method.
[assembly: InternalsVisibleTo("FlagMind.Tests")]

namespace Tombstone;

public sealed class TombstoneClient : IDisposable
{
    private readonly string _sdkKey;
    private readonly string _environment;
    private readonly string _apiUrl;
    private readonly string _gatewayUrl;
    private readonly Dictionary<string, object?> _defaults;
    private readonly FlagCache _cache = new();
    private readonly EvaluationEngine _engine = new();
    private readonly HttpClient _http;
    private readonly int _lagRefetchDebounceMs;
    private CancellationTokenSource? _cts;
    // Debounce timer for coalescing a burst of "lag" events into a SINGLE
    // snapshot refetch. Null when no refetch is pending. Guarded by
    // _lagRefetchLock; cancelled on Dispose to stay cancel-safe.
    private CancellationTokenSource? _lagRefetchCts;
    private readonly object _lagRefetchLock = new();
    private bool _connected;

    public TombstoneClient(string sdkKey, string environment,
        string? apiUrl = null, string? gatewayUrl = null,
        Dictionary<string, object?>? defaults = null,
        int lagRefetchDebounceMs = 500,
        HttpMessageHandler? httpMessageHandler = null)
    {
        _sdkKey = sdkKey;
        _environment = environment;
        _apiUrl = apiUrl ?? "http://localhost:8081";
        _gatewayUrl = gatewayUrl ?? "http://localhost:8080";
        _defaults = defaults ?? new();
        _lagRefetchDebounceMs = lagRefetchDebounceMs;
        _http = httpMessageHandler is null ? new HttpClient() : new HttpClient(httpMessageHandler);
        _http.DefaultRequestHeaders.Authorization =
            new AuthenticationHeaderValue("Bearer", sdkKey);
    }

    public async Task ConnectAsync(CancellationToken ct = default)
    {
        await FetchSnapshotAsync(ct);
        _cts = CancellationTokenSource.CreateLinkedTokenSource(ct);
        _ = Task.Run(() => RunSseListenerAsync(_cts.Token), _cts.Token);
        _connected = true;
    }

    public EvaluationResult<T> Evaluate<T>(string flagKey, EvaluationContext context)
    {
        var state = _cache.Get(flagKey);
        var def = _defaults.TryGetValue(flagKey, out var d) && d is T t ? t : default!;
        // Passes a real flagLookup backed by _cache, NOT the omitted-param
        // default used before -- EvaluationEngine.Evaluate defaults
        // flagLookup to `_ => null` when omitted, documented there as being
        // for "callers [who have] no snapshot access". This client DOES
        // have snapshot access via _cache, but never threaded it through.
        // Before FetchSnapshotAsync's own fix (populating real
        // Prerequisites), Prerequisites was always empty and Step 2 never
        // actually ran against real data, so a null-returning lookup was
        // dead code from this call path specifically. Once Prerequisites
        // are real, omitting flagLookup here would make ANY hard-gated
        // prerequisite permanently PrerequisiteFailed regardless of the
        // real dependency's state -- swapping "prerequisites silently
        // ignored" for "every gated flag permanently blocked", which is
        // worse. Found by adversarial review of the identical bug in the
        // Java SDK's equivalent fix (PR #231), confirmed and fixed here too.
        return _engine.Evaluate(state, context, def, flagKey, flagLookup: k => _cache.Get(k));
    }

    public bool IsEnabled(string flagKey, EvaluationContext context)
        => Evaluate<bool>(flagKey, context).Value;

    public bool IsConnected => _connected;
    public IEnumerable<string> FlagKeys() => _cache.FlagKeys();

    private async Task FetchSnapshotAsync(CancellationToken ct)
    {
        var url = $"{_apiUrl}/api/v1/environments/snapshot?environment={_environment}";
        var resp = await _http.GetAsync(url, ct);
        if (!resp.IsSuccessStatusCode) return;
        var json = await resp.Content.ReadAsStringAsync(ct);
        var parsed = ParseSnapshotResponse(json);
        _cache.LoadSnapshot(parsed.Flags, parsed.Ts);
    }

    // Internal (not private) so a test in this assembly can exercise the
    // real wire-parsing logic directly with a hand-built JSON string,
    // without standing up a mock HTTP server.
    //
    // Before this fix, every FlagEnvironmentState built from a real
    // snapshot never passed Prerequisites at all (defaulted to empty) and
    // hardcoded UpdatedAt to 0L, regardless of what the wire actually
    // sent -- this client's prerequisite gating never worked against a
    // real backend at all (found while investigating SDK-4's
    // prerequisites-streaming follow-up). flag-api's real snapshot
    // response has no targeting_rules/target_list/hash_version fields
    // today -- those stay empty/default 1, same as before.
    //
    // Returns the snapshot's own top-level ts alongside the parsed flags
    // (flag-api's environments.go: Ts: time.Now().Unix()) -- FlagCache.
    // LoadSnapshot needs it to compare against any live prerequisites_updated
    // event that may have already advanced a flag's own PrerequisitesUpdatedAt
    // further than this snapshot itself reflects.
    internal static (List<FlagEnvironmentState> Flags, long Ts) ParseSnapshotResponse(string json)
    {
        using var doc = JsonDocument.Parse(json);
        var ts = doc.RootElement.TryGetProperty("ts", out var tsEl) ? tsEl.GetInt64() : 0L;
        var flags = doc.RootElement.GetProperty("flags").EnumerateArray()
            .Select(f => new FlagEnvironmentState(
                f.GetProperty("flag_id").GetString() ?? "",
                f.GetProperty("flag_key").GetString() ?? "",
                f.GetProperty("environment").GetString() ?? "",
                f.GetProperty("enabled").GetBoolean(),
                f.GetProperty("rollout_pct").GetInt32(),
                f.GetProperty("safe_default").GetString() ?? "false",
                f.TryGetProperty("updated_at", out var ua) ? ua.GetInt64() : 0L,
                Prerequisites: ParsePrerequisites(f),
                TargetingRules: ParseTargetingRules(f)
            )).ToList();
        return (flags, ts);
    }

    // flag-api's real wire shape (services/flag-api/internal/api/v1/
    // environments.go's SnapshotPrerequisite): "flag_key" (NOT
    // "prereq_flag_key" -- that's only flag_prerequisites' own DB column
    // name, matching proto's ParentCondition message and every other SDK's
    // own FlagPrerequisite type), plus "required_variation"/"gate". "gate"
    // defaults to true (hard-blocking) when the wire omits it, matching
    // flag-api's own AddPrerequisite default.
    private static List<FlagPrerequisite> ParsePrerequisites(JsonElement flag)
    {
        if (!flag.TryGetProperty("prerequisites", out var raw) || raw.ValueKind != JsonValueKind.Array)
            return new();
        return raw.EnumerateArray()
            .Select(p => new FlagPrerequisite(
                p.TryGetProperty("flag_key", out var fk) ? fk.GetString() ?? "" : "",
                p.TryGetProperty("required_variation", out var rv) ? rv.GetString() ?? "true" : "true",
                !(p.TryGetProperty("gate", out var g) && g.ValueKind == JsonValueKind.False)
            ))
            .ToList();
    }

    // flag-api's real per-rule wire shape (services/flag-api/internal/api/v1/
    // targeting_rules.go): "id"/"rule_type"/"attribute"/"operator"/"values"/
    // "variation"/"priority" -- ONE flat condition per rule row. This SDK's
    // own TargetingRule model (mirroring Python/Java/Ruby's GrowthBook-style
    // design: multiple AND-combined conditions per rule + per-rule rollout
    // sub-bucketing) predates the real backend format and doesn't match it
    // 1:1 -- adapted here into a single-element conditions list, with
    // RolloutPct fixed at 100 (there is no per-rule rollout concept on the
    // backend; 100 means "always apply once matched"), mirroring the Java/
    // Ruby SDKs' identical ParseTargetingRules adapter (PRs #247/#248).
    private static List<TargetingRule> ParseTargetingRules(JsonElement flag)
    {
        if (!flag.TryGetProperty("targeting_rules", out var raw) || raw.ValueKind != JsonValueKind.Array)
            return new();
        var result = new List<TargetingRule>();
        foreach (var r in raw.EnumerateArray())
        {
            if (r.ValueKind != JsonValueKind.Object) continue;
            var values = r.TryGetProperty("values", out var v) && v.ValueKind == JsonValueKind.Array
                ? v.EnumerateArray().Select(StringifyWireValue).ToList()
                : new List<string>();
            var condition = new PropertyCondition(
                r.TryGetProperty("attribute", out var attr) ? attr.GetString() ?? "" : "",
                r.TryGetProperty("operator", out var op) ? op.GetString() ?? "" : "",
                values
            );
            result.Add(new TargetingRule(
                r.TryGetProperty("id", out var id) ? id.GetString() ?? "" : "",
                new List<PropertyCondition> { condition },
                100.0,
                r.TryGetProperty("variation", out var variation) ? variation.GetString() ?? "" : "",
                r.TryGetProperty("priority", out var priority) && priority.ValueKind == JsonValueKind.Number
                    ? priority.GetInt32() : 0
            ));
        }
        return result;
    }

    // A JSON number that happens to be a whole value (e.g. flag-api's JSONB
    // "values" column round-tripping 21.0) must render as "21", not "21.0"
    // -- RuleMatcher's Eq/In/Neq/Nin operators compare via plain string
    // equality against EvaluationContext.Attrs, and a real caller's own
    // attribute is far more likely to be a plain int (21) or a bare numeric
    // string ("21") than "21.0", so "21.0" would silently fail to
    // match/exclude a value it should. Numeric operators that go through
    // double.TryParse (Gt/Gte/Lt/Lte) are unaffected either way. The
    // identical .ToString() coercion gap was found by adversarial review of
    // the Ruby SDK's own equivalent adapter (PR #248); fixed here
    // proactively.
    private static string StringifyWireValue(JsonElement v) => v.ValueKind switch
    {
        JsonValueKind.Null => "",
        JsonValueKind.Number when v.TryGetDouble(out var d) && !double.IsInfinity(d) && d == Math.Truncate(d)
            => ((long)d).ToString(CultureInfo.InvariantCulture),
        JsonValueKind.Number => v.GetRawText(),
        JsonValueKind.True => "true",
        JsonValueKind.False => "false",
        JsonValueKind.String => v.GetString() ?? "",
        _ => v.GetRawText(),
    };

    private async Task RunSseListenerAsync(CancellationToken ct)
    {
        while (!ct.IsCancellationRequested)
        {
            try
            {
                var url = $"{_gatewayUrl}/api/v1/stream?environment={_environment}";
                using var resp = await _http.GetAsync(url, HttpCompletionOption.ResponseHeadersRead, ct);
                using var stream = await resp.Content.ReadAsStreamAsync(ct);
                using var reader = new StreamReader(stream);
                // Track the current SSE frame's event type. The gateway writes an
                // `event: lag` frame right BEFORE it DROPS a real flag-update event
                // whenever this client's send buffer is full — we fell behind (see
                // services/gateway/internal/hub/hub.go). A blank line terminates each
                // frame and resets the type back to the default (flag-update) branch.
                var eventType = "";
                while (!ct.IsCancellationRequested && await reader.ReadLineAsync(ct) is { } line)
                {
                    if (line.Length == 0)
                    {
                        eventType = "";
                    }
                    else if (line.StartsWith("event:", StringComparison.Ordinal))
                    {
                        eventType = line[6..].Trim();
                    }
                    else if (line.StartsWith("data:", StringComparison.Ordinal))
                    {
                        if (eventType == "lag")
                        {
                            // The dropped update would otherwise leave the cache
                            // silently stale until the next event or a full reconnect.
                            // Recover it by re-running the SAME snapshot fetch that
                            // ConnectAsync uses, debounced so a burst collapses into one.
                            ScheduleLagRefetch(ct);
                        }
                        else if (eventType == "prerequisites_updated")
                        {
                            // services/flag-api/internal/api/v1/prerequisites.go's
                            // PrerequisitesEvent -- a distinct payload shape
                            // (flag_key/environment/prerequisites/ts, no
                            // enabled/rollout_pct/reason at all) from a real flag
                            // event, so it gets its own handler rather than being
                            // routed through ApplyEvent, which would otherwise
                            // coerce those missing keys into false/0 defaults for
                            // a flag that was never actually disabled.
                            ApplyPrerequisitesEvent(line[5..].Trim());
                        }
                        else if (eventType == "targeting_rules_updated")
                        {
                            // services/flag-api/internal/api/v1/targeting_rules.go's
                            // TargetingRulesEvent -- mirrors
                            // ApplyPrerequisitesEvent exactly, for the same
                            // reason (a distinct payload shape from a real
                            // flag event, so it gets its own handler rather
                            // than being routed through ApplyEvent).
                            ApplyTargetingRulesEvent(line[5..].Trim());
                        }
                        else
                        {
                            ApplyEvent(line[5..].Trim());
                        }
                    }
                }
            }
            catch (OperationCanceledException) { break; }
            catch { await Task.Delay(3000, ct); }
        }
    }

    private void ApplyEvent(string json)
    {
        try
        {
            using var doc = JsonDocument.Parse(json);
            var r = doc.RootElement;
            _cache.ApplyEvent(
                r.GetProperty("flag_key").GetString() ?? "",
                r.GetProperty("enabled").GetBoolean(),
                r.GetProperty("rollout_pct").GetInt32(),
                r.TryGetProperty("ts", out var ts) ? ts.GetInt64() : 0L
            );
        }
        catch { /* malformed event — ignore */ }
    }

    private void ApplyPrerequisitesEvent(string json)
    {
        try
        {
            using var doc = JsonDocument.Parse(json);
            var r = doc.RootElement;
            var flagKey = r.TryGetProperty("flag_key", out var fk) ? fk.GetString() : null;
            if (string.IsNullOrEmpty(flagKey)) return;
            var ts = r.TryGetProperty("ts", out var tsEl) ? tsEl.GetInt64() : 0L;
            _cache.ApplyPrerequisitesEvent(flagKey, ParsePrerequisites(r), ts);
        }
        catch { /* malformed event — ignore */ }
    }

    private void ApplyTargetingRulesEvent(string json)
    {
        try
        {
            using var doc = JsonDocument.Parse(json);
            var r = doc.RootElement;
            var flagKey = r.TryGetProperty("flag_key", out var fk) ? fk.GetString() : null;
            if (string.IsNullOrEmpty(flagKey)) return;
            var ts = r.TryGetProperty("ts", out var tsEl) ? tsEl.GetInt64() : 0L;
            _cache.ApplyTargetingRulesEvent(flagKey, ParseTargetingRules(r), ts);
        }
        catch { /* malformed event — ignore */ }
    }

    // Debounced full-snapshot refetch, triggered by "lag" events. Each lag frame
    // cancels and recreates the delay, so a burst arriving within the debounce
    // window coalesces into a SINGLE FetchSnapshotAsync — the exact snapshot path
    // ConnectAsync uses to populate the cache — fired _lagRefetchDebounceMs after
    // the last frame. The delay token is linked to the SSE listener token so a
    // disconnect/Dispose cancels any pending refetch.
    private void ScheduleLagRefetch(CancellationToken ct)
    {
        lock (_lagRefetchLock)
        {
            _lagRefetchCts?.Cancel();
            _lagRefetchCts?.Dispose();
            var cts = CancellationTokenSource.CreateLinkedTokenSource(ct);
            _lagRefetchCts = cts;
            var token = cts.Token;
            _ = Task.Run(async () =>
            {
                try
                {
                    await Task.Delay(_lagRefetchDebounceMs, token);
                    await FetchSnapshotAsync(token);
                }
                catch (OperationCanceledException) { /* superseded by a newer lag frame or stopped */ }
                catch { /* refetch failed — the next event or reconnect will recover */ }
            }, token);
        }
    }

    public void Dispose()
    {
        _cts?.Cancel();
        lock (_lagRefetchLock)
        {
            _lagRefetchCts?.Cancel();
            _lagRefetchCts?.Dispose();
            _lagRefetchCts = null;
        }
        _cts?.Dispose();
        _http.Dispose();
        _connected = false;
    }
}
