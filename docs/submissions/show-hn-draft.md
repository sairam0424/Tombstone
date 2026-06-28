# Show HN Post Draft

## Title
```
Show HN: Tombstone – self-hosted feature flag platform with circuit-breaker auto-rollback
```

## First comment (post immediately after submitting — this is what drives engagement)

```
I built this after reading about Knight Capital: they lost $440M in 45 minutes because a
feature flag that should have been deleted got accidentally reactivated. The standard
industry answer is "use a kill switch" — but kill switches require a human to notice,
diagnose, and act. By then it's too late.

Tombstone adds two things I couldn't find in any OSS flag system:

1. Circuit breaker — when a flag causes >5% errors over 100 requests in a 10s window, it
   automatically rolls back without paging anyone. MTTR goes from "however long it takes
   your on-call to wake up" to seconds.

2. Blast radius scoring — before you change any flag, the evaluator tells you whether it's
   BLOCKED, HIGH, MEDIUM, or LOW risk based on traffic %, dependent flags, and historical
   incident correlation.

There's also a "What Changed?" query: given an incident timestamp, it returns the flags
that changed in the preceding window ordered by blast radius. The answer to "was it a flag?"
takes one API call instead of 20 minutes of log archaeology.

Stack: Go (flag-api, gateway, evaluator) + Python (intelligence/ML) + TypeScript (SDKs,
dashboard). Everything runs with `make dev`. MIT licensed.

Happy to answer questions about the circuit breaker implementation or the Thompson
Sampling rollout engine.
```

## Optimal posting time
- Tuesday–Thursday, 8–11am US Eastern
- Do NOT post Friday–Sunday
- Monitor comments for first 2 hours and respond to every technical question

## Expected HN questions to prepare for

1. **"How is this different from Unleash/Flagsmith/Flipt?"**
   → Focus on circuit-breaker auto-rollback (no OSS tool has this), blast-radius scoring, and the causal dependency graph. Those don't exist elsewhere in OSS.

2. **"Why not just use LaunchDarkly?"**
   → Per-seat pricing at scale ($50k+/yr for 50 engineers), data sovereignty (flags contain business logic), and LaunchDarkly doesn't have automated rollback.

3. **"How does the circuit breaker know which flag caused the error?"**
   → SDKs report evaluation events with flag key + outcome to the evaluator service. The breaker tracks per-flag error rates in a rolling window in Redis.

4. **"What's the operational overhead of running 8 services?"**
   → Honest answer: non-trivial. For small teams, start with docker-compose. For the minimal footprint, flag-api + gateway is the core; evaluator/intelligence are optional safety layers.

5. **"Is the ML rollout actually useful or is it ML for marketing?"**
   → Beta posteriors for Thompson Sampling are ~50 lines of Python. The value is removing the "manually babysit 25% rollout for a week" workflow, not the ML itself.
