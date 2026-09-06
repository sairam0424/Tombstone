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
    operator: str  # eq, neq, contains, startsWith, endsWith, semver_gt, semver_gte,
    # semver_lt, semver_lte, semver_eq, date_before, date_after,
    # gt, gte, lt, lte, in, nin
    values: list  # always a list; single-value operators use values[0]
    negate: bool = False


@dataclass
class TargetingRule:
    id: str
    conditions: list[PropertyCondition]
    rollout_pct: float
    variation: object  # the value to return when this rule matches
    priority: int = 0


@dataclass
class FlagEnvironmentState:
    flag_key: str
    enabled: bool
    rollout_pct: float
    safe_default: object
    environment: str
    targeting_rules: list[TargetingRule] = field(default_factory=list)
    prerequisites: list[dict] = field(default_factory=list)
    # prerequisites schema: [{"flag_key": str, "required_variation": str, "gate": bool}]
    # Matches proto's ParentCondition / flag-api's SnapshotPrerequisite wire shape
    # exactly -- required_variation is always a string (e.g. "true"/"false"), not
    # a bool, since it must also support future multivariate prerequisites.
    # "gate" defaults to True (hard-blocking) when omitted, preserving legacy behavior.
    # gate=False marks a soft prerequisite: an unmet dependency is skipped rather
    # than failing the whole evaluation.
    hash_version: int = 1
    target_list: list = field(default_factory=list)


@dataclass
class FlagSnapshot:
    environment: str
    flags: list
    hash: str
    ts: int
