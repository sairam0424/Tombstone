"""
Experiment collision detection.

Two experiments "collide" if they share enough of their user populations that
results could be confounded.  The severity ladder:

    overlap >= 0.9  → blocked   (auto-reject; near-total overlap)
    overlap >= 0.7  → warning   (human review required)
    overlap <  0.7  → clean     (no collision)

Population overlap is estimated via a Jaccard-like score based on:
  1. Rollout percentages (the primary signal).
  2. Environment match (different environments = zero overlap).
  3. Targeting rule similarity (future extension point).

For targeting rules the current implementation uses a simple set-intersection
on rule dict fingerprints.  A richer implementation would parse rule ASTs and
compute semantic overlap, but that requires the full flag schema and is left
as a future enhancement.
"""
from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, List


@dataclass
class ExperimentSpec:
    flag_key: str
    environment: str
    rollout_pct: float              # 0.0 – 100.0
    targeting_rules: list = field(default_factory=list)  # list of rule dicts


def _targeting_rule_similarity(
    rules_a: list[Any],
    rules_b: list[Any],
) -> float:
    """
    Estimate how similar two sets of targeting rules are.

    Returns a float in [0, 1]:
        1.0  — identical rules (or both empty → everyone targeted)
        0.0  — completely disjoint rules (non-overlapping user segments)
        0.5  — partially overlapping (default when uncertain)

    The current implementation fingerprints each rule dict via its sorted
    string representation and computes a Jaccard score on the fingerprint sets.
    """
    if not rules_a and not rules_b:
        # Both have no targeting rules → both target 100% of the population
        return 1.0

    if not rules_a or not rules_b:
        # One has no rules (catch-all), the other restricts.  Assume partial overlap.
        return 0.5

    fp_a = {str(sorted(r.items())) if isinstance(r, dict) else str(r) for r in rules_a}
    fp_b = {str(sorted(r.items())) if isinstance(r, dict) else str(r) for r in rules_b}

    intersection = fp_a & fp_b
    union = fp_a | fp_b

    return len(intersection) / len(union) if union else 1.0


def jaccard_overlap(a: ExperimentSpec, b: ExperimentSpec) -> float:
    """
    Estimate the population overlap between two experiments as a value in [0, 1].

    Logic:
        1. Different environments → 0.0 (no shared users).
        2. Base rollout overlap = min(a.pct, b.pct) / max(a.pct, b.pct).
        3. Targeting rule similarity scales the base overlap:
               overlap = base * rule_similarity

    The result is a conservative lower-bound estimate; the true overlap could
    be higher if targeting rules cover the same user attributes.
    """
    if a.rollout_pct == 0 or b.rollout_pct == 0:
        return 0.0

    if a.environment != b.environment:
        return 0.0

    base = min(a.rollout_pct, b.rollout_pct) / max(a.rollout_pct, b.rollout_pct)
    rule_sim = _targeting_rule_similarity(a.targeting_rules, b.targeting_rules)

    return float(base * rule_sim)


def detect_collisions(
    new_exp: ExperimentSpec,
    active_experiments: List[ExperimentSpec],
    threshold: float = 0.7,
) -> List[dict]:
    """
    Find all active experiments that collide with the proposed new experiment.

    An experiment collides when its estimated population overlap with `new_exp`
    meets or exceeds `threshold` (default 0.7).

    Args:
        new_exp:            The experiment being evaluated for launch.
        active_experiments: Experiments currently running.
        threshold:          Minimum overlap score to flag as a collision.

    Returns:
        List of collision dicts, sorted by overlap_score descending:
            {
                "flag_key":      str,
                "overlap_score": float,   # 0.0–1.0 rounded to 3 dp
                "blocked":       bool,    # True when overlap >= 0.9
                "warning":       bool,    # True when 0.7 <= overlap < 0.9
            }
        Empty list if no collisions detected.
    """
    collisions = []
    for exp in active_experiments:
        if exp.flag_key == new_exp.flag_key:
            # Skip self-comparison
            continue
        overlap = jaccard_overlap(new_exp, exp)
        if overlap >= threshold:
            collisions.append(
                {
                    "flag_key": exp.flag_key,
                    "overlap_score": round(overlap, 3),
                    "blocked": overlap >= 0.9,
                    "warning": 0.7 <= overlap < 0.9,
                }
            )

    return sorted(collisions, key=lambda x: x["overlap_score"], reverse=True)
