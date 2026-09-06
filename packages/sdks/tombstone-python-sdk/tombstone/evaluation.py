# packages/sdks/flagmind-python/tombstone/evaluation.py
"""Tombstone 5-step evaluation pipeline — mirrors TypeScript evaluation.ts.

Step 1: Preliminary checks (flag missing, disabled)
Step 2: Prerequisites (gate flags must evaluate to required_variation)
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

FNV_OFFSET = 2166136261
FNV_PRIME = 16777619


def _fnv1a_raw(s: str) -> int:
    """FNV-1a 32-bit hash, returns raw 32-bit unsigned int. Single pass."""
    h = FNV_OFFSET
    for c in s.encode("utf-8"):
        h ^= c
        h = (h * FNV_PRIME) & 0xFFFFFFFF
    return h & 0xFFFFFFFF


def _is_in_rollout_fnv(flag_key: str, user_id: str, rollout_pct: float) -> bool:
    """Double-pass FNV-1a rollout check — exact TypeScript hashVersion=2 equivalent.

    TypeScript: (fnv(String(fnv(flagKey + userId))) % 10000) / 10000 < rolloutPct / 100
    Resolution: 0.01% (10000 buckets), not 1% (100 buckets).
    """
    h1 = _fnv1a_raw(flag_key + user_id)
    h2 = _fnv1a_raw(str(h1))
    return (h2 % 10000) / 10000 < rollout_pct / 100


def _stringify_variation(value: object) -> str:
    """Render an evaluated flag value the same way Java's PrerequisiteChecker
    does (String.valueOf) so a prerequisite's required_variation -- always a
    wire string, e.g. "true"/"false" -- compares equal across languages.

    Python's str(True) is "True" (capital T), unlike Java's
    String.valueOf(true) or Ruby/JS's native lowercase boolean stringification
    -- comparing that directly against the wire's lowercase "true"/"false"
    convention would silently and permanently fail every boolean-flag
    prerequisite check, exactly the class of bug this function exists to
    avoid reintroducing.
    """
    if isinstance(value, bool):
        return "true" if value else "false"
    return str(value)


def _parse_safe_default(safe_default: object, fallback: object) -> object:
    """Canonical OFF-path default: parse safe_default into the target type,
    falling back to the CALLER's own default on any type mismatch. Matches
    Java's EvaluationEngine.parseSafeDefault (documented there as "the
    canonical model") and TypeScript's evaluation.ts parseSafeDefault exactly.

    Before this fix, Step 1's OFF-path returned default_value unconditionally
    and never consulted safe_default at all -- the only one of the 4 SDKs
    that didn't. Dormant at the top level (every existing test's fixture
    happened to set safe_default equal to its own default_value), but a real
    divergence once _check_prerequisites' recursive evaluate() call means a
    disabled DEPENDENCY's own configured safe_default -- not the caller's
    hardcoded literal False -- determines whether a required_variation
    comparison passes (found by adversarial review of this PR).

    safe_default is typed as `object`, not `str`, throughout this codebase:
    flag-api's real wire value is always a string (schema.sql: safe_default
    TEXT NOT NULL DEFAULT 'false'), but this SDK's own dataclass defaults
    and SSE event-merge fallbacks use a bare Python bool. Both are handled.
    """
    if isinstance(fallback, bool):
        if isinstance(safe_default, bool):
            return safe_default
        if isinstance(safe_default, str):
            return safe_default == "true"
        return fallback
    if isinstance(fallback, (int, float)):
        if isinstance(safe_default, (int, float)):
            return safe_default
        if isinstance(safe_default, str):
            try:
                return float(safe_default)
            except ValueError:
                return fallback
        return fallback
    if isinstance(fallback, str):
        return safe_default if isinstance(safe_default, str) else fallback
    return fallback


def _check_prerequisites(
    flag_state: FlagEnvironmentState,
    context: EvaluationContext,
    all_flags: dict[str, FlagEnvironmentState],
    evaluation_cache: dict[str, str | None],
    flag_key: str,
    seen_keys: set[str] | None = None,
) -> bool:
    """Return True if all prerequisites are satisfied, False otherwise.

    Uses evaluation_cache to memoize results — each dependent flag is
    evaluated at most once per top-level call (PostHog shipped pattern).
    seen_keys tracks the current dependency chain to detect and break cycles.

    Prerequisite dict keys match the real wire contract (flag-api's
    SnapshotPrerequisite / proto's ParentCondition): "flag_key" (NOT
    "prereq_flag_key" -- that name is flag_prerequisites' own DB column,
    never the REST/snapshot JSON key) and "required_variation" (a string,
    e.g. "true"/"false"/a variation key -- NOT a bool under "required_value",
    which was never a real key any backend sends). Before this fix both
    reads used the wrong key name and dict.get's fallback default silently
    stood in for every real prerequisite: dep_key resolved to "" (no flag
    is ever keyed by empty string, so every dependency lookup missed) and
    required defaulted to True regardless of the real required_variation,
    so any flag with a hard-gated (gate=True, the default) prerequisite was
    unconditionally blocked, and any flag with a soft-gated one was
    unconditionally skipped -- prerequisite gating did not work at all
    against a real backend (found while investigating SDK-4's
    prerequisites-streaming follow-up).
    """
    if seen_keys is None:
        seen_keys = set()
    seen_keys = seen_keys | {flag_key}  # immutable update — don't mutate caller's set

    for prereq in flag_state.prerequisites:
        dep_key = prereq.get("flag_key", "")
        required_variation = prereq.get("required_variation", "true")
        gate = prereq.get("gate", True)

        if dep_key in evaluation_cache:
            dep_variation = evaluation_cache[dep_key]
        elif dep_key in seen_keys:
            # Cycle detected — fail-open (skip this prereq, treat as satisfied)
            logger.debug(
                "Circular prerequisite detected: '%s' already in chain %s",
                dep_key,
                seen_keys,
            )
            continue
        else:
            dep_flag = all_flags.get(dep_key)
            if dep_flag is None:
                logger.debug("Prerequisite flag '%s' not found in snapshot", dep_key)
                dep_variation = None
            else:
                dep_eval = evaluate(
                    dep_flag,
                    context,
                    False,
                    dep_key,
                    all_flags=all_flags,
                    evaluation_cache=evaluation_cache,
                    _seen_keys=seen_keys,
                )
                dep_variation = _stringify_variation(dep_eval.value)
            evaluation_cache[dep_key] = dep_variation

        if dep_variation != required_variation:
            if not gate:
                # Soft prerequisite — unmet, but non-blocking. Skip and continue.
                continue
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
            logger.debug(
                "Rule '%s' inconclusive for flag '%s': %s", rule.id, flag_key, exc
            )
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
    evaluation_cache: dict[str, str | None] | None = None,
    _seen_keys: set[str] | None = None,
) -> EvaluationResult:
    """5-step evaluation pipeline.

    all_flags: full snapshot dict, required for prerequisite evaluation.
               If None, prerequisites are skipped (safe for callers that
               don't need prerequisite support).
    evaluation_cache: mutable dict passed through recursive prerequisite
                      calls to prevent redundant re-evaluation.
    _seen_keys: internal — tracks the current prerequisite chain for cycle detection.
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
            value=_parse_safe_default(flag_state.safe_default, default_value),
            reason="OFF",
            from_cache=True,
            flag_key=flag_key,
        )

    # ── Step 2: Prerequisites ─────────────────────────────────────────────────
    if flag_state.prerequisites:
        prereqs_met = _check_prerequisites(
            flag_state,
            context,
            all_flags,
            evaluation_cache,
            flag_key,
            seen_keys=_seen_keys,
        )
        if not prereqs_met:
            return EvaluationResult(
                value=default_value,
                reason="PREREQUISITE_FAILED",
                from_cache=True,
                flag_key=flag_key,
            )

    # ── Step 3: Individual targeting — explicit target list ───────────────────
    if flag_state.target_list and context.user_id in flag_state.target_list:
        return EvaluationResult(
            value=True, reason="TARGET_MATCH", from_cache=True, flag_key=flag_key
        )

    # ── Step 4: Targeting rules (sorted by priority ascending, 0 = highest) ──
    if flag_state.targeting_rules:
        sorted_rules = sorted(flag_state.targeting_rules, key=lambda r: r.priority)
        rule_result = _match_targeting_rules(sorted_rules, context, flag_key)
        if rule_result is not None:
            return rule_result

    # ── Step 5: Fallthrough rollout ───────────────────────────────────────────
    if flag_state.rollout_pct >= 100:
        return EvaluationResult(
            value=True, reason="FALLTHROUGH", from_cache=True, flag_key=flag_key
        )

    if flag_state.rollout_pct <= 0:
        return EvaluationResult(
            value=default_value,
            reason="FALLTHROUGH",
            from_cache=True,
            flag_key=flag_key,
        )

    if flag_state.hash_version == 2:
        in_rollout = _is_in_rollout_fnv(
            flag_key, context.user_id, flag_state.rollout_pct
        )
    else:
        bucket = mmh3.hash(flag_key + context.user_id, seed=0, signed=False) % 100
        in_rollout = bucket < flag_state.rollout_pct

    return EvaluationResult(
        value=True if in_rollout else default_value,
        reason="FALLTHROUGH",
        from_cache=True,
        flag_key=flag_key,
    )
