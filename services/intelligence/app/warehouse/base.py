"""
Warehouse connector Protocol — structural interface for all data warehouse adapters.

All concrete connectors must satisfy this Protocol. The fetch_metric / test_connection
methods operate exclusively on aggregated results — raw user rows must never leave the
customer's warehouse (zero-copy privacy guarantee).
"""
from __future__ import annotations

from datetime import datetime
from typing import Protocol, runtime_checkable

import pandas as pd


@runtime_checkable
class WarehouseConnector(Protocol):
    """Structural interface for BigQuery, Snowflake, Databricks, and any future connectors."""

    async def fetch_metric(
        self,
        metric_sql: str,
        start: datetime,
        end: datetime,
    ) -> pd.DataFrame:
        """
        Execute metric_sql over the time window [start, end] and return aggregated
        results as a DataFrame.

        Requirements:
        - metric_sql MUST be a SELECT that returns only aggregated columns
          (COUNT, AVG, SUM, STDDEV, …).  No raw per-user rows.
        - The implementation must substitute {start} and {end} format placeholders
          before executing (ISO-8601 strings).
        - Returned DataFrame columns are connector-specific but must not contain PII.
        """
        ...

    async def test_connection(self) -> bool:
        """
        Verify the connector can reach the warehouse without running an expensive query.
        Returns True on success, False on any error.
        """
        ...
