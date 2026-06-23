---
kind: signal
category: observation
frequency: once
sources: [engineering]
domain: [flag-cleanup, incident-response, rollout-advisor, governance]
status: reviewed
---

Tombstone v2.1.0 is complete. All 32 items from the 10-phase beast/ultimate upgrade plan are implemented and on main.

## Summary of what shipped
- 10 phases, 32 features, 223 files changed, 30,449 insertions
- 4 autonomous domain loops wired and ready to activate
- Complete loop-engineer harness installed
- All CI checks passing on public repo (unlimited minutes)

**Remaining activations needed (manual, 5 min):**
- Set GitHub Actions repo variables: TOMBSTONE_INTELLIGENCE_URL, TOMBSTONE_EVALUATOR_URL, TOMBSTONE_API_URL
- Set SLACK_BOT_TOKEN + SLACK_SIGNING_SECRET for Slack app
- Set ANTHROPIC_API_KEY for Argos rule generation

## Timeline
2026-06-24 | v2.1.0 shipped. All 10 phases complete. Loop harness deployed.
