from dataclasses import dataclass, field
from typing import Literal


MetricType = Literal["conversion", "revenue", "latency", "error_rate", "custom"]
StatMethod = Literal["frequentist", "bayesian", "sequential", "cuped"]


@dataclass
class ExperimentMetric:
    name: str
    metric_type: MetricType
    sql_expression: str      # SQL fragment selecting the metric value per user
    higher_is_better: bool = True


@dataclass
class ExperimentDefinition:
    id: str
    flag_key: str            # The feature flag driving the experiment
    control_variant: str     # Value of the flag for control (e.g. "false")
    treatment_variant: str   # Value of the flag for treatment (e.g. "true")
    metrics: list[ExperimentMetric] = field(default_factory=list)
    stat_method: StatMethod = "bayesian"
    min_sample_size: int = 100
    created_at: int = 0


@dataclass
class VariantStats:
    variant: str
    sample_size: int
    mean: float
    std: float
    conversion_rate: float | None = None


@dataclass
class MetricResult:
    metric_name: str
    control: VariantStats
    treatment: VariantStats
    relative_lift: float         # (treatment_mean - control_mean) / control_mean
    p_value: float | None        # frequentist only
    probability_beats_control: float | None  # bayesian: P(treatment > control)
    is_significant: bool
    confidence_level: float = 0.95


@dataclass
class ExperimentResult:
    experiment_id: str
    flag_key: str
    total_users: int
    metrics: list[MetricResult] = field(default_factory=list)
    recommendation: str = ""     # "SHIP" | "NO_SHIP" | "CONTINUE"
    summary: str = ""
