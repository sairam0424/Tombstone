import os
import re
from datetime import datetime

import httpx
from fastapi import APIRouter, HTTPException
from pydantic import BaseModel, field_validator

from app.experiments.analyzer import ExperimentAnalyzer
from app.experiments.models import ExperimentDefinition
from app.warehouse.connector import get_connector

router = APIRouter(prefix="/api/v1/experiments", tags=["experiments"])
analyzer = ExperimentAnalyzer()

# Valid warehouse backend identifiers accepted by get_connector()
VALID_WAREHOUSE_TYPES = ("postgresql", "postgres", "redshift", "snowflake", "bigquery")


class RunExperimentRequest(BaseModel):
    experiment_id: str
    flag_key: str
    control_variant: str = "false"
    treatment_variant: str = "true"
    metric_name: str
    metric_sql: str           # e.g. "CASE WHEN converted THEN 1 ELSE 0 END"
    event_table: str          # warehouse table with user events
    flag_event_table: str     # warehouse table recording flag evaluations
    # Valid values: postgresql, postgres, redshift, snowflake, bigquery
    warehouse_type: str = "postgresql"
    warehouse_dsn: str        # connection string for customer's warehouse
    stat_method: str = "bayesian"
    min_sample_size: int = 100
    control_covariate: list[float] = []
    treatment_covariate: list[float] = []
    min_detectable_effect: float = 0.05

    @field_validator("warehouse_type")
    @classmethod
    def validate_warehouse_type(cls, v: str) -> str:
        if v not in VALID_WAREHOUSE_TYPES:
            raise ValueError(
                f"Invalid warehouse_type {v!r}. "
                f"Must be one of: {', '.join(VALID_WAREHOUSE_TYPES)}"
            )
        return v


class RunExperimentResponse(BaseModel):
    experiment_id: str
    flag_key: str
    recommendation: str
    relative_lift: float
    is_significant: bool
    sample_sizes: dict[str, int]
    probability_beats_control: float | None = None
    p_value: float | None = None
    ai_explanation: str = ""
    explanation_generated: bool = False


async def generate_ship_explanation(
    flag_key: str,
    recommendation: str,
    relative_lift: float,
    is_significant: bool,
    sample_sizes: dict[str, int],
    metric_name: str,
    probability_beats_control: float | None,
    anthropic_api_key: str,
) -> str:
    """
    Generate a plain-language ship/no-ship explanation via Claude.
    Returns empty string if no API key or on any error.
    """
    if not anthropic_api_key:
        return ""

    lift_pct = relative_lift * 100
    sig_label = "statistically significant" if is_significant else "not statistically significant"
    prob_line = (
        f"The probability that treatment beats control is {probability_beats_control:.1%}."
        if probability_beats_control is not None
        else ""
    )

    prompt = (
        f"An A/B experiment for feature flag '{flag_key}' on metric '{metric_name}' "
        f"produced the following results: relative lift of {lift_pct:.2f}%, result is {sig_label}. "
        f"Sample sizes — control: {sample_sizes.get('control', 0)}, "
        f"treatment: {sample_sizes.get('treatment', 0)}. "
        f"{prob_line} "
        f"The statistical recommendation is: {recommendation}. "
        "Please summarize this experiment result in 2-3 sentences for a product team. "
        "Use plain language, calibrate confidence to the statistical significance, "
        "and end with an actionable recommendation. "
        "No markdown, no bullet points."
    )

    try:
        async with httpx.AsyncClient(timeout=15.0) as client:
            response = await client.post(
                "https://api.anthropic.com/v1/messages",
                headers={
                    "x-api-key": anthropic_api_key,
                    "anthropic-version": "2023-06-01",
                    "content-type": "application/json",
                },
                json={
                    "model": "claude-haiku-4-5-20251001",
                    "max_tokens": 200,
                    "messages": [{"role": "user", "content": prompt}],
                },
            )
            response.raise_for_status()
            data = response.json()
            return data["content"][0]["text"].strip()
    except Exception:
        return ""


@router.post("/analyze", response_model=RunExperimentResponse)
async def analyze_experiment(req: RunExperimentRequest):
    """
    Run warehouse-native experiment analysis.
    Aggregation happens in the customer's warehouse — no raw data sent to Tombstone.
    """
    try:
        connector = get_connector(req.warehouse_type, req.warehouse_dsn)
        metrics = await connector.query_experiment_metrics(
            flag_key=req.flag_key,
            control_value=req.control_variant,
            treatment_value=req.treatment_variant,
            metric_sql=req.metric_sql,
            event_table=req.event_table,
            flag_event_table=req.flag_event_table,
        )
    except Exception as e:
        raise HTTPException(status_code=502, detail=f"Warehouse query failed: {e}") from e

    control = metrics.get("control")
    treatment = metrics.get("treatment")
    if not control or not treatment:
        raise HTTPException(status_code=422, detail="Insufficient data: one or both variants returned no rows")

    experiment = ExperimentDefinition(
        id=req.experiment_id,
        flag_key=req.flag_key,
        control_variant=req.control_variant,
        treatment_variant=req.treatment_variant,
        stat_method=req.stat_method,
        min_sample_size=req.min_sample_size,
    )

    control_data = [control.mean] * control.sample_size
    treatment_data = [treatment.mean] * treatment.sample_size

    if req.stat_method == "cuped" and req.control_covariate and req.treatment_covariate:
        result = analyzer.analyze_cuped(
            experiment=experiment,
            control_data=control_data,
            treatment_data=treatment_data,
            control_covariate=req.control_covariate,
            treatment_covariate=req.treatment_covariate,
            metric_name=req.metric_name,
        )
    elif req.stat_method == "sequential":
        result = analyzer.analyze_sequential(
            control_data=control_data,
            treatment_data=treatment_data,
            metric_name=req.metric_name,
        )
    else:
        result = analyzer.analyze(
            experiment=experiment,
            control_data=control_data,
            treatment_data=treatment_data,
            metric_name=req.metric_name,
        )

    recommendation = analyzer.recommend([result])

    ai_key = os.environ.get("ANTHROPIC_API_KEY", "")
    explanation = await generate_ship_explanation(
        flag_key=req.flag_key,
        recommendation=recommendation,
        relative_lift=result.relative_lift,
        is_significant=result.is_significant,
        sample_sizes={"control": control.sample_size, "treatment": treatment.sample_size},
        metric_name=req.metric_name,
        probability_beats_control=result.probability_beats_control,
        anthropic_api_key=ai_key,
    )

    return RunExperimentResponse(
        experiment_id=req.experiment_id,
        flag_key=req.flag_key,
        recommendation=recommendation,
        relative_lift=result.relative_lift,
        is_significant=result.is_significant,
        sample_sizes={
            "control": control.sample_size,
            "treatment": treatment.sample_size,
        },
        probability_beats_control=result.probability_beats_control,
        p_value=result.p_value,
        ai_explanation=explanation,
        explanation_generated=bool(explanation),
    )


@router.get("/power-calculator")
async def power_calculator(
    baseline_conversion: float = 0.05,
    mde: float = 0.10,
    alpha: float = 0.05,
    power: float = 0.80,
):
    """
    Calculate the required sample size per variant for a given power and MDE.
    """
    n = analyzer.required_sample_size(baseline_conversion, mde, alpha, power)
    return {
        "sample_size_per_variant": n,
        "total_sample_size": n * 2,
        "assumptions": {
            "baseline_conversion": baseline_conversion,
            "mde": mde,
            "alpha": alpha,
            "power": power,
        },
    }


# ---------------------------------------------------------------------------
# Warehouse import endpoint
# ---------------------------------------------------------------------------

# Disallowed DML/DDL keywords (injection guard — case-insensitive, word-boundary)
_FORBIDDEN_SQL_PATTERN = re.compile(
    r"\b(INSERT|UPDATE|DELETE|DROP|TRUNCATE|ALTER|CREATE|REPLACE|MERGE|UPSERT)\b",
    re.IGNORECASE,
)

# Accepted connector identifiers for the warehouse-import endpoint
VALID_IMPORT_CONNECTORS = frozenset({"bigquery", "snowflake", "databricks"})

# Maximum rows returned in the sample preview
_SAMPLE_SIZE = 5


class WarehouseImportRequest(BaseModel):
    connector: str
    metric_sql: str
    start: datetime
    end: datetime

    @field_validator("connector")
    @classmethod
    def validate_connector(cls, v: str) -> str:
        v = v.lower().strip()
        if v not in VALID_IMPORT_CONNECTORS:
            raise ValueError(
                f"Invalid connector {v!r}. Must be one of: "
                + ", ".join(sorted(VALID_IMPORT_CONNECTORS))
            )
        return v

    @field_validator("metric_sql")
    @classmethod
    def validate_metric_sql(cls, v: str) -> str:
        match = _FORBIDDEN_SQL_PATTERN.search(v)
        if match:
            raise ValueError(
                f"metric_sql contains forbidden keyword {match.group(0)!r}. "
                "Only SELECT aggregation queries are permitted."
            )
        return v


class WarehouseImportResponse(BaseModel):
    rows: int
    columns: list[str]
    sample: list[dict]


def _build_connector(connector_name: str):
    """
    Instantiate the appropriate connector from environment variables.
    Credentials are sourced from env — never passed in request bodies.
    """
    if connector_name == "bigquery":
        from app.warehouse.bigquery import BigQueryConnector
        return BigQueryConnector()

    if connector_name == "snowflake":
        from app.warehouse.snowflake import SnowflakeConnector
        return SnowflakeConnector()

    if connector_name == "databricks":
        from app.warehouse.databricks import DatabricksConnector
        return DatabricksConnector()

    raise ValueError(f"Unknown connector: {connector_name!r}")


@router.post("/{experiment_id}/warehouse-import", response_model=WarehouseImportResponse)
async def warehouse_import(experiment_id: str, req: WarehouseImportRequest):
    """
    Pull aggregated experiment metrics from a data warehouse into Tombstone.

    Zero-copy privacy guarantee: metric_sql must be a SELECT that returns only
    aggregated columns (COUNT, AVG, SUM, etc.). DML/DDL keywords are rejected.
    Raw user rows are never transmitted to Tombstone.
    """
    try:
        connector = _build_connector(req.connector)
    except (ImportError, RuntimeError) as exc:
        raise HTTPException(status_code=503, detail=str(exc)) from exc
    except ValueError as exc:
        raise HTTPException(status_code=422, detail=str(exc)) from exc

    try:
        df = await connector.fetch_metric(req.metric_sql, req.start, req.end)
    except Exception as exc:
        raise HTTPException(
            status_code=502,
            detail=f"Warehouse query failed ({req.connector}): {exc}",
        ) from exc

    row_count = len(df)
    columns = list(df.columns)
    sample = df.head(_SAMPLE_SIZE).to_dict(orient="records")

    return WarehouseImportResponse(rows=row_count, columns=columns, sample=sample)
