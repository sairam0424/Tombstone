module Tombstone
  # Raised when a targeting-rule condition cannot be evaluated locally
  # (missing attribute, unparseable numeric/date/semver value). Caught
  # per-rule by RuleMatcher, which treats it as "this rule did not
  # match" and continues to the next priority-sorted rule. Mirrors
  # Python's InconclusiveMatchError, which is caught internally and
  # never expected to propagate to SDK callers.
  class InconclusiveMatchError < StandardError; end
end
