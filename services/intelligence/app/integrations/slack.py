"""Slack Incoming Webhook notifier for Tombstone events."""

import logging
import os
from typing import Any

import httpx

logger = logging.getLogger(__name__)


class SlackNotifier:
    """Posts Tombstone notifications to a Slack channel via Incoming Webhooks."""

    def __init__(self, webhook_url: str | None = None) -> None:
        self._webhook_url = webhook_url or os.environ.get("SLACK_WEBHOOK_URL", "")

    async def notify_rollback(
        self,
        flag_key: str,
        environment: str,
        error_rate: float,
        triggered_by: str,
        rollback_url: str,
    ) -> bool:
        """Post a red attachment announcing an auto-rollback event.

        Args:
            flag_key: The flag that was disabled.
            environment: Deployment environment (e.g. "production").
            error_rate: Observed error rate that triggered the rollback (0-100).
            triggered_by: Actor / system that initiated the rollback.
            rollback_url: Deep-link into the Tombstone dashboard for this flag.

        Returns:
            True if Slack accepted the payload (2xx), False otherwise.
        """
        payload: dict[str, Any] = {
            "attachments": [
                {
                    "color": "#CC0000",
                    "fallback": f"Tombstone Auto-Rollback: {flag_key} disabled in {environment}",
                    "title": "Tombstone Auto-Rollback",
                    "text": (
                        f"Flag *{flag_key}* has been automatically disabled in "
                        f"*{environment}*.\n"
                        f"Error rate: *{error_rate:.1f}%*\n"
                        f"Triggered by: {triggered_by}"
                    ),
                    "mrkdwn_in": ["text"],
                    "actions": [
                        {
                            "type": "button",
                            "text": "View in Dashboard",
                            "url": rollback_url,
                            "style": "danger",
                        }
                    ],
                }
            ]
        }
        return await self._post(payload)

    async def notify_stale_flag(
        self,
        flag_key: str,
        owner_id: str,
        days_at_100_pct: int,
        recommended_action: str,
    ) -> bool:
        """Post an amber/red attachment for a stale flag requiring cleanup.

        Args:
            flag_key: The stale flag key.
            owner_id: User or team responsible for this flag.
            days_at_100_pct: How many consecutive days the flag has been at 100%.
            recommended_action: Human-readable suggested action (e.g. "Archive flag").

        Returns:
            True if Slack accepted the payload, False otherwise.
        """
        color = "#FF4500" if days_at_100_pct >= 30 else "#FFA500"
        payload: dict[str, Any] = {
            "attachments": [
                {
                    "color": color,
                    "fallback": f"Tombstone Stale Flag: {flag_key} ({days_at_100_pct} days at 100%)",
                    "title": "Tombstone Stale Flag Detected",
                    "fields": [
                        {"title": "Flag Key", "value": flag_key, "short": True},
                        {"title": "Owner", "value": owner_id, "short": True},
                        {
                            "title": "Days at 100%",
                            "value": str(days_at_100_pct),
                            "short": True,
                        },
                        {
                            "title": "Recommended Action",
                            "value": recommended_action,
                            "short": False,
                        },
                    ],
                    "footer": "Tombstone Intelligence",
                    "mrkdwn_in": ["fields"],
                }
            ]
        }
        return await self._post(payload)

    async def notify_incident_correlation(
        self,
        incident_id: str,
        flag_key: str,
        correlation_score: float,
        rollback_url: str,
    ) -> bool:
        """Post a purple attachment when a flag correlates with an active incident.

        Args:
            incident_id: PagerDuty / OpsGenie incident identifier.
            flag_key: Flag that may be causally related to the incident.
            correlation_score: Confidence score (0.0 – 1.0).
            rollback_url: One-click rollback URL in the Tombstone dashboard.

        Returns:
            True if Slack accepted the payload, False otherwise.
        """
        payload: dict[str, Any] = {
            "attachments": [
                {
                    "color": "#6A0DAD",
                    "fallback": (
                        f"Tombstone Incident Correlation: incident {incident_id} "
                        f"may be related to flag {flag_key}"
                    ),
                    "title": "Tombstone Incident Correlation Detected",
                    "text": (
                        f"Incident *{incident_id}* correlates with flag *{flag_key}*.\n"
                        f"Correlation score: *{correlation_score:.2f}*"
                    ),
                    "mrkdwn_in": ["text"],
                    "actions": [
                        {
                            "type": "button",
                            "text": "Rollback Flag",
                            "url": rollback_url,
                            "style": "danger",
                        }
                    ],
                    "footer": "Tombstone Intelligence",
                }
            ]
        }
        return await self._post(payload)

    async def _post(self, payload: dict[str, Any]) -> bool:
        """HTTP POST the payload to the configured Slack Incoming Webhook URL.

        Args:
            payload: Slack message payload dict.

        Returns:
            True on 2xx response, False on any error or non-2xx status.
        """
        if not self._webhook_url:
            logger.warning("SLACK_WEBHOOK_URL not configured; skipping notification")
            return False

        try:
            async with httpx.AsyncClient(timeout=10.0) as client:
                response = await client.post(self._webhook_url, json=payload)
                response.raise_for_status()
                return True
        except httpx.HTTPStatusError as exc:
            logger.error(
                "Slack webhook returned %s: %s",
                exc.response.status_code,
                exc.response.text,
            )
            return False
        except httpx.RequestError as exc:
            logger.error("Failed to reach Slack webhook: %s", exc)
            return False
