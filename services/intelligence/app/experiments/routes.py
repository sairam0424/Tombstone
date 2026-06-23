import os

import httpx
import numpy as np
from fastapi import APIRouter, HTTPException
from pydantic import BaseModel, field_validator

from app.experiments.analyzer import ExperimentAnalyzer
from app.experiments.collision import ExperimentSpec, detect_collisions
from app.experiments.cuped import cuped_effect_size
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
# Collision detection
# ---------------------------------------------------------------------------


class CollisionExperimentSpec(BaseModel):
    flag_key: str
    environment: str
    rollout_pct: float
    targeting_rules: list = []


class CheckCollisionRequest(BaseModel):
    new_experiment: CollisionExperimentSpec
    active_experiments: list[CollisionExperimentSpec]
    threshold: float = 0.7


class CollisionResult(BaseModel):
    flag_key: str
    overlap_score: float
    blocked: bool
    warning: bool


class CheckCollisionResponse(BaseModel):
    collisions: list[CollisionResult]
    has_blocked: bool
    has_warnings: bool
    safe_to_launch: bool


@router.post("/check-collision", response_model=CheckCollisionResponse)
async def check_collision(req: CheckCollisionRequest):
    """
    Detect population overlap between a proposed experiment and active experiments.

    Returns collision details with severity:
      - blocked (overlap >= 0.9): launch should be rejected automatically.
      - warning (0.7 <= overlap < 0.9): human review required.
      - safe_to_launch: True only when no blocked collisions exist.
    """
    new_spec = ExperimentSpec(
        flag_key=req.new_experiment.flag_key,
        environment=req.new_experiment.environment,
        rollout_pct=req.new_experiment.rollout_pct,
        targeting_rules=req.new_experiment.targeting_rules,
    )
    active_specs = [
        ExperimentSpec(
            flag_key=e.flag_key,
            environment=e.environment,
            rollout_pct=e.rollout_pct,
            targeting_rules=e.targeting_rules,
        )
        for e in req.active_experiments
    ]

    raw = detect_collisions(new_spec, active_specs, threshold=req.threshold)
    collisions = [CollisionResult(**c) for c in raw]
    has_blocked = any(c.blocked for c in collisions)
    has_warnings = any(c.warning for c in collisions)

    return CheckCollisionResponse(
        collisions=collisions,
        has_blocked=has_blocked,
        has_warnings=has_warnings,
        safe_to_launch=not has_blocked,
    )


# ---------------------------------------------------------------------------
# CUPED variance reduction endpoint (warehouse data path)
# ---------------------------------------------------------------------------


class CupedAdjustRequest(BaseModel):
    treatment: list[float]
    control: list[float]
    pre_treatment: list[float]
    pre_control: list[float]


class CupedAdjustResponse(BaseModel):
    effect_size: float
    p_value: float
    variance_reduction_pct: float
    adjusted_treatment_mean: float
    adjusted_control_mean: float
    ci_lower: float
    ci_upper: float
    t_statistic: float
    degrees_of_freedom: float
    is_significant: bool


@router.post("/cuped-adjust", response_model=CupedAdjustResponse)
async def cuped_adjust(req: CupedAdjustRequest):
    """
    Apply CUPED variance reduction to experiment data using pre-experiment covariates.

    Sends raw per-user metric values (treatment + control) alongside their
    pre-experiment covariate values (e.g. prior-week activity).  Returns
    CUPED-adjusted effect size, p-value, and confidence interval.

    Typical variance reduction: 20-40%, allowing the same statistical power
    with fewer observations.
    """
    if len(req.treatment) < 2 or len(req.control) < 2:
        raise HTTPException(status_code=422, detail="Each variant requires at least 2 observations.")

    if len(req.treatment) != len(req.pre_treatment):
        raise HTTPException(
            status_code=422,
            detail="treatment and pre_treatment must have equal length.",
        )
    if len(req.control) != len(req.pre_control):
        raise HTTPException(
            status_code=422,
            detail="control and pre_control must have equal length.",
        )

    result = cuped_effect_size(
        treatment=np.array(req.treatment),
        control=np.array(req.control),
        pre_treatment=np.array(req.pre_treatment),
        pre_control=np.array(req.pre_control),
    )

    return CupedAdjustResponse(
        effect_size=result["effect_size"],
        p_value=result["p_value"],
        variance_reduction_pct=result["variance_reduction_pct"],
        adjusted_treatment_mean=result["adjusted_treatment_mean"],
        adjusted_control_mean=result["adjusted_control_mean"],
        ci_lower=result["ci_lower"],
        ci_upper=result["ci_upper"],
        t_statistic=result["t_statistic"],
        degrees_of_freedom=result["degrees_of_freedom"],
        is_significant=result["p_value"] < 0.05,
    )
