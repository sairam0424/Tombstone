"""
Zero-copy warehouse connector for experiment analysis.
Raw user event data NEVER leaves the customer's warehouse.
Tombstone only receives aggregated statistics (mean, std, sample size).
"""
from __future__ import annotations

import abc
from dataclasses import dataclass
from typing import Any

from app.warehouse.executor import run_warehouse_query


@dataclass
class AggregatedMetric:
    """Aggregated statistics returned from the warehouse — no raw PII."""
    variant: str
    sample_size: int
    mean: float
    std: float
    sum: float


class WarehouseConnector(abc.ABC):
    """Base class for all warehouse connectors."""

    @abc.abstractmethod
    async def query_experiment_metrics(
        self,
        flag_key: str,
        control_value: str,
        treatment_value: str,
        metric_sql: str,
        event_table: str,
        flag_event_table: str,
    ) -> dict[str, AggregatedMetric]:
        """
        Runs a metric aggregation query and returns per-variant statistics.
        Never returns individual user rows — only aggregates.
        """

    async def test_connection(self) -> bool:
        try:
            await self.query_experiment_metrics("_test", "false", "true", "1", "dual", "dual")
            return True
        except Exception:
            return False


class PostgresConnector(WarehouseConnector):
    """
    Connector for PostgreSQL-compatible warehouses.
    Supports: PostgreSQL, Redshift, CockroachDB.
    """

    def __init__(self, dsn: str):
        self._dsn = dsn
        self._pool: Any = None

    async def _get_pool(self) -> Any:
        if self._pool is None:
            import asyncpg  # type: ignore[import]
            self._pool = await asyncpg.create_pool(self._dsn, min_size=1, max_size=2, max_inactive_connection_lifetime=30.0)
        return self._pool

    async def query_experiment_metrics(
        self,
        flag_key: str,
        control_value: str,
        treatment_value: str,
        metric_sql: str,
        event_table: str,
        flag_event_table: str,
    ) -> dict[str, AggregatedMetric]:
        pool = await self._get_pool()

        # Zero-copy pattern: aggregate in-warehouse, return only statistics
        query = f"""
            WITH flag_assignments AS (
                SELECT user_id,
                       CASE WHEN flag_value = $1 THEN 'control'
                            WHEN flag_value = $2 THEN 'treatment'
                            ELSE NULL END AS variant
                FROM {flag_event_table}
                WHERE flag_key = $3
            ),
            user_metrics AS (
                SELECT fa.user_id, fa.variant, ({metric_sql}) AS metric_value
                FROM {event_table} e
                JOIN flag_assignments fa ON e.user_id = fa.user_id
                WHERE fa.variant IS NOT NULL
            )
            SELECT
                variant,
                COUNT(*) AS sample_size,
                AVG(metric_value) AS mean,
                STDDEV(metric_value) AS std,
                SUM(metric_value) AS sum
            FROM user_metrics
            GROUP BY variant
        """

        rows = await pool.fetch(query, control_value, treatment_value, flag_key)
        result: dict[str, AggregatedMetric] = {}
        for row in rows:
            result[row["variant"]] = AggregatedMetric(
                variant=row["variant"],
                sample_size=int(row["sample_size"]),
                mean=float(row["mean"] or 0),
                std=float(row["std"] or 0),
                sum=float(row["sum"] or 0),
            )
        return result


class SnowflakeConnector(WarehouseConnector):
    """
    Connector for Snowflake.
    Uses the synchronous snowflake-connector-python driver wrapped in
    run_warehouse_query (dedicated bounded thread pool + timeout) to avoid
    blocking the event loop. Maintains the same zero-copy CTE pattern.

    Install: pip install 'tombstone-intelligence[snowflake]'
    """

    def __init__(
        self,
        account: str,
        user: str,
        warehouse: str,
        database: str,
        schema: str,
        private_key_path: str = "",
    ):
        self._account = account
        self._user = user
        self._warehouse = warehouse
        self._database = database
        self._schema = schema
        self._private_key_path = private_key_path

    def _connect(self) -> Any:
        import snowflake.connector  # type: ignore[import]

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

        return snowflake.connector.connect(**connect_kwargs)

    def _run_query(
        self,
        flag_key: str,
        control_value: str,
        treatment_value: str,
        metric_sql: str,
        event_table: str,
        flag_event_table: str,
    ) -> dict[str, AggregatedMetric]:
        # Zero-copy pattern: aggregate in-warehouse, return only statistics
        # Snowflake uses %s positional params with cursor.execute
        query = f"""
            WITH flag_assignments AS (
                SELECT user_id,
                       CASE WHEN flag_value = %s THEN 'control'
                            WHEN flag_value = %s THEN 'treatment'
                            ELSE NULL END AS variant
                FROM {flag_event_table}
                WHERE flag_key = %s
            ),
            user_metrics AS (
                SELECT fa.user_id, fa.variant, ({metric_sql}) AS metric_value
                FROM {event_table} e
                JOIN flag_assignments fa ON e.user_id = fa.user_id
                WHERE fa.variant IS NOT NULL
            )
            SELECT
                variant,
                COUNT(*) AS sample_size,
                AVG(metric_value) AS mean,
                STDDEV(metric_value) AS std,
                SUM(metric_value) AS sum
            FROM user_metrics
            GROUP BY variant
        """

        conn = self._connect()
        try:
            cur = conn.cursor()
            cur.execute(query, (control_value, treatment_value, flag_key))
            rows = cur.fetchall()
            col_names = [desc[0].lower() for desc in cur.description]
        finally:
            conn.close()

        result: dict[str, AggregatedMetric] = {}
        for row in rows:
            row_dict = dict(zip(col_names, row))
            variant = row_dict["variant"]
            result[variant] = AggregatedMetric(
                variant=variant,
                sample_size=int(row_dict["sample_size"]),
                mean=float(row_dict["mean"] or 0),
                std=float(row_dict["std"] or 0),
                sum=float(row_dict["sum"] or 0),
            )
        return result

    async def query_experiment_metrics(
        self,
        flag_key: str,
        control_value: str,
        treatment_value: str,
        metric_sql: str,
        event_table: str,
        flag_event_table: str,
    ) -> dict[str, AggregatedMetric]:
        return await run_warehouse_query(
            self._run_query,
            flag_key,
            control_value,
            treatment_value,
            metric_sql,
            event_table,
            flag_event_table,
        )


class BigQueryConnector(WarehouseConnector):
    """
    Connector for Google BigQuery.
    Uses the synchronous google-cloud-bigquery client wrapped in
    run_warehouse_query (dedicated bounded thread pool + timeout).
    BigQuery uses @param named parameter syntax for queries.

    Install: pip install 'tombstone-intelligence[bigquery]'
    """

    def __init__(
        self,
        project_id: str,
        dataset_id: str,
        credentials_json: str = "",
    ):
        self._project_id = project_id
        self._dataset_id = dataset_id
        self._credentials_json = credentials_json

    def _get_client(self) -> Any:
        from google.cloud import bigquery  # type: ignore[import]

        if self._credentials_json:
            import json

            from google.oauth2.service_account import Credentials  # type: ignore[import]

            info = json.loads(self._credentials_json)
            creds = Credentials.from_service_account_info(info)
            return bigquery.Client(project=self._project_id, credentials=creds)

        # Fall back to application default credentials (ADC)
        return bigquery.Client(project=self._project_id)

    def _run_query(
        self,
        flag_key: str,
        control_value: str,
        treatment_value: str,
        metric_sql: str,
        event_table: str,
        flag_event_table: str,
    ) -> dict[str, AggregatedMetric]:
        from google.cloud import bigquery  # type: ignore[import]

        # Qualify table references with dataset when not already qualified
        def _qualify(table: str) -> str:
            return table if "." in table else f"{self._project_id}.{self._dataset_id}.{table}"

        qualified_event = _qualify(event_table)
        qualified_flag = _qualify(flag_event_table)

        # Zero-copy pattern: aggregate in-warehouse, return only statistics
        # BigQuery uses @param named parameter syntax
        query = f"""
            WITH flag_assignments AS (
                SELECT user_id,
                       CASE WHEN flag_value = @control_value THEN 'control'
                            WHEN flag_value = @treatment_value THEN 'treatment'
                            ELSE NULL END AS variant
                FROM `{qualified_flag}`
                WHERE flag_key = @flag_key
            ),
            user_metrics AS (
                SELECT fa.user_id, fa.variant, ({metric_sql}) AS metric_value
                FROM `{qualified_event}` e
                JOIN flag_assignments fa ON e.user_id = fa.user_id
                WHERE fa.variant IS NOT NULL
            )
            SELECT
                variant,
                COUNT(*) AS sample_size,
                AVG(metric_value) AS mean,
                STDDEV(metric_value) AS std,
                SUM(metric_value) AS sum
            FROM user_metrics
            GROUP BY variant
        """

        job_config = bigquery.QueryJobConfig(
            query_parameters=[
                bigquery.ScalarQueryParameter("control_value", "STRING", control_value),
                bigquery.ScalarQueryParameter("treatment_value", "STRING", treatment_value),
                bigquery.ScalarQueryParameter("flag_key", "STRING", flag_key),
            ]
        )

        client = self._get_client()
        job = client.query(query, job_config=job_config)
        rows = job.result()  # blocks until complete

        result: dict[str, AggregatedMetric] = {}
        for row in rows:
            variant = row["variant"]
            result[variant] = AggregatedMetric(
                variant=variant,
                sample_size=int(row["sample_size"]),
                mean=float(row["mean"] or 0),
                std=float(row["std"] or 0),
                sum=float(row["sum"] or 0),
            )
        return result

    async def query_experiment_metrics(
        self,
        flag_key: str,
        control_value: str,
        treatment_value: str,
        metric_sql: str,
        event_table: str,
        flag_event_table: str,
    ) -> dict[str, AggregatedMetric]:
        return await run_warehouse_query(
            self._run_query,
            flag_key,
            control_value,
            treatment_value,
            metric_sql,
            event_table,
            flag_event_table,
        )


class RedshiftConnector(PostgresConnector):
    """
    Connector for Amazon Redshift.
    Inherits PostgresConnector because Redshift is PostgreSQL-wire-compatible.
    Constructs the asyncpg DSN from explicit host/port/db/user/password params
    rather than accepting a raw DSN string, since Redshift credentials are
    typically provided as separate environment variables.
    """

    def __init__(
        self,
        host: str,
        port: int,
        database: str,
        user: str,
        password: str,
    ):
        dsn = f"postgresql://{user}:{password}@{host}:{port}/{database}"
        super().__init__(dsn=dsn)


def get_connector(warehouse_type: str, connection_string: str, **kwargs: Any) -> WarehouseConnector:
    """
    Factory: returns the right connector for the warehouse type.

    Supported warehouse_type values:
        "postgresql" / "postgres"  -> PostgresConnector(connection_string)
        "redshift"                 -> PostgresConnector(connection_string)
                                      (pass a postgresql:// DSN, or use
                                       RedshiftConnector directly for named args)
        "snowflake"                -> SnowflakeConnector(**kwargs)
                                      Required kwargs: account, user, warehouse,
                                      database, schema
                                      Optional kwargs: private_key_path
        "bigquery"                 -> BigQueryConnector(**kwargs)
                                      Required kwargs: project_id, dataset_id
                                      Optional kwargs: credentials_json
    """
    if warehouse_type in ("postgresql", "postgres"):
        return PostgresConnector(connection_string)

    if warehouse_type == "redshift":
        # Accept either a pre-built postgresql:// DSN in connection_string,
        # or named kwargs (host, port, database, user, password).
        if connection_string:
            return PostgresConnector(connection_string)
        return RedshiftConnector(
            host=kwargs["host"],
            port=int(kwargs["port"]),
            database=kwargs["database"],
            user=kwargs["user"],
            password=kwargs["password"],
        )

    if warehouse_type == "snowflake":
        return SnowflakeConnector(
            account=kwargs["account"],
            user=kwargs["user"],
            warehouse=kwargs["warehouse"],
            database=kwargs["database"],
            schema=kwargs["schema"],
            private_key_path=kwargs.get("private_key_path", ""),
        )

    if warehouse_type == "bigquery":
        return BigQueryConnector(
            project_id=kwargs["project_id"],
            dataset_id=kwargs["dataset_id"],
            credentials_json=kwargs.get("credentials_json", ""),
        )

    raise ValueError(
        f"Unsupported warehouse type: {warehouse_type!r}. "
        "Supported: postgresql, postgres, redshift, snowflake, bigquery"
    )
