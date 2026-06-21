from __future__ import annotations

from typing import Annotated

from fastapi import APIRouter, Depends, HTTPException, Request
from pydantic import BaseModel, Field

from app.rollout.thompson import (
    FlagPosterior,
    RolloutRecommendation,
    ThompsonSamplingEngine,
)


router = APIRouter(prefix="/api/v1/rollout", tags=["rollout"])


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
    engine.update(
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
    engine.update(
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
