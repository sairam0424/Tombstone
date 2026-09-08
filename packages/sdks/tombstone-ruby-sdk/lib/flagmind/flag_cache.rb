require "monitor"

module Tombstone
  class FlagCache
    def initialize
      @lock = Monitor.new
      @cache = {}.freeze
      # Sentinel meaning "no snapshot loaded yet" -- a real flag-api
      # snapshot ts (Time.now.to_i at response generation) will never be
      # this low, so the very first load_snapshot call always applies.
      @last_snapshot_ts = nil
      # Tracks which flag keys' CURRENT prerequisites/prerequisites_updated_at
      # came from a live prerequisites_updated event (true) rather than a
      # snapshot load (false/absent). A tied ts alone cannot distinguish "a
      # live event that must win a tie against a slower, still-in-flight
      # snapshot" from "two DIFFERENT snapshots that happen to share
      # flag-api's coarse 1-second-resolution ts, where the second (newer
      # LOAD, regardless of ts) must still win" -- without this,
      # load_snapshot's own >= tie-break would incorrectly freeze
      # prerequisites on the FIRST of two same-ts snapshots forever. This
      # protection is ONE-SHOT: load_snapshot always resets this to false
      # after evaluating it (see load_snapshot's own comment for why
      # re-propagating true would make the protection "sticky" and veto a
      # later, independent snapshot too).
      @prerequisites_from_live_event = {}
      # Identical purpose to @prerequisites_from_live_event above, tracked
      # for targeting_rules independently -- see apply_targeting_rules_event
      # and load_snapshot's own "keep_live_targeting_rules" comment. Kept as
      # a genuinely separate hash (not merged into one "any live event"
      # flag): a live PREREQUISITES event must never protect targetingRules
      # from an unrelated tied snapshot, and vice versa -- each feature's
      # tie-break is independent of the other's.
      @targeting_rules_from_live_event = {}
    end

    # flag-api's snapshot ts is simply the response-generation wall-clock
    # time (services/flag-api/internal/api/v1/environments.go: Ts:
    # time.Now().Unix()), not a per-flag "last changed" value -- so a
    # SLOWER, already-in-flight fetch_snapshot call (e.g. a lag-triggered
    # refetch racing a reconnect-triggered one) can resolve AFTER a newer
    # one already loaded. Rejecting an incoming snapshot whose own ts is
    # older than the last one actually applied prevents that older
    # response from silently clobbering fresher state for every flag, not
    # just prerequisites.
    def load_snapshot(flags, snapshot_ts = 0)
      @lock.synchronize do
        return if @last_snapshot_ts && snapshot_ts < @last_snapshot_ts

        next_from_live_event = {}
        next_targeting_rules_from_live_event = {}
        new_cache = flags.each_with_object({}) do |f, h|
          existing = @cache[f.flag_key]
          # A live prerequisites_updated event may have already advanced
          # this flag's prerequisites_updated_at to OR PAST this snapshot's
          # own ts if the snapshot fetch was still in flight when the live
          # event arrived and applied -- in that case the snapshot
          # reflects an OLDER (or, on an exact-tie second, no LATER) point
          # in time for THIS flag specifically, even though the snapshot
          # as a whole passed the monotonicity check above (which only
          # compares against the last snapshot's ts, not any per-flag
          # live update). Uses >=, not >: flag-api's snapshot endpoint
          # (environments.go: Ts: time.Now().Unix()) and its
          # prerequisites-event publisher (prerequisites.go: ts :=
          # time.Now().Unix()) both derive ts from the SAME 1-second-
          # resolution wall clock, so a live event and a racing snapshot
          # fetch landing in the same wall-clock second get an IDENTICAL
          # ts even though the snapshot's DB read can predate the event's
          # own commit -- apply_prerequisites_event's own staleness guard
          # (strict "<") already treats a tie as "fresh enough to apply",
          # so this preservation check must treat the SAME tie as "fresh
          # enough to keep", or the two guards disagree on who wins a tie
          # and this one silently loses.
          #
          # Also requires @prerequisites_from_live_event to be true: without
          # it, a SECOND snapshot sharing the exact same ts as a FIRST
          # snapshot (no live event involved at all) would incorrectly take
          # this same "preserve" branch and freeze prerequisites on the
          # first snapshot's value forever.
          keep_live = existing &&
                      @prerequisites_from_live_event[f.flag_key] &&
                      existing.prerequisites_updated_at >= snapshot_ts
          # Identical reasoning to keep_live above, applied to targeting_rules
          # against services/flag-api/internal/api/v1/targeting_rules.go's
          # TargetingRulesEvent -- see apply_targeting_rules_event's own
          # comment. Tracked via its OWN @targeting_rules_from_live_event
          # hash so a live PREREQUISITES event never protects targetingRules
          # (or vice versa) from an unrelated tied snapshot.
          keep_live_targeting_rules = existing &&
                                       @targeting_rules_from_live_event[f.flag_key] &&
                                       existing.targeting_rules_updated_at >= snapshot_ts
          h[f.flag_key] = f.dup.tap do |s|
            if keep_live
              s.prerequisites = existing.prerequisites
              s.prerequisites_updated_at = existing.prerequisites_updated_at
            else
              s.prerequisites_updated_at = snapshot_ts
            end
            if keep_live_targeting_rules
              s.targeting_rules = existing.targeting_rules
              s.targeting_rules_updated_at = existing.targeting_rules_updated_at
            else
              s.targeting_rules_updated_at = snapshot_ts
            end
          end.freeze
          # ONE-SHOT consumption, always false here (never keep_live/
          # keep_live_targeting_rules): this load_snapshot call has now
          # fully resolved the race between the live event and ITS OWN
          # specific in-flight snapshot. Re-propagating true would make the
          # protection "sticky", vetoing a later, independent snapshot that
          # merely happens to tie the same coarse-resolution second too.
          # Residual, accepted limitation: two snapshot fetches that were
          # BOTH already in flight when the SAME live event fired will only
          # have the first-arriving one correctly blocked; solving that
          # fully would require a signal finer than flag-api's
          # 1-second-resolution wall-clock ts.
          next_from_live_event[f.flag_key] = false
          next_targeting_rules_from_live_event[f.flag_key] = false
        end.freeze

        @cache = new_cache
        @prerequisites_from_live_event = next_from_live_event
        @targeting_rules_from_live_event = next_targeting_rules_from_live_event
        @last_snapshot_ts = snapshot_ts
      end
    end

    # Immutable update — creates new frozen hash, never mutates existing
    def apply_event(flag_key, enabled, rollout_pct, ts)
      @lock.synchronize do
        existing = @cache[flag_key]
        return unless existing
        updated = existing.dup.tap do |s|
          s.enabled = enabled
          s.rollout_pct = rollout_pct
          s.updated_at = ts
        end.freeze
        @cache = @cache.merge(flag_key => updated).freeze
      end
    end

    # Applies a live "prerequisites_updated" SSE event -- full replacement
    # of a flag's prerequisite list, not a delta. No-ops for a flag with no
    # existing cache entry (nothing to merge a partial update into -- the
    # next full snapshot refetch is what correctly picks up a flag this
    # client has never seen before).
    #
    # Rejects an incoming event whose ts is OLDER than the currently-cached
    # prerequisites_updated_at: services/flag-api/internal/api/v1/
    # prerequisites.go's own PrerequisitesEvent doc comment discloses that
    # publish_prerequisites_updated's SELECT-then-XAdd has no per-flag lock,
    # so concurrent AddPrerequisite/DeletePrerequisite calls on the SAME
    # flag can have their events arrive here out of real commit order under
    # scheduling delays -- comparing ts against what's already cached
    # (rather than unconditionally overwriting on arrival order) closes
    # that gap at the point where staleness actually matters.
    def apply_prerequisites_event(flag_key, prerequisites, ts)
      @lock.synchronize do
        existing = @cache[flag_key]
        return unless existing
        return if ts < existing.prerequisites_updated_at

        updated = existing.dup.tap do |s|
          s.prerequisites = prerequisites
          s.prerequisites_updated_at = ts
        end.freeze
        @cache = @cache.merge(flag_key => updated).freeze
        # Marks this flag's prerequisites as LIVE-sourced -- see
        # @prerequisites_from_live_event's own comment for why load_snapshot
        # needs this distinction, not just a ts comparison, to decide
        # whether a tied-or-older incoming snapshot should be allowed to
        # overwrite it.
        @prerequisites_from_live_event = @prerequisites_from_live_event.merge(flag_key => true)
      end
    end

    # Applies a live "targeting_rules_updated" SSE event -- full replacement
    # of a flag's targeting-rule list FOR THIS ENVIRONMENT, not a delta
    # (matching TargetingRulesEvent's own documented design on the flag-api
    # side -- services/flag-api/internal/api/v1/targeting_rules.go).
    # No-ops for a flag with no existing cache entry, mirrors
    # apply_prerequisites_event's own staleness guard exactly (strict "<",
    # comparing against targeting_rules_updated_at).
    def apply_targeting_rules_event(flag_key, targeting_rules, ts)
      @lock.synchronize do
        existing = @cache[flag_key]
        return unless existing
        return if ts < existing.targeting_rules_updated_at

        updated = existing.dup.tap do |s|
          s.targeting_rules = targeting_rules
          s.targeting_rules_updated_at = ts
        end.freeze
        @cache = @cache.merge(flag_key => updated).freeze
        # Marks this flag's targetingRules as LIVE-sourced -- see
        # @targeting_rules_from_live_event's own comment (on initialize).
        @targeting_rules_from_live_event = @targeting_rules_from_live_event.merge(flag_key => true)
      end
    end

    def get(flag_key)
      @cache[flag_key]
    end

    def flag_keys
      @cache.keys
    end

    def size
      @cache.size
    end
  end
end
