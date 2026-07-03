"""
Databricks connector — zero-copy metric fetcher.

Install optional dependency:
    pip install 'tombstone-intelligence[databricks]'
    # or: pip install databricks-sql-connector>=3.0
"""
from __future__ import annotations

import os
from datetime import datetime
from typing import Any

import pandas as pd

from app.warehouse.executor import run_warehouse_query


class DatabricksConnector:
    """
    Fetches aggregated experiment metrics from Databricks SQL.

    Only aggregated results are returned — raw user rows never leave the warehouse.
    Authenticates via a personal access token or OAuth M2M token.
    """

    def __init__(
        self,
        server_hostname: str | None = None,
        http_path: str | None = None,
        access_token: str | None = None,
        catalog: str | None = None,
        schema: str | None = None,
    ) -> None:
        self._server_hostname = server_hostname or os.getenv("DATABRICKS_HOST", "")
        self._http_path = http_path or os.getenv("DATABRICKS_HTTP_PATH", "")
        self._access_token = access_token or os.getenv("DATABRICKS_TOKEN", "")
        self._catalog = catalog or os.getenv("DATABRICKS_CATALOG", "")
        self._schema = schema or os.getenv("DATABRICKS_SCHEMA", "")

    # ------------------------------------------------------------------
    # Internal helpers
    # ------------------------------------------------------------------

    def _connect(self) -> Any:
        """Return a Databricks SQL connection (lazy import)."""
        try:
            from databricks import sql as dbsql  # type: ignore[import]
        except ImportError as exc:
            raise RuntimeError(
                "databricks-sql-connector not installed. "
                "Run: pip install 'databricks-sql-connector>=3.0'"
            ) from exc

        connect_kwargs: dict[str, Any] = {
            "server_hostname": self._server_hostname,
            "http_path": self._http_path,
            "access_token": self._access_token,
        }
        if self._catalog:
            connect_kwargs["catalog"] = self._catalog
        if self._schema:
            connect_kwargs["schema"] = self._schema

        return dbsql.connect(**connect_kwargs)

    def _run_fetch(self, metric_sql: str, start: datetime, end: datetime) -> pd.DataFrame:
        """Synchronous fetch — run via run_warehouse_query to avoid blocking the event loop."""
        sql = metric_sql.format(start=start.isoformat(), end=end.isoformat())
        conn = self._connect()
        try:
            with conn.cursor() as cur:
                cur.execute(sql)
                col_names = [desc[0] for desc in cur.description]
                rows = cur.fetchall()
            return pd.DataFrame(rows, columns=col_names)
        finally:
            conn.close()

    def _run_test(self) -> bool:
        """Synchronous connectivity check."""
        try:
            conn = self._connect()
            with conn.cursor() as cur:
                cur.execute("SELECT 1")
            conn.close()
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
        return await run_warehouse_query(self._run_fetch, metric_sql, start, end)

    async def test_connection(self) -> bool:
        """Verify Databricks connectivity without running an expensive query."""
        return await run_warehouse_query(self._run_test)
