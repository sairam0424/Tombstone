# Tombstone Discoverability Roadmap

> Research basis: 103-agent deep research, 102 claims extracted, 25 adversarially verified (10 killed), sources from GitHub docs, npm, PyPI, OpenFeature, CNCF, awesome-go, Linux Foundation.

Prioritized by effort-to-impact ratio. Do the top tier first — each takes under 30 minutes and directly unlocks distribution.

---

## Tier 1 — Zero-Cost, Do Today (each < 30 min)

### 1. GitHub Topics — Fill All 20 Slots

GitHub topic browse pages are a primary cross-discovery surface. Every topic is a separate browse page; users finding `circuit-breaker` or `blast-radius` see Tombstone alongside other relevant projects.

**Go to:** `github.com/sairam0424/Tombstone` → Settings → Topics → paste these 20:

```
feature-flags
feature-toggles
feature-flag-management
circuit-breaker
blast-radius
go
golang
python
typescript
devops
gitops
platform-engineering
opentelemetry
observability
self-hosted
redis
kafka
postgresql
openfeature
hacktoberfest
```

**Why these specifically:** Mirrors Unleash (19 topics) and Flipt (17 topics) strategies. `feature-flags` + `feature-toggles` as separate entries doubles browse-page surface area. `hacktoberfest` surfaces the repo every October to thousands of contributors actively looking for projects.

Also check Settings → Topics for GitHub's **auto-generated suggestions** — Tombstone is public so GitHub has already analysed the codebase. Accept relevant ones before manually filling slots.

---

### 2. Repository Description + Website

The one-liner under the repo name is indexed by GitHub search AND shown in every topic browse page card.

**Current:** _(likely empty or generic)_

**Set to:**
```
Production intelligence layer for feature flags — circuit-breaker auto-rollback, blast radius gates, ML-driven rollout, and incident correlation. Self-hosted, MIT licensed.
```

**Website field:** Set to your docs URL or `https://github.com/sairam0424/Tombstone` if no docs site yet.

---

### 3. OpenFeature — interested-parties.md PR

OpenFeature is a CNCF project. Being listed as an interested party puts Tombstone in front of every developer evaluating OpenFeature-compatible tools.

**Action:**
1. Fork `github.com/open-feature/community`
2. Add Tombstone to `interested-parties.md` — one line: `| Tombstone | https://github.com/sairam0424/Tombstone | Self-hosted production intelligence layer for feature flags |`
3. Open PR — no OFEP required, no vetting committee, just standard maintainer review

**Verified:** Real merged PRs (PR #364, #311, #510) confirm this is a standard PR with no formal gate. Takes 10 minutes.

Once the OpenFeature TypeScript provider is confirmed working, submit a second PR to `ADOPTERS.md`.

---

### 4. GitHub Social Preview Image

GitHub shows the social preview on link unfurls in Slack, Twitter, LinkedIn, Discord. Without one it shows a generic dark card — easy to scroll past.

**Size:** 1280×640px
**Content:** Tombstone logo / name + tagline + one killer stat ("Circuit-breaker auto-rollback in < 10s" or "Blast radius gates for 5,000+ flags")
**Tool:** Use `carbon.now.sh` or a Figma/Canva template → export PNG → upload at Settings → Social preview

---

### 5. npm Package Keywords

`@tombstone/core` on npm is indexed for keyword search. Add keywords to `workspace-dashboard/src/../packages/sdks/@tombstone/core/package.json`:

```json
"keywords": [
  "feature-flags",
  "feature-toggles",
  "feature-management",
  "openfeature",
  "circuit-breaker",
  "self-hosted",
  "devops",
  "typescript",
  "sdk"
]
```

Same for `@tombstone/react`, `@tombstone/edge`, `@tombstone/eval`. npm search indexes title + description + keywords — the README weight is confirmed but undocumented in rank order, so keywords field is the safe bet.

---

## Tier 2 — High Impact, 1–3 Hours Each

### 6. Show HN Post

Hacker News Show HN posts for self-hosted developer tools consistently drive thousands of GitHub stars in 24 hours when they hit the front page. The key is framing.

**Winning framing for HN:**
- Lead with the problem, not the solution: *"Knight Capital lost $440M in 45 minutes from a misconfigured feature flag. I built Tombstone to prevent this."*
- Post on a Tuesday–Thursday between 9–11am US Eastern
- Be in the comments for the first 2 hours to answer every technical question
- Do NOT cross-post simultaneously — wait until HN settles (48h) before posting elsewhere

**Title template:**
```
Show HN: Tombstone – Self-hosted feature flag platform with circuit-breaker auto-rollback and ML-driven rollout
```

---

### 7. Reddit Launches

Post in all three — same day is fine, each gets a different angle:

| Subreddit | Angle | Title |
|-----------|-------|-------|
| **r/devops** | Ops safety | "I built a feature flag system that auto-rolls back bad flags before your on-call fires" |
| **r/golang** | Technical | "Built a self-hosted feature flag platform in Go — circuit breaker, blast radius scoring, OPA RBAC" |
| **r/selfhosted** | Self-hosting | "Tombstone: self-hosted feature flags with ML rollout recommendations — make dev, all 8 services start" |
| **r/ExperiencedDevs** | War story | "After reading about Knight Capital, I built tombstoning into our flag system — here's why" |

**Rules:** Include a direct GitHub link, offer to answer questions, don't make it feel like a press release.

---

### 8. awesome-go Submission

awesome-go is a permanent, high-value backlink and the #1 discovery list for Go developers. It drives steady GitHub traffic indefinitely after listing.

**Blocking requirements (verified from CONTRIBUTING.md):**
- Go Report Card grade: **A-, A, or A+** — hard block, PR will be rejected below this
- Test coverage: **≥80%** on non-data packages

**Action:**
1. Check current grades: `https://goreportcard.com/report/github.com/sairam0424/Tombstone/services/flag-api` (and gateway, evaluator)
2. Fix any linting/vet issues — `golangci-lint run` locally until clean
3. Add Go Report Card badge to README (also improves impression on the PR)
4. Submit PR to `avelino/awesome-go` under the **DevOps Tools** category — check `https://awesome-go.com` for whether a "Feature Flags" or "Feature Management" section exists first (the research found conflicting signals; verify before writing the PR)

**Badge to add to README:**
```markdown
[![Go Report Card](https://goreportcard.com/badge/github.com/sairam0424/Tombstone)](https://goreportcard.com/report/github.com/sairam0424/Tombstone)
```

---

### 9. README Badges Row

Badges serve two purposes: credibility signals at first glance, and inbound links from badge-click traffic. Add above the Quick Start:

```markdown
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/sairam0424/Tombstone)](https://goreportcard.com/report/github.com/sairam0424/Tombstone)
[![OpenFeature](https://img.shields.io/badge/OpenFeature-compatible-blueviolet)](https://openfeature.dev)
[![CI](https://github.com/sairam0424/Tombstone/actions/workflows/ci.yml/badge.svg)](https://github.com/sairam0424/Tombstone/actions/workflows/ci.yml)
```

---

### 10. dev.to + Hashnode Launch Articles

Both platforms have large developer audiences and SEO authority that outranks personal blogs. Write **one canonical article** and cross-post with canonical URL set to your original.

**Article title options (pick the strongest):**
- *"How We Prevent Knight Capital–Style Flag Disasters with Circuit Breakers"*
- *"Feature Flags at Scale: Why You Need Blast Radius Scores, Not Just Kill Switches"*
- *"I Built an ML-Driven Feature Flag Platform — Here's What Thompson Sampling Actually Does"*

**Platform details:**
- **dev.to**: `canonical_url` frontmatter field, tags: `devops`, `go`, `opensource`, `featureflags`
- **Hashnode**: `originalArticleURL` field in settings
- **Medium**: Import from URL → auto-sets canonical to your source

---

## Tier 3 — Ecosystem Credibility (1–2 weeks)

### 11. CNCF Sandbox Application

CNCF Sandbox is the cloud-native equivalent of being listed on a prestigious index. Application is public and itself drives passive discoverability via the sandbox issues list.

**Entry point:** `github.com/cncf/sandbox/issues/new` → `application.yml` template

**Process (verified):**
- TOC reviews ~every 2 months, FIFO batches of 7–10
- Acceptance requires TOC vote + signed Contribution Agreement
- **Do not apply prematurely** — projects need demonstrated early adoption first. Apply after getting 50+ GitHub stars and at least 3–5 real users willing to be named.

**Tombstone's fit:** "Production intelligence layer for feature flags" aligns directly with CNCF's platform-engineering charter. The circuit-breaker, OPA RBAC, OTel integration, and Kubernetes operator are all CNCF-adjacent technologies.

---

### 12. OpenFeature Provider Registration

Beyond just interested-parties.md, registering Tombstone as an official OpenFeature provider puts it in the OpenFeature ecosystem index — surfaced at `openfeature.dev/ecosystem`.

**Requirements:** Implement the OpenFeature Provider interface (already done in `@tombstone/core`), pass the OpenFeature conformance test suite, submit PR to `open-feature/openfeature.dev`.

**Tombstone already has:** `TombstoneProvider` in `packages/sdks/@tombstone/core/src/provider.ts` — verify it passes the conformance suite, then register.

---

### 13. Conference CFPs

**KubeCon NA 2026 (Atlanta, Nov 9–12):** CFP closed May 31. ❌ Missed.

**Next actionable windows:**
- **KubeCon EU 2027**: CFP typically opens Aug–Sep 2026. Monitor `events.linuxfoundation.org`
- **KubeCon NA 2026 co-located events**: OpenFeature Day, Platform Engineering Day may have later CFP windows — check individually
- **SREcon**: `usenix.org/conference/srecon` — rolling CFP, directly relevant audience
- **Platform Engineering Summit**: Emerging conference track, CFP usually Q3

**Talk abstract to prepare now:**
> *"From 3am Pages to Zero False Alarms: How Tombstone's Blast Radius Gates and Circuit Breaker Auto-Rollback Eliminate Flag-Induced Incidents"*

Include the Knight Capital story, live demo of circuit breaker tripping, before/after MTTR numbers.

---

### 14. awesome-selfhosted Submission

`github.com/awesome-selfhosted/awesome-selfhosted` is the largest selfhosted software discovery list — 200k+ GitHub stars. Relevant section: **"Feature Flags / Configuration Management"**.

**Requirements:** Must be actively maintained, have a working demo or `make dev` quickstart, MIT/Apache license (✅ MIT).

Submit PR with:
```markdown
- [Tombstone](https://github.com/sairam0424/Tombstone) - Production intelligence layer for feature flags with circuit-breaker auto-rollback, blast radius gates, ML-driven rollout, and incident correlation. `MIT` `Go/Python/TypeScript`
```

---

## Tier 4 — Content Engine (ongoing)

### 15. Comparison Content

The highest-converting developer content is honest comparisons. Write one comprehensive comparison post:

**Title:** *"Tombstone vs. Unleash vs. Flipt vs. GrowthBook vs. LaunchDarkly — Feature Flag Platforms Compared (2026)"*

**Key angles to cover:**
- Circuit-breaker auto-rollback (unique to Tombstone in OSS)
- Self-hosted vs managed cloud
- ML rollout recommendations (unique to Tombstone in OSS)
- Blast radius / incident correlation (unique to Tombstone)
- CUPED + mSPRT experimentation (Tombstone has both; most OSS tools have neither)

The `docs/WHY_TOMBSTONE.md` you already have is the foundation — turn it into a published article with real benchmarks.

---

### 16. YouTube Demo Video

A 5–10 minute demo video is the single most effective content type for developer tools. Developers want to see it work before they clone it.

**Script outline:**
1. (0–1min) The problem: Knight Capital story, 3am flag incident
2. (1–3min) `make dev`, dashboard open, create first flag
3. (3–5min) Trigger circuit breaker — show auto-rollback in action
4. (5–7min) Blast radius pre-check before changing a flag
5. (7–9min) "What Changed?" incident query

**Upload to:** YouTube (primary), embed in README, tweet the link.

---

### 17. Twitter/X Strategy

Post a thread on launch day — threads get 3–5x the reach of single posts for technical topics.

**Thread structure:**
1. Hook tweet: "Knight Capital lost $440M in 45 minutes from a feature flag. Here's the system I built so that never happens to you. 🧵"
2. The problem (1 tweet)
3. Circuit breaker demo GIF (1 tweet)
4. Blast radius explanation (1 tweet)
5. ML rollout (Thompson Sampling in plain English) (1 tweet)
6. GitHub link + `make dev` quickstart (1 tweet)

**Tag:** `#devops`, `#golang`, `#opensource`, `#featureflags`, `#platformengineering`

---

### 18. LinkedIn Article

LinkedIn's algorithm distributes technical articles to non-followers via interest matching. Use the carousel format (highest engagement at 7.0% vs 3.25% for link posts).

**Create a carousel PDF** with:
- Slide 1: "Feature flags at scale: 5 things that break" 
- Slides 2–6: Each capability (circuit breaker, blast radius, ML rollout, tombstoning, incident correlation)
- Slide 7: GitHub link

---

## Key Metrics to Track

| Signal | Tool | Target |
|--------|------|--------|
| GitHub stars | `star-history.com` | 100 in first month |
| npm weekly downloads | npmjs.com/@tombstone/core | 500/week by month 3 |
| Go Report Card grade | goreportcard.com | A+ before awesome-go PR |
| Google Search impressions | Google Search Console | Index within 2 weeks |
| HN Show HN points | Hacker News | >100 points = front page |

---

## Immediate Action Checklist

```
[ ] Add 20 GitHub topics (15 min)
[ ] Update repo description (5 min)
[ ] Add social preview image (30 min)
[ ] Add keywords to all npm package.json files (15 min)
[ ] Submit PR to open-feature/community interested-parties.md (10 min)
[ ] Add Go Report Card + CI + License badges to README (10 min)
[ ] Check goreportcard.com grades for flag-api, gateway, evaluator
[ ] Draft Show HN post (save as draft, post Tuesday–Thursday 9–11am ET)
[ ] Draft Reddit posts for r/devops, r/golang, r/selfhosted
[ ] Submit to awesome-selfhosted (after Go Report Card is A+)
[ ] Write comparison article (Tombstone vs Unleash/Flagsmith/Flipt/GrowthBook/LD)
[ ] Record 5-min demo video (circuit breaker + blast radius)
```
