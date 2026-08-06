package io.tombstone.evaluation;

/** Thrown when a targeting-rule condition cannot be evaluated locally
 *  (missing attribute, unparseable numeric/date/semver value). Caught
 *  per-rule by RuleMatcher, which treats it as "this rule did not
 *  match" and continues to the next priority-sorted rule. Unchecked —
 *  mirrors Python's InconclusiveMatchError, which is caught internally
 *  and never expected to propagate to SDK callers. */
public class InconclusiveMatchException extends RuntimeException {
    public InconclusiveMatchException(String message) {
        super(message);
    }
}
