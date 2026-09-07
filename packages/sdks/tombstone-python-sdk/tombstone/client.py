import logging
import threading
from typing import Any

import httpx

from tombstone.evaluation import evaluate
from tombstone.types import (
    EvaluationContext,
    EvaluationResult,
    FlagEnvironmentState,
    PropertyCondition,
    TargetingRule,
)

logger = logging.getLogger(__name__)

# A slow client can receive a burst of gateway "lag" frames back-to-back
# (one per dropped update). Coalesce them into a single snapshot refetch by
# waiting this long after the LAST lag frame before re-syncing the cache.
_DEFAULT_REFETCH_DEBOUNCE_SECONDS = 0.5


def _stringify_wire_value(v: object) -> str:
    """Renders a wire "values" element the same way a real context
    attribute would naturally stringify, so eq/in/neq/nin's plain
    string-equality comparison actually matches. A JSON integer literal
    parses to Python's native arbitrary-precision int (exact, no
    precision loss at any size) -- but a JSON float literal that happens
    to be a whole number (e.g. wire "21.0") parses to a Python float and
    must have its trailing zero fraction stripped, or "21.0" would
    silently fail to match a context attribute supplied as a plain int or
    bare numeric string "21". Found by adversarial review of the
    Ruby/.NET SDKs' identical adapters (PRs #248/#249); fixed here
    proactively.

    Residual, accepted limitation shared with every double-precision-based
    language: an EXPLICIT-decimal float literal (not a plain integer)
    above 2**53 has already lost precision by the time Python's json
    module parses it into a float, before this function ever runs --
    unlike a plain integer literal, which Python's arbitrary-precision int
    preserves exactly at any size (a real, structural advantage over the
    other 4 SDKs' native numeric types, not something this SDK had to
    work around).
    """
    if isinstance(v, bool):
        return "true" if v else "false"
    if isinstance(v, float):
        return str(int(v)) if v == int(v) else str(v)
    if v is None:
        return ""
    return str(v)


def _parse_targeting_rules(raw: object) -> list[TargetingRule]:
    """Adapts flag-api's real, FLAT per-rule wire shape (services/flag-api/
    internal/api/v1/targeting_rules.go: "id"/"rule_type"/"attribute"/
    "operator"/"values"/"variation"/"priority" -- ONE condition per rule
    row) into this SDK's own richer TargetingRule(conditions: list[...],
    rollout_pct, variation, priority) model, mirroring the Java/Ruby/.NET
    SDKs' identical adapters (PRs #247-249): a single-element conditions
    list, rollout_pct fixed at 100.0 (there is no per-rule rollout concept
    on the backend; 100 means "always apply once matched").

    The EARLIER version of this parsing (both here and in the pre-existing
    _apply_snapshot) assumed a NESTED "conditions" key per rule
    (r.get("conditions", [])), which does not exist anywhere on the real
    wire -- every real targeting rule parsed as an EMPTY conditions list,
    making targeting_rules completely unreachable against a real backend,
    the same class of gap the other 4 SDKs found and fixed for their own
    equivalent adapters (targeting_rules had zero real snapshot data
    exercising this path before this change, so the gap was never
    previously reachable). Found while wiring the real backend format
    into this SDK's live-event path for the first time.
    """
    if not isinstance(raw, list):
        return []
    result: list[TargetingRule] = []
    for r in raw:
        if not isinstance(r, dict):
            continue
        values = r.get("values")
        condition = PropertyCondition(
            attribute=r.get("attribute", ""),
            operator=r.get("operator", ""),
            values=[_stringify_wire_value(v) for v in values]
            if isinstance(values, list)
            else [],
            negate=False,
        )
        priority = r.get("priority", 0)
        result.append(
            TargetingRule(
                id=r.get("id", ""),
                conditions=[condition],
                rollout_pct=100.0,
                variation=r.get("variation", True),
                priority=priority if isinstance(priority, int) else 0,
            )
        )
    return result


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
        # None means "no snapshot loaded yet" -- a real flag-api snapshot ts
        # (time.Now().Unix() at response generation) will never be negative,
        # so the very first _apply_snapshot call always applies regardless.
        self._last_snapshot_ts: int | None = None
        # Tracks which flag keys' CURRENT prerequisites/targeting_rules came
        # from a live *_updated event (True) rather than a snapshot load
        # (False/absent) -- see _apply_prerequisites_event/
        # _apply_targeting_rules_event's own doc comments for why
        # _apply_snapshot needs this distinction, not just a ts comparison,
        # to decide whether a tied-or-older incoming snapshot should be
        # allowed to overwrite a live update. Both maps, and _last_snapshot_ts
        # above, were MISSING entirely from this SDK's original
        # prerequisites-streaming implementation -- a real, pre-existing gap
        # (no monotonicity guard, no per-flag tie-break at all) discovered
        # while adding targeting_rules_updated consumption; retrofitted here
        # for prerequisites too, not just targeting_rules.
        self._prerequisites_from_live_event: dict[str, bool] = {}
        self._targeting_rules_from_live_event: dict[str, bool] = {}
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
        snapshot_ts = payload.get("ts", 0)

        # Held for the ENTIRE method body (not just the final cache swap):
        # this makes _apply_snapshot fully mutually exclusive with
        # _apply_event/_apply_prerequisites_event/_apply_targeting_rules_event
        # (which already hold the same lock for their whole bodies), so a
        # concurrent live-event thread can never observe -- or race against
        # -- a partially-built snapshot. No separate "atomic CacheState"
        # object is needed the way the Java/.NET SDKs' AtomicReference/
        # volatile-record designs require, for the same reason Ruby's
        # Monitor#synchronize doesn't need one either.
        with self._lock:
            # flag-api's snapshot ts is simply the response-generation
            # wall-clock time (services/flag-api/internal/api/v1/
            # environments.go), not a per-flag "last changed" value -- so a
            # SLOWER, already-in-flight snapshot fetch (e.g. a lag-triggered
            # refetch racing a reconnect-triggered one) can resolve AFTER a
            # newer one already loaded. Rejecting an incoming snapshot whose
            # own ts is older than the last one actually applied prevents
            # that older response from silently clobbering fresher state for
            # every flag, not just prerequisites/targeting_rules.
            #
            # This guard, and the entire per-flag tie-break protection below,
            # were BOTH missing from this SDK's original prerequisites-
            # streaming implementation -- a real, pre-existing gap (no
            # monotonicity guard, no live-event tie-break at all) discovered
            # while adding targeting_rules_updated consumption. Retrofitted
            # here for prerequisites too, not just targeting_rules, since
            # the exact same race applies to both.
            if (
                self._last_snapshot_ts is not None
                and snapshot_ts < self._last_snapshot_ts
            ):
                return

            new_cache: dict[str, FlagEnvironmentState] = {}
            next_prereq_live: dict[str, bool] = {}
            next_rules_live: dict[str, bool] = {}

            for raw in payload.get("flags", []):
                try:
                    flag_key = raw["flag_key"]
                except (KeyError, TypeError):
                    logger.warning(
                        "Tombstone: skipping malformed flag entry in snapshot: %r",
                        raw,
                    )
                    continue

                try:
                    targeting_rules = _parse_targeting_rules(
                        raw.get("targeting_rules", [])
                    )

                    existing = self._cache.get(flag_key)
                    # A live prerequisites_updated event may have already
                    # advanced this flag's prerequisites_updated_at to OR
                    # PAST this snapshot's own ts if the snapshot fetch was
                    # still in flight when the live event arrived and
                    # applied -- see _apply_prerequisites_event's own doc
                    # comment for why >= (not >), and why the live-event
                    # marker is ALSO required (not just a ts comparison
                    # alone: a second, independent snapshot merely tying the
                    # same coarse-resolution second, with no live event
                    # involved, must still apply its own data).
                    keep_live_prerequisites = (
                        existing is not None
                        and self._prerequisites_from_live_event.get(flag_key, False)
                        and existing.prerequisites_updated_at >= snapshot_ts
                    )
                    # Identical reasoning to keep_live_prerequisites above,
                    # tracked independently for targeting_rules -- see
                    # _apply_targeting_rules_event's own doc comment. A live
                    # PREREQUISITES event must never protect targeting_rules
                    # (or vice versa) from an unrelated tied snapshot.
                    keep_live_targeting_rules = (
                        existing is not None
                        and self._targeting_rules_from_live_event.get(flag_key, False)
                        and existing.targeting_rules_updated_at >= snapshot_ts
                    )

                    new_cache[flag_key] = FlagEnvironmentState(
                        flag_key=flag_key,
                        enabled=raw.get("enabled", False),
                        rollout_pct=float(raw.get("rollout_pct", 0.0)),
                        safe_default=raw.get("safe_default", False),
                        environment=payload.get("environment", self._environment),
                        targeting_rules=(
                            existing.targeting_rules
                            if keep_live_targeting_rules
                            else targeting_rules
                        ),
                        prerequisites=(
                            existing.prerequisites
                            if keep_live_prerequisites
                            else raw.get("prerequisites", [])
                        ),
                        hash_version=raw.get("hash_version", 1),
                        target_list=raw.get("target_list", []),
                        prerequisites_updated_at=(
                            existing.prerequisites_updated_at
                            if keep_live_prerequisites
                            else snapshot_ts
                        ),
                        targeting_rules_updated_at=(
                            existing.targeting_rules_updated_at
                            if keep_live_targeting_rules
                            else snapshot_ts
                        ),
                    )
                    # ONE-SHOT consumption, always False here (never
                    # keep_live_prerequisites/keep_live_targeting_rules):
                    # this snapshot apply has now fully resolved the race
                    # between the live event and ITS OWN specific in-flight
                    # snapshot. Re-propagating True would make the
                    # protection "sticky", vetoing a later, independent
                    # snapshot that merely happens to tie the same
                    # coarse-resolution second too.
                    next_prereq_live[flag_key] = False
                    next_rules_live[flag_key] = False
                except Exception as exc:
                    logger.warning(
                        "Tombstone: failed to deserialize flag '%s': %s",
                        flag_key,
                        exc,
                    )

            self._cache = new_cache
            self._prerequisites_from_live_event = next_prereq_live
            self._targeting_rules_from_live_event = next_rules_live
            self._last_snapshot_ts = snapshot_ts

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
        "prerequisites_updated"/"targeting_rules_updated" each carry a
        flag's full, current prerequisite/targeting-rule list (services/
        flag-api/internal/api/v1/prerequisites.go, targeting_rules.go) and
        are applied separately from _apply_event, since their payload
        shapes have no enabled/rollout_pct/reason keys at all -- routing
        them through _apply_event would silently zero those fields out.
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
                    elif event_type == "targeting_rules_updated":
                        self._apply_targeting_rules_event(payload)
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
                    targeting_rules_updated_at=existing.targeting_rules_updated_at
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
                    targeting_rules_updated_at=existing.targeting_rules_updated_at,
                )
                new_cache = dict(self._cache)
                new_cache[flag_key] = updated
                self._cache = new_cache
                # Marks this flag's prerequisites as LIVE-sourced -- see
                # _apply_snapshot's own "keep_live_prerequisites" comment
                # for why it needs this distinction, not just a ts
                # comparison, to decide whether a tied-or-older incoming
                # snapshot should be allowed to overwrite it.
                next_prereq_live = dict(self._prerequisites_from_live_event)
                next_prereq_live[flag_key] = True
                self._prerequisites_from_live_event = next_prereq_live
        except Exception as exc:
            logger.warning(
                "Tombstone: failed to apply prerequisites_updated event: %s", exc
            )

    def _apply_targeting_rules_event(self, raw_json: str) -> None:
        """Apply a live "targeting_rules_updated" SSE event (services/
        flag-api/internal/api/v1/targeting_rules.go's TargetingRulesEvent):
        {"flag_key", "environment", "targeting_rules", "ts"}.

        Full replacement, not a delta -- matches TargetingRulesEvent's own
        documented design. Mirrors _apply_prerequisites_event's own
        staleness guard exactly (strict "<", comparing against
        targeting_rules_updated_at, not prerequisites_updated_at).
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
                    return
                if ts < existing.targeting_rules_updated_at:
                    logger.debug(
                        "Tombstone: dropping stale targeting_rules_updated for '%s' "
                        "(event ts=%s older than cached ts=%s)",
                        flag_key,
                        ts,
                        existing.targeting_rules_updated_at,
                    )
                    return

                targeting_rules = _parse_targeting_rules(
                    event.get("targeting_rules", [])
                )

                updated = FlagEnvironmentState(
                    flag_key=existing.flag_key,
                    enabled=existing.enabled,
                    rollout_pct=existing.rollout_pct,
                    safe_default=existing.safe_default,
                    environment=existing.environment,
                    targeting_rules=targeting_rules,
                    prerequisites=existing.prerequisites,
                    hash_version=existing.hash_version,
                    target_list=existing.target_list,
                    prerequisites_updated_at=existing.prerequisites_updated_at,
                    targeting_rules_updated_at=ts,
                )
                new_cache = dict(self._cache)
                new_cache[flag_key] = updated
                self._cache = new_cache
                # Marks this flag's targeting_rules as LIVE-sourced -- see
                # _apply_snapshot's own "keep_live_targeting_rules" comment.
                next_rules_live = dict(self._targeting_rules_from_live_event)
                next_rules_live[flag_key] = True
                self._targeting_rules_from_live_event = next_rules_live
        except Exception as exc:
            logger.warning(
                "Tombstone: failed to apply targeting_rules_updated event: %s", exc
            )
