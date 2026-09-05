import numpy as np
from scipy import stats  # type: ignore[import]

from app.experiments.models import (
    ExperimentDefinition,
    MetricResult,
    VariantStats,
)
from app.experiments.srm import srm_check
from app.warehouse.connector import AggregatedMetric


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
            conversion_rate=float(np.mean(control_arr > 0))
            if len(control_arr) > 0
            else 0.0,
        )
        treatment_stats = VariantStats(
            variant="treatment",
            sample_size=len(treatment_arr),
            mean=float(np.mean(treatment_arr)) if len(treatment_arr) > 0 else 0.0,
            std=float(np.std(treatment_arr)) if len(treatment_arr) > 0 else 0.0,
            conversion_rate=float(np.mean(treatment_arr > 0))
            if len(treatment_arr) > 0
            else 0.0,
        )

        relative_lift = 0.0
        if control_stats.mean != 0:
            relative_lift = (treatment_stats.mean - control_stats.mean) / abs(
                control_stats.mean
            )

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
            probability_beats_control=round(prob_beats_control, 4)
            if prob_beats_control is not None
            else None,
            is_significant=is_significant,
        )

    def analyze_from_stats(
        self,
        experiment: ExperimentDefinition,
        control: AggregatedMetric,
        treatment: AggregatedMetric,
        metric_name: str,
    ) -> MetricResult:
        """
        Frequentist/Bayesian analysis directly from warehouse-computed
        sufficient statistics (n, mean, variance, conversion_count) — no
        per-user array reconstruction.

        `analyze()`'s `[mean] * sample_size` reconstruction pattern collapses
        every user in a variant to one identical value, which fabricates a
        near-zero within-variant variance: a t-test on that reconstructed
        array reports p ~ 0.0 for ANY nonzero mean difference, and the
        Bayesian branch's `np.sum(arr > 0)` conversion count becomes
        `sample_size` for any positive mean, corrupting the conversion rate
        to 100%. This method uses the warehouse's own aggregates instead, so
        both statistics are computed from the real per-variant spread.

        CUPED is NOT covered here, and never will be through this
        warehouse-aggregate path — it needs real per-user outcome/covariate
        pairs to compute a covariance, which cannot be reconstructed from
        marginal aggregates (EXP-1 PR 3/3 formally closed this: CUPED is
        scoped exclusively to POST /api/v1/experiments/cuped-adjust, which
        takes raw per-user arrays directly; `stat_method="cuped"` is
        rejected by /analyze rather than silently computing a fabricated
        result from a `[mean] * sample_size` reconstruction). mSPRT has its
        own sufficient-stats method,
        `analyze_sequential_from_stats()` — it no longer routes through
        `analyze_sequential()`'s reconstruction either.
        """
        # conversion_count must satisfy 0 <= conversion_count <= sample_size to be a
        # meaningful count of "successes" out of the same trials sample_size counts.
        # The real warehouse connectors always uphold this (both are computed in the
        # same GROUP BY over the same rows), but analyze_from_stats() also accepts a
        # bare AggregatedMetric directly -- clamp defensively so a future/alternate
        # connector or data anomaly can't feed a negative Beta shape parameter into
        # np.random.beta() (an uncaught ValueError -> unhandled 500) or report a
        # conversion_rate outside [0, 1].
        control_conv = max(0, min(control.conversion_count, control.sample_size))
        treat_conv = max(0, min(treatment.conversion_count, treatment.sample_size))

        # Sample variance (ddof=1, matching the warehouse's VARIANCE() aggregate and
        # what ttest_ind_from_stats expects for its own test statistic).
        control_std = float(np.sqrt(control.variance)) if control.variance > 0 else 0.0
        treatment_std = (
            float(np.sqrt(treatment.variance)) if treatment.variance > 0 else 0.0
        )
        # VariantStats.std is reported in analyze()'s population (ddof=0) convention
        # for consistency across stat_method branches -- converted from the sample
        # value above, not recomputed, since the warehouse never returns raw rows.
        control_std_reported = (
            control_std * np.sqrt((control.sample_size - 1) / control.sample_size)
            if control.sample_size > 1
            else 0.0
        )
        treatment_std_reported = (
            treatment_std * np.sqrt((treatment.sample_size - 1) / treatment.sample_size)
            if treatment.sample_size > 1
            else 0.0
        )

        control_stats = VariantStats(
            variant="control",
            sample_size=control.sample_size,
            mean=control.mean,
            std=float(control_std_reported),
            conversion_rate=(control_conv / control.sample_size)
            if control.sample_size > 0
            else 0.0,
        )
        treatment_stats = VariantStats(
            variant="treatment",
            sample_size=treatment.sample_size,
            mean=treatment.mean,
            std=float(treatment_std_reported),
            conversion_rate=(treat_conv / treatment.sample_size)
            if treatment.sample_size > 0
            else 0.0,
        )

        relative_lift = 0.0
        if control_stats.mean != 0:
            relative_lift = (treatment_stats.mean - control_stats.mean) / abs(
                control_stats.mean
            )

        p_value: float | None = None
        prob_beats_control: float | None = None
        is_significant = False

        if experiment.stat_method == "frequentist":
            # Both variances collapsing to exactly zero (e.g. a fixed per-plan price,
            # or a variant that's uniformly 100%/0% on a binary metric) makes the
            # t-test's within-group-variance prerequisite genuinely unmet, not just
            # "very significant" -- ttest_ind_from_stats would otherwise return
            # t=inf/p=0.0 with no warning, overstating confidence in a deterministic
            # (not statistically inferred) difference. Treat as non-computable,
            # matching how insufficient sample size is already reported below.
            has_variance = control_std > 0 or treatment_std > 0
            if control.sample_size >= 2 and treatment.sample_size >= 2 and has_variance:
                _, p_value = stats.ttest_ind_from_stats(
                    mean1=treatment.mean,
                    std1=treatment_std,
                    nobs1=treatment.sample_size,
                    mean2=control.mean,
                    std2=control_std,
                    nobs2=control.sample_size,
                )
                is_significant = float(p_value) < (1 - 0.95)

        elif experiment.stat_method == "bayesian":
            # Beta-Binomial conjugate for conversion metrics, using the warehouse's
            # real (clamped) conversion_count — not a reconstructed array.
            control_total = control.sample_size
            treat_total = treatment.sample_size

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
            probability_beats_control=round(prob_beats_control, 4)
            if prob_beats_control is not None
            else None,
            is_significant=is_significant,
        )

    def recommend(
        self,
        results: list[MetricResult],
        n_control: int | None = None,
        n_treatment: int | None = None,
        expected_ratio: float = 0.5,
    ) -> str:
        """
        BLOCKED_SRM: n_control/n_treatment were supplied and their split is
                     statistically incompatible with expected_ratio (EXP-2)
                     -- checked FIRST, before any metric result, since a
                     broken traffic split invalidates every metric computed
                     on top of it. n_control/n_treatment are optional
                     (default None) so a caller with no sample-size figures
                     handy keeps today's exact behavior.
        SHIP: all primary metrics significant and positive.
        NO_SHIP: any primary metric significant and negative.
        CONTINUE: insufficient data or mixed signals.
        """
        if n_control is not None and n_treatment is not None:
            srm = srm_check(n_control, n_treatment, expected_ratio)
            if srm.is_mismatch:
                return "BLOCKED_SRM"

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
        Rejects null when the e-value (likelihood ratio) exceeds 1/alpha.

        Always-valid confidence intervals:
            CI = delta +/- sqrt(2 * var_pooled * (1/n_c + 1/n_t) * log(2/(alpha * rho)))
        where rho = current sample fraction used for anytime-valid bounds.

        Returns e_value and ci bounds as extra fields embedded in metric_name
        for downstream display.

        No production callers as of EXP-1 PR2 — `/analyze`'s "sequential"
        path now calls `analyze_sequential_from_stats()` instead. Retained
        as the ground-truth reference implementation for that method's
        fidelity tests (see TestSequentialFromStats in
        test_experiment_analyzer.py) and to document, via its own output,
        the exact reconstruction bug that method fixes.
        """
        c = np.array(control_data)
        t = np.array(treatment_data)

        if len(c) < 2 or len(t) < 2:
            from app.experiments.models import VariantStats, MetricResult

            cs = VariantStats(
                "control", len(c), float(np.mean(c)) if len(c) else 0, 0, None
            )
            ts = VariantStats(
                "treatment", len(t), float(np.mean(t)) if len(t) else 0, 0, None
            )
            return MetricResult(metric_name, cs, ts, 0.0, None, None, False)

        n_c, n_t = len(c), len(t)
        mean_c, mean_t = np.mean(c), np.mean(t)
        var_pooled = (np.var(c) * n_c + np.var(t) * n_t) / (n_c + n_t)

        if var_pooled == 0:
            relative_lift = 0.0
            is_sig = False
            e_value = 1.0
            ci_lower = 0.0
            ci_upper = 0.0
            recommendation = "continue"
        else:
            delta = mean_t - mean_c
            se = np.sqrt(var_pooled * (1 / n_c + 1 / n_t))

            # mSPRT e-value: mixture likelihood ratio with normal(0, tau^2) prior.
            # e = P(delta | H1 mixture) / P(delta | H0) where, under the normal-
            # normal conjugate mixture, the marginal of delta under H1 is
            # N(0, se^2+tau^2) and under H0 is N(0, se^2). Working through that
            # ratio in closed form gives log(e) = 0.5*log(v/tau^2) + m^2/(2*v)
            # exactly (verified independently by direct substitution of v/m
            # below, and cross-checked against Monte Carlo integration) -- a
            # third term, `- delta**2/(2*se**2)`, used to be subtracted here
            # with no basis in that derivation. Since se shrinks as sample size
            # grows, that erroneous factor (exp(-delta^2/(2*se^2))) got MORE
            # punishing with more data, making true positives converge toward
            # non-significance instead of confirming them -- the opposite of
            # mSPRT's intended always-valid power (found by adversarial review
            # of EXP-2; e.g. delta=2.0, se=0.5, tau=1.0 gave e=0.090, never
            # significant, when the true e-value is 269.15).
            v = 1 / (1 / tau**2 + 1 / se**2)
            m = v * delta / se**2
            log_ratio = 0.5 * np.log(v / tau**2) + m**2 / (2 * v)
            e_value = float(np.exp(log_ratio))

            is_sig = e_value >= (1 / alpha)
            relative_lift = float(delta / abs(mean_c)) if mean_c != 0 else 0.0

            # Always-valid (anytime-valid) confidence interval via Ville's inequality.
            # Margin: sqrt(2 * sigma^2 * (1/n_c + 1/n_t) * log(2 / (alpha * rho)))
            # rho is a tuning parameter for the boundary shape; rho=alpha gives tight bounds.
            rho = alpha
            log_term = np.log(2.0 / (alpha * rho)) if alpha * rho > 0 else 0.0
            margin = (
                np.sqrt(2.0 * var_pooled * (1.0 / n_c + 1.0 / n_t) * log_term)
                if log_term > 0
                else se * 1.96
            )
            ci_lower = float(delta - margin)
            ci_upper = float(delta + margin)

            # Futility check: CI entirely within practical equivalence zone (+/-1% of control)
            equiv_zone = abs(mean_c) * 0.01 if mean_c != 0 else 0.001
            futile = abs(ci_upper) <= equiv_zone and abs(ci_lower) <= equiv_zone

            if is_sig:
                recommendation = "stop_significant"
            elif futile:
                recommendation = "stop_futility"
            else:
                recommendation = "continue"

        from app.experiments.models import VariantStats, MetricResult

        cs = VariantStats(
            "control", n_c, float(mean_c), float(np.std(c)), float(np.mean(c > 0))
        )
        ts = VariantStats(
            "treatment", n_t, float(mean_t), float(np.std(t)), float(np.mean(t > 0))
        )

        # Encode e-value, CI, and recommendation into metric_name suffix for API consumers
        # (MetricResult dataclass has no dedicated fields for these; keep model stable)
        suffix = (
            f"_msprt|e={e_value:.4f}"
            f"|ci=[{ci_lower:.4f},{ci_upper:.4f}]"
            f"|{recommendation}"
        )
        return MetricResult(
            metric_name=f"{metric_name}{suffix}",
            control=cs,
            treatment=ts,
            relative_lift=round(relative_lift, 4),
            p_value=None,
            probability_beats_control=None,
            is_significant=is_sig,
        )

    def analyze_sequential_from_stats(
        self,
        control: AggregatedMetric,
        treatment: AggregatedMetric,
        metric_name: str,
        alpha: float = 0.05,
        tau: float = 1.0,
    ) -> MetricResult:
        """
        mSPRT sequential testing directly from warehouse-computed sufficient
        statistics (n, mean, variance, conversion_count) — no per-user array
        reconstruction. See `analyze_from_stats()` for the general bug this
        fixes.

        For mSPRT specifically, the reconstructed `[mean] * sample_size`
        array makes `var_pooled` exactly 0.0 (every element identical), which
        already forces this method's own pre-existing `var_pooled == 0`
        guard to report `is_significant=False`/`recommendation="continue"`
        unconditionally — so mSPRT via `/analyze`'s warehouse-aggregate path
        has never been able to report anything OTHER than "continue",
        regardless of the real e-value, for as long as it's existed. Feeding
        the warehouse's real per-variant variance restores that guard to
        only fire for genuinely zero-variance data (its actual intent).
        """
        n_c, n_t = control.sample_size, treatment.sample_size

        if n_c < 2 or n_t < 2:
            cs = VariantStats(
                "control", n_c, float(control.mean) if n_c else 0.0, 0.0, None
            )
            ts = VariantStats(
                "treatment", n_t, float(treatment.mean) if n_t else 0.0, 0.0, None
            )
            return MetricResult(metric_name, cs, ts, 0.0, None, None, False)

        mean_c, mean_t = control.mean, treatment.mean
        control_conv = max(0, min(control.conversion_count, n_c))
        treat_conv = max(0, min(treatment.conversion_count, n_t))

        # Convert the warehouse's sample variance (ddof=1, from SQL VARIANCE())
        # to population variance (ddof=0), matching analyze_sequential()'s own
        # bare np.var() convention exactly, so var_pooled means the same thing
        # here as it always has for this method.
        var_c_pop = control.variance * (n_c - 1) / n_c if control.variance > 0 else 0.0
        var_t_pop = (
            treatment.variance * (n_t - 1) / n_t if treatment.variance > 0 else 0.0
        )
        var_pooled = (var_c_pop * n_c + var_t_pop * n_t) / (n_c + n_t)

        if var_pooled == 0:
            relative_lift = 0.0
            is_sig = False
            e_value = 1.0
            ci_lower = 0.0
            ci_upper = 0.0
            recommendation = "continue"
        else:
            delta = mean_t - mean_c
            se = np.sqrt(var_pooled * (1 / n_c + 1 / n_t))

            # mSPRT e-value: mixture likelihood ratio with normal(0, tau^2) prior.
            # See analyze_sequential's identical formula above for the full
            # derivation note -- this method mirrors it exactly (same bug,
            # same fix, per this method's own docstring below).
            v = 1 / (1 / tau**2 + 1 / se**2)
            m = v * delta / se**2
            log_ratio = 0.5 * np.log(v / tau**2) + m**2 / (2 * v)
            e_value = float(np.exp(log_ratio))

            is_sig = e_value >= (1 / alpha)
            relative_lift = float(delta / abs(mean_c)) if mean_c != 0 else 0.0

            rho = alpha
            log_term = np.log(2.0 / (alpha * rho)) if alpha * rho > 0 else 0.0
            margin = (
                np.sqrt(2.0 * var_pooled * (1.0 / n_c + 1.0 / n_t) * log_term)
                if log_term > 0
                else se * 1.96
            )
            ci_lower = float(delta - margin)
            ci_upper = float(delta + margin)

            equiv_zone = abs(mean_c) * 0.01 if mean_c != 0 else 0.001
            futile = abs(ci_upper) <= equiv_zone and abs(ci_lower) <= equiv_zone

            if is_sig:
                recommendation = "stop_significant"
            elif futile:
                recommendation = "stop_futility"
            else:
                recommendation = "continue"

        cs = VariantStats(
            "control",
            n_c,
            float(mean_c),
            float(np.sqrt(var_c_pop)),
            control_conv / n_c,
        )
        ts = VariantStats(
            "treatment",
            n_t,
            float(mean_t),
            float(np.sqrt(var_t_pop)),
            treat_conv / n_t,
        )

        suffix = (
            f"_msprt|e={e_value:.4f}"
            f"|ci=[{ci_lower:.4f},{ci_upper:.4f}]"
            f"|{recommendation}"
        )
        return MetricResult(
            metric_name=f"{metric_name}{suffix}",
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
