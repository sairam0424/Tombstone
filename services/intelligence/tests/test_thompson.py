"""
Tests for ThompsonSamplingEngine (app/rollout/thompson.py) — zero test
files of any kind existed for this module before EXP-2 PR C. Covers both
the new seeded-RNG determinism (EXP-2) and recommend()'s pre-existing gate
logic (min observations, confidence threshold, schedule advancement),
none of which had ever been exercised by any test.
"""

from __future__ import annotations

import numpy as np
import pytest

from app.rollout.thompson import (
    _CONFIDENCE_THRESHOLD,
    _MIN_OBSERVATIONS,
    ROLLOUT_SCHEDULE,
    ThompsonSamplingEngine,
)


async def _seed_posterior(
    engine: ThompsonSamplingEngine,
    flag_key: str,
    environment: str,
    successes: int,
    failures: int,
    current_rollout_pct: int,
) -> None:
    await engine.update(
        flag_key=flag_key,
        environment=environment,
        successes=successes,
        failures=failures,
        current_rollout_pct=current_rollout_pct,
    )


class TestSeededDeterminism:
    """
    EXP-2: recommend() used to construct a brand-new, unseeded
    np.random.default_rng() on every call -- confidence/sampled_success_rate
    changed on every single invocation for the identical posterior, with no
    way to write a ground-truth-pinned test or reproduce a specific
    recommendation for debugging.
    """

    @pytest.mark.asyncio
    async def test_same_seed_gives_identical_recommendations_across_engines(self):
        """Two independently-constructed engines with the same seed and the
        same posterior history must produce byte-identical recommendations
        -- proves determinism isn't an accident of call ordering."""
        engine_a = ThompsonSamplingEngine(seed=42)
        engine_b = ThompsonSamplingEngine(seed=42)
        for engine in (engine_a, engine_b):
            await _seed_posterior(
                engine,
                "checkout-v2",
                "production",
                successes=189,
                failures=1,
                current_rollout_pct=10,
            )

        rec_a = engine_a.recommend("checkout-v2", "production")
        rec_b = engine_b.recommend("checkout-v2", "production")

        assert rec_a.confidence == rec_b.confidence
        assert rec_a.sampled_success_rate == rec_b.sampled_success_rate
        assert rec_a.should_advance == rec_b.should_advance

    @pytest.mark.asyncio
    async def test_seed_42_matches_a_pinned_ground_truth_value(self):
        """
        Ground-truth check, not just self-consistency: alpha=190/beta=2
        (posterior after 189 successes + 1 failure, starting from the
        default alpha=beta=1.0) with seed=42 must reproduce the EXACT
        confidence/mean this fixture is known to produce -- computed
        directly via np.random.default_rng(42).beta(190, 2, size=1000)
        before writing this test, independent of recommend()'s own code.
        A future refactor that changes HOW the RNG is seeded/consumed
        (e.g. drawing in a different order, or an extra draw inserted
        before this one) would change these pinned numbers and fail here,
        even though test_same_seed_gives_identical_recommendations_across_engines
        above would still pass.
        """
        rng = np.random.default_rng(42)
        expected_samples = rng.beta(190, 2, size=1000)
        expected_confidence = float(np.mean(expected_samples > 0.95))
        expected_mean = float(np.mean(expected_samples))

        engine = ThompsonSamplingEngine(seed=42)
        await _seed_posterior(
            engine,
            "checkout-v2",
            "production",
            successes=189,
            failures=1,
            current_rollout_pct=10,
        )

        rec = engine.recommend("checkout-v2", "production")

        assert rec.confidence == pytest.approx(expected_confidence)
        assert rec.sampled_success_rate == pytest.approx(expected_mean)
        # Direct assertion on the posterior itself (found by adversarial
        # review of PR #217): without this, a wrong default prior or a
        # broken update() arithmetic would fail this test with a confusing
        # numeric SAMPLE mismatch rather than a clear, directly-attributable
        # posterior-value assertion.
        posterior = engine.get_posterior("checkout-v2", "production")
        assert posterior is not None
        assert posterior.alpha == 190.0
        assert posterior.beta == 2.0

    @pytest.mark.asyncio
    async def test_sequential_recommend_calls_advance_the_shared_rng_state(self):
        """
        The core EXP-2 claim this class exists to prove: self._rng is
        created ONCE in __init__ and persists/advances across calls,
        rather than recommend() reconstructing a fresh Generator (from a
        stored seed or otherwise) every time it runs. Every other test in
        this class calls recommend() at most once per engine instance, so
        none of them actually exercise this -- found by adversarial review
        of PR #217. Two successive calls on the SAME seeded engine (same
        flag, so the posterior doesn't change between them either) must
        draw DIFFERENT samples, proving the generator's internal state
        genuinely advanced.
        """
        engine = ThompsonSamplingEngine(seed=42)
        await _seed_posterior(
            engine,
            "checkout-v2",
            "production",
            successes=30,
            failures=30,
            current_rollout_pct=10,
        )

        first = engine.recommend("checkout-v2", "production")
        second = engine.recommend("checkout-v2", "production")

        assert first.sampled_success_rate != second.sampled_success_rate

    @pytest.mark.asyncio
    async def test_no_seed_still_produces_a_valid_recommendation(self):
        """Backward compatibility: production's real call site (app/main.py)
        constructs ThompsonSamplingEngine() with no seed at all -- must keep
        working exactly as before, just non-deterministically."""
        engine = ThompsonSamplingEngine()
        await _seed_posterior(
            engine,
            "checkout-v2",
            "production",
            successes=60,
            failures=10,
            current_rollout_pct=10,
        )

        rec = engine.recommend("checkout-v2", "production")

        assert 0.0 <= rec.confidence <= 1.0
        assert 0.0 <= rec.sampled_success_rate <= 1.0

    @pytest.mark.asyncio
    async def test_different_seeds_can_produce_different_draws(self):
        """Proves seeding actually changes the draw, not just accepted and
        silently ignored -- two different seeds over the SAME posterior
        must not always coincide."""
        results = []
        for seed in (1, 2, 3, 4, 5):
            engine = ThompsonSamplingEngine(seed=seed)
            await _seed_posterior(
                engine,
                "checkout-v2",
                "production",
                successes=30,
                failures=30,
                current_rollout_pct=10,
            )
            results.append(
                engine.recommend("checkout-v2", "production").sampled_success_rate
            )

        assert len(set(results)) > 1


class TestUpdate:
    """
    update()'s conjugate accumulation (alpha += successes, beta +=
    failures, total_observations += successes + failures) and its
    ValueError guard had zero test coverage of any kind before this class
    -- found by adversarial review of PR #217. The new _seed_posterior
    helper (used by every test above) calls update() directly, so these
    gaps were exercised implicitly but never actually verified.
    """

    @pytest.mark.asyncio
    async def test_two_successive_updates_accumulate_not_overwrite(self):
        engine = ThompsonSamplingEngine(seed=1)

        await _seed_posterior(
            engine,
            "checkout-v2",
            "production",
            successes=20,
            failures=5,
            current_rollout_pct=10,
        )
        await _seed_posterior(
            engine,
            "checkout-v2",
            "production",
            successes=15,
            failures=3,
            current_rollout_pct=25,
        )

        posterior = engine.get_posterior("checkout-v2", "production")
        assert posterior is not None
        # Default prior alpha=beta=1.0, then two updates on top of it --
        # a bug that overwrote rather than accumulated would land on
        # alpha=16.0/beta=4.0 (only the second update) instead.
        assert posterior.alpha == 1.0 + 20 + 15
        assert posterior.beta == 1.0 + 5 + 3
        assert posterior.total_observations == (20 + 5) + (15 + 3)
        # current_rollout_pct reflects the MOST RECENT update, not the first.
        assert posterior.current_rollout_pct == 25

    @pytest.mark.asyncio
    async def test_negative_successes_raises_value_error(self):
        engine = ThompsonSamplingEngine(seed=1)

        with pytest.raises(ValueError):
            await engine.update(
                flag_key="checkout-v2",
                environment="production",
                successes=-1,
                failures=0,
                current_rollout_pct=10,
            )

    @pytest.mark.asyncio
    async def test_negative_failures_raises_value_error(self):
        engine = ThompsonSamplingEngine(seed=1)

        with pytest.raises(ValueError):
            await engine.update(
                flag_key="checkout-v2",
                environment="production",
                successes=0,
                failures=-1,
                current_rollout_pct=10,
            )


class TestRecommendGateLogic:
    """recommend()'s pre-existing gating had zero test coverage of any
    kind before this class. Every test uses a fixed seed so the specific
    confidence/success-rate values are reproducible, but these tests are
    about the GATE LOGIC (which branch fires), not the RNG itself."""

    @pytest.mark.asyncio
    async def test_unknown_flag_returns_a_conservative_default(self):
        engine = ThompsonSamplingEngine(seed=1)

        rec = engine.recommend("never-seen-flag", "production")

        assert rec.should_advance is False
        assert rec.current_pct == 0
        assert rec.recommended_pct == 0
        assert "not yet observed" in rec.reason.lower()

    @pytest.mark.asyncio
    async def test_insufficient_observations_holds_regardless_of_success_rate(self):
        """Even a flawless track record must not advance below
        _MIN_OBSERVATIONS -- gate 1 fires before confidence is even
        consulted."""
        engine = ThompsonSamplingEngine(seed=1)
        assert (
            _MIN_OBSERVATIONS > 10
        )  # sanity-check the fixture below is actually short of it
        await _seed_posterior(
            engine,
            "checkout-v2",
            "production",
            successes=10,
            failures=0,
            current_rollout_pct=5,
        )

        rec = engine.recommend("checkout-v2", "production")

        assert rec.should_advance is False
        assert rec.recommended_pct == rec.current_pct == 5
        assert "insufficient observations" in rec.reason.lower()

    @pytest.mark.asyncio
    async def test_high_confidence_with_sufficient_observations_advances(self):
        engine = ThompsonSamplingEngine(seed=42)
        await _seed_posterior(
            engine,
            "checkout-v2",
            "production",
            successes=189,
            failures=1,
            current_rollout_pct=10,
        )

        rec = engine.recommend("checkout-v2", "production")

        assert rec.confidence >= _CONFIDENCE_THRESHOLD
        assert rec.should_advance is True
        assert rec.current_pct == 10
        assert rec.recommended_pct == 25  # next step in ROLLOUT_SCHEDULE above 10
        assert rec.recommended_pct in ROLLOUT_SCHEDULE

    @pytest.mark.asyncio
    async def test_low_confidence_with_sufficient_observations_holds(self):
        engine = ThompsonSamplingEngine(seed=42)
        await _seed_posterior(
            engine,
            "checkout-v2",
            "production",
            successes=29,
            failures=29,
            current_rollout_pct=10,
        )

        rec = engine.recommend("checkout-v2", "production")

        assert rec.confidence < _CONFIDENCE_THRESHOLD
        assert rec.should_advance is False
        assert rec.recommended_pct == rec.current_pct == 10

    @pytest.mark.asyncio
    async def test_already_at_100_percent_never_advances_further(self):
        engine = ThompsonSamplingEngine(seed=42)
        await _seed_posterior(
            engine,
            "checkout-v2",
            "production",
            successes=189,
            failures=1,
            current_rollout_pct=100,
        )

        rec = engine.recommend("checkout-v2", "production")

        assert rec.should_advance is False
        assert rec.current_pct == 100
        assert rec.recommended_pct == 100
        assert "100%" in rec.reason
