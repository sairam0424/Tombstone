"""
LinUCB disjoint contextual bandit for flag rollout personalization.

Improves on Thompson Sampling by incorporating context features
(e.g. user segment, device, geography) to make rollout decisions.

Persists A and b matrices to Redis as base64-encoded numpy bytes.
Falls back to Thompson Sampling when < 50 observations.
"""
from __future__ import annotations

import base64
import json
import logging
from dataclasses import dataclass, field

import numpy as np

logger = logging.getLogger(__name__)

_MIN_LINUCB_OBS: int = 50

REDIS_KEY_A = "tombstone:linucb:{flag_key}:{env}:A"
REDIS_KEY_B = "tombstone:linucb:{flag_key}:{env}:b"
REDIS_KEY_META = "tombstone:linucb:{flag_key}:{env}:meta"
REDIS_TTL = 90 * 24 * 3600  # 90 days


@dataclass
class LinUCBArmState:
    A: np.ndarray  # d×d matrix (precision matrix)
    b: np.ndarray  # d-vector (reward-weighted context accumulator)
    n_obs: int = 0


class LinUCBBandit:
    """LinUCB disjoint contextual bandit for autonomous flag rollout.

    Each flag-environment pair is modelled as a separate arm with independent
    (A, b) matrices.  The UCB score for the treatment arm is:

        theta = A^{-1} b
        ucb   = theta^T x + alpha * sqrt(x^T A^{-1} x)

    where x is the d-dimensional context vector.

    Arms:
        arm=0  control  (do not advance rollout / hold / rollback)
        arm=1  treatment (advance rollout)

    We maintain two independent arms per flag-environment pair; the bandit
    selects the arm with the higher UCB score.
    """

    def __init__(self, alpha: float = 1.0, d: int = 5) -> None:
        """
        alpha: exploration parameter (higher = more exploration).
        d: context feature dimension.
            Default 5: [rollout_pct, error_rate, request_count, hour_of_day, day_of_week]
        """
        self.alpha = alpha
        self.d = d
        # Keyed by "<flag_key>:<env>:<arm_id>" where arm_id in {0, 1}
        self._arms: dict[str, LinUCBArmState] = {}

    # ------------------------------------------------------------------
    # Internals
    # ------------------------------------------------------------------

    @staticmethod
    def _arm_key(flag_key: str, environment: str, arm_id: int) -> str:
        return f"{flag_key}:{environment}:{arm_id}"

    def _get_or_create_arm(self, flag_key: str, environment: str, arm_id: int) -> LinUCBArmState:
        key = self._arm_key(flag_key, environment, arm_id)
        if key not in self._arms:
            self._arms[key] = LinUCBArmState(
                A=np.eye(self.d, dtype=np.float64),
                b=np.zeros(self.d, dtype=np.float64),
            )
        return self._arms[key]

    def _default_arm(self) -> LinUCBArmState:
        return LinUCBArmState(
            A=np.eye(self.d, dtype=np.float64),
            b=np.zeros(self.d, dtype=np.float64),
        )

    def _context_vector(
        self,
        rollout_pct: float,
        error_rate: float,
        request_count: int,
        hour: int,
        day: int,
    ) -> np.ndarray:
        """Normalize context features to [0, 1] vector of length d=5."""
        return np.array(
            [
                rollout_pct / 100.0,
                float(np.clip(error_rate, 0.0, 1.0)),
                min(request_count, 10_000) / 10_000.0,
                hour / 23.0,
                day / 6.0,
            ],
            dtype=np.float64,
        )

    def _ucb_score(self, arm_state: LinUCBArmState, context: np.ndarray) -> float:
        """Compute the UCB score: theta^T x + alpha * sqrt(x^T A^{-1} x)."""
        A_inv = np.linalg.inv(arm_state.A)
        theta = A_inv @ arm_state.b
        exploration = float(np.sqrt(context @ A_inv @ context))
        return float(theta @ context) + self.alpha * exploration

    # ------------------------------------------------------------------
    # Public API
    # ------------------------------------------------------------------

    def select_arm(
        self,
        flag_key: str,
        environment: str,
        context: np.ndarray,
    ) -> tuple[int, float]:
        """Return (arm: 0=control / 1=treatment, ucb_score).

        arm=1 (treatment / advance) is selected when its UCB score is higher
        than arm=0 (control / hold).
        """
        score_control = self._ucb_score(
            self._get_or_create_arm(flag_key, environment, 0), context
        )
        score_treatment = self._ucb_score(
            self._get_or_create_arm(flag_key, environment, 1), context
        )

        if score_treatment >= score_control:
            return 1, score_treatment
        return 0, score_control

    def update(
        self,
        flag_key: str,
        environment: str,
        arm_id: int,
        context: np.ndarray,
        reward: float,
    ) -> None:
        """Update A and b matrices for the played arm with a new observation.

        LinUCB update rule (immutable matrices — new ndarray objects):
            A_new = A_old + x x^T
            b_new = b_old + reward * x
        """
        arm_state = self._get_or_create_arm(flag_key, environment, arm_id)
        new_A = arm_state.A + np.outer(context, context)
        new_b = arm_state.b + reward * context
        key = self._arm_key(flag_key, environment, arm_id)
        self._arms[key] = LinUCBArmState(
            A=new_A,
            b=new_b,
            n_obs=arm_state.n_obs + 1,
        )

    def n_observations(self, flag_key: str, environment: str) -> int:
        """Total observations across both arms for a flag-environment pair."""
        total = 0
        for arm_id in (0, 1):
            key = self._arm_key(flag_key, environment, arm_id)
            if key in self._arms:
                total += self._arms[key].n_obs
        return total

    # ------------------------------------------------------------------
    # Redis persistence — same base64+numpy pattern as Thompson phase 1.1
    # ------------------------------------------------------------------

    @staticmethod
    def _ndarray_to_b64(arr: np.ndarray) -> str:
        return base64.b64encode(arr.tobytes()).decode("ascii")

    @staticmethod
    def _b64_to_ndarray(b64: str, shape: tuple[int, ...]) -> np.ndarray:
        raw = base64.b64decode(b64)
        return np.frombuffer(raw, dtype=np.float64).reshape(shape).copy()

    async def save_to_redis(
        self, flag_key: str, env: str, redis_client
    ) -> None:
        """Persist A and b matrices for both arms as base64 numpy bytes."""
        for arm_id in (0, 1):
            key_a = REDIS_KEY_A.format(flag_key=flag_key, env=env) + f":{arm_id}"
            key_b = REDIS_KEY_B.format(flag_key=flag_key, env=env) + f":{arm_id}"
            key_meta = REDIS_KEY_META.format(flag_key=flag_key, env=env) + f":{arm_id}"

            arm_state = self._get_or_create_arm(flag_key, env, arm_id)
            try:
                await redis_client.set(
                    key_a,
                    self._ndarray_to_b64(arm_state.A),
                    ex=REDIS_TTL,
                )
                await redis_client.set(
                    key_b,
                    self._ndarray_to_b64(arm_state.b),
                    ex=REDIS_TTL,
                )
                await redis_client.set(
                    key_meta,
                    json.dumps({"n_obs": arm_state.n_obs, "d": self.d}),
                    ex=REDIS_TTL,
                )
            except Exception:
                logger.exception(
                    "LinUCB Redis save failed for %s:%s arm=%d", flag_key, env, arm_id
                )

    async def load_from_redis(self, redis_client) -> None:
        """Restore all arm states from Redis on startup.  Fails open (no-op on error)."""
        # Scan for all known arm keys by looking for meta keys
        pattern = "tombstone:linucb:*:meta:*"
        try:
            cursor = 0
            while True:
                cursor, keys = await redis_client.scan(cursor, match=pattern, count=100)
                for raw_key in keys:
                    try:
                        meta_key = (
                            raw_key.decode("utf-8")
                            if isinstance(raw_key, bytes)
                            else raw_key
                        )
                        # Parse "tombstone:linucb:{flag_key}:{env}:meta:{arm_id}"
                        # The flag_key itself may contain ':' — use rsplit with maxsplit
                        prefix = "tombstone:linucb:"
                        suffix = meta_key[len(prefix):]
                        # suffix is "{flag_key}:{env}:meta:{arm_id}"
                        parts = suffix.rsplit(":", 3)
                        if len(parts) != 4 or parts[2] != "meta":
                            continue
                        flag_key, env, _, arm_id_str = parts
                        arm_id = int(arm_id_str)

                        meta_raw = await redis_client.get(meta_key)
                        if meta_raw is None:
                            continue
                        meta = json.loads(meta_raw)
                        d = meta.get("d", self.d)
                        n_obs = meta.get("n_obs", 0)

                        key_a = (
                            REDIS_KEY_A.format(flag_key=flag_key, env=env)
                            + f":{arm_id}"
                        )
                        key_b = (
                            REDIS_KEY_B.format(flag_key=flag_key, env=env)
                            + f":{arm_id}"
                        )
                        raw_A = await redis_client.get(key_a)
                        raw_b = await redis_client.get(key_b)
                        if raw_A is None or raw_b is None:
                            continue

                        A = self._b64_to_ndarray(
                            raw_A.decode("ascii")
                            if isinstance(raw_A, bytes)
                            else raw_A,
                            (d, d),
                        )
                        b = self._b64_to_ndarray(
                            raw_b.decode("ascii")
                            if isinstance(raw_b, bytes)
                            else raw_b,
                            (d,),
                        )

                        arm_key = self._arm_key(flag_key, env, arm_id)
                        self._arms[arm_key] = LinUCBArmState(A=A, b=b, n_obs=n_obs)
                        logger.debug(
                            "LinUCB restored arm %s:%s arm=%d n_obs=%d",
                            flag_key,
                            env,
                            arm_id,
                            n_obs,
                        )
                    except Exception:
                        logger.exception(
                            "LinUCB Redis load failed for key %s", raw_key
                        )

                if cursor == 0:
                    break
        except Exception:
            logger.exception("LinUCB load_from_redis scan failed — starting fresh")
