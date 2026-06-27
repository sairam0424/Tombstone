# Amplify the dev.to Article

Published: https://dev.to/sai_ram_0000/i-built-a-self-hosted-feature-flag-platform-that-auto-rolls-back-bad-flags-heres-why-2m0k

## Post these right now (copy-paste ready)

### Twitter/X thread (post as a thread, not one tweet)

**Tweet 1 (hook):**
```
Knight Capital lost $440M in 45 minutes from a feature flag.

I built Tombstone to make sure that never happens to you 🧵
```

**Tweet 2:**
```
The problem: every OSS flag tool answers "what's the value of this flag?"

Nobody answers "which of my 5,000 flags is responsible for this incident?"

Tombstone does.
```

**Tweet 3:**
```
Circuit-breaker auto-rollback:

When a flag causes >5% errors over 100 requests in 10s → it disables automatically.

No pager. No runbook. MTTR: seconds.
```

**Tweet 4:**
```
Blast radius scoring:

Before you change a flag, you see BLOCKED / HIGH / MEDIUM / LOW.

BLOCKED changes require a written justification before you can proceed.

Deliberate friction for high-risk changes.
```

**Tweet 5:**
```
Self-hosted, MIT licensed. Starts with:

git clone https://github.com/sairam0424/Tombstone
cp infra/.env.example infra/.env
make dev

Dashboard at localhost:3000. No cloud account needed.

Full writeup: [dev.to link]
```

---

### LinkedIn post

```
I just published a writeup on why I built Tombstone — a self-hosted feature flag platform with circuit-breaker auto-rollback.

The short version: Knight Capital lost $440M in 45 minutes from a misconfigured feature flag in 2012. Standard tooling says "add a kill switch." But kill switches require a human to notice, diagnose, and act.

Tombstone closes that loop automatically.

When a flag causes >5% errors in a 10-second window, it disables itself. Before you change a flag, you see its blast-radius tier (BLOCKED/HIGH/MEDIUM/LOW). And when an incident fires, one API call tells you which flag caused it.

MIT licensed, self-hosted via Docker Compose. Full writeup on dev.to (link in comments).

#devops #platformengineering #opensource #featureflags
```

---

### CNCF Slack — post in #openfeature AFTER the interested-parties PR (#554) is merged

```
Hey #openfeature — just published a writeup on Tombstone, the self-hosted 
feature flag platform I've been building with an OpenFeature-compatible 
TypeScript provider.

Key differentiators: circuit-breaker auto-rollback, blast-radius scoring, 
causal incident correlation. MIT, Docker Compose.

Article: https://dev.to/sai_ram_0000/...
GitHub: https://github.com/sairam0424/Tombstone

Happy to answer questions about the OpenFeature provider implementation.
```

---

### CNCF Slack — post in #wg-platforms

```
Sharing a tool for platform teams: Tombstone — self-hosted feature flag 
platform with blast-radius gating and circuit-breaker auto-rollback.

Built for teams managing 500+ flags in production. Makes CI/CD-level safety 
available to flag changes the same way feature flags made deployments safer.

MIT licensed. https://github.com/sairam0424/Tombstone
```

---

### DevOps Weekly email (send now — dev.to post is the news peg)

To: gareth@morethanseven.net
Subject: Tombstone — self-hosted feature flag platform with circuit-breaker auto-rollback

```
Just published a writeup on Tombstone, a self-hosted feature flag platform 
I built. Circuit-breaker auto-rollback on SLO breach, blast-radius scoring 
before rollouts, causal incident correlation. MIT, Docker Compose.

Article: https://dev.to/sai_ram_0000/i-built-a-self-hosted-feature-flag-platform-that-auto-rolls-back-bad-flags-heres-why-2m0k
GitHub: https://github.com/sairam0424/Tombstone
```
