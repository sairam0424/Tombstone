import mmh3

from tombstone.types import EvaluationContext, EvaluationResult, FlagEnvironmentState


def evaluate(
    flag_state: FlagEnvironmentState | None,
    context: EvaluationContext,
    default_value: object,
    flag_key: str,
) -> EvaluationResult:
    if flag_state is None:
        return EvaluationResult(
            value=default_value,
            reason="ERROR",
            from_cache=False,
            flag_key=flag_key,
        )

    if not flag_state.enabled:
        return EvaluationResult(
            value=default_value,
            reason="OFF",
            from_cache=True,
            flag_key=flag_key,
        )

    if flag_state.rollout_pct >= 100:
        return EvaluationResult(
            value=True,
            reason="FALLTHROUGH",
            from_cache=True,
            flag_key=flag_key,
        )

    if flag_state.rollout_pct <= 0:
        return EvaluationResult(
            value=default_value,
            reason="FALLTHROUGH",
            from_cache=True,
            flag_key=flag_key,
        )

    bucket = mmh3.hash(flag_key + context.user_id, seed=0, signed=False) % 100
    in_rollout = bucket < flag_state.rollout_pct

    return EvaluationResult(
        value=True if in_rollout else default_value,
        reason="FALLTHROUGH",
        from_cache=True,
        flag_key=flag_key,
    )
