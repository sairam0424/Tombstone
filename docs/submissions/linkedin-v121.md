# LinkedIn — v1.2.1 Launch Post + Carousel Script

---

## LinkedIn Post (text format — no markdown, LinkedIn renders plain text)

Tombstone v1.2.1 is live.

It's a self-hosted feature flag platform that auto-rolls back bad deploys — no human required.

Here's the backstory:

In 2012, Knight Capital lost $440M in 45 minutes because of a feature flag that should have been deleted. It was accidentally reactivated during a deploy.

The standard answer: "use a kill switch."

The problem: kill switches require a human to notice, diagnose, and flip. At 3am. During an incident. While production is on fire.

I built the alternative.

Tombstone's evaluator tracks per-flag error rates in a rolling Redis window. When a flag causes more than 5% errors over 100 requests in a 10-second window, it's killed automatically. MTTR goes from minutes to seconds.

The other piece I couldn't find anywhere: causal incident correlation.

Given an incident timestamp, one API call returns the flags that changed in the preceding window, ranked by how recently they changed (a flag that changed 2 minutes before the incident scores higher than one that changed 28 minutes before), with one-click rollback buttons.

"Was it a flag?" is now a single API call, not 20 minutes of log archaeology.

v1.2.1 adds the production hardening layer: distributed rate limiting via Redis Lua (global across replicas, not per-process), a Redis Streams dead-letter queue with manual replay for malformed messages, adaptive load shedding under Postgres connection pool exhaustion, and a snapshot reconciliation loop that corrects stale SSE state within 5 minutes.

Also: complete documentation suite — evaluation model, Kubernetes deployment, 9 SDK integration guides, 5 operational runbooks.

Everything starts with: make dev

MIT licensed. No telemetry. Self-hosted.

GitHub link in comments (LinkedIn deprioritizes posts with external links in the body).

What would you want from a self-hosted feature flag platform?

---

**LinkedIn rules reminder (from medium-publishing.md):**
- Put the GitHub link in the FIRST COMMENT, not the body (reduces link penalty)
- Format: text-only is fine, but carousel PDF would get higher engagement (~7% vs 4.5%)
- Reply to every comment within a few hours
- No engagement-bait ("comment YES if you agree")

---

## First Comment (post immediately after)

GitHub: https://github.com/sairum0424/tombstone

`make dev` — full stack (API :8081, dashboard :3000) in one command.

---

## Carousel PDF Script (optional — higher engagement than text post)

If converting to carousel, create 8–10 slides. Suggested structure:

### Slide 1 — Hook
Title: "Knight Capital lost $440M in 45 minutes. Because of a feature flag."
Subtitle: "Here's how to prevent the next one."
Visual: timeline showing 8:01am deploy → 9:30am shutdown

### Slide 2 — The problem
Title: "Kill switches still require a human."
Body:
- Notice the error spike
- Diagnose which flag
- Find the kill switch
- Flip it
- Hope you were fast enough
Visual: clock showing 3am

### Slide 3 — Circuit breaker auto-rollback
Title: "What if the flag rolled back itself?"
Body: "5% error rate over 100 requests in a 10-second window → automatic kill. MTTR: seconds, not minutes."
Visual: state machine diagram (CLOSED → OPEN → HALF_OPEN)

### Slide 4 — Causal incident correlation
Title: "Which flag caused this?"
Body: "GET /incidents/{id}/correlation returns ranked flag candidates with one-click rollback. One API call. Not 20 minutes of log archaeology."
Visual: screenshot mockup of correlation response JSON

### Slide 5 — Blast radius gating
Title: "Before you change a flag, know the blast radius."
Body:
- BLOCKED: >80% traffic + prior incidents
- HIGH / MEDIUM / LOW: graduated warnings
- Cannot override BLOCKED without explicit confirmation
Visual: BLOCKED badge screenshot

### Slide 6 — v1.2.1 resilience
Title: "v1.2.1: production hardening."
Body (bullet list):
- Distributed rate limiting via Redis Lua
- Redis Streams DLQ with manual replay
- Adaptive load shedding (TCP Vegas)
- Snapshot reconciliation loop
- 26-agent pre-release validation

### Slide 7 — Stack
Title: "The stack."
Body:
- Go 1.22 (7 services)
- Python 3.12 (ML intelligence)
- TypeScript (9 SDKs + dashboard + CLI)
- PostgreSQL 16 + Redis + Kafka
- OpenFeature-compatible
- MIT licensed

### Slide 8 — CTA
Title: "Self-hosted. No telemetry. One command."
Body: "make dev"
Subtitle: "github.com/sairum0424/tombstone"
Visual: terminal showing make dev output

---

## Carousel production notes

- Export as PDF (not images — PDF carousel is a native LinkedIn format)
- Dimensions: 1080x1080px per slide (square) or 1920x1080px (landscape)
- Use high-contrast colors — readable on mobile thumbnail
- Add slide numbers (1/8, 2/8, etc.) in corner
- Last slide must have CTA + GitHub URL visible without clicking through
