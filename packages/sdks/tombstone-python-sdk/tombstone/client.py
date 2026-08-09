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

    def connect(self) -> None:
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

        sse_thread = threading.Thread(
            target=self._sse_listener, name="flagmind-sse", daemon=True
        )
        sse_thread.start()

    def evaluate(
        self, flag_key: str, context: EvaluationContext
    ) -> EvaluationResult:
        try:
            with self._lock:
                flag_state = self._cache.get(flag_key)
                all_flags = dict(self._cache)  # shallow copy under lock
            default_value = self._defaults.get(flag_key, False)
            return evaluate(
                flag_state, context, default_value, flag_key,
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
        from tombstone.types import FlagEnvironmentState, TargetingRule, PropertyCondition

        new_cache: dict[str, FlagEnvironmentState] = {}
        for raw in payload.get("flags", []):
            try:
                flag_key = raw["flag_key"]
            except (KeyError, TypeError):
                logger.warning("Tombstone: skipping malformed flag entry in snapshot: %r", raw)
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
                logger.warning("Tombstone: failed to deserialize flag '%s': %s", flag_key, exc)

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
                        for line in response.iter_lines():
                            if line.startswith("data:"):
                                payload = line[5:].strip()
                                if payload:
                                    self._apply_event(payload)
            except Exception as exc:
                logger.debug("Tombstone: SSE reconnect after error: %s", exc)

    def _apply_event(self, raw_json: str) -> None:
        import json

        try:
            event = json.loads(raw_json)
            flag_key = event.get("flag_key")
            if not flag_key:
                return
            state = FlagEnvironmentState(
                flag_key=flag_key,
                enabled=event.get("enabled", False),
                rollout_pct=float(event.get("rollout_pct", 0)),
                safe_default=event.get("safe_default", False),
                environment=event.get("environment", self._environment),
                hash_version=event.get("hash_version", 1),
                target_list=event.get("target_list", []),
            )
            with self._lock:
                new_cache = dict(self._cache)
                new_cache[flag_key] = state
                self._cache = new_cache
        except Exception as exc:
            logger.warning("Tombstone: failed to apply SSE event: %s", exc)
