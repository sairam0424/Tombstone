"""Tombstone OpenFeature provider — vendor-neutral feature flag evaluation."""
from __future__ import annotations

import logging
from dataclasses import dataclass, field
from typing import Any

from tombstone.client import TombstoneClient
from tombstone.types import EvaluationContext

logger = logging.getLogger(__name__)

# ─── OpenFeature types (inline — no peer dep required) ───────────────────────


@dataclass
class ResolutionDetails:
    """Mirrors the OpenFeature ResolutionDetails structure."""

    value: Any
    reason: str = "DEFAULT"
    error_code: str = ""
    error_message: str = ""


@dataclass
class OpenFeatureEvaluationContext:
    """Minimal subset of the OpenFeature EvaluationContext contract."""

    targeting_key: str = "anonymous"
    attributes: dict[str, str] = field(default_factory=dict)


# ─── Reason mapping ───────────────────────────────────────────────────────────

_REASON_MAP: dict[str, str] = {
    "OFF": "DISABLED",
    "FALLTHROUGH": "DEFAULT",
    "TARGET_MATCH": "TARGETING_MATCH",
    "RULE_MATCH": "TARGETING_MATCH",
    "PREREQUISITE_FAILED": "DEFAULT",
    "ERROR": "ERROR",
}


def _map_reason(flagmind_reason: str) -> str:
    return _REASON_MAP.get(flagmind_reason, "UNKNOWN")


def _build_context(context: OpenFeatureEvaluationContext | None) -> EvaluationContext:
    if context is None:
        return EvaluationContext(user_id="anonymous")
    return EvaluationContext(
        user_id=context.targeting_key or "anonymous",
        attrs=context.attributes or {},
    )


# ─── TombstoneProvider ─────────────────────────────────────────────────────────


class TombstoneProvider:
    """OpenFeature Provider for Tombstone.

    Usage::

        from tombstone import TombstoneClient
        from tombstone.openfeature import TombstoneProvider, OpenFeatureEvaluationContext

        client = TombstoneClient(sdk_key="...", environment="production")
        client.connect()
        provider = TombstoneProvider(client)

        ctx = OpenFeatureEvaluationContext(targeting_key="user-42")
        details = provider.resolve_boolean_details("payments.new-flow", False, ctx)
        print(details.value, details.reason)
    """

    def __init__(self, client: TombstoneClient) -> None:
        self._client = client

    @property
    def name(self) -> str:
        return "flagmind"

    # ── Boolean ──────────────────────────────────────────────────────────────

    def resolve_boolean_details(
        self,
        flag_key: str,
        default_value: bool,
        context: OpenFeatureEvaluationContext | None = None,
    ) -> ResolutionDetails:
        try:
            ctx = _build_context(context)
            result = self._client.evaluate(flag_key, ctx)
            value = result.value
            if not isinstance(value, bool):
                value = default_value
            return ResolutionDetails(value=value, reason=_map_reason(result.reason))
        except Exception as exc:  # noqa: BLE001
            logger.debug("Tombstone OpenFeature: resolve_boolean_details error: %s", exc)
            return ResolutionDetails(
                value=default_value,
                reason="ERROR",
                error_code="GENERAL",
                error_message=str(exc),
            )

    # ── String ───────────────────────────────────────────────────────────────

    def resolve_string_details(
        self,
        flag_key: str,
        default_value: str,
        context: OpenFeatureEvaluationContext | None = None,
    ) -> ResolutionDetails:
        try:
            ctx = _build_context(context)
            result = self._client.evaluate(flag_key, ctx)
            value = result.value
            if not isinstance(value, str):
                value = default_value
            return ResolutionDetails(value=value, reason=_map_reason(result.reason))
        except Exception as exc:  # noqa: BLE001
            logger.debug("Tombstone OpenFeature: resolve_string_details error: %s", exc)
            return ResolutionDetails(
                value=default_value,
                reason="ERROR",
                error_code="GENERAL",
                error_message=str(exc),
            )

    # ── Number ───────────────────────────────────────────────────────────────

    def resolve_number_details(
        self,
        flag_key: str,
        default_value: float | int,
        context: OpenFeatureEvaluationContext | None = None,
    ) -> ResolutionDetails:
        try:
            ctx = _build_context(context)
            result = self._client.evaluate(flag_key, ctx)
            value = result.value
            if not isinstance(value, (int, float)):
                value = default_value
            return ResolutionDetails(value=value, reason=_map_reason(result.reason))
        except Exception as exc:  # noqa: BLE001
            logger.debug("Tombstone OpenFeature: resolve_number_details error: %s", exc)
            return ResolutionDetails(
                value=default_value,
                reason="ERROR",
                error_code="GENERAL",
                error_message=str(exc),
            )

    # ── Object ───────────────────────────────────────────────────────────────

    def resolve_object_details(
        self,
        flag_key: str,
        default_value: object,
        context: OpenFeatureEvaluationContext | None = None,
    ) -> ResolutionDetails:
        try:
            ctx = _build_context(context)
            result = self._client.evaluate(flag_key, ctx)
            value = result.value
            if not isinstance(value, dict):
                value = default_value
            return ResolutionDetails(value=value, reason=_map_reason(result.reason))
        except Exception as exc:  # noqa: BLE001
            logger.debug("Tombstone OpenFeature: resolve_object_details error: %s", exc)
            return ResolutionDetails(
                value=default_value,
                reason="ERROR",
                error_code="GENERAL",
                error_message=str(exc),
            )
