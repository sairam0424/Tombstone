from __future__ import annotations

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
class PropertyCondition:
    attribute: str
    operator: str   # eq, neq, contains, startsWith, endsWith, semver_gt, semver_gte,
                    # semver_lt, semver_lte, semver_eq, date_before, date_after,
                    # gt, gte, lt, lte, in, nin
    values: list    # always a list; single-value operators use values[0]
    negate: bool = False


@dataclass
class TargetingRule:
    id: str
    conditions: list[PropertyCondition]
    rollout_pct: float
    variation: object  # the value to return when this rule matches


@dataclass
class FlagEnvironmentState:
    flag_key: str
    enabled: bool
    rollout_pct: float
    safe_default: object
    environment: str
    targeting_rules: list[TargetingRule] = field(default_factory=list)
    prerequisites: list[dict] = field(default_factory=list)
    # prerequisites schema: [{"flag_key": str, "required_value": bool}]


@dataclass
class FlagSnapshot:
    environment: str
    flags: list
    hash: str
    ts: int
