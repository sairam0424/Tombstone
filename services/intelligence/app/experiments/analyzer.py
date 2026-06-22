
import numpy as np
from scipy import stats  # type: ignore[import]

from app.experiments.models import (
    ExperimentDefinition, MetricResult, VariantStats,
)


class ExperimentAnalyzer:
    """
    Runs statistical analysis on experiment data.
    Supports frequentist t-test and Bayesian Beta-Binomial analysis.
    Uses CUPED variance reduction when pre-experiment covariates are provided.
    Supports mSPRT sequential testing for always-valid inference.
    """

    def analyze(
        self,
        experiment: ExperimentDefinition,
        control_data: list[float],
        treatment_data: list[float],
        metric_name: str,
    ) -> MetricResult:
        control_arr = np.array(control_data)
        treatment_arr = np.array(treatment_data)

        control_stats = VariantStats(
            variant="control",
            sample_size=len(control_arr),
            mean=float(np.mean(control_arr)) if len(control_arr) > 0 else 0.0,
            std=float(np.std(control_arr)) if len(control_arr) > 0 else 0.0,
            conversion_rate=float(np.mean(control_arr > 0)) if len(control_arr) > 0 else 0.0,
        )
        treatment_stats = VariantStats(
            variant="treatment",
            sample_size=len(treatment_arr),
            mean=float(np.mean(treatment_arr)) if len(treatment_arr) > 0 else 0.0,
            std=float(np.std(treatment_arr)) if len(treatment_arr) > 0 else 0.0,
            conversion_rate=float(np.mean(treatment_arr > 0)) if len(treatment_arr) > 0 else 0.0,
        )

        relative_lift = 0.0
        if control_stats.mean != 0:
            relative_lift = (treatment_stats.mean - control_stats.mean) / abs(control_stats.mean)

        p_value: float | None = None
        prob_beats_control: float | None = None
        is_significant = False

        if experiment.stat_method == "frequentist":
            if len(control_arr) >= 2 and len(treatment_arr) >= 2:
                _, p_value = stats.ttest_ind(treatment_arr, control_arr)
                is_significant = float(p_value) < (1 - 0.95)

        elif experiment.stat_method == "bayesian":
            # Beta-Binomial conjugate for conversion metrics
            control_conv = int(np.sum(control_arr > 0))
            treat_conv = int(np.sum(treatment_arr > 0))
            control_total = len(control_arr)
            treat_total = len(treatment_arr)

            # Monte Carlo sampling (10k samples)
            alpha_c, beta_c = 1 + control_conv, 1 + (control_total - control_conv)
            alpha_t, beta_t = 1 + treat_conv, 1 + (treat_total - treat_conv)
            samples_c = np.random.beta(alpha_c, beta_c, 10_000)
            samples_t = np.random.beta(alpha_t, beta_t, 10_000)
            prob_beats_control = float(np.mean(samples_t > samples_c))
            is_significant = prob_beats_control > 0.95

        min_n = experiment.min_sample_size
        if control_stats.sample_size < min_n or treatment_stats.sample_size < min_n:
            is_significant = False  # insufficient data

        return MetricResult(
            metric_name=metric_name,
            control=control_stats,
            treatment=treatment_stats,
            relative_lift=round(relative_lift, 4),
            p_value=round(float(p_value), 4) if p_value is not None else None,
            probability_beats_control=round(prob_beats_control, 4) if prob_beats_control is not None else None,
            is_significant=is_significant,
        )

    def recommend(self, results: list[MetricResult]) -> str:
        """
        SHIP: all primary metrics significant and positive.
        NO_SHIP: any primary metric significant and negative.
        CONTINUE: insufficient data or mixed signals.
        """
        if not results:
            return "CONTINUE"

        significant = [r for r in results if r.is_significant]
        if not significant:
            return "CONTINUE"

        negative = [r for r in significant if r.relative_lift < 0]
        if negative:
            return "NO_SHIP"

        positive = [r for r in significant if r.relative_lift > 0]
        if len(positive) == len(results):
            return "SHIP"

        return "CONTINUE"

    def analyze_cuped(
        self,
        experiment: ExperimentDefinition,
        control_data: list[float],
        treatment_data: list[float],
        control_covariate: list[float],
        treatment_covariate: list[float],
        metric_name: str,
    ) -> MetricResult:
        """
        CUPED: Controlled-experiment Using Pre-Experiment Data.
        Regresses out pre-experiment covariate to reduce variance.
        Typical variance reduction: 30-50% -> shorter experiment runtime needed.

        Steps:
        1. Compute theta = cov(outcome, covariate) / var(covariate) on pooled data
        2. Adjust: outcome_adj = outcome - theta * (covariate - mean(covariate))
        3. Run standard t-test on adjusted outcomes
        """
        control_arr = np.array(control_data)
        treatment_arr = np.array(treatment_data)
        cov_c = np.array(control_covariate)
        cov_t = np.array(treatment_covariate)

        all_outcomes = np.concatenate([control_arr, treatment_arr])
        all_covariates = np.concatenate([cov_c, cov_t])

        if np.var(all_covariates) == 0:
            return self.analyze(experiment, control_data, treatment_data, metric_name)

        theta = np.cov(all_outcomes, all_covariates)[0][1] / np.var(all_covariates)
        global_cov_mean = np.mean(all_covariates)

        adjusted_control = control_arr - theta * (cov_c - global_cov_mean)
        adjusted_treatment = treatment_arr - theta * (cov_t - global_cov_mean)

        variance_reduction = (
            1 - (np.var(adjusted_control) / np.var(control_arr))
            if np.var(control_arr) > 0
            else 0
        )

        result = self.analyze(experiment, list(adjusted_control), list(adjusted_treatment), metric_name)
        result.metric_name = f"{metric_name}_cuped (variance_reduction={variance_reduction:.1%})"
        return result

    def analyze_sequential(
        self,
        control_data: list[float],
        treatment_data: list[float],
        metric_name: str,
        alpha: float = 0.05,
        tau: float = 1.0,
    ) -> MetricResult:
        """
        mSPRT: mixture Sequential Probability Ratio Test.
        Always-valid inference — can peek at results at any time without
        inflating the false positive rate (unlike standard t-tests).

        Uses a normal mixture prior with variance tau^2.
        Rejects null when the likelihood ratio exceeds 1/alpha.
        """
        c = np.array(control_data)
        t = np.array(treatment_data)

        if len(c) < 2 or len(t) < 2:
            from app.experiments.models import VariantStats, MetricResult
            cs = VariantStats("control", len(c), float(np.mean(c)) if len(c) else 0, 0, None)
            ts = VariantStats("treatment", len(t), float(np.mean(t)) if len(t) else 0, 0, None)
            return MetricResult(metric_name, cs, ts, 0.0, None, None, False)

        n_c, n_t = len(c), len(t)
        mean_c, mean_t = np.mean(c), np.mean(t)
        var_pooled = (np.var(c) * n_c + np.var(t) * n_t) / (n_c + n_t)

        if var_pooled == 0:
            relative_lift = 0.0
            is_sig = False
        else:
            delta = mean_t - mean_c
            se = np.sqrt(var_pooled * (1 / n_c + 1 / n_t))
            v = 1 / (1 / tau**2 + 1 / se**2)
            m = v * delta / se**2
            log_ratio = 0.5 * np.log(v / tau**2) + m**2 / (2 * v) - delta**2 / (2 * se**2)
            likelihood_ratio = np.exp(log_ratio)
            is_sig = likelihood_ratio >= (1 / alpha)
            relative_lift = float(delta / abs(mean_c)) if mean_c != 0 else 0.0

        from app.experiments.models import VariantStats, MetricResult
        cs = VariantStats("control", n_c, float(mean_c), float(np.std(c)), float(np.mean(c > 0)))
        ts = VariantStats("treatment", n_t, float(mean_t), float(np.std(t)), float(np.mean(t > 0)))
        return MetricResult(
            metric_name=f"{metric_name}_msprt",
            control=cs,
            treatment=ts,
            relative_lift=round(relative_lift, 4),
            p_value=None,
            probability_beats_control=None,
            is_significant=is_sig,
        )

    def required_sample_size(
        self,
        baseline_conversion: float,
        minimum_detectable_effect: float,
        alpha: float = 0.05,
        power: float = 0.80,
    ) -> int:
        """
        Calculates minimum sample size per variant for a two-sample proportion test.

        Args:
            baseline_conversion: Current conversion rate (0.0-1.0)
            minimum_detectable_effect: Smallest lift you care to detect (relative, 0.0-1.0)
            alpha: Type I error rate (default 0.05)
            power: Statistical power (default 0.80)

        Returns:
            Minimum sample size per variant
        """
        from scipy import stats
        p1 = baseline_conversion
        p2 = p1 * (1 + minimum_detectable_effect)
        z_alpha = stats.norm.ppf(1 - alpha / 2)
        z_beta = stats.norm.ppf(power)
        p_bar = (p1 + p2) / 2
        n = (
            z_alpha * np.sqrt(2 * p_bar * (1 - p_bar))
            + z_beta * np.sqrt(p1 * (1 - p1) + p2 * (1 - p2))
        ) ** 2 / (p2 - p1) ** 2
        return int(np.ceil(n))
