# Awesome-Lists PR Guide

Exact content for each awesome-list PR. Submit in this order (easiest → hardest).

---

## 1. seifrajhi/awesome-platform-engineering-tools

**Fork:** https://github.com/seifrajhi/awesome-platform-engineering-tools
**File to edit:** README.md
**Section:** Feature flags and change management

**Line to add (alphabetical — after "Flipt", before "Unleash"):**
```markdown
- [Tombstone](https://github.com/sairam0424/Tombstone) - Self-hosted production intelligence layer for 5,000+ feature flags — blast-radius gates, circuit-breaker auto-rollback, causal incident correlation, and OpenFeature-compatible. Go + Python + TypeScript. MIT.
```

**PR title:** `feat: add Tombstone to feature flags section`

**PR body:**
```
Tombstone is a self-hosted production intelligence layer for feature flags.
Key differentiators vs existing entries (Unleash, Flipt): automated circuit-breaker
rollback on SLO breach, blast-radius scoring before each rollout, and causal
incident correlation ("What Changed?" query). OpenFeature-compatible TypeScript SDK.
MIT licensed, actively maintained.

GitHub: https://github.com/sairam0424/Tombstone
```

---

## 2. shospodarets/awesome-platform-engineering

**Fork:** https://github.com/shospodarets/awesome-platform-engineering
**File to edit:** README.md
**Section:** Feature flags, environments and change management

**Line to add (alphabetical — after "Flagsmith", before "Unleash"):**
```markdown
- [Tombstone](https://github.com/sairam0424/Tombstone) - Self-hosted feature flag platform with blast-radius gating, circuit-breaker auto-rollback, and causal incident correlation for production-scale flag inventory. Go + Python + TypeScript. MIT.
```

**PR title:** `add Tombstone — self-hosted feature flag intelligence layer`

**PR body:**
```
Tombstone is an open-source self-hosted feature flag platform aimed at teams
managing large flag inventories in production. Unlike simpler delivery tools,
it adds blast-radius scoring (BLOCKED/HIGH/MEDIUM/LOW per change), circuit-breaker
auto-rollback on error threshold breach, and "What Changed?" incident correlation.
MIT licensed. Actively maintained.
```

---

## 3. rootsongjc/awesome-cloud-native

**Fork:** https://github.com/rootsongjc/awesome-cloud-native
**File to edit:** README.md
**Section:** Configuration & Policy Automation (where OpenFeature and Unleash already appear)

**Line to add (alphabetical — after "OpenFeature", before "Unleash"):**
```markdown
- [Tombstone](https://github.com/sairam0424/Tombstone) - Self-hosted feature flag platform with OpenFeature compatibility, blast-radius gating, circuit-breaker auto-rollback, and causal incident correlation. Go + Python + TypeScript. MIT.
```

**PR title:** `add Tombstone to Configuration & Policy Automation`

**PR body:**
```
Tombstone is an OpenFeature-compatible self-hosted feature flag platform.
It extends standard flag delivery with blast-radius scoring, circuit-breaker
auto-rollback, and causal incident correlation. Kubernetes operator included
(FeatureFlag/FlagPolicy CRDs). Relevant to cloud-native practitioners managing
flag-driven deployments at scale.
```

---

## 4. pheature-flags/awesome-feature-flags

**Fork:** https://github.com/pheature-flags/awesome-feature-flags
**File to edit:** README.md
**Action:** Add a new "## Self-Hosted" section if it doesn't exist, then add Tombstone

**Section to add:**
```markdown
## Self-Hosted

- [Tombstone](https://github.com/sairam0424/Tombstone) - Production intelligence layer for 5,000+ feature flags. Self-hosted. MIT. Circuit-breaker auto-rollback, blast-radius gates, causal incident correlation, ML rollout recommendations.
- [Flipt](https://github.com/flipt-io/flipt) - Open source, self-hosted feature flag solution. Go. MIT.
- [Flagsmith](https://github.com/Flagsmith/flagsmith) - Open source feature flag and remote config service. Python/TypeScript. BSD.
```

**PR title:** `add Self-Hosted section with Tombstone, Flipt, Flagsmith`

**PR body:**
```
This PR adds a Self-Hosted subsection to distinguish open-source self-deployed
options from SaaS products. Starting with three actively maintained projects:
Tombstone (Go+Python+TS), Flipt (Go), and Flagsmith (Python/TS). A dedicated
section helps developers who specifically need on-premises deployment find
relevant tools more quickly.
```

---

## 5. dastergon/awesome-sre

**Fork:** https://github.com/dastergon/awesome-sre
**File to edit:** README.md
**Section:** On-Call / Incident Management tools (or SRE Tools if that section exists)

**Line to add:**
```markdown
- [Tombstone](https://github.com/sairam0424/Tombstone) - Feature flag incident correlation and circuit-breaker auto-rollback — identifies which flag caused a production incident via causal dependency graph and "What Changed?" query.
```

**PR title:** `add Tombstone — feature flag incident correlation tool`

**PR body:**
```
Tombstone is relevant to SRE practice: it adds causal incident correlation
for feature flag changes ("What Changed?" query returns flags that changed
before an incident, ordered by blast radius) and circuit-breaker auto-rollback
on SLO breach. The reliability framing is central to the tool — not just flag
delivery. MIT, self-hosted.
```

---

## Commit message to use for all PRs
```
feat: add Tombstone to [section name]

Tombstone is a self-hosted feature flag platform with circuit-breaker
auto-rollback, blast-radius scoring, and OpenFeature compatibility.
MIT licensed, actively maintained.
GitHub: https://github.com/sairam0424/Tombstone
```

Note: use `git commit --signoff` for open-feature PRs. Standard commit for awesome-lists.
