# packages/sdks/flagmind-python/tombstone/evaluation.py
"""Tombstone 5-step evaluation pipeline — mirrors TypeScript evaluation.ts.

Step 1: Preliminary checks (flag missing, disabled)
Step 2: Prerequisites (gate flags must evaluate to required_value)
Step 3: Individual targeting rules (attribute-based rule matching)
Step 4: Rule matching fallthrough — not used here; rules already checked in step 3
Step 5: Fallthrough rollout (MurmurHash3 bucket)
"""
from __future__ import annotations

import logging

import mmh3

from tombstone.exceptions import InconclusiveMatchError
from tombstone.matching import match_property
from tombstone.types import (
    EvaluationContext,
    EvaluationResult,
    FlagEnvironmentState,
    TargetingRule,
)

logger = logging.getLogger(__name__)


def _check_prerequisites(
    flag_state: FlagEnvironmentState,
    context: EvaluationContext,
    all_flags: dict[str, FlagEnvironmentState],
    evaluation_cache: dict[str, bool],
    flag_key: str,
) -> bool:
    """Return True if all prerequisites are satisfied, False otherwise.

    Uses evaluation_cache to memoize results — each dependent flag is
    evaluated at most once per top-level call (PostHog shipped pattern).
    """
    for prereq in flag_state.prerequisites:
        dep_key = prereq.get("flag_key", "")
        required = prereq.get("required_value", True)

        if dep_key in evaluation_cache:
            dep_result = evaluation_cache[dep_key]
        else:
            dep_flag = all_flags.get(dep_key)
            if dep_flag is None:
                logger.debug("Prerequisite flag '%s' not found in snapshot", dep_key)
                evaluation_cache[dep_key] = False
                dep_result = False
            else:
                dep_eval = evaluate(
                    dep_flag, context, False, dep_key,
                    all_flags=all_flags,
                    evaluation_cache=evaluation_cache,
                )
                dep_result = bool(dep_eval.value)
                evaluation_cache[dep_key] = dep_result

        if dep_result != required:
            return False

    return True


def _match_targeting_rules(
    rules: list[TargetingRule],
    context: EvaluationContext,
    flag_key: str,
) -> EvaluationResult | None:
    """Return EvaluationResult if any rule matches, None if no rule matches."""
    for rule in rules:
        try:
            all_conditions_met = all(
                match_property(cond, context) for cond in rule.conditions
            )
        except InconclusiveMatchError as exc:
            logger.debug("Rule '%s' inconclusive for flag '%s': %s", rule.id, flag_key, exc)
            continue  # try next rule

        if all_conditions_met:
            # Rule matched — check rollout within the matched cohort
            bucket = mmh3.hash(flag_key + context.user_id, seed=0, signed=False) % 100
            if bucket < rule.rollout_pct:
                return EvaluationResult(
                    value=rule.variation,
                    reason="RULE_MATCH",
                    from_cache=True,
                    flag_key=flag_key,
                )

    return None


def evaluate(
    flag_state: FlagEnvironmentState | None,
    context: EvaluationContext,
    default_value: object,
    flag_key: str,
    all_flags: dict[str, FlagEnvironmentState] | None = None,
    evaluation_cache: dict[str, bool] | None = None,
) -> EvaluationResult:
    """5-step evaluation pipeline.

    all_flags: full snapshot dict, required for prerequisite evaluation.
               If None, prerequisites are skipped (safe for callers that
               don't need prerequisite support).
    evaluation_cache: mutable dict passed through recursive prerequisite
                      calls to prevent redundant re-evaluation.
    """
    # Initialise cache on first call
    if evaluation_cache is None:
        evaluation_cache = {}
    if all_flags is None:
        all_flags = {}

    # ── Step 1: Preliminary checks ────────────────────────────────────────────
    if flag_state is None:
        return EvaluationResult(
            value=default_value, reason="ERROR", from_cache=False, flag_key=flag_key
        )

    if not flag_state.enabled:
        return EvaluationResult(
            value=default_value, reason="OFF", from_cache=True, flag_key=flag_key
        )

    # ── Step 2: Prerequisites ─────────────────────────────────────────────────
    if flag_state.prerequisites:
        prereqs_met = _check_prerequisites(
            flag_state, context, all_flags, evaluation_cache, flag_key
        )
        if not prereqs_met:
            return EvaluationResult(
                value=default_value,
                reason="PREREQUISITE_FAILED",
                from_cache=True,
                flag_key=flag_key,
            )

    # ── Step 3: Individual targeting rules ────────────────────────────────────
    if flag_state.targeting_rules:
        rule_result = _match_targeting_rules(flag_state.targeting_rules, context, flag_key)
        if rule_result is not None:
            return rule_result

    # ── Step 5: Fallthrough rollout (MurmurHash3) ─────────────────────────────
    if flag_state.rollout_pct >= 100:
        return EvaluationResult(
            value=True, reason="FALLTHROUGH", from_cache=True, flag_key=flag_key
        )

    if flag_state.rollout_pct <= 0:
        return EvaluationResult(
            value=default_value, reason="FALLTHROUGH", from_cache=True, flag_key=flag_key
        )

    bucket = mmh3.hash(flag_key + context.user_id, seed=0, signed=False) % 100
    in_rollout = bucket < flag_state.rollout_pct

    return EvaluationResult(
        value=True if in_rollout else default_value,
        reason="FALLTHROUGH",
        from_cache=True,
        flag_key=flag_key,
    )
