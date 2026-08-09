require "set"

module Tombstone
  module PrerequisiteChecker
    # Canonical model: string-compare mechanism against the dependency's
    # stringified boolean outcome (forward-compatible with future
    # multivariate prerequisites — see design spec Section 3). Cycle
    # detection via explicit seen-set (Python's approach); memoization
    # via cache hash keyed by dependency flag key (Python's approach).
    def self.check_all(prerequisites, flag_lookup, cache, seen, current_flag_key, engine, context)
      chain_seen = seen.dup
      chain_seen.add(current_flag_key)

      prerequisites.each do |prereq|
        dep_key = prereq.flag_key
        dep_variation = if cache.key?(dep_key)
          cache[dep_key]
        elsif chain_seen.include?(dep_key)
          next  # cycle detected — fail open, skip this one prerequisite
        else
          dep_flag = flag_lookup.call(dep_key)
          if dep_flag.nil?
            nil
          else
            # Recursive evaluation via engine's 7-arg overload (Task 8)
            dep_result = engine.evaluate(dep_flag, context, false, dep_key, flag_lookup, cache, chain_seen)
            dep_result.value.to_s
          end
        end

        cache[dep_key] = dep_variation unless cache.key?(dep_key)

        if dep_variation != prereq.required_variation
          next unless prereq.gate  # soft — unmet but non-blocking
          return false             # hard gate — block entire parent flag
        end
      end

      true
    end
  end
end
