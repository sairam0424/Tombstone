from dataclasses import dataclass, field


@dataclass
class EvaluationContext:
    user_id: str
    org_id: str = ""
    attrs: dict = field(default_factory=dict)


@dataclass
class EvaluationResult:
    value: object
    reason: str
    from_cache: bool
    flag_key: str


@dataclass
class FlagEnvironmentState:
    flag_key: str
    enabled: bool
    rollout_pct: float
    safe_default: object
    environment: str


@dataclass
class FlagSnapshot:
    environment: str
    flags: list
    hash: str
    ts: int
