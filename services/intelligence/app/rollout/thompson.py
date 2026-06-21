from __future__ import annotations

from dataclasses import dataclass

import numpy as np


ROLLOUT_SCHEDULE = [1, 5, 10, 25, 50, 75, 100]

_MIN_OBSERVATIONS: int = 50
_SAMPLE_DRAWS: int = 1000
_ERROR_THRESHOLD: float = 0.05
_CONFIDENCE_THRESHOLD: float = 0.90


@dataclass
class FlagPosterior:
    flag_key: str
    environment: str
    current_rollout_pct: int
    alpha: float = 1.0
    beta: float = 1.0
    total_observations: int = 0
    autonomous_enabled: bool = False


@dataclass
class RolloutRecommendation:
    flag_key: str
    environment: str
    current_pct: int
    recommended_pct: int
    confidence: float
    sampled_success_rate: float
    should_advance: bool
    reason: str


class ThompsonSamplingEngine:
    """Autonomous flag rollout engine using Thompson Sampling over Beta posteriors.

    Each flag-environment pair maintains a Beta(alpha, beta) posterior over the
    true success rate.  After every telemetry window the posterior is updated via
    conjugate Bayesian update (alpha += successes, beta += failures).  A
    recommendation to advance the rollout percentage is issued when:

        P(sampled_success_rate > 1 - error_threshold) >= confidence_threshold

    with at least `_MIN_OBSERVATIONS` total observations recorded.
    """

    def __init__(self) -> None:
        self._posteriors: dict[str, FlagPosterior] = {}

    # ------------------------------------------------------------------
    # Internal helpers
    # ------------------------------------------------------------------

    @staticmethod
    def _posterior_key(flag_key: str, environment: str) -> str:
        return f"{flag_key}:{environment}"

    def _get_or_create(
        self, flag_key: str, environment: str, current_rollout_pct: int = 0
    ) -> FlagPosterior:
        key = self._posterior_key(flag_key, environment)
        if key not in self._posteriors:
            self._posteriors[key] = FlagPosterior(
                flag_key=flag_key,
                environment=environment,
                current_rollout_pct=current_rollout_pct,
            )
        return self._posteriors[key]

    @staticmethod
    def _next_schedule_pct(current_pct: int) -> int | None:
        """Return the next rollout percentage above current_pct, or None if at 100."""
        for pct in ROLLOUT_SCHEDULE:
            if pct > current_pct:
                return pct
        return None

    # ------------------------------------------------------------------
    # Public API
    # ------------------------------------------------------------------

    def update(
        self,
        flag_key: str,
        environment: str,
        successes: int,
        failures: int,
        current_rollout_pct: int,
    ) -> FlagPosterior:
        """Update the Beta posterior for a flag-environment pair.

        Uses conjugate Bayesian update:
            alpha_new = alpha_old + successes
            beta_new  = beta_old  + failures
        """
        if successes < 0 or failures < 0:
            raise ValueError("successes and failures must be non-negative")

        posterior = self._get_or_create(flag_key, environment, current_rollout_pct)

        # Immutable update — create a new FlagPosterior instead of mutating
        updated = FlagPosterior(
            flag_key=posterior.flag_key,
            environment=posterior.environment,
            current_rollout_pct=current_rollout_pct,
            alpha=posterior.alpha + successes,
            beta=posterior.beta + failures,
            total_observations=posterior.total_observations + successes + failures,
            autonomous_enabled=posterior.autonomous_enabled,
        )

        key = self._posterior_key(flag_key, environment)
        self._posteriors[key] = updated
        return updated

    def recommend(self, flag_key: str, environment: str) -> RolloutRecommendation:
        """Produce a rollout recommendation via Thompson Sampling.

        Draws `_SAMPLE_DRAWS` samples from the Beta posterior and computes:
            confidence = fraction of samples where success_rate > 1 - error_threshold

        Returns a recommendation to advance only when:
            1. total_observations >= _MIN_OBSERVATIONS
            2. confidence >= _CONFIDENCE_THRESHOLD
            3. There is a next step in ROLLOUT_SCHEDULE above current_pct
        """
        key = self._posterior_key(flag_key, environment)
        if key not in self._posteriors:
            # Return a conservative default for unknown flags
            return RolloutRecommendation(
                flag_key=flag_key,
                environment=environment,
                current_pct=0,
                recommended_pct=0,
                confidence=0.0,
                sampled_success_rate=0.0,
                should_advance=False,
                reason="No posterior data — flag not yet observed",
            )

        posterior = self._posteriors[key]

        # Thompson Sample
        rng = np.random.default_rng()
        samples = rng.beta(posterior.alpha, posterior.beta, size=_SAMPLE_DRAWS)
        sampled_success_rate: float = float(np.mean(samples))
        confidence: float = float(np.mean(samples > (1.0 - _ERROR_THRESHOLD)))

        current_pct = posterior.current_rollout_pct
        next_pct = self._next_schedule_pct(current_pct)

        # Gate 1: insufficient observations
        if posterior.total_observations < _MIN_OBSERVATIONS:
            return RolloutRecommendation(
                flag_key=flag_key,
                environment=environment,
                current_pct=current_pct,
                recommended_pct=current_pct,
                confidence=confidence,
                sampled_success_rate=sampled_success_rate,
                should_advance=False,
                reason=(
                    f"Insufficient observations: "
                    f"{posterior.total_observations} < {_MIN_OBSERVATIONS} required"
                ),
            )

        # Gate 2: already at 100%
        if next_pct is None:
            return RolloutRecommendation(
                flag_key=flag_key,
                environment=environment,
                current_pct=current_pct,
                recommended_pct=current_pct,
                confidence=confidence,
                sampled_success_rate=sampled_success_rate,
                should_advance=False,
                reason="Rollout already at 100% — no further advancement possible",
            )

        # Gate 3: confidence threshold
        should_advance = confidence >= _CONFIDENCE_THRESHOLD
        if should_advance:
            reason = (
                f"Confidence {confidence:.3f} >= {_CONFIDENCE_THRESHOLD} threshold; "
                f"advancing from {current_pct}% to {next_pct}%"
            )
        else:
            reason = (
                f"Confidence {confidence:.3f} < {_CONFIDENCE_THRESHOLD} threshold; "
                f"holding at {current_pct}%"
            )

        return RolloutRecommendation(
            flag_key=flag_key,
            environment=environment,
            current_pct=current_pct,
            recommended_pct=next_pct if should_advance else current_pct,
            confidence=confidence,
            sampled_success_rate=sampled_success_rate,
            should_advance=should_advance,
            reason=reason,
        )

    def all_recommendations(self) -> list[RolloutRecommendation]:
        """Return recommendations for all flags that have autonomous rollout enabled."""
        return [
            self.recommend(p.flag_key, p.environment)
            for p in self._posteriors.values()
            if p.autonomous_enabled
        ]

    def enable_autonomous(self, flag_key: str, environment: str) -> FlagPosterior:
        """Opt a flag-environment pair into autonomous rollout mode."""
        posterior = self._get_or_create(flag_key, environment)
        key = self._posterior_key(flag_key, environment)
        updated = FlagPosterior(
            flag_key=posterior.flag_key,
            environment=posterior.environment,
            current_rollout_pct=posterior.current_rollout_pct,
            alpha=posterior.alpha,
            beta=posterior.beta,
            total_observations=posterior.total_observations,
            autonomous_enabled=True,
        )
        self._posteriors[key] = updated
        return updated

    def disable_autonomous(self, flag_key: str, environment: str) -> FlagPosterior | None:
        """Remove a flag-environment pair from autonomous rollout mode.

        Returns the updated posterior, or None if the flag was not tracked.
        """
        key = self._posterior_key(flag_key, environment)
        if key not in self._posteriors:
            return None
        posterior = self._posteriors[key]
        updated = FlagPosterior(
            flag_key=posterior.flag_key,
            environment=posterior.environment,
            current_rollout_pct=posterior.current_rollout_pct,
            alpha=posterior.alpha,
            beta=posterior.beta,
            total_observations=posterior.total_observations,
            autonomous_enabled=False,
        )
        self._posteriors[key] = updated
        return updated

    def get_posterior(self, flag_key: str, environment: str) -> FlagPosterior | None:
        """Return the raw posterior state for a flag-environment pair."""
        key = self._posterior_key(flag_key, environment)
        return self._posteriors.get(key)
