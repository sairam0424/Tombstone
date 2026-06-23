"""
Snowflake connector — zero-copy metric fetcher.

Install optional dependency:
    pip install 'tombstone-intelligence[snowflake]'
    # or: pip install snowflake-connector-python>=3.0
"""
from __future__ import annotations

import asyncio
import os
from datetime import datetime
from typing import Any

import pandas as pd


class SnowflakeConnector:
    """
    Fetches aggregated experiment metrics from Snowflake.

    Only aggregated results are returned — raw user rows never leave the warehouse.
    Supports password and private-key authentication.
    """

    def __init__(
        self,
        account: str | None = None,
        user: str | None = None,
        password: str | None = None,
        warehouse: str | None = None,
        database: str | None = None,
        schema: str | None = None,
        private_key_path: str | None = None,
    ) -> None:
        self._account = account or os.getenv("SNOWFLAKE_ACCOUNT", "")
        self._user = user or os.getenv("SNOWFLAKE_USER", "")
        self._password = password or os.getenv("SNOWFLAKE_PASSWORD", "")
        self._warehouse = warehouse or os.getenv("SNOWFLAKE_WAREHOUSE", "")
        self._database = database or os.getenv("SNOWFLAKE_DATABASE", "")
        self._schema = schema or os.getenv("SNOWFLAKE_SCHEMA", "")
        self._private_key_path = private_key_path or os.getenv("SNOWFLAKE_PRIVATE_KEY_PATH", "")

    # ------------------------------------------------------------------
    # Internal helpers
    # ------------------------------------------------------------------

    def _connect(self) -> Any:
        """Return a Snowflake connection (lazy import)."""
        try:
            import snowflake.connector  # type: ignore[import]
        except ImportError as exc:
            raise RuntimeError(
                "snowflake-connector-python not installed. "
                "Run: pip install 'snowflake-connector-python>=3.0'"
            ) from exc

        connect_kwargs: dict[str, Any] = {
            "account": self._account,
            "user": self._user,
            "warehouse": self._warehouse,
            "database": self._database,
            "schema": self._schema,
        }

        if self._private_key_path:
            from cryptography.hazmat.backends import default_backend  # type: ignore[import]
            from cryptography.hazmat.primitives.serialization import (  # type: ignore[import]
                Encoding,
                NoEncryption,
                PrivateFormat,
                load_pem_private_key,
            )

            with open(self._private_key_path, "rb") as key_file:
                private_key = load_pem_private_key(
                    key_file.read(),
                    password=None,
                    backend=default_backend(),
                )
            connect_kwargs["private_key"] = private_key.private_bytes(
                encoding=Encoding.DER,
                format=PrivateFormat.PKCS8,
                encryption_algorithm=NoEncryption(),
            )
        elif self._password:
            connect_kwargs["password"] = self._password

        return snowflake.connector.connect(**connect_kwargs)

    def _run_fetch(self, metric_sql: str, start: datetime, end: datetime) -> pd.DataFrame:
        """Synchronous fetch — run via asyncio.to_thread to avoid blocking the event loop."""
        sql = metric_sql.format(start=start.isoformat(), end=end.isoformat())
        conn = self._connect()
        try:
            cur = conn.cursor()
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
            conn.cursor().execute("SELECT 1")
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
        return await asyncio.to_thread(self._run_fetch, metric_sql, start, end)

    async def test_connection(self) -> bool:
        """Verify Snowflake connectivity without running an expensive query."""
        return await asyncio.to_thread(self._run_test)
