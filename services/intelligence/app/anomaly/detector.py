import numpy as np
from collections import defaultdict, deque
from dataclasses import dataclass, field
from datetime import datetime
from typing import Deque

from app.anomaly.ensemble import AnomalyEnsemble


@dataclass
class FlagMetrics:
    """Rolling window of evaluation counts and error rates for one flag."""
    error_rates: Deque[float] = field(default_factory=lambda: deque(maxlen=672))  # 7 days × 96 windows/day
    eval_counts: Deque[int] = field(default_factory=lambda: deque(maxlen=672))


class AnomalyDetector:
    """
    Detects anomalous flag evaluation patterns using Z-score deviation.
    Baseline: 7-day rolling window of per-flag error rates.
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
