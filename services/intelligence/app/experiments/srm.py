"""
Sample Ratio Mismatch (SRM) detection.

SRM means the ACTUAL observed split between an experiment's arms doesn't
match the INTENDED allocation ratio — e.g. a 50/50 control/treatment
config that actually landed 45/55. That is a strong signal the
randomization/bucketing itself is broken (a hashing bug, bot traffic
skewing one arm, an SDK version only some users have, etc.), which
invalidates any statistical result computed on top of that split — SRM
must be checked and blocked on BEFORE trusting a p-value or lift number,
not after.

Detected via a standard chi-square goodness-of-fit test comparing the
observed per-arm counts against the counts expected under the intended
allocation ratio. EXP-2's plan item specifies a p<0.001 threshold — far
stricter than the conventional p<0.05 significance level, deliberately:
SRM checks fire on every single analysis run regardless of how the
experiment turns out, so a conventional 0.05 false-positive rate would
incorrectly block a meaningful fraction of genuinely healthy experiments
by chance alone.
"""

from __future__ import annotations

from dataclasses import dataclass

from scipy import stats  # type: ignore[import]

SRM_ALPHA: float = 0.001


@dataclass
class SRMResult:
    chi2_stat: float
    p_value: float
    is_mismatch: bool


def srm_check(
    n_control: int,
    n_treatment: int,
    expected_ratio: float = 0.5,
) -> SRMResult:
    """
    Chi-square goodness-of-fit test for sample ratio mismatch between two
    experiment arms.

    Args:
        n_control:      observed sample count in the control arm.
        n_treatment:    observed sample count in the treatment arm.
        expected_ratio: intended fraction of traffic allocated to control
                        (0.5 for an even 50/50 split; e.g. 0.9 for a 90/10
                        control-heavy rollout).

    Returns:
        SRMResult with is_mismatch=True when p_value < SRM_ALPHA — the
        observed split is statistically incompatible with the intended
        ratio and any statistical result from this data should be
        distrusted until the allocation bug is found and fixed.

        Returns chi2_stat=0.0/p_value=1.0/is_mismatch=False (i.e. "not
        computable, do not block") when there is no real total to test —
        n_control=0 and n_treatment=0 together, or expected_ratio is
        outside (0.0, 1.0) exclusive (a degenerate 0% or 100% allocation
        has no "mismatch" to detect against).
    """
    total = n_control + n_treatment
    if total <= 0 or not (0.0 < expected_ratio < 1.0):
        return SRMResult(chi2_stat=0.0, p_value=1.0, is_mismatch=False)

    expected_control = total * expected_ratio
    expected_treatment = total * (1.0 - expected_ratio)

    chi2_stat, p_value = stats.chisquare(
        f_obs=[n_control, n_treatment],
        f_exp=[expected_control, expected_treatment],
    )

    return SRMResult(
        chi2_stat=float(chi2_stat),
        p_value=float(p_value),
        is_mismatch=bool(p_value < SRM_ALPHA),
    )
