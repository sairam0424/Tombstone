# tombstone.rb — entry point alias for backward compat with spec files that require "tombstone"
require_relative "flagmind/types"
require_relative "flagmind/evaluation_engine"
require_relative "flagmind/flag_cache"
require_relative "flagmind/client"

# Expose top-level Tombstone namespace
Tombstone = Flagmind unless defined?(Tombstone)
