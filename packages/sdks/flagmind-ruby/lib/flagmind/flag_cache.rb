require "monitor"

module Tombstone
  class FlagCache
    def initialize
      @lock = Monitor.new
      @cache = {}.freeze
    end

    def load_snapshot(flags)
      new_cache = flags.each_with_object({}) { |f, h| h[f.flag_key] = f }.freeze
      @lock.synchronize { @cache = new_cache }
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
