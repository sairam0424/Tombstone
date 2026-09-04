using System.Net.Http.Headers;
using System.Text.Json;

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
        return _engine.Evaluate(state, context, def, flagKey);
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
        using var doc = JsonDocument.Parse(json);
        var flags = doc.RootElement.GetProperty("flags").EnumerateArray()
            .Select(f => new FlagEnvironmentState(
                f.GetProperty("flag_id").GetString() ?? "",
                f.GetProperty("flag_key").GetString() ?? "",
                f.GetProperty("environment").GetString() ?? "",
                f.GetProperty("enabled").GetBoolean(),
                f.GetProperty("rollout_pct").GetInt32(),
                f.GetProperty("safe_default").GetString() ?? "false",
                0L
            )).ToList();
        _cache.LoadSnapshot(flags);
    }

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
