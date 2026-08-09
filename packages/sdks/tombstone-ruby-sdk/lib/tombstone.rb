# Tombstone Ruby SDK entrypoint
require_relative "flagmind/types"
require_relative "flagmind/errors"
require_relative "flagmind/prerequisite_checker"
require_relative "flagmind/rule_matcher"
require_relative "flagmind/evaluation_engine"
require_relative "flagmind/flag_cache"
require_relative "flagmind/client"

# Expose Tombstone as the top-level namespace (the directory structure under lib/flagmind/
# stays as-is for now — renaming directories is deferred to naming cleanup in Task 10).
module Tombstone
  VERSION = "0.2.0"  # bump from 0.1.0 since this is a breaking entrypoint change
end
