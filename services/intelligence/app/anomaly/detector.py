import numpy as np
from collections import defaultdict, deque
from dataclasses import dataclass, field
from datetime import datetime
from typing import Deque

from app.anomaly.ensemble import AnomalyEnsemble


@dataclass
class FlagMetrics:
    """Rolling window of evaluation counts and error rates for one flag."""

    # INT-4: 672 x 10s windows = 6720s ≈ 1.87h, NOT "7 days" -- the original
    # comment's own arithmetic (96 windows/day x 7 days = 672) assumed a
    # 15-minute window, but record() below is called once per 10s window,
    # not per 15-min one. Deliberately kept at this size (a true 7-day
    # window would be ~130x larger per flag; see ensemble.py's
    # _10S_MAXLEN comment for the capacity-planning tradeoff) -- this is
    # only a documentation fix, not a behavior change.
    error_rates: Deque[float] = field(default_factory=lambda: deque(maxlen=672))
    eval_counts: Deque[int] = field(default_factory=lambda: deque(maxlen=672))


class AnomalyDetector:
    """
    Detects anomalous flag evaluation patterns using Z-score deviation.
    Baseline: ~1.87h rolling window of per-flag error rates (672 x 10s
    windows -- NOT 7 days, despite this class's history of calling it one;
    see FlagMetrics's comment above).
    Anomaly signal: current window Z-score > 2.5 std deviations from baseline.

    Phase 3.1 upgrade: also maintains an AnomalyEnsemble (3-model, ImDiffusion-inspired).
    When >= 50 observations are available the ensemble result is returned by detect();
    otherwise the classic Z-score path is used (backward compatible).
    """

    def __init__(self, z_threshold: float = 2.5):
        self._metrics: dict[str, FlagMetrics] = defaultdict(FlagMetrics)
        self._z_threshold = z_threshold
        self._ensemble = AnomalyEnsemble()

    def record(self, flag_key: str, error_count: int, total_count: int) -> None:
        """Record a 10-second evaluation window for a flag."""
        rate = error_count / total_count if total_count > 0 else 0.0
        m = self._metrics[flag_key]
        m.error_rates.append(rate)
        m.eval_counts.append(total_count)
        # Also feed the ensemble so it builds its own windowed state
        self._ensemble.record(flag_key, error_rate=rate, ts=datetime.utcnow())

    def get_score(self, flag_key: str) -> float:
        """
        Returns the Z-score of the most recent window vs the rolling baseline.
        Higher score = more anomalous. Returns 0.0 if insufficient history.
        """
        m = self._metrics.get(flag_key)
        if m is None or len(m.error_rates) < 10:
            return 0.0

        rates = np.array(list(m.error_rates))
        if len(rates) < 2:
            return 0.0

        mean = np.mean(rates[:-1])
        std = np.std(rates[:-1])
        if std == 0:
            return 0.0

        current = rates[-1]
        return float(abs(current - mean) / std)

    def is_anomaly(self, flag_key: str) -> bool:
        return self.get_score(flag_key) > self._z_threshold

    def all_anomalies(self) -> list[dict]:
        return [
            {"flag_key": k, "score": self.get_score(k)}
            for k in self._metrics
            if self.is_anomaly(k)
        ]

    # ------------------------------------------------------------------
    # Ensemble API (Phase 3.1)
    # ------------------------------------------------------------------

    def detect(self, flag_key: str) -> dict:
        """
        Primary detection method that uses the ensemble when data is sufficient,
        falling back to Z-score for flags with < 50 observations.

        Returns the full ensemble schema so callers always receive consistent output.
        """
        m = self._metrics.get(flag_key)
        current_rate = float(m.error_rates[-1]) if (m and m.error_rates) else 0.0

        result = self._ensemble.detect(flag_key, current_rate)

        if not result["sufficient_data"]:
            # Augment with classic Z-score
            z = self.get_score(flag_key)
            result["zscore"] = round(z, 4)
            result["anomaly"] = z > self._z_threshold
            result["score"] = round(min(z / 5.0, 1.0), 4)

        return result

    def get_ensemble(self) -> AnomalyEnsemble:
        """Expose the ensemble for daily retraining tasks."""
        return self._ensemble

    def evict(self, flag_key: str) -> bool:
        """
        Remove a flag's tracked state entirely (INT-4: called when a flag
        is archived) -- both the classic Z-score FlagMetrics and the
        ensemble's own per-flag state, which otherwise leak forever (no
        persistence, no TTL) for the lifetime of the process. Returns True
        if any state existed and was removed.
        """
        had_metrics = self._metrics.pop(flag_key, None) is not None
        had_ensemble = self._ensemble.evict(flag_key)
        return had_metrics or had_ensemble
