"""TombstoneTestClient — deterministic, override-driven flag evaluation for tests.

Usage::

    from tombstone.testing import TombstoneTestClient

    client = TombstoneTestClient.create_isolated()
    client.override('checkout_v2', True)
    assert client.evaluate('checkout_v2', False) is True
    client.clear_overrides()
"""
from __future__ import annotations

from typing import Any


class TombstoneTestClient:
    """Deterministic test double for the Tombstone SDK.

    All state mutations return new dicts (immutable-update style) so snapshots
    of prior state are never silently modified.
    """

    def __init__(self) -> None:
        self._overrides: dict[str, Any] = {}
        self._bucket_assignments: dict[str, dict[str, bool]] = {}
        # {flag_key: {user_id: in_cohort}}

    # ------------------------------------------------------------------
    # Overrides
    # ------------------------------------------------------------------

    def override(self, flag_key: str, value: Any) -> "TombstoneTestClient":
        """Set a fixed return value for *flag_key*, regardless of context."""
        self._overrides = {**self._overrides, flag_key: value}  # immutable update
        return self

    def clear_override(self, flag_key: str) -> "TombstoneTestClient":
        """Remove the override for a single flag key."""
        self._overrides = {k: v for k, v in self._overrides.items() if k != flag_key}
        return self

    def clear_overrides(self) -> "TombstoneTestClient":
        """Remove all overrides — subsequent evaluate() calls return defaultValue."""
        self._overrides = {}
        return self

    # ------------------------------------------------------------------
    # Bucket assignments
    # ------------------------------------------------------------------

    def assign_to_bucket(
        self, flag_key: str, user_id: str, in_cohort: bool = True
    ) -> "TombstoneTestClient":
        """Force *user_id* to be inside (or outside) the rollout cohort for *flag_key*.

        Useful for deterministic experiment assignment in tests without relying on
        MurmurHash percentile buckets.
        """
        buckets = {**self._bucket_assignments.get(flag_key, {}), user_id: in_cohort}
        self._bucket_assignments = {**self._bucket_assignments, flag_key: buckets}
        return self

    # ------------------------------------------------------------------
    # Evaluation
    # ------------------------------------------------------------------

    def evaluate(self, flag_key: str, default_value: Any = None) -> Any:
        """Return the flag value for *flag_key*.

        Resolution order:
        1. Explicit override → return override value.
        2. Fall through to *default_value*.

        Note: bucket assignments are checked via :meth:`is_enabled` when a
        *user_id* is available. For typed evaluation with bucket control,
        call :meth:`is_enabled`.
        """
        return self._overrides.get(flag_key, default_value)

    def is_enabled(self, flag_key: str, user_id: str | None = None) -> bool:
        """Return whether *flag_key* is enabled.

        Resolution order:
        1. Explicit override → ``bool(override_value)``.
        2. Bucket assignment for *user_id* (if provided).
        3. ``False`` (safe default).
        """
        if flag_key in self._overrides:
            return bool(self._overrides[flag_key])
        if user_id is not None and flag_key in self._bucket_assignments:
            return self._bucket_assignments[flag_key].get(user_id, False)
        return False

    # ------------------------------------------------------------------
    # Factories
    # ------------------------------------------------------------------

    @classmethod
    def create_isolated(cls) -> "TombstoneTestClient":
        """Return a client with no overrides — all flags return their defaultValue."""
        return cls()

    @classmethod
    def with_flags(cls, flags: dict[str, Any]) -> "TombstoneTestClient":
        """Return a client with *flags* pre-configured as overrides."""
        client = cls()
        for key, val in flags.items():
            client.override(key, val)
        return client
