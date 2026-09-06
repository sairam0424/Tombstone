import logging
import threading
from typing import Any

import httpx

from tombstone.evaluation import evaluate
from tombstone.types import (
    EvaluationContext,
    EvaluationResult,
    FlagEnvironmentState,
)

logger = logging.getLogger(__name__)

# A slow client can receive a burst of gateway "lag" frames back-to-back
# (one per dropped update). Coalesce them into a single snapshot refetch by
# waiting this long after the LAST lag frame before re-syncing the cache.
_DEFAULT_REFETCH_DEBOUNCE_SECONDS = 0.5


class TombstoneClient:
    def __init__(
        self,
        sdk_key: str,
        environment: str = "production",
        gateway_url: str = "http://localhost:8080",
        api_url: str = "http://localhost:8081",
        defaults: dict[str, Any] | None = None,
    ) -> None:
        self._sdk_key = sdk_key
        self._environment = environment
        self._gateway_url = gateway_url.rstrip("/")
        self._api_url = api_url.rstrip("/")
        self._defaults: dict[str, Any] = defaults or {}
        self._cache: dict[str, FlagEnvironmentState] = {}
        self._lock = threading.Lock()
        # Debounced snapshot refetch triggered by gateway "lag" events.
        self._refetch_lock = threading.Lock()
        self._refetch_timer: threading.Timer | None = None
        self._refetch_debounce_seconds = _DEFAULT_REFETCH_DEBOUNCE_SECONDS
        self._stopped = False

    def connect(self) -> None:
        self._fetch_snapshot()

        sse_thread = threading.Thread(
            target=self._sse_listener, name="flagmind-sse", daemon=True
        )
        sse_thread.start()

    def close(self) -> None:
        """Stop background work and cancel any pending refetch timer.

        Safe to call multiple times and safe to call concurrently with a
        "lag" event racing to schedule a refetch — after this returns no new
        refetch timer will be scheduled.
        """
        with self._refetch_lock:
            self._stopped = True
            if self._refetch_timer is not None:
                self._refetch_timer.cancel()
                self._refetch_timer = None

    def _fetch_snapshot(self) -> None:
        """Fetch the full environment snapshot and replace the flag cache.

        This is the same code path connect() uses to populate the cache; the
        "lag" event handler reuses it to recover updates the gateway dropped.
        """
        try:
            with httpx.Client(
                headers={"Authorization": f"Bearer {self._sdk_key}"},
                timeout=10.0,
            ) as client:
                resp = client.get(
                    f"{self._api_url}/api/v1/environments/snapshot",
                    params={"environment": self._environment},
                )
                resp.raise_for_status()
                self._apply_snapshot(resp.json())
        except Exception as exc:
            logger.warning("Tombstone: failed to fetch snapshot: %s", exc)

    def evaluate(self, flag_key: str, context: EvaluationContext) -> EvaluationResult:
        try:
            with self._lock:
                flag_state = self._cache.get(flag_key)
                all_flags = dict(self._cache)  # shallow copy under lock
            default_value = self._defaults.get(flag_key, False)
            return evaluate(
                flag_state,
                context,
                default_value,
                flag_key,
                all_flags=all_flags,
                evaluation_cache={},
            )
        except Exception as exc:
            logger.error("Tombstone: evaluate error for %s: %s", flag_key, exc)
            return EvaluationResult(
                value=self._defaults.get(flag_key, False),
                reason="ERROR",
                from_cache=False,
                flag_key=flag_key,
            )

    def is_enabled(
        self,
        flag_key: str,
        context: EvaluationContext,
        default: bool = False,
    ) -> bool:
        result = self.evaluate(flag_key, context)
        value = result.value
        if isinstance(value, bool):
            return value
        return bool(value) if value is not None else default

    def flag_keys(self) -> list[str]:
        with self._lock:
            return list(self._cache.keys())

    def _apply_snapshot(self, payload: dict) -> None:
        from tombstone.types import (
            FlagEnvironmentState,
            TargetingRule,
            PropertyCondition,
        )

        new_cache: dict[str, FlagEnvironmentState] = {}
        for raw in payload.get("flags", []):
            try:
                flag_key = raw["flag_key"]
            except (KeyError, TypeError):
                logger.warning(
                    "Tombstone: skipping malformed flag entry in snapshot: %r", raw
                )
                continue

            try:
                # Deserialize targeting rules
                targeting_rules = []
                for r in raw.get("targeting_rules", []):
                    conditions = [
                        PropertyCondition(
                            attribute=c["attribute"],
                            operator=c["operator"],
                            values=c.get("values", []),
                            negate=c.get("negate", False),
                        )
                        for c in r.get("conditions", [])
                    ]
                    targeting_rules.append(
                        TargetingRule(
                            id=r.get("id", ""),
                            conditions=conditions,
                            rollout_pct=float(r.get("rollout_pct", 100.0)),
                            variation=r.get("variation", True),
                        )
                    )

                new_cache[flag_key] = FlagEnvironmentState(
                    flag_key=flag_key,
                    enabled=raw.get("enabled", False),
                    rollout_pct=float(raw.get("rollout_pct", 0.0)),
                    safe_default=raw.get("safe_default", False),
                    environment=payload.get("environment", self._environment),
                    targeting_rules=targeting_rules,
                    prerequisites=raw.get("prerequisites", []),
                    hash_version=raw.get("hash_version", 1),
                    target_list=raw.get("target_list", []),
                    # The snapshot's own ts is the correct "known-good as of"
                    # timestamp for these prerequisites -- any live
                    # prerequisites_updated event older than this snapshot
                    # fetch is necessarily already stale/superseded.
                    prerequisites_updated_at=payload.get("ts", 0),
                )
            except Exception as exc:
                logger.warning(
                    "Tombstone: failed to deserialize flag '%s': %s", flag_key, exc
                )

        with self._lock:
            self._cache = new_cache

    def _sse_listener(self) -> None:
        url = f"{self._gateway_url}/api/v1/stream"
        while True:
            try:
                with httpx.Client(
                    headers={"Authorization": f"Bearer {self._sdk_key}"},
                    timeout=None,
                ) as client:
                    with client.stream(
                        "GET",
                        url,
                        params={"environment": self._environment},
                    ) as response:
                        self._consume_sse_lines(response.iter_lines())
            except Exception as exc:
                logger.debug("Tombstone: SSE reconnect after error: %s", exc)

    def _consume_sse_lines(self, lines) -> None:
        """Parse an SSE line stream, tracking each frame's event type.

        Flag-update frames ("event: flag_updated" / "kill_switch") apply
        directly to the cache. A "lag" frame is written by the gateway right
        before it DROPS a flag update for a client that fell behind, so it
        triggers a debounced full-snapshot refetch to recover the drop.
        "prerequisites_updated" carries a flag's full, current prerequisite
        list (services/flag-api/internal/api/v1/prerequisites.go) and is
        applied separately from _apply_event, since its payload shape has
        no enabled/rollout_pct/reason keys at all -- routing it through
        _apply_event would silently zero those fields out.
        """
        event_type = "message"
        for line in lines:
            if line.startswith("event:"):
                event_type = line[6:].strip()
            elif line.startswith("data:"):
                payload = line[5:].strip()
                if payload:
                    if event_type == "lag":
                        self._schedule_snapshot_refetch()
                    elif event_type == "prerequisites_updated":
                        self._apply_prerequisites_event(payload)
                    else:
                        self._apply_event(payload)
                # Reset for the next frame — an event type applies only to the
                # data line that immediately follows it.
                event_type = "message"

    def _schedule_snapshot_refetch(self) -> None:
        """Debounce a full-snapshot refetch after gateway "lag" event(s).

        Coalesces a burst of lag frames into a single refetch by cancelling
        and rescheduling the timer on each frame, so the refetch fires once
        the client has stopped falling behind.
        """
        with self._refetch_lock:
            if self._stopped:
                return
            if self._refetch_timer is not None:
                self._refetch_timer.cancel()
            timer = threading.Timer(
                self._refetch_debounce_seconds, self._fetch_snapshot
            )
            timer.daemon = True
            self._refetch_timer = timer
            timer.start()

    def _apply_event(self, raw_json: str) -> None:
        import json

        try:
            event = json.loads(raw_json)
            flag_key = event.get("flag_key")
            if not flag_key:
                return
            # flag-api's real FlagEvent (services/flag-api/internal/api/v1/
            # flags.go) carries exactly flag_key/enabled/rollout_pct/reason/
            # ts/environment -- never safe_default/hash_version/target_list/
            # targeting_rules/prerequisites (SDK-4 investigation). Before
            # this fix, every field the event doesn't carry was overwritten
            # with a hardcoded default (False/1/[]) instead of preserved,
            # so ANY real SSE event for a flag -- a kill-switch, a rollback
            # step, literally any enabled/rollout_pct change -- silently
            # wiped that flag's cached prerequisites and targeting_rules to
            # empty client-side, until the next full snapshot refetch
            # restored them: a live correctness regression window, not
            # merely "rules don't propagate live". Merging against the
            # existing cached entry (mirroring @tombstone/core's cache.ts
            # applyEvent, which already does this correctly) closes it.
            with self._lock:
                existing = self._cache.get(flag_key)

                # event.get(key, fallback) only falls back when key is
                # ABSENT -- a key present with an explicit JSON null returns
                # None itself, not the fallback (found by adversarial
                # review of this fix). For this merge, "the event doesn't
                # tell us this field's value" and "the event explicitly
                # says null" should both mean the same thing -- fall back to
                # whatever's already cached, never overwrite a real value
                # with a bare None -- so this treats a None RESULT as
                # "no real value provided" regardless of why it's None.
                # Currently dormant either way (flag-api's real FlagEvent,
                # all non-pointer Go types, never serializes these keys as
                # JSON null today), but hardens the pattern before any
                # future event schema legitimately sends an explicit null
                # for one of these fields (e.g. a Go pointer/slice field).
                def _field_or_existing(key: str, existing_value):
                    value = event.get(key)
                    return existing_value if value is None else value

                state = FlagEnvironmentState(
                    flag_key=flag_key,
                    enabled=event.get("enabled", False),
                    rollout_pct=float(event.get("rollout_pct", 0)),
                    safe_default=_field_or_existing(
                        "safe_default", existing.safe_default if existing else False
                    ),
                    environment=event.get("environment", self._environment),
                    hash_version=_field_or_existing(
                        "hash_version", existing.hash_version if existing else 1
                    ),
                    target_list=_field_or_existing(
                        "target_list", existing.target_list if existing else []
                    ),
                    targeting_rules=existing.targeting_rules if existing else [],
                    prerequisites=existing.prerequisites if existing else [],
                    prerequisites_updated_at=existing.prerequisites_updated_at
                    if existing
                    else 0,
                )
                new_cache = dict(self._cache)
                new_cache[flag_key] = state
                self._cache = new_cache
        except Exception as exc:
            logger.warning("Tombstone: failed to apply SSE event: %s", exc)

    def _apply_prerequisites_event(self, raw_json: str) -> None:
        """Apply a live "prerequisites_updated" SSE event (services/flag-api/
        internal/api/v1/prerequisites.go's PrerequisitesEvent):
        {"flag_key", "environment", "prerequisites", "ts"}.

        Full replacement, not a delta -- matches PrerequisitesEvent's own
        documented design (it always carries the flag's CURRENT FULL
        prerequisite list, not an add/remove delta).

        Guards against a disclosed, real ordering hazard (PrerequisitesEvent's
        own doc comment): publishPrerequisitesUpdated's SELECT-then-XAdd has
        no per-flag lock, so two concurrent AddPrerequisite/DeletePrerequisite
        calls on the SAME flag can have their events arrive here in an order
        that does not match their real DB-commit order, if an earlier
        commit's own publish step is delayed past a later commit's. Rejecting
        an incoming event whose ts is OLDER than what's already cached
        (rather than unconditionally overwriting on arrival order) closes
        that gap at the point where staleness actually matters -- the next
        full snapshot refetch (reconnect, or a "lag" event) still eventually
        corrects a rare, permanently-stuck case if this client's own cache
        somehow never receives the true final event at all.
        """
        import json

        try:
            event = json.loads(raw_json)
            flag_key = event.get("flag_key")
            if not flag_key:
                return
            ts = int(event.get("ts", 0))

            with self._lock:
                existing = self._cache.get(flag_key)
                if existing is None:
                    # Nothing cached for this flag at all (e.g. it was
                    # created after this client's last snapshot fetch) --
                    # the next full snapshot refetch will pick it up
                    # correctly; there's no existing entry to merge a
                    # partial prerequisites-only update into.
                    return
                if ts < existing.prerequisites_updated_at:
                    logger.debug(
                        "Tombstone: dropping stale prerequisites_updated for '%s' "
                        "(event ts=%s older than cached ts=%s)",
                        flag_key,
                        ts,
                        existing.prerequisites_updated_at,
                    )
                    return

                updated = FlagEnvironmentState(
                    flag_key=existing.flag_key,
                    enabled=existing.enabled,
                    rollout_pct=existing.rollout_pct,
                    safe_default=existing.safe_default,
                    environment=existing.environment,
                    targeting_rules=existing.targeting_rules,
                    prerequisites=event.get("prerequisites", []),
                    hash_version=existing.hash_version,
                    target_list=existing.target_list,
                    prerequisites_updated_at=ts,
                )
                new_cache = dict(self._cache)
                new_cache[flag_key] = updated
                self._cache = new_cache
        except Exception as exc:
            logger.warning(
                "Tombstone: failed to apply prerequisites_updated event: %s", exc
            )
