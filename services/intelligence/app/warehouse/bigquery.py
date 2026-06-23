"""
BigQuery connector — zero-copy metric fetcher.

Install optional dependency:
    pip install 'tombstone-intelligence[bigquery]'
    # or: pip install google-cloud-bigquery[pandas]>=3.0
"""
from __future__ import annotations

import asyncio
import os
from datetime import datetime
from typing import Any

import pandas as pd


class BigQueryConnector:
    """
    Fetches aggregated experiment metrics from Google BigQuery.

    Only aggregated results are returned — raw user rows never leave the warehouse.
    Authentication falls back to Application Default Credentials (ADC) when
    credentials_json is not supplied.
    """

    def __init__(
        self,
        project_id: str | None = None,
        credentials_json: str | None = None,
    ) -> None:
        self.project_id = project_id or os.getenv("BQ_PROJECT_ID")
        self._credentials_json = credentials_json or os.getenv("BQ_CREDENTIALS_JSON", "")

    # ------------------------------------------------------------------
    # Internal helpers
    # ------------------------------------------------------------------

    def _get_client(self) -> Any:
        """Return an authenticated BigQuery client (lazy import)."""
        try:
            from google.cloud import bigquery  # type: ignore[import]
        except ImportError as exc:
            raise RuntimeError(
                "google-cloud-bigquery not installed. "
                "Run: pip install 'google-cloud-bigquery[pandas]>=3.0'"
            ) from exc

        if self._credentials_json:
            import json
            from google.oauth2.service_account import Credentials  # type: ignore[import]

            info = json.loads(self._credentials_json)
            creds = Credentials.from_service_account_info(info)
            return bigquery.Client(project=self.project_id, credentials=creds)

        # ADC path (works on GCP, local gcloud auth, Workload Identity, etc.)
        return bigquery.Client(project=self.project_id)

    def _run_fetch(self, metric_sql: str, start: datetime, end: datetime) -> pd.DataFrame:
        """Synchronous fetch — run via asyncio.to_thread to avoid blocking the event loop."""
        sql = metric_sql.format(start=start.isoformat(), end=end.isoformat())
        client = self._get_client()
        return client.query(sql).to_dataframe()

    def _run_test(self) -> bool:
        """Synchronous connectivity check."""
        try:
            from google.cloud import bigquery  # type: ignore[import]
        except ImportError:
            return False
        try:
            client = self._get_client()
            list(client.query("SELECT 1 AS ping").result())
            return True
        except Exception:
            return False

    # ------------------------------------------------------------------
    # Protocol implementation
    # ------------------------------------------------------------------

    async def fetch_metric(
        self,
        metric_sql: str,
        start: datetime,
        end: datetime,
    ) -> pd.DataFrame:
        """
        Execute metric_sql (with {start}/{end} placeholders) and return aggregated rows.

        metric_sql must SELECT only aggregated columns — no raw PII.
        """
        return await asyncio.to_thread(self._run_fetch, metric_sql, start, end)

    async def test_connection(self) -> bool:
        """Verify BigQuery connectivity without running an expensive query."""
        return await asyncio.to_thread(self._run_test)
