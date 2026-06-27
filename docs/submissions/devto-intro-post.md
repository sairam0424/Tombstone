# I Built a Self-Hosted Feature Flag Platform That Auto-Rolls Back Bad Flags — Here's Why

After reading about the Knight Capital incident one too many times, I got frustrated with every feature flag tool I'd used. They all answer the same question well: "what's the value of this flag?" None of them answer the question I actually need during a 3am incident: "which of my 5,000 flags is causing this?"

So I built Tombstone.

## What it does differently

Every OSS flag platform I've seen is a delivery system. Flag state in, evaluation result out. Tombstone adds a safety layer on top of that:

**Circuit-breaker auto-rollback.** When a flag causes >5% errors over 100 requests in a 10-second window, it disables automatically. No pager. No runbook. MTTR goes from "however long it takes your on-call to wake up" to ~30 seconds.

**Blast radius scoring.** Before you change a flag, you see its tier: BLOCKED, HIGH, MEDIUM, or LOW. BLOCKED changes (flags touching >50% of traffic with a poor error history) require a written justification before you can proceed. Deliberate friction for high-risk changes.

**"What Changed?" incident query.** Given an incident timestamp, returns flags that changed in the preceding window ranked by blast radius. One API call instead of 20 minutes of log archaeology.

## Quick start

```bash
git clone https://github.com/sairam0424/Tombstone
cp infra/.env.example infra/.env  # zero changes needed
make dev                           # dashboard at localhost:3000
```

All 8 services start in one command. PostgreSQL, Redis, Kafka, everything included.

## The honest tradeoffs

It's a complex stack. 8 services isn't right for every team. For small teams, Unleash or Flipt are better choices. Tombstone earns its complexity when you're managing 500+ flags across multiple services and production reliability is a first-class concern.

The ML rollout recommendations (Thompson Sampling + LinUCB contextual bandit) need ~50 observations per flag before they kick in. New flags get "insufficient data" and the system steps aside — the right behavior, but worth knowing.

**Stack:** Go (flag-api, gateway, evaluator) + Python 3.12 (intelligence/ML) + TypeScript (SDKs, dashboard). MIT licensed.

GitHub: https://github.com/sairam0424/Tombstone

---

*Tags: devops, go, opensource, featureflags*
