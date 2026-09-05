import os

import httpx
import numpy as np
from fastapi import APIRouter, HTTPException
from pydantic import BaseModel, field_validator

from app.experiments.analyzer import ExperimentAnalyzer
from app.experiments.collision import ExperimentSpec, detect_collisions
from app.experiments.cuped import cuped_effect_size
from app.experiments.models import ExperimentDefinition
from app.experiments.srm import srm_check
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
    metric_sql: str  # e.g. "CASE WHEN converted THEN 1 ELSE 0 END"
    event_table: str  # warehouse table with user events
    flag_event_table: str  # warehouse table recording flag evaluations
    # Valid values: postgresql, postgres, redshift, snowflake, bigquery
    warehouse_type: str = "postgresql"
    warehouse_dsn: str  # connection string for customer's warehouse
    # Valid values: bayesian, frequentist, sequential -- NOT "cuped": CUPED
    # needs real per-user outcome/covariate pairs, which this warehouse-
    # aggregate endpoint cannot provide; see POST /cuped-adjust instead
    # (EXP-1 PR 3/3).
    stat_method: str = "bayesian"
    min_sample_size: int = 100
    min_detectable_effect: float = 0.05
    # EXP-2: the intended fraction of traffic allocated to control, used
    # ONLY by the SRM (Sample Ratio Mismatch) gate below. Defaults to an
    # even 50/50 split, but Tombstone's own rollout_pct-driven canaries are
    # routinely non-50/50 (e.g. 0.9 for a 90/10 control-heavy rollout) --
    # callers running a real canary-style experiment MUST set this, or the
    # gate will compare their real, healthy split against the wrong
    # expectation and permanently return recommendation="BLOCKED_SRM"
    # (found by adversarial review of PR #216).
    expected_control_ratio: float = 0.5

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
    # Valid values: "SHIP" | "NO_SHIP" | "CONTINUE" | "BLOCKED_SRM" (EXP-2 --
    # see app/experiments/srm.py; a mismatch means the traffic split itself
    # is broken, so relative_lift/is_significant/p_value above still reflect
    # whatever the stat_method computed, but should not be trusted).
    recommendation: str
    relative_lift: float
    is_significant: bool
    sample_sizes: dict[str, int]
    probability_beats_control: float | None = None
    p_value: float | None = None
    srm_p_value: float | None = None
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
    sig_label = (
        "statistically significant"
        if is_significant
        else "not statistically significant"
    )
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
    if req.stat_method == "cuped":
        # EXP-1 PR 3/3 formally closed this: CUPED needs real per-user
        # outcome/covariate pairs to compute a covariance, which cannot be
        # reconstructed from warehouse aggregates. A prior version of this
        # branch reconstructed a `[control.mean] * control.sample_size`
        # constant-value array per variant and fed it into CUPED's
        # covariance/adjustment math -- the exact same fabrication bug class
        # EXP-1 PR1/PR2 fixed for the frequentist/Bayesian/mSPRT paths, just
        # laundered through an extra transformation before reaching the
        # t-test. Rather than silently computing a fabricated result,
        # reject explicitly and point callers at the endpoint that actually
        # works correctly with real data. Checked BEFORE the warehouse query
        # below (not after) -- this is fully derivable from the request body
        # alone, so there is no reason to spend a real round-trip against
        # the customer's own warehouse_dsn (up to WAREHOUSE_QUERY_TIMEOUT_S)
        # only to reject the request anyway (found by adversarial review).
        raise HTTPException(
            status_code=400,
            detail=(
                "stat_method='cuped' is not supported via /analyze: this "
                "endpoint only resolves warehouse-aggregated statistics "
                "(mean/variance per variant), not individual per-user "
                "observations, which CUPED's covariance calculation "
                "requires. Use POST /api/v1/experiments/cuped-adjust with "
                "raw per-user treatment/control/pre_treatment/pre_control "
                "arrays instead."
            ),
        )

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
        # asyncio.TimeoutError (raised by run_warehouse_query after
        # WAREHOUSE_QUERY_TIMEOUT_S) is a subclass of Exception, so it's
        # already covered here — a 502 can now also mean "warehouse query
        # exceeded 30s" rather than a driver-level failure.
        raise HTTPException(
            status_code=502, detail=f"Warehouse query failed: {e}"
        ) from e

    control = metrics.get("control")
    treatment = metrics.get("treatment")
    if not control or not treatment:
        raise HTTPException(
            status_code=422,
            detail="Insufficient data: one or both variants returned no rows",
        )

    sample_sizes = {
        "control": control.sample_size,
        "treatment": treatment.sample_size,
    }

    experiment = ExperimentDefinition(
        id=req.experiment_id,
        flag_key=req.flag_key,
        control_variant=req.control_variant,
        treatment_variant=req.treatment_variant,
        stat_method=req.stat_method,
        min_sample_size=req.min_sample_size,
    )

    if req.stat_method == "sequential":
        result = analyzer.analyze_sequential_from_stats(
            control=control,
            treatment=treatment,
            metric_name=req.metric_name,
        )
    else:
        result = analyzer.analyze_from_stats(
            experiment=experiment,
            control=control,
            treatment=treatment,
            metric_name=req.metric_name,
        )

    # EXP-2: SRM (Sample Ratio Mismatch) is checked exactly ONCE here, then
    # the same already-computed SRMResult is passed into recommend() below
    # -- not two independent srm_check() calls with the same arguments,
    # which could silently drift apart if a future change updated one call
    # site's expected_control_ratio without updating the other (found by
    # adversarial review of PR #216). recommend() checks it FIRST, ahead of
    # every metric result, since a broken traffic split invalidates any
    # statistical result computed on top of it -- see app/experiments/
    # srm.py's module docstring for why. The stat_method analysis above
    # still runs even when SRM ends up mismatched (it is a cheap,
    # already-fetched in-process computation, not worth gating); what IS
    # worth skipping is the real network round-trip below.
    srm = srm_check(
        control.sample_size, treatment.sample_size, req.expected_control_ratio
    )
    recommendation = analyzer.recommend([result], srm_result=srm)

    if recommendation == "BLOCKED_SRM":
        # Null out the fields that were computed on top of a split this
        # gate just determined is broken -- an API consumer reading
        # is_significant=False/p_value=None gets an unambiguous signal
        # even without special-casing recommendation=="BLOCKED_SRM" first
        # (found by adversarial review of PR #216: returning the raw,
        # untrustworthy computed values here let a naive client render
        # "treatment up 20%, statistically significant" from data known to
        # be corrupted -- exactly what this gate exists to prevent).
        return RunExperimentResponse(
            experiment_id=req.experiment_id,
            flag_key=req.flag_key,
            recommendation=recommendation,
            relative_lift=0.0,
            is_significant=False,
            sample_sizes=sample_sizes,
            probability_beats_control=None,
            p_value=None,
            srm_p_value=round(srm.p_value, 6),
            ai_explanation="",
            explanation_generated=False,
        )

    ai_key = os.environ.get("ANTHROPIC_API_KEY", "")
    explanation = await generate_ship_explanation(
        flag_key=req.flag_key,
        recommendation=recommendation,
        relative_lift=result.relative_lift,
        is_significant=result.is_significant,
        sample_sizes=sample_sizes,
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
        sample_sizes=sample_sizes,
        probability_beats_control=result.probability_beats_control,
        p_value=result.p_value,
        srm_p_value=round(srm.p_value, 6),
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
# CUPED variance reduction endpoint (raw per-user data path -- EXP-1 PR 3/3
# formally scoped CUPED to ONLY this endpoint; /analyze's stat_method="cuped"
# is rejected rather than attempting to reconstruct per-user data from
# warehouse aggregates, which cannot be done correctly)
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
        raise HTTPException(
            status_code=422, detail="Each variant requires at least 2 observations."
        )

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
