"""Inbound webhook receivers for PagerDuty and OpsGenie alert events."""

import logging
from typing import Any

from fastapi import APIRouter, HTTPException, Request, status

logger = logging.getLogger(__name__)

router = APIRouter(prefix="/api/v1/webhooks", tags=["webhooks"])

# PagerDuty v3 event types that trigger correlation analysis
_PAGERDUTY_ACTIONABLE_EVENTS = {"incident.triggered", "incident.acknowledged"}

# OpsGenie alert actions that trigger correlation analysis
_OPSGENIE_ACTIONABLE_ACTIONS = {"Create", "Acknowledge"}


@router.post("/pagerduty", status_code=status.HTTP_200_OK)
async def receive_pagerduty(request: Request) -> dict[str, Any]:
    """Accept PagerDuty v3 webhook payloads and correlate with flag changes.

    Handles ``incident.triggered`` and ``incident.acknowledged`` events.
    Ignores all other event types gracefully.

    Expected payload shape (PagerDuty v3 webhooks bundle multiple events):
    ```json
    {
      "messages": [
        {
          "event": "incident.triggered",
          "incident": {
            "id": "P12ABCD",
            "created_at": "2024-01-15T10:30:00Z"
          }
        }
      ]
    }
    ```

    Returns:
        JSON body with ``processed_events`` list; each entry includes
        ``incident_id``, ``event_type``, ``created_at``, and
        ``correlation_triggered`` flag.
    """
    try:
        body: dict[str, Any] = await request.json()
    except Exception as exc:
        logger.warning("Failed to parse PagerDuty webhook body: %s", exc)
        raise HTTPException(
            status_code=status.HTTP_400_BAD_REQUEST,
            detail="Invalid JSON payload",
        ) from exc

    messages: list[dict[str, Any]] = body.get("messages", [])
    if not messages:
        # Also handle single-message format (older PagerDuty integrations)
        single_event = body.get("event")
        if single_event:
            messages = [body]

    processed: list[dict[str, Any]] = []

    for message in messages:
        event_type: str = message.get("event", "")
        if event_type not in _PAGERDUTY_ACTIONABLE_EVENTS:
            logger.debug("Ignoring PagerDuty event type: %s", event_type)
            continue

        incident: dict[str, Any] = message.get("incident", {})
        incident_id: str = incident.get("id", "")
        created_at: str = incident.get("created_at", "")

        if not incident_id:
            logger.warning("PagerDuty event %s missing incident.id; skipping", event_type)
            continue

        logger.info(
            "PagerDuty %s received: incident_id=%s created_at=%s",
            event_type,
            incident_id,
            created_at,
        )

        # TODO: invoke app.state.correlator.correlate() when request.app.state
        #       exposes the correlator to this router; stub returns True for now.
        correlation_triggered = True

        processed.append(
            {
                "incident_id": incident_id,
                "event_type": event_type,
                "created_at": created_at,
                "correlation_triggered": correlation_triggered,
            }
        )

    return {"processed_events": processed, "total": len(processed)}


@router.post("/opsgenie", status_code=status.HTTP_200_OK)
async def receive_opsgenie(request: Request) -> dict[str, Any]:
    """Accept OpsGenie alert webhook payloads and correlate with flag changes.

    Handles ``Create`` and ``Acknowledge`` alert actions.
    Ignores all other actions gracefully.

    Expected payload shape:
    ```json
    {
      "action": "Create",
      "alert": {
        "alertId": "a12b34c5-...",
        "message": "High error rate on checkout service"
      }
    }
    ```

    Returns:
        JSON body with ``alert_id``, ``action``, and ``correlation_triggered`` flag.
    """
    try:
        body: dict[str, Any] = await request.json()
    except Exception as exc:
        logger.warning("Failed to parse OpsGenie webhook body: %s", exc)
        raise HTTPException(
            status_code=status.HTTP_400_BAD_REQUEST,
            detail="Invalid JSON payload",
        ) from exc

    action: str = body.get("action", "")
    alert: dict[str, Any] = body.get("alert", {})
    alert_id: str = alert.get("alertId", "")

    if action not in _OPSGENIE_ACTIONABLE_ACTIONS:
        logger.debug("Ignoring OpsGenie action: %s", action)
        return {
            "alert_id": alert_id,
            "action": action,
            "correlation_triggered": False,
        }

    if not alert_id:
        logger.warning("OpsGenie %s action missing alert.alertId", action)
        raise HTTPException(
            status_code=status.HTTP_422_UNPROCESSABLE_ENTITY,
            detail="alert.alertId is required",
        )

    logger.info("OpsGenie %s received: alert_id=%s", action, alert_id)

    # TODO: invoke app.state.correlator.correlate() once router has app context
    correlation_triggered = True

    return {
        "alert_id": alert_id,
        "action": action,
        "correlation_triggered": correlation_triggered,
    }
