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
    private readonly HttpClient _http = new();
    private CancellationTokenSource? _cts;
    private bool _connected;

    public TombstoneClient(string sdkKey, string environment,
        string? apiUrl = null, string? gatewayUrl = null,
        Dictionary<string, object?>? defaults = null)
    {
        _sdkKey = sdkKey;
        _environment = environment;
        _apiUrl = apiUrl ?? "http://localhost:8081";
        _gatewayUrl = gatewayUrl ?? "http://localhost:8080";
        _defaults = defaults ?? new();
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
                while (!ct.IsCancellationRequested && await reader.ReadLineAsync(ct) is { } line)
                {
                    if (line.StartsWith("data:", StringComparison.Ordinal))
                    {
                        ApplyEvent(line[5..].Trim());
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

    public void Dispose()
    {
        _cts?.Cancel();
        _cts?.Dispose();
        _http.Dispose();
        _connected = false;
    }
}
