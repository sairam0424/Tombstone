from __future__ import annotations

import asyncio
import logging
from dataclasses import dataclass

import httpx

from app.rollout.thompson import ThompsonSamplingEngine


logger = logging.getLogger(__name__)


@dataclass
class ExecutionResult:
    flag_key: str
    environment: str
    old_pct: int
    new_pct: int
    success: bool
    error: str | None = None


class RolloutExecutor:
    """Periodically evaluates Thompson Sampling recommendations and applies them.

    For every flag-environment pair that has autonomous rollout enabled and a
    confidence-cleared recommendation, this executor issues a PATCH request to
    the flag-api to advance the rollout percentage.
    """

    def __init__(
        self,
        engine: ThompsonSamplingEngine,
        flag_api_url: str,
        api_token: str,
        interval_seconds: int = 300,
    ) -> None:
        self._engine = engine
        self._flag_api_url = flag_api_url.rstrip("/")
        self._api_token = api_token
        self._interval_seconds = interval_seconds

    # ------------------------------------------------------------------
    # Public API
    # ------------------------------------------------------------------

    async def run(self) -> None:
        """Loop forever, evaluating recommendations every interval_seconds."""
        logger.info(
            "RolloutExecutor started — evaluation interval: %ds",
            self._interval_seconds,
        )
        while True:
            await asyncio.sleep(self._interval_seconds)
            try:
                results = await self._evaluate_and_advance()
                advanced = [r for r in results if r.success]
                failed = [r for r in results if not r.success]
                if advanced:
                    logger.info(
                        "Autonomous rollout advanced %d flag(s): %s",
                        len(advanced),
                        [(r.flag_key, r.environment, r.old_pct, r.new_pct) for r in advanced],
                    )
                if failed:
                    logger.warning(
                        "Autonomous rollout failed for %d flag(s): %s",
                        len(failed),
                        [(r.flag_key, r.environment, r.error) for r in failed],
                    )
            except Exception:
                logger.exception("Unhandled error in RolloutExecutor._evaluate_and_advance")

    async def _evaluate_and_advance(self) -> list[ExecutionResult]:
        """Fetch all recommendations and issue PATCH calls for advanceable flags."""
        recommendations = self._engine.all_recommendations()
        advanceable = [r for r in recommendations if r.should_advance]

        if not advanceable:
            return []

        results: list[ExecutionResult] = []
        async with httpx.AsyncClient(
            headers={"Authorization": f"Bearer {self._api_token}"},
            timeout=10.0,
        ) as client:
            for rec in advanceable:
                result = await self._patch_flag(client, rec.flag_key, rec.environment, rec.current_pct, rec.recommended_pct)
                results.append(result)

                # Keep the in-memory posterior current_rollout_pct in sync on success
                if result.success:
                    posterior = self._engine.get_posterior(rec.flag_key, rec.environment)
                    if posterior is not None:
                        from app.rollout.thompson import FlagPosterior

                        # Rebuild the posterior with the updated percentage (immutable)
                        key = f"{rec.flag_key}:{rec.environment}"
                        updated = FlagPosterior(
                            flag_key=posterior.flag_key,
                            environment=posterior.environment,
                            current_rollout_pct=rec.recommended_pct,
                            alpha=posterior.alpha,
                            beta=posterior.beta,
                            total_observations=posterior.total_observations,
                            autonomous_enabled=posterior.autonomous_enabled,
                        )
                        self._engine._posteriors[key] = updated  # noqa: SLF001

        return results

    async def _patch_flag(
        self,
        client: httpx.AsyncClient,
        flag_key: str,
        environment: str,
        old_pct: int,
        new_pct: int,
    ) -> ExecutionResult:
        url = f"{self._flag_api_url}/api/v1/flags/{flag_key}/environments/{environment}"
        payload = {
            "enabled": True,
            "rollout_pct": new_pct,
            "updated_by": "autonomous-rollout",
        }
        try:
            response = await client.patch(url, json=payload)
            response.raise_for_status()
            logger.debug(
                "PATCH %s -> %d (rollout %d%% -> %d%%)",
                url,
                response.status_code,
                old_pct,
                new_pct,
            )
            return ExecutionResult(
                flag_key=flag_key,
                environment=environment,
                old_pct=old_pct,
                new_pct=new_pct,
                success=True,
            )
        except httpx.HTTPStatusError as exc:
            error_msg = (
                f"HTTP {exc.response.status_code}: {exc.response.text[:200]}"
            )
            logger.error(
                "Failed to advance %s/%s: %s", flag_key, environment, error_msg
            )
            return ExecutionResult(
                flag_key=flag_key,
                environment=environment,
                old_pct=old_pct,
                new_pct=new_pct,
                success=False,
                error=error_msg,
            )
        except httpx.RequestError as exc:
            error_msg = f"Request error: {exc}"
            logger.error(
                "Failed to reach flag-api for %s/%s: %s",
                flag_key,
                environment,
                error_msg,
            )
            return ExecutionResult(
                flag_key=flag_key,
                environment=environment,
                old_pct=old_pct,
                new_pct=new_pct,
                success=False,
                error=error_msg,
            )
