# Reddit Post Drafts

Post all 4 on the same day as the Show HN for maximum star velocity.

---

## r/devops

**Title:** I built a self-hosted feature flag platform that auto-rolls back bad flags before the incident page fires

**Body:**
Been thinking about the Knight Capital incident for a while — $440M lost in 45 minutes from a misconfigured feature flag. The standard tooling answer is "add a kill switch," but kill switches require a human to notice, wake up, and act.

I built Tombstone to close that loop: when a flag causes >5% errors over 100 requests in a 10-second window, it automatically rolls back without human intervention. The circuit breaker state lives in Redis, so it's fast and survives service restarts.

Two other things I couldn't find in any OSS flag tool:

**Blast radius scoring** — before you change a flag, you get a tier (BLOCKED/HIGH/MEDIUM/LOW) based on traffic %, dependent flags, and 30-day historical incident correlation. BLOCKED changes require a written justification before proceeding — deliberate friction.

**"What Changed?" incident correlation** — given an incident timestamp, returns flags changed in the preceding window ordered by blast radius. Answers the "was it a flag?" question in one API call.

Self-hosted, MIT, runs with `make dev`. Go + Python + TypeScript.

GitHub: https://github.com/sairam0424/Tombstone

Genuinely curious: what's the most painful thing about managing feature flags at scale in your org?

---

## r/golang

**Title:** Built a production feature flag platform in Go — circuit breaker, blast radius scoring, OPA RBAC, Merkle audit log

**Body:**
Started this as "I want to understand how LaunchDarkly works internally" and ended up with a full system. The Go parts:

**flag-api** — chi router, sqlc for type-safe queries, append-only Merkle-chained audit log (`sha256(id|event_type|actor|prev_state|new_state|ts)` with `prev_hash` linking entries), OPA policy-as-code RBAC with `fsnotify` hot-reload. Tombstoning pattern (permanent flag key archival) to prevent Knight Capital-style key reuse incidents.

**gateway** — `sync.Map` lock-free SSE hub (measured 40% throughput improvement vs RWMutex at 10k connections), Redis Streams consumer groups (`XREADGROUP`), backpressure `lag` events when client buffers fill.

**evaluator** — circuit breaker (5% errors/100 requests/10s window → OPEN state), per-flag Redis state, auto-rollback callback, blast radius 4-tier classification.

One interesting Go decision: using `go.work` multi-module workspace with `GOWORK=off` inside Docker (each service builds independently for smaller images). Works well.

GitHub: https://github.com/sairam0424/Tombstone

Would appreciate feedback on the circuit breaker implementation — happy to discuss trade-offs.

---

## r/selfhosted

**Title:** Tombstone: self-hosted feature flags with ML rollout recommendations — entire stack starts with `make dev`

**Body:**
Sharing a self-hosted alternative to LaunchDarkly/Unleash I've been building.

**What it is:** A feature flag platform that treats your flags as a causal graph of production behavior rather than just configuration. The idea: at 5,000+ flags across 10+ services, you need automated safety, not just UI.

**Quick start:**
```bash
git clone https://github.com/sairam0424/Tombstone
cd Tombstone
cp infra/.env.example infra/.env  # zero changes needed for local dev
make dev                           # starts 8 services + postgres + redis + kafka
```

Dashboard at localhost:3000. All defaults work out of the box.

**The self-hosted interesting bits:**
- The .env.example has working defaults — no secret generation needed to get started
- Intelligence service bundles BAAI/bge-m3 embedding model (~400MB) baked into the Docker image at build time, so startup is fast after first build
- Everything is Docker Compose; no cloud accounts required
- MIT licensed, no telemetry, no phone-home

Stack: Go + Python 3.12 + TypeScript. 8 services total.

GitHub: https://github.com/sairam0424/Tombstone

---

## r/ExperiencedDevs

**Title:** After the Knight Capital post-mortem, I built tombstoning into our flag system — here's what I learned

**Body:**
The Knight Capital Group incident (2012, $440M in 45 minutes) keeps coming up whenever feature flags are discussed. The root cause: a flag key that should have been permanently retired was accidentally reused for a new feature, causing the old code to activate in production.

The standard advice is "archive your flags." But archival is soft — nothing stops you from creating a new flag with the same key.

Tombstoning is hard archival: once a flag key is retired, it's written to a `flag_tombstones` table with a DB unique constraint. The service layer also checks before creates. Key reuse is physically impossible. The tombstone record stores the original metadata and retirement reason as a permanent audit artifact.

I built this into Tombstone (https://github.com/sairam0424/Tombstone) along with circuit-breaker auto-rollback and blast-radius scoring.

Three things I found genuinely hard to get right:

1. **The circuit breaker false positive rate.** At 5%/100 requests/10s, you get false trips during legitimate traffic spikes. The HALF_OPEN recovery window (5 minutes) gives the system time to self-correct without requiring human intervention.

2. **Blast radius justification UX.** BLOCKED-tier changes require a 10-character minimum justification. Long enough to be intentional, short enough to not be bureaucratic. Found this threshold empirically.

3. **Merkle-chained audit logs.** Easy to implement; surprisingly hard to explain to compliance teams. The value is tamper detection without a separate ledger service.

Curious if others have built similar safety patterns around flags, and what failure modes I'm missing.
