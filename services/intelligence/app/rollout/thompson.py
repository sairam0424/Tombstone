from __future__ import annotations

import json
import logging
from dataclasses import dataclass
from datetime import datetime, timezone
from typing import TYPE_CHECKING, Any

import numpy as np

if TYPE_CHECKING:
    pass

logger = logging.getLogger(__name__)

ROLLOUT_SCHEDULE = [1, 5, 10, 25, 50, 75, 100]

_REDIS_KEY_PREFIX = "tombstone:thompson"
_REDIS_TTL_SECONDS = 7_776_000  # 90 days

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

    Posteriors are written through to Redis on every update so that the engine
    can be restored to its previous state after a service restart.
    """

    def __init__(self) -> None:
        self._posteriors: dict[str, FlagPosterior] = {}
        self._redis: Any | None = None  # set via set_redis_client or load_all_from_redis

    # ------------------------------------------------------------------
    # Redis helpers
    # ------------------------------------------------------------------

    def set_redis_client(self, redis_client: Any) -> None:
        """Attach a redis.asyncio client so write-through can fire on updates."""
        self._redis = redis_client

    @staticmethod
    def _redis_key(flag_key: str, environment: str) -> str:
        return f"{_REDIS_KEY_PREFIX}:{flag_key}:{environment}"

    @staticmethod
    def _serialize(posterior: FlagPosterior) -> str:
        return json.dumps(
            {
                "alpha": posterior.alpha,
                "beta": posterior.beta,
                "observations": posterior.total_observations,
                "last_updated": datetime.now(timezone.utc).isoformat(),
            }
        )

    async def _write_to_redis(self, posterior: FlagPosterior) -> None:
        """Write-through: persist posterior to Redis.  Fails open on any error."""
        if self._redis is None:
            return
        try:
            key = self._redis_key(posterior.flag_key, posterior.environment)
            payload = self._serialize(posterior)
            await self._redis.set(key, payload, ex=_REDIS_TTL_SECONDS)
        except Exception as exc:  # noqa: BLE001
            logger.warning(
                "Thompson write-through to Redis failed (flag=%s env=%s): %s",
                posterior.flag_key,
                posterior.environment,
                exc,
            )

    async def load_all_from_redis(self, redis_client: Any) -> None:
        """Restore in-memory posteriors from Redis on service startup.

        Uses SCAN to iterate over all tombstone:thompson:* keys.  Any Redis
        error causes a warning and an empty in-memory state — the service will
        still start successfully.
        """
        self._redis = redis_client
        restored = 0
        try:
            cursor: int = 0
            pattern = f"{_REDIS_KEY_PREFIX}:*"
            while True:
                cursor, keys = await redis_client.scan(
                    cursor=cursor, match=pattern, count=200
                )
                for raw_key in keys:
                    key_str = (
                        raw_key.decode() if isinstance(raw_key, bytes) else raw_key
                    )
                    # Key format: tombstone:thompson:{flag_key}:{environment}
                    # flag_key itself may contain colons, so split from the right
                    # to isolate environment (last segment) and flag_key (all middle segments).
                    parts = key_str.split(":", 3)  # ['tombstone', 'thompson', flag_key, env]
                    if len(parts) != 4:
                        logger.warning("Skipping malformed Redis key: %s", key_str)
                        continue
                    _, _, flag_key, environment = parts

                    raw_value = await redis_client.get(raw_key)
                    if raw_value is None:
                        continue
                    try:
                        data = json.loads(
                            raw_value.decode() if isinstance(raw_value, bytes) else raw_value
                        )
                        posterior = FlagPosterior(
                            flag_key=flag_key,
                            environment=environment,
                            current_rollout_pct=0,  # unknown at restore time; will be refreshed on next update
                            alpha=float(data["alpha"]),
                            beta=float(data["beta"]),
                            total_observations=int(data["observations"]),
                            autonomous_enabled=False,
                        )
                        internal_key = self._posterior_key(flag_key, environment)
                        self._posteriors[internal_key] = posterior
                        restored += 1
                    except Exception as exc:  # noqa: BLE001
                        logger.warning(
                            "Failed to deserialise Thompson posterior for key %s: %s",
                            key_str,
                            exc,
                        )
                if cursor == 0:
                    break
            logger.info("Restored %d Thompson posteriors from Redis", restored)
        except Exception as exc:  # noqa: BLE001
            logger.warning(
                "Could not restore Thompson posteriors from Redis — starting with empty "
                "in-memory state. Cause: %s",
                exc,
            )

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

    async def update(
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

        Writes through to Redis on every update (fails open).
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

        # Write-through: persist updated posterior to Redis (fails open)
        await self._write_to_redis(updated)

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
