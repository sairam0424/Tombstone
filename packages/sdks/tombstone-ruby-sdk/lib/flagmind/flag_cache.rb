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
          if existing && existing.prerequisites_updated_at >= snapshot_ts
            h[f.flag_key] = f.dup.tap do |s|
              s.prerequisites = existing.prerequisites
              s.prerequisites_updated_at = existing.prerequisites_updated_at
            end.freeze
          else
            h[f.flag_key] = f.dup.tap { |s| s.prerequisites_updated_at = snapshot_ts }.freeze
          end
        end.freeze

        @cache = new_cache
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
