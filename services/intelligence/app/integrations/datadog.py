"""Datadog Events API integration for Tombstone flag-change markers."""

import logging
import os
from typing import Any

import httpx

logger = logging.getLogger(__name__)

_DATADOG_EVENTS_URL = "https://api.datadoghq.com/api/v1/events"


class DatadogIntegration:
    """Posts Tombstone events to Datadog as vertical markers on metric graphs."""

    def __init__(self, api_key: str | None = None) -> None:
        self._api_key = api_key or os.environ.get("DD_API_KEY", "")

    async def post_flag_change_event(
        self,
        flag_key: str,
        environment: str,
        old_state: str,
        new_state: str,
        actor: str,
        event_type: str,
    ) -> bool:
        """Post a Datadog event marking a flag state change.

        The event appears as a vertical marker on any Datadog dashboard graph
        whose time range includes the change timestamp.

        Args:
            flag_key: The flag that changed (e.g. "checkout-v2").
            environment: Target environment (e.g. "production").
            old_state: Previous flag state (e.g. "enabled").
            new_state: New flag state (e.g. "disabled").
            actor: User or system that made the change.
            event_type: Category of change (e.g. "flag_toggled", "rollout_updated").

        Returns:
            True if Datadog accepted the payload (2xx), False otherwise.
        """
        payload: dict[str, Any] = {
            "title": f"Tombstone: flag {flag_key} changed in {environment}",
            "text": (
                f"Flag **{flag_key}** changed from `{old_state}` → `{new_state}` "
                f"in **{environment}** by {actor}."
            ),
            "alert_type": "info",
            "source_type_name": "tombstone",
            "tags": [
                f"flag:{flag_key}",
                f"environment:{environment}",
                "service:tombstone",
                f"event_type:{event_type}",
            ],
        }
        return await self._post_event(payload)

    async def post_rollback_event(
        self,
        flag_key: str,
        environment: str,
        error_rate: float,
    ) -> bool:
        """Post an error-severity Datadog event for an auto-rollback.

        Args:
            flag_key: The flag that was rolled back.
            environment: Deployment environment.
            error_rate: Observed error rate (0–100) that triggered the rollback.

        Returns:
            True if Datadog accepted the payload, False otherwise.
        """
        payload: dict[str, Any] = {
            "title": f"Tombstone Auto-Rollback: {flag_key} disabled in {environment}",
            "text": (
                f"Flag **{flag_key}** was automatically disabled in **{environment}** "
                f"due to an elevated error rate of {error_rate:.1f}%."
            ),
            "alert_type": "error",
            "source_type_name": "tombstone",
            "tags": [
                f"flag:{flag_key}",
                f"environment:{environment}",
                "service:tombstone",
                "event_type:auto_rollback",
            ],
        }
        return await self._post_event(payload)

    async def _post_event(self, payload: dict[str, Any]) -> bool:
        """HTTP POST an event payload to the Datadog Events v1 API.

        Args:
            payload: Datadog event body dict.

        Returns:
            True on 2xx response, False on any error or non-2xx status.
        """
        if not self._api_key:
            logger.warning("DD_API_KEY not configured; skipping Datadog event")
            return False

        headers = {
            "DD-API-KEY": self._api_key,
            "Content-Type": "application/json",
        }

        try:
            async with httpx.AsyncClient(timeout=10.0) as client:
                response = await client.post(
                    _DATADOG_EVENTS_URL, json=payload, headers=headers
                )
                response.raise_for_status()
                return True
        except httpx.HTTPStatusError as exc:
            logger.error(
                "Datadog Events API returned %s: %s",
                exc.response.status_code,
                exc.response.text,
            )
            return False
        except httpx.RequestError as exc:
            logger.error("Failed to reach Datadog Events API: %s", exc)
            return False
