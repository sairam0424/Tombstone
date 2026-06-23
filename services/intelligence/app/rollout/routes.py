from __future__ import annotations

import datetime
from typing import Annotated, Literal

from fastapi import APIRouter, Depends, HTTPException, Request
from pydantic import BaseModel, Field

from app.rollout.linucb import LinUCBBandit, _MIN_LINUCB_OBS
from app.rollout.thompson import (
    ROLLOUT_SCHEDULE,
    FlagPosterior,
    RolloutRecommendation,
    ThompsonSamplingEngine,
)


router = APIRouter(prefix="/api/v1/rollout", tags=["rollout"])

_LINUCB_ALPHA = 1.0
_LINUCB_D = 5

# Module-level bandit instance — replaced by the one on app.state at startup
# (routes use app.state.linucb_bandit injected during lifespan)



# ------------------------------------------------------------------
# Request / response models
# ------------------------------------------------------------------


class EnableAutonomousRequest(BaseModel):
    flag_key: str = Field(..., description="Feature flag identifier")
    environment: str = Field(default="production", description="Target environment")
    initial_rollout_pct: int = Field(
        default=0, ge=0, le=100, description="Current rollout percentage"
    )


class DisableAutonomousRequest(BaseModel):
    flag_key: str
    environment: str = "production"


class TelemetryUpdateRequest(BaseModel):
    flag_key: str
    environment: str = "production"
    successes: int = Field(..., ge=0, description="Number of successful evaluations")
    failures: int = Field(..., ge=0, description="Number of failed / error evaluations")
    current_rollout_pct: int = Field(..., ge=0, le=100)


class PosteriorResponse(BaseModel):
    flag_key: str
    environment: str
    current_rollout_pct: int
    alpha: float
    beta: float
    total_observations: int
    autonomous_enabled: bool
    mean_success_rate: float


class RecommendationResponse(BaseModel):
    flag_key: str
    environment: str
    current_pct: int
    recommended_pct: int
    confidence: float
    sampled_success_rate: float
    should_advance: bool
    reason: str


class ContextualRecommendRequest(BaseModel):
    environment: str = Field(default="production", description="Target environment")
    rollout_pct: float = Field(
        ..., ge=0.0, le=100.0, description="Current rollout percentage (0-100)"
    )
    error_rate: float = Field(
        ..., ge=0.0, le=1.0, description="Current error rate fraction (0.0-1.0)"
    )
    request_count: int = Field(
        ..., ge=0, description="Request count in the observation window"
    )


class ContextualRecommendResponse(BaseModel):
    flag_key: str
    environment: str
    arm: int = Field(description="0=control/hold, 1=treatment/advance")
    ucb_score: float
    recommendation: Literal["advance", "hold", "rollback"]
    suggested_pct: int
    n_observations: int
    engine_used: Literal["linucb", "thompson"]
    reason: str


# ------------------------------------------------------------------
# Dependency: retrieve the shared ThompsonSamplingEngine from app.state
# ------------------------------------------------------------------


def _get_engine(request: Request) -> ThompsonSamplingEngine:
    engine: ThompsonSamplingEngine | None = getattr(request.app.state, "rollout_engine", None)
    if engine is None:
        raise HTTPException(
            status_code=503,
            detail="Thompson Sampling engine not initialised — check lifespan startup",
        )
    return engine


EngineDep = Annotated[ThompsonSamplingEngine, Depends(_get_engine)]


def _get_linucb(request: Request) -> LinUCBBandit:
    bandit: LinUCBBandit | None = getattr(request.app.state, "linucb_bandit", None)
    if bandit is None:
        raise HTTPException(
            status_code=503,
            detail="LinUCB bandit not initialised — check lifespan startup",
        )
    return bandit


LinUCBDep = Annotated[LinUCBBandit, Depends(_get_linucb)]


# ------------------------------------------------------------------
# Helper converters
# ------------------------------------------------------------------


def _posterior_to_response(p: FlagPosterior) -> PosteriorResponse:
    mean = p.alpha / (p.alpha + p.beta)
    return PosteriorResponse(
        flag_key=p.flag_key,
        environment=p.environment,
        current_rollout_pct=p.current_rollout_pct,
        alpha=p.alpha,
        beta=p.beta,
        total_observations=p.total_observations,
        autonomous_enabled=p.autonomous_enabled,
        mean_success_rate=round(mean, 6),
    )


def _recommendation_to_response(r: RolloutRecommendation) -> RecommendationResponse:
    return RecommendationResponse(
        flag_key=r.flag_key,
        environment=r.environment,
        current_pct=r.current_pct,
        recommended_pct=r.recommended_pct,
        confidence=round(r.confidence, 6),
        sampled_success_rate=round(r.sampled_success_rate, 6),
        should_advance=r.should_advance,
        reason=r.reason,
    )


# ------------------------------------------------------------------
# Routes
# ------------------------------------------------------------------


@router.post("/enable", response_model=PosteriorResponse, status_code=200)
async def enable_autonomous_rollout(
    body: EnableAutonomousRequest,
    engine: EngineDep,
) -> PosteriorResponse:
    """Opt a flag-environment pair into autonomous rollout mode."""
    # Ensure posterior exists with the supplied current percentage before enabling
    await engine.update(
        flag_key=body.flag_key,
        environment=body.environment,
        successes=0,
        failures=0,
        current_rollout_pct=body.initial_rollout_pct,
    )
    posterior = engine.enable_autonomous(body.flag_key, body.environment)
    return _posterior_to_response(posterior)


@router.post("/disable", response_model=PosteriorResponse, status_code=200)
async def disable_autonomous_rollout(
    body: DisableAutonomousRequest,
    engine: EngineDep,
) -> PosteriorResponse:
    """Remove a flag-environment pair from autonomous rollout mode."""
    posterior = engine.disable_autonomous(body.flag_key, body.environment)
    if posterior is None:
        raise HTTPException(
            status_code=404,
            detail=f"No posterior found for {body.flag_key}:{body.environment}",
        )
    return _posterior_to_response(posterior)


@router.post("/update", response_model=RecommendationResponse, status_code=200)
async def feed_telemetry(
    body: TelemetryUpdateRequest,
    engine: EngineDep,
) -> RecommendationResponse:
    """Feed a telemetry window into the Beta posterior and return the current recommendation."""
    await engine.update(
        flag_key=body.flag_key,
        environment=body.environment,
        successes=body.successes,
        failures=body.failures,
        current_rollout_pct=body.current_rollout_pct,
    )
    recommendation = engine.recommend(body.flag_key, body.environment)
    return _recommendation_to_response(recommendation)


@router.get("/recommendations", response_model=list[RecommendationResponse], status_code=200)
async def get_all_recommendations(engine: EngineDep) -> list[RecommendationResponse]:
    """Return recommendations for all flags opted into autonomous rollout."""
    return [_recommendation_to_response(r) for r in engine.all_recommendations()]


@router.get(
    "/posterior/{flag_key}",
    response_model=PosteriorResponse,
    status_code=200,
)
async def get_posterior(
    flag_key: str,
    engine: EngineDep,
    environment: str = "production",
) -> PosteriorResponse:
    """Return the raw Beta posterior state for a specific flag-environment pair."""
    posterior = engine.get_posterior(flag_key, environment)
    if posterior is None:
        raise HTTPException(
            status_code=404,
            detail=f"No posterior found for {flag_key}:{environment}",
        )
    return _posterior_to_response(posterior)


@router.post(
    "/{flag_key}/contextual-recommend",
    response_model=ContextualRecommendResponse,
    status_code=200,
)
async def contextual_recommend(
    flag_key: str,
    body: ContextualRecommendRequest,
    engine: EngineDep,
    bandit: LinUCBDep,
) -> ContextualRecommendResponse:
    """Context-aware rollout recommendation using LinUCB contextual bandit.

    Falls back to Thompson Sampling when fewer than 50 observations are
    available for this flag-environment pair.

    Body:
        environment      — target environment (default: production)
        rollout_pct      — current rollout percentage 0-100
        error_rate       — current error rate fraction 0.0-1.0
        request_count    — request count in the observation window

    Returns:
        arm              — 0=hold/control, 1=advance/treatment
        ucb_score        — raw UCB score (higher = more confident in arm choice)
        recommendation   — "advance" | "hold" | "rollback"
        suggested_pct    — suggested next rollout percentage
        n_observations   — total observations recorded for this flag
        engine_used      — "linucb" or "thompson" (fallback)
        reason           — human-readable explanation
    """
    env = body.environment
    n_obs = bandit.n_observations(flag_key, env)

    # ------------------------------------------------------------------
    # Fallback path: insufficient LinUCB observations — use Thompson
    # ------------------------------------------------------------------
    if n_obs < _MIN_LINUCB_OBS:
        rec = engine.recommend(flag_key, env)
        current_pct = int(body.rollout_pct)

        if rec.should_advance:
            recommendation: Literal["advance", "hold", "rollback"] = "advance"
            suggested_pct = rec.recommended_pct
        else:
            recommendation = "hold"
            suggested_pct = current_pct

        return ContextualRecommendResponse(
            flag_key=flag_key,
            environment=env,
            arm=1 if rec.should_advance else 0,
            ucb_score=round(rec.confidence, 6),
            recommendation=recommendation,
            suggested_pct=suggested_pct,
            n_observations=n_obs,
            engine_used="thompson",
            reason=(
                f"LinUCB needs {_MIN_LINUCB_OBS} observations ({n_obs} so far); "
                f"delegating to Thompson Sampling. {rec.reason}"
            ),
        )

    # ------------------------------------------------------------------
    # LinUCB path: use context-aware arm selection
    # ------------------------------------------------------------------
    now = datetime.datetime.now(tz=datetime.timezone.utc)
    context = bandit._context_vector(  # noqa: SLF001
        rollout_pct=body.rollout_pct,
        error_rate=body.error_rate,
        request_count=body.request_count,
        hour=now.hour,
        day=now.weekday(),
    )

    arm, ucb_score = bandit.select_arm(flag_key, env, context)
    current_pct = int(body.rollout_pct)

    # Determine recommendation from arm + error rate heuristic
    _ROLLBACK_ERROR_THRESHOLD = 0.10
    if body.error_rate >= _ROLLBACK_ERROR_THRESHOLD:
        recommendation = "rollback"
        # Find the previous schedule step
        prev_pct = 0
        for pct in ROLLOUT_SCHEDULE:
            if pct < current_pct:
                prev_pct = pct
        suggested_pct = prev_pct
        reason = (
            f"LinUCB arm={arm} (ucb={ucb_score:.4f}) overridden: "
            f"error_rate={body.error_rate:.3f} >= {_ROLLBACK_ERROR_THRESHOLD} rollback threshold; "
            f"suggesting rollback from {current_pct}% to {suggested_pct}%"
        )
    elif arm == 1:
        recommendation = "advance"
        # Find the next schedule step above current_pct
        next_pct = current_pct
        for pct in ROLLOUT_SCHEDULE:
            if pct > current_pct:
                next_pct = pct
                break
        suggested_pct = next_pct
        reason = (
            f"LinUCB selected treatment arm (ucb={ucb_score:.4f}); "
            f"advancing rollout from {current_pct}% to {suggested_pct}%"
        )
    else:
        recommendation = "hold"
        suggested_pct = current_pct
        reason = (
            f"LinUCB selected control arm (ucb={ucb_score:.4f}); "
            f"holding rollout at {current_pct}%"
        )

    return ContextualRecommendResponse(
        flag_key=flag_key,
        environment=env,
        arm=arm,
        ucb_score=round(ucb_score, 6),
        recommendation=recommendation,
        suggested_pct=suggested_pct,
        n_observations=n_obs,
        engine_used="linucb",
        reason=reason,
    )
