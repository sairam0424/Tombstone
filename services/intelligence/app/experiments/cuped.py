"""
CUPED: Controlled-experiment Using Pre-Experiment Data.

Reduces variance by removing the portion of outcome variance explained by a
pre-experiment covariate (e.g. last-week conversion, prior spend).

Typical variance reduction: 20-40%.  Less data required for the same power.

Reference: Deng, Xu, Kohavi, Walker — "Improving the Sensitivity of Online
           Controlled Experiments by Utilizing Pre-Experiment Data" (KDD 2013).
"""
from __future__ import annotations

import numpy as np
from scipy import stats
from typing import Tuple


def cuped_adjustment(
    treatment: np.ndarray,
    control: np.ndarray,
    pre_treatment: np.ndarray,
    pre_control: np.ndarray,
) -> Tuple[np.ndarray, np.ndarray, float]:
    """
    Apply CUPED adjustment to treatment and control outcome arrays.

    theta = Cov(Y, X) / Var(X)  where X = pre-experiment covariate
    Y_adjusted = Y - theta * (X - mean(X))

    Args:
        treatment:     Post-experiment outcome values for treatment group.
        control:       Post-experiment outcome values for control group.
        pre_treatment: Pre-experiment covariate values for treatment group.
        pre_control:   Pre-experiment covariate values for control group.

    Returns:
        (adjusted_treatment, adjusted_control, variance_reduction_pct)
        variance_reduction_pct is in [0, 100].  A value of 30 means 30% less
        variance after adjustment.
    """
    treatment = np.asarray(treatment, dtype=float)
    control = np.asarray(control, dtype=float)
    pre_treatment = np.asarray(pre_treatment, dtype=float)
    pre_control = np.asarray(pre_control, dtype=float)

    Y = np.concatenate([treatment, control])
    X = np.concatenate([pre_treatment, pre_control])

    var_X = np.var(X)
    if var_X == 0:
        # Covariate has no variance — adjustment is a no-op.
        return treatment.copy(), control.copy(), 0.0

    # Pooled OLS estimate of theta
    theta = np.cov(Y, X)[0, 1] / var_X
    X_mean = X.mean()

    adj_treatment = treatment - theta * (pre_treatment - X_mean)
    adj_control = control - theta * (pre_control - X_mean)

    orig_var = np.var(Y)
    adj_var = np.var(np.concatenate([adj_treatment, adj_control]))
    reduction = max(0.0, 1.0 - adj_var / orig_var) * 100.0 if orig_var > 0 else 0.0

    return adj_treatment, adj_control, float(reduction)


def cuped_effect_size(
    treatment: np.ndarray,
    control: np.ndarray,
    pre_treatment: np.ndarray,
    pre_control: np.ndarray,
) -> dict:
    """
    Compute CUPED-adjusted treatment effect and full test statistics.

    Returns a dict with:
        effect_size               — adjusted mean difference (treatment - control)
        p_value                   — two-sample t-test on adjusted values
        variance_reduction_pct    — % variance removed by CUPED
        adjusted_treatment_mean
        adjusted_control_mean
        ci_lower / ci_upper       — 95% CI on adjusted effect (Welch t CI)
        t_statistic
        degrees_of_freedom
    """
    treatment = np.asarray(treatment, dtype=float)
    control = np.asarray(control, dtype=float)
    pre_treatment = np.asarray(pre_treatment, dtype=float)
    pre_control = np.asarray(pre_control, dtype=float)

    adj_t, adj_c, var_reduction = cuped_adjustment(
        treatment, control, pre_treatment, pre_control
    )

    t_stat, p_value = stats.ttest_ind(adj_t, adj_c, equal_var=False)

    # Welch degrees of freedom for CI
    n_t, n_c = len(adj_t), len(adj_c)
    var_t = np.var(adj_t, ddof=1) if n_t > 1 else 0.0
    var_c = np.var(adj_c, ddof=1) if n_c > 1 else 0.0

    se = np.sqrt(var_t / n_t + var_c / n_c) if (n_t > 0 and n_c > 0) else 0.0

    # Welch–Satterthwaite df
    if se > 0 and n_t > 1 and n_c > 1:
        df_num = (var_t / n_t + var_c / n_c) ** 2
        df_den = (var_t / n_t) ** 2 / (n_t - 1) + (var_c / n_c) ** 2 / (n_c - 1)
        df = df_num / df_den if df_den > 0 else n_t + n_c - 2
        t_crit = float(stats.t.ppf(0.975, df))
        ci_lower = float((adj_t.mean() - adj_c.mean()) - t_crit * se)
        ci_upper = float((adj_t.mean() - adj_c.mean()) + t_crit * se)
    else:
        df = float(n_t + n_c - 2)
        ci_lower = float(adj_t.mean() - adj_c.mean())
        ci_upper = float(adj_t.mean() - adj_c.mean())

    return {
        "effect_size": float(adj_t.mean() - adj_c.mean()),
        "p_value": float(p_value),
        "variance_reduction_pct": float(var_reduction),
        "adjusted_treatment_mean": float(adj_t.mean()),
        "adjusted_control_mean": float(adj_c.mean()),
        "ci_lower": ci_lower,
        "ci_upper": ci_upper,
        "t_statistic": float(t_stat),
        "degrees_of_freedom": float(df),
    }
