using System.Collections.Immutable;

namespace Tombstone;

public class FlagCache
{
    private volatile ImmutableDictionary<string, FlagEnvironmentState> _cache
        = ImmutableDictionary<string, FlagEnvironmentState>.Empty;

    public void LoadSnapshot(IEnumerable<FlagEnvironmentState> flags)
    {
        _cache = flags.ToImmutableDictionary(f => f.FlagKey);
    }

    // Immutable update — creates new dictionary, never mutates existing
    public void ApplyEvent(string flagKey, bool enabled, int rolloutPct, long ts)
    {
        var current = _cache;
        if (!current.TryGetValue(flagKey, out var existing)) return;
        var updated = existing with { Enabled = enabled, RolloutPct = rolloutPct, UpdatedAt = ts };
        _cache = current.SetItem(flagKey, updated);
    }

    public FlagEnvironmentState? Get(string flagKey) =>
        _cache.TryGetValue(flagKey, out var s) ? s : null;

    public IEnumerable<string> FlagKeys() => _cache.Keys;
}
