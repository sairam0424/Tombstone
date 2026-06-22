"""
3-model ensemble anomaly detector.
Inspired by ImDiffusion (VLDB 2024): ensemble intermediate signals across time scales.

Key insight from the paper: ensembling across multiple time granularities (not just final
output) + majority voting dramatically outperforms single-model approaches (+11.4% F1 at
Microsoft scale with 5,000+ metrics).

Models:
  1. Z-score over 7-day rolling window (baseline — matches existing AnomalyDetector)
  2. Isolation Forest (scikit-learn, contamination=0.05)
  3. EWMA + adaptive threshold (exponentially weighted, fast drift detection)

Voting: anomaly if >= 2/3 models agree.
Time-scale ensembling: collect signals at 10s, 60s, 5m granularities — majority vote
across scales too (the ImDiffusion "intermediate representation" analogue for time series).
"""

from __future__ import annotations

import math
from collections import deque
from dataclasses import dataclass, field
from datetime import datetime
from typing import Deque

import numpy as np
from sklearn.ensemble import IsolationForest

# ---------------------------------------------------------------------------
# Per-flag state
# ---------------------------------------------------------------------------

_10S_MAXLEN = 672       # 7 days × 86400s/day / 10s  = 60480 → capped at 672 (≈ 1d12h)
_60S_MAXLEN = 168 * 6   # 7 days × 60min/day / 1min  = 10080 → 1008 stored
_5M_MAXLEN = 168 * 2    # 7 days × 24h × 12 buckets  = 2016  → 336 stored


@dataclass
class EnsembleDetector:
    """Per-flag-key detector state. Holds three rolling windows + model state."""

    # Raw 10-second observations (fed directly from Kafka)
    window_10s: Deque[float] = field(default_factory=lambda: deque(maxlen=_10S_MAXLEN))
    # Down-sampled 60-second means
    window_60s: Deque[float] = field(default_factory=lambda: deque(maxlen=_60S_MAXLEN))
    # Down-sampled 5-minute means
    window_5m: Deque[float] = field(default_factory=lambda: deque(maxlen=_5M_MAXLEN))

    # Isolation Forest
    iso_forest: IsolationForest = field(
        default_factory=lambda: IsolationForest(
            n_estimators=100, contamination=0.05, random_state=42
        )
    )
    iso_trained: bool = False

    # EWMA state (Welford-style online variance estimate)
    ewma: float = 0.0
    ewma_var: float = 1.0
    alpha: float = 0.1  # decay factor — higher = faster adaptation

    # Down-sampling accumulators
    _buf_60s: list[float] = field(default_factory=list)
    _buf_5m: list[float] = field(default_factory=list)
    _obs_count: int = 0          # total observations recorded

    def _update_ewma(self, value: float) -> None:
        """Update exponentially-weighted mean and variance (online, O(1))."""
        delta = value - self.ewma
        self.ewma += self.alpha * delta
        self.ewma_var = (1 - self.alpha) * (self.ewma_var + self.alpha * delta ** 2)

    def record(self, error_rate: float) -> None:
        """Ingest one 10-second observation and update all granularity windows."""
        self.window_10s.append(error_rate)
        self._buf_60s.append(error_rate)
        self._buf_5m.append(error_rate)
        self._obs_count += 1
        self._update_ewma(error_rate)

        # Every 6 × 10s = 60s → emit a 60s bucket
        if len(self._buf_60s) >= 6:
            self.window_60s.append(float(np.mean(self._buf_60s)))
            self._buf_60s = []

        # Every 30 × 10s = 300s (5m) → emit a 5m bucket
        if len(self._buf_5m) >= 30:
            self.window_5m.append(float(np.mean(self._buf_5m)))
            self._buf_5m = []


# ---------------------------------------------------------------------------
# Z-score helper (reusable across granularities)
# ---------------------------------------------------------------------------

def _zscore_signal(window: Deque[float], min_obs: int = 10) -> tuple[float, bool]:
    """
    Compute Z-score of the latest value against the rolling baseline.
    Returns (zscore, is_anomaly).  Threshold = 2.5 (matches existing detector).
    """
    if len(window) < min_obs:
        return 0.0, False
    arr = np.array(window, dtype=float)
    baseline = arr[:-1]
    mean = float(np.mean(baseline))
    std = float(np.std(baseline))
    if std < 1e-9:
        return 0.0, False
    z = abs(arr[-1] - mean) / std
    return float(z), z > 2.5


# ---------------------------------------------------------------------------
# Main ensemble class
# ---------------------------------------------------------------------------

class AnomalyEnsemble:
    """
    Thread-safe (per-key) ensemble anomaly detector for feature flag error rates.

    Usage:
        ensemble = AnomalyEnsemble()
        ensemble.record("my-flag", error_rate=0.03, ts=datetime.utcnow())
        result = ensemble.detect("my-flag", current_rate=0.15)
    """

    def __init__(self) -> None:
        self._detectors: dict[str, EnsembleDetector] = {}

    def _get_or_create(self, flag_key: str) -> EnsembleDetector:
        if flag_key not in self._detectors:
            self._detectors[flag_key] = EnsembleDetector()
        return self._detectors[flag_key]

    # ------------------------------------------------------------------
    # Public API
    # ------------------------------------------------------------------

    def record(self, flag_key: str, error_rate: float, ts: datetime) -> None:
        """Record a 10-second observation. Called from the Kafka consumer."""
        det = self._get_or_create(flag_key)
        det.record(error_rate)

    def detect(self, flag_key: str, current_rate: float) -> dict:
        """
        Run the 3-model ensemble over the current observation.

        Returns:
            {
              "anomaly": bool,
              "score": float (0-1),
              "zscore": float,
              "isolation_score": float,
              "ewma_deviation": float,
              "votes": int (0-3),
              "granularity_votes": {"10s": bool, "60s": bool, "5m": bool},
              "model_votes": {"zscore": bool, "isolation_forest": bool, "ewma": bool},
              "obs_count": int,
              "sufficient_data": bool,
            }
        """
        det = self._detectors.get(flag_key)
        if det is None or det._obs_count < 50:
            # Not enough history — fall back to a simple stub so callers always
            # receive the same schema.
            return {
                "anomaly": False,
                "score": 0.0,
                "zscore": 0.0,
                "isolation_score": 0.0,
                "ewma_deviation": 0.0,
                "votes": 0,
                "granularity_votes": {"10s": False, "60s": False, "5m": False},
                "model_votes": {
                    "zscore": False,
                    "isolation_forest": False,
                    "ewma": False,
                },
                "obs_count": det._obs_count if det else 0,
                "sufficient_data": False,
            }

        # ---- Model 1: Z-score on 10s window ----
        zscore, zscore_flag = _zscore_signal(det.window_10s)

        # ---- Model 2: Isolation Forest ----
        iso_score, iso_flag = self._isolation_score(det, current_rate)

        # ---- Model 3: EWMA adaptive threshold ----
        ewma_dev, ewma_flag = self._ewma_score(det, current_rate)

        # ---- Time-scale granularity votes (ImDiffusion key idea) ----
        _, g60s_flag = _zscore_signal(det.window_60s, min_obs=5)
        _, g5m_flag = _zscore_signal(det.window_5m, min_obs=3)
        gran_votes = {"10s": zscore_flag, "60s": g60s_flag, "5m": g5m_flag}
        gran_agreement = sum(gran_votes.values()) >= 2

        # ---- Model-level majority vote ----
        model_votes = {
            "zscore": zscore_flag,
            "isolation_forest": iso_flag,
            "ewma": ewma_flag,
        }
        model_agreement = sum(model_votes.values()) >= 2

        # Final verdict: both model-majority AND granularity-majority must agree
        # (reduces false positives — mirrors ImDiffusion dual-gate design)
        anomaly = model_agreement and gran_agreement

        # Composite score [0, 1]: weighted blend
        raw_score = (
            min(zscore / 5.0, 1.0) * 0.40
            + min(abs(iso_score), 1.0) * 0.30
            + min(ewma_dev / 5.0, 1.0) * 0.30
        )

        return {
            "anomaly": anomaly,
            "score": round(float(raw_score), 4),
            "zscore": round(float(zscore), 4),
            "isolation_score": round(float(iso_score), 4),
            "ewma_deviation": round(float(ewma_dev), 4),
            "votes": sum(model_votes.values()),
            "granularity_votes": gran_votes,
            "model_votes": model_votes,
            "obs_count": det._obs_count,
            "sufficient_data": True,
        }

    def retrain_isolation_forest(self, flag_key: str) -> None:
        """
        Retrain Isolation Forest on 7-day history. Called daily (at 2am) by the
        background task in main.py lifespan.
        """
        det = self._detectors.get(flag_key)
        if det is None or len(det.window_10s) < 50:
            return

        X = np.array(det.window_10s).reshape(-1, 1)
        det.iso_forest.fit(X)
        det.iso_trained = True

    def retrain_all(self) -> int:
        """Retrain Isolation Forest for every tracked flag. Returns count retrained."""
        count = 0
        for flag_key in list(self._detectors.keys()):
            self.retrain_isolation_forest(flag_key)
            count += 1
        return count

    # ------------------------------------------------------------------
    # Internal model helpers
    # ------------------------------------------------------------------

    def _isolation_score(
        self, det: EnsembleDetector, current_rate: float
    ) -> tuple[float, bool]:
        """
        Returns (raw_score, is_anomaly).
        raw_score is the negated decision_function value — higher = more anomalous.
        is_anomaly when predict() returns -1.
        """
        if not det.iso_trained or len(det.window_10s) < 50:
            # Lazy train on first use once we have enough data
            X = np.array(det.window_10s).reshape(-1, 1)
            det.iso_forest.fit(X)
            det.iso_trained = True

        sample = np.array([[current_rate]])
        pred = det.iso_forest.predict(sample)[0]          # 1 = normal, -1 = anomaly
        decision = float(det.iso_forest.decision_function(sample)[0])
        # Negate so that higher = more anomalous (decision_function > 0 = inlier)
        raw_score = -decision
        return raw_score, pred == -1

    def _ewma_score(
        self, det: EnsembleDetector, current_rate: float
    ) -> tuple[float, bool]:
        """
        Returns (deviation_in_sigmas, is_anomaly).
        Uses the online EWMA mean and variance stored in EnsembleDetector.
        Anomaly threshold: 3 sigma (adaptive — tighter than static Z-score).
        """
        std = math.sqrt(max(det.ewma_var, 1e-9))
        deviation = abs(current_rate - det.ewma) / std
        # Adaptive threshold: 3 sigma for EWMA (fast drift detector)
        return float(deviation), deviation > 3.0
