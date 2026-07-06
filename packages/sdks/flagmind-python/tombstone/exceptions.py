# packages/sdks/flagmind-python/tombstone/exceptions.py
"""Tombstone SDK evaluation exceptions.

InconclusiveMatchError: a property match cannot be determined for the current
    context (e.g. attribute missing, type mismatch). The caller should catch
    this and continue to the next rule or fallthrough.

RequiresServerEvaluation: the evaluation requires server-side data not
    available in the local cache (e.g. cohort membership). Propagate
    immediately — the client layer will fall back to the flag-api REST API.
"""


class InconclusiveMatchError(Exception):
    """Raised when a targeting rule condition cannot be evaluated locally."""


class RequiresServerEvaluation(Exception):
    """Raised when evaluation requires a server round-trip. Never swallow."""
