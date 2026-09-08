using System.Collections.Immutable;
using System.Threading;

namespace Tombstone;

// Every mutator below goes through UpdateState(...), a CompareExchange
// retry loop, not a plain read-then-write pair: it recomputes the whole
// next-state function against the LATEST _state whenever a concurrent
// writer's CompareExchange lands first, so two racing mutations (e.g. the
// SSE listener's ApplyPrerequisitesEvent racing a lag-recovery
// LoadSnapshot) can never silently discard one another's update -- each
// is folded into whatever state was actually current at the moment it
// commits, not the value each happened to read at call time. Previously
// this class used a plain read-modify-write (read current -> build a new
// CacheState from it -> write), disclosed here as a real "lost update"
// bug: whichever write landed second would discard the OTHER's entire
// state, not just the key it touched. Fixed here as its own, independent
// piece of work, matching the Java SDK's equivalent
// AtomicReference&lt;CacheState&gt;.updateAndGet fix.
public class FlagCache
{
    // Combines what used to be three separately-updated fields (_cache,
    // _prerequisitesFromLiveEvent, _lastSnapshotTs) into one immutable record
    // swapped via a single `volatile` reference write. A reference-typed
    // field's reads/writes are atomic under the CLR memory model, and
    // `volatile` adds the cross-thread visibility/ordering guarantee needed
    // here -- together giving the same "any read always sees a mutually
    // consistent triple" property Java's AtomicReference<CacheState> gives.
    //
    // Found by adversarial review of PRs #241/#242/#243: with the three
    // pieces of state as separate fields, a genuinely concurrent thread
    // (this class's own ApplyPrerequisitesEvent/LoadSnapshot are called from
    // real concurrent tasks in production -- an SSE-listener task racing a
    // lag-recovery task, per the top-of-file race disclosed above) could
    // interleave between updating _cache and updating
    // _prerequisitesFromLiveEvent, letting a concurrent reader observe them
    // in a mutually INCONSISTENT combination -- silently defeating the
    // tie-break protection those two fields exist to provide together.
    // Ruby's equivalent fix needed no such change: its FlagCache wraps both
    // field updates inside the same Monitor#synchronize critical section,
    // which already provides the mutual exclusion this record+volatile
    // design achieves lock-free.
    private sealed record CacheState(
        ImmutableDictionary<string, FlagEnvironmentState> Cache,
        ImmutableDictionary<string, bool> PrerequisitesFromLiveEvent,
        ImmutableDictionary<string, bool> TargetingRulesFromLiveEvent,
        long LastSnapshotTs);

    // Sentinel meaning "no snapshot loaded yet" -- a real flag-api snapshot
    // ts (time.Now().Unix() at response generation) will never be this low,
    // so the very first LoadSnapshot call always applies.
    private const long NoSnapshotYet = long.MinValue;

    private volatile CacheState _state = new(
        ImmutableDictionary<string, FlagEnvironmentState>.Empty,
        ImmutableDictionary<string, bool>.Empty,
        ImmutableDictionary<string, bool>.Empty,
        NoSnapshotYet);

    // CAS retry loop: `updater` computes the next state from whatever is
    // ACTUALLY current at the moment of the exchange attempt, not a value
    // read earlier -- if a concurrent mutator's CompareExchange commits
    // first, this retries `updater` against the new current value instead
    // of clobbering it. `updater` returning the SAME reference it was
    // given (an early-return no-op, e.g. a staleness guard) short-circuits
    // without ever attempting a CompareExchange.
    private void UpdateState(Func<CacheState, CacheState> updater)
    {
        CacheState current;
        CacheState next;
        do
        {
            current = _state;
            next = updater(current);
            if (ReferenceEquals(next, current)) return;
        } while (Interlocked.CompareExchange(ref _state, next, current) != current);
    }

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
        UpdateState(current =>
        {
            if (current.LastSnapshotTs != NoSnapshotYet && snapshotTs < current.LastSnapshotTs) return current;

            // Uses .Add(), not the indexer: flag-api's snapshot response is
            // expected to contain each flag key at most once -- a duplicate
            // key is a backend data-integrity bug, and .Add() fails loud
            // (ArgumentException) instead of silently coalescing to
            // last-write-wins, matching the original flags.ToImmutableDictionary(...)
            // behavior this Builder-based loop replaced.
            var next = ImmutableDictionary.CreateBuilder<string, FlagEnvironmentState>();
            var nextFromLiveEvent = ImmutableDictionary.CreateBuilder<string, bool>();
            var nextTargetingRulesFromLiveEvent = ImmutableDictionary.CreateBuilder<string, bool>();
            foreach (var f in flags)
            {
                // A live prerequisites_updated event may have already advanced
                // this flag's PrerequisitesUpdatedAt to OR PAST this snapshot's
                // own ts if the snapshot fetch was still in flight when the live
                // event arrived and applied -- in that case the snapshot
                // reflects an OLDER (or, on an exact-tie second, no LATER) point
                // in time for THIS flag specifically, even though the snapshot
                // as a whole passed the monotonicity check above (which only
                // compares against the last snapshot's ts, not any per-flag
                // live update). Uses >=, not >: flag-api's snapshot endpoint
                // and its prerequisites-event publisher both derive ts from
                // time.Now().Unix() (1-second resolution), so a live event and a
                // racing snapshot fetch landing in the same wall-clock second
                // get an IDENTICAL ts even though the snapshot's DB read can
                // predate the event's own commit -- ApplyPrerequisitesEvent's
                // own staleness guard (strict "<") already treats a tie as
                // "fresh enough to apply", so this preservation check must treat
                // the SAME tie as "fresh enough to keep", or the two guards
                // disagree on who wins a tie and this one silently loses.
                //
                // Also requires PrerequisitesFromLiveEvent to be true: without
                // it, a SECOND snapshot sharing the exact same ts as a FIRST
                // snapshot (no live event involved at all) would incorrectly
                // take this same "preserve" branch and freeze prerequisites on
                // the first snapshot's value forever.
                var keepLivePrerequisites =
                    current.Cache.TryGetValue(f.FlagKey, out var existing) &&
                    current.PrerequisitesFromLiveEvent.TryGetValue(f.FlagKey, out var fromLive) && fromLive &&
                    existing.PrerequisitesUpdatedAt >= snapshotTs;
                // Identical reasoning to keepLivePrerequisites above, applied to
                // TargetingRules against services/flag-api/internal/api/v1/
                // targeting_rules.go's TargetingRulesEvent -- see
                // ApplyTargetingRulesEvent's own doc comment. Tracked via its OWN
                // TargetingRulesFromLiveEvent map so a live PREREQUISITES event
                // never protects TargetingRules (or vice versa) from an
                // unrelated tied snapshot.
                var keepLiveTargetingRules =
                    existing is not null &&
                    current.TargetingRulesFromLiveEvent.TryGetValue(f.FlagKey, out var rulesFromLive) && rulesFromLive &&
                    existing.TargetingRulesUpdatedAt >= snapshotTs;
                var merged = keepLivePrerequisites
                    ? f with { Prerequisites = existing!.Prerequisites, PrerequisitesUpdatedAt = existing.PrerequisitesUpdatedAt }
                    : f with { PrerequisitesUpdatedAt = snapshotTs };
                merged = keepLiveTargetingRules
                    ? merged with { TargetingRules = existing!.TargetingRules, TargetingRulesUpdatedAt = existing.TargetingRulesUpdatedAt }
                    : merged with { TargetingRulesUpdatedAt = snapshotTs };
                next.Add(f.FlagKey, merged);
                // ONE-SHOT consumption, always false here (never
                // keepLivePrerequisites/keepLiveTargetingRules): this
                // LoadSnapshot call has now fully resolved the race between the
                // live event and ITS OWN specific in-flight snapshot. Re-
                // propagating true would make the protection "sticky", vetoing a
                // later, independent snapshot that merely happens to tie the
                // same coarse-resolution second too. Residual, accepted
                // limitation: two snapshot fetches that were BOTH already in
                // flight when the SAME live event fired will only have the
                // first-arriving one correctly blocked; solving that fully
                // would require a signal finer than flag-api's
                // 1-second-resolution wall-clock ts.
                nextFromLiveEvent.Add(f.FlagKey, false);
                nextTargetingRulesFromLiveEvent.Add(f.FlagKey, false);
            }
            return new CacheState(
                next.ToImmutable(), nextFromLiveEvent.ToImmutable(),
                nextTargetingRulesFromLiveEvent.ToImmutable(), snapshotTs);
        });
    }

    // Immutable update — creates a new CacheState, never mutates existing
    public void ApplyEvent(string flagKey, bool enabled, int rolloutPct, long ts)
    {
        UpdateState(current =>
        {
            if (!current.Cache.TryGetValue(flagKey, out var existing)) return current;
            var updated = existing with { Enabled = enabled, RolloutPct = rolloutPct, UpdatedAt = ts };
            return current with { Cache = current.Cache.SetItem(flagKey, updated) };
        });
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
        UpdateState(current =>
        {
            if (!current.Cache.TryGetValue(flagKey, out var existing)) return current;
            if (ts < existing.PrerequisitesUpdatedAt) return current;
            var updated = existing with { Prerequisites = prerequisites, PrerequisitesUpdatedAt = ts };
            // Both the cache entry AND the live-event marker are folded into
            // the SAME new CacheState and published via ONE CompareExchange
            // -- see this class's own CacheState comment for why splitting
            // these into two separate writes (the bug this replaced) is
            // unsafe under real concurrency.
            return new CacheState(
                current.Cache.SetItem(flagKey, updated),
                current.PrerequisitesFromLiveEvent.SetItem(flagKey, true),
                current.TargetingRulesFromLiveEvent,
                current.LastSnapshotTs);
        });
    }

    // Applies a live "targeting_rules_updated" SSE event -- full replacement
    // of a flag's targeting-rule list FOR THIS ENVIRONMENT, not a delta
    // (matching TargetingRulesEvent's own documented design on the flag-api
    // side -- services/flag-api/internal/api/v1/targeting_rules.go).
    // No-ops for a flag with no existing cache entry, mirrors
    // ApplyPrerequisitesEvent's own staleness guard exactly (strict "<",
    // comparing against TargetingRulesUpdatedAt).
    public void ApplyTargetingRulesEvent(string flagKey, List<TargetingRule> targetingRules, long ts)
    {
        UpdateState(current =>
        {
            if (!current.Cache.TryGetValue(flagKey, out var existing)) return current;
            if (ts < existing.TargetingRulesUpdatedAt) return current;
            var updated = existing with { TargetingRules = targetingRules, TargetingRulesUpdatedAt = ts };
            return new CacheState(
                current.Cache.SetItem(flagKey, updated),
                current.PrerequisitesFromLiveEvent,
                current.TargetingRulesFromLiveEvent.SetItem(flagKey, true),
                current.LastSnapshotTs);
        });
    }

    public FlagEnvironmentState? Get(string flagKey) =>
        _state.Cache.TryGetValue(flagKey, out var s) ? s : null;

    public IEnumerable<string> FlagKeys() => _state.Cache.Keys;
}
