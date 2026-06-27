# Newsletter Submission Emails

Send these after a substantive release or when the Show HN is live.

---

## DevOps Weekly — gareth@morethanseven.net

**Subject:** Tombstone — self-hosted feature flag platform with circuit-breaker auto-rollback

```
Tombstone is a self-hosted production intelligence layer for feature flags — blast-radius
gating before rollouts, circuit-breaker auto-rollback on SLO breach, and "What Changed?"
causal incident correlation. MIT, Docker Compose, Go + Python + TypeScript.
GitHub: https://github.com/sairam0424/Tombstone
```

(3 sentences max — that's the format Gareth uses)

---

## Golang Weekly — cooperpress.com/submit

**Form fields:**
- URL: https://github.com/sairam0424/Tombstone
- Title: Tombstone — Self-Hosted Feature Flag Platform with Circuit Breaker Auto-Rollback
- Description: A production intelligence layer for feature flags built in Go. Implements circuit-breaker auto-rollback (5%/100req/10s threshold), blast-radius scoring, OPA hot-reload RBAC, Merkle-chained audit logs, and sync.Map lock-free SSE hub. Multi-module workspace (go.work) across 7 services.

**Best angle for Go Weekly:** lead with the Go architecture (sync.Map SSE hub, sqlc, OPA hot-reload, go.work workspace pattern) — not the product pitch.

---

## console.dev — hello@console.dev

**Subject:** Tombstone — self-hosted feature flag intelligence layer for production

```
I built Tombstone: a self-hosted alternative to LaunchDarkly/Unleash that adds
blast-radius gates, circuit-breaker auto-rollback, and causal incident correlation.
The core idea: treat 5,000 active flags as a causal graph of production behavior
so you can answer "which flag caused this incident?" rather than "what's the flag value?"

- MIT licensed, self-hostable via Docker Compose (`make dev` — zero config for local dev)
- CLI (@tombstone/cli), REST API, MCP server (8 tools), VS Code + JetBrains plugins
- Dashboard with dark mode at localhost:3000
- Go + Python + TypeScript, v1.0.0
- GitHub: https://github.com/sairam0424/Tombstone

Let me know if it's a fit for the newsletter.
```

---

## TLDR DevOps / TLDR Open Source — tldr.tech/submit

**Submission (keep to 2-3 sentences):**
```
Tombstone is a self-hosted feature flag platform with circuit-breaker auto-rollback —
when a flag causes >5% errors, it disables automatically without human intervention.
Includes blast-radius scoring, causal incident correlation ("What Changed?"), and
ML-driven rollout recommendations. MIT, Docker Compose. https://github.com/sairam0424/Tombstone
```
