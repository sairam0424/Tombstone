using System.Collections.Immutable;

namespace Tombstone;

// ApplyEvent's read-current/build/write-back shape (read _cache into a local,
// derive a new ImmutableDictionary from it, then reassign _cache) is not a
// CAS loop -- two concurrent mutations reading the same `current` can race,
// and whichever writes _cache second wins outright, silently discarding the
// other's update rather than merging it. Pre-existing, not introduced here.
// LoadSnapshot's per-flag preservation check below now ALSO reads _cache
// (previously it only ever overwrote unconditionally), extending the same
// shape to a second method rather than introducing a new failure mode.
public class FlagCache
{
    private volatile ImmutableDictionary<string, FlagEnvironmentState> _cache
        = ImmutableDictionary<string, FlagEnvironmentState>.Empty;
    // Sentinel meaning "no snapshot loaded yet" -- a real flag-api snapshot
    // ts (time.Now().Unix() at response generation) will never be this low,
    // so the very first LoadSnapshot call always applies.
    private long _lastSnapshotTs = long.MinValue;

    // flag-api's snapshot ts is simply time.Now().Unix() at response
    // generation (services/flag-api/internal/api/v1/environments.go), not a
    // per-flag "last changed" value -- so a SLOWER, already-in-flight
    // FetchSnapshotAsync call (e.g. a lag-triggered refetch racing a
    // reconnect-triggered one) can resolve AFTER a newer one already
    // loaded. Rejecting an incoming snapshot whose own ts is older than the
    // last one actually applied prevents that older response from silently
    // clobbering fresher state for every flag, not just prerequisites.
    public void LoadSnapshot(IEnumerable<FlagEnvironmentState> flags, long snapshotTs = 0)
    {
        if (_lastSnapshotTs != long.MinValue && snapshotTs < _lastSnapshotTs) return;

        var current = _cache;
        _cache = flags.ToImmutableDictionary(f => f.FlagKey, f =>
        {
            // A live prerequisites_updated event may have already advanced
            // this flag's PrerequisitesUpdatedAt to OR PAST this snapshot's
            // own ts if the snapshot fetch was still in flight when the live
            // event arrived and applied -- in that case the snapshot
            // reflects an OLDER (or, on an exact-tie second, no LATER) point
            // in time for THIS flag specifically, even though the snapshot
            // as a whole passed the monotonicity check above (which only
            // compares against the last snapshot's ts, not any per-flag
            // live update). Uses >= , not >: flag-api's snapshot endpoint
            // and its prerequisites-event publisher both derive ts from
            // time.Now().Unix() (1-second resolution), so a live event and a
            // racing snapshot fetch landing in the same wall-clock second
            // get an IDENTICAL ts even though the snapshot's DB read can
            // predate the event's own commit -- ApplyPrerequisitesEvent's
            // own staleness guard (strict "<") already treats a tie as
            // "fresh enough to apply", so this preservation check must treat
            // the SAME tie as "fresh enough to keep", or the two guards
            // disagree on who wins a tie and this one silently loses.
            if (current.TryGetValue(f.FlagKey, out var existing) && existing.PrerequisitesUpdatedAt >= snapshotTs)
            {
                return f with { Prerequisites = existing.Prerequisites, PrerequisitesUpdatedAt = existing.PrerequisitesUpdatedAt };
            }
            return f with { PrerequisitesUpdatedAt = snapshotTs };
        });
        _lastSnapshotTs = snapshotTs;
    }

    // Immutable update — creates new dictionary, never mutates existing
    public void ApplyEvent(string flagKey, bool enabled, int rolloutPct, long ts)
    {
        var current = _cache;
        if (!current.TryGetValue(flagKey, out var existing)) return;
        var updated = existing with { Enabled = enabled, RolloutPct = rolloutPct, UpdatedAt = ts };
        _cache = current.SetItem(flagKey, updated);
    }

    // Applies a live "prerequisites_updated" SSE event -- full replacement of
    // a flag's prerequisite list, not a delta. No-ops for a flag with no
    // existing cache entry (nothing to merge a partial update into -- the
    // next full snapshot refetch is what correctly picks up a flag this
    // client has never seen before).
    //
    // Rejects an incoming event whose ts is OLDER than the currently-cached
    // PrerequisitesUpdatedAt: services/flag-api/internal/api/v1/
    // prerequisites.go's own PrerequisitesEvent doc comment discloses that
    // publishPrerequisitesUpdated's SELECT-then-XAdd has no per-flag lock, so
    // concurrent AddPrerequisite/DeletePrerequisite calls on the SAME flag
    // can have their events arrive here out of real commit order under
    // scheduling delays -- comparing ts against what's already cached
    // (rather than unconditionally overwriting on arrival order) closes that
    // gap at the point where staleness actually matters.
    public void ApplyPrerequisitesEvent(string flagKey, List<FlagPrerequisite> prerequisites, long ts)
    {
        var current = _cache;
        if (!current.TryGetValue(flagKey, out var existing)) return;
        if (ts < existing.PrerequisitesUpdatedAt) return;
        var updated = existing with { Prerequisites = prerequisites, PrerequisitesUpdatedAt = ts };
        _cache = current.SetItem(flagKey, updated);
    }

    public FlagEnvironmentState? Get(string flagKey) =>
        _cache.TryGetValue(flagKey, out var s) ? s : null;

    public IEnumerable<string> FlagKeys() => _cache.Keys;
}
