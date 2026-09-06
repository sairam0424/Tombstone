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
                state = FlagEnvironmentState(
                    flag_key=flag_key,
                    enabled=event.get("enabled", False),
                    rollout_pct=float(event.get("rollout_pct", 0)),
                    safe_default=event.get(
                        "safe_default", existing.safe_default if existing else False
                    ),
                    environment=event.get("environment", self._environment),
                    hash_version=event.get(
                        "hash_version", existing.hash_version if existing else 1
                    ),
                    target_list=event.get(
                        "target_list", existing.target_list if existing else []
                    ),
                    targeting_rules=existing.targeting_rules if existing else [],
                    prerequisites=existing.prerequisites if existing else [],
                )
                new_cache = dict(self._cache)
                new_cache[flag_key] = state
                self._cache = new_cache
        except Exception as exc:
            logger.warning("Tombstone: failed to apply SSE event: %s", exc)
