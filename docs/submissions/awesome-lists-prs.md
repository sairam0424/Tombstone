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

---

## 6. avelino/awesome-go

**Fork:** https://github.com/avelino/awesome-go
**File to edit:** README.md
**Section:** `### DevOps Tools` (under `## Software Packages`)
**Alphabetical position:** After `[tlm]`, before `[traefik]`

**Line to add:**
```markdown
- [Tombstone](https://github.com/sairam0424/Tombstone) - Self-hosted production intelligence layer for feature flags: blast-radius gating, circuit-breaker auto-rollback, causal incident correlation, and OpenFeature-compatible SDK.
```

**PR title:** `feat(devops-tools): add Tombstone — feature flag intelligence layer`

**PR body (fill into the PULL_REQUEST_TEMPLATE):**

```
## Required links

- [x] Forge link (github.com, gitlab.com, etc): https://github.com/sairam0424/Tombstone
- [x] pkg.go.dev: https://pkg.go.dev/github.com/sairam0424/Tombstone/services/flag-api
- [x] goreportcard.com: https://goreportcard.com/report/github.com/sairam0424/Tombstone
- [x] Coverage service link: https://codecov.io/gh/sairam0424/Tombstone

## Pre-submission checklist

- [x] I have read the Contribution Guidelines
- [x] I have read the Quality Standards

## Repository requirements

- [x] The repo has a `go.mod` file and at least one SemVer release (`vX.Y.Z`).
- [x] The repo has an open source license (MIT).
- [x] The repo documentation has a pkg.go.dev link.
- [x] The repo documentation has a goreportcard link (grade A- or better).
- [x] The repo documentation has a coverage service link (Codecov badge in README).

- [x] The repo has a continuous integration process (GitHub Actions — .github/workflows/ci.yml).
- [x] CI runs tests that must pass before merging.

## Pull Request content

- [x] This PR adds/removes/changes **only one** package.
- [x] The package has been added in **alphabetical order** (after `tlm`, before `traefik`).
- [x] The link text is the **exact project name**: `Tombstone`.
- [x] The description is clear, concise, non-promotional, and **ends with a period**.
- [x] The link in README.md matches the forge link above.

## Category quality

- [x] The packages around my addition still meet the Quality Standards.

---

**Why DevOps Tools?**

Tombstone is a self-hosted Go platform for managing feature flags in production at scale. It belongs in DevOps Tools because its primary value proposition is operational: automated circuit-breaker rollback when error rates spike, blast-radius scoring before each flag change (BLOCKED / HIGH / MEDIUM / LOW), and a causal incident-correlation query ("which of my 5,000 active flags changed before this alert fired?"). These are reliability and operations concerns, not just feature delivery.

The Go services are: `flag-api` (CRUD + approval workflows + Merkle-linked audit log), `gateway` (Redis Streams → SSE fan-out), `evaluator` (circuit-breaker engine, blast-radius scoring), `gitops-sync` (YAML-as-code), `ast-rewriter` (dead-code scanner), `marketplace` (Slack/Datadog/PagerDuty/OpsGenie integrations), and `tombstone-operator` (Kubernetes operator with FeatureFlag/FlagPolicy CRDs).

**Why it is different from existing list entries:**

No other entry in `### DevOps Tools` covers feature flag lifecycle management with auto-rollback and incident correlation. The closest tools in the broader Go ecosystem are Flipt (flag delivery, no rollback intelligence) and go-feature-flag (lightweight flag evaluation), but neither provides circuit-breaker auto-rollback or causal incident correlation — which are the core of Tombstone's value.
```

---

## 7. awesome-selfhosted/awesome-selfhosted-data

**Target repo (PRs go here, NOT awesome-selfhosted):** https://github.com/awesome-selfhosted/awesome-selfhosted-data
**File to create:** `software/tombstone.yml`
**Section rendered to:** Software Development - Feature Toggle

**File content (`software/tombstone.yml`):**
```yaml
name: Tombstone
website_url: https://github.com/sairam0424/Tombstone
source_code_url: https://github.com/sairam0424/Tombstone
description: Production intelligence layer for feature flags with blast-radius gating, circuit-breaker auto-rollback, causal incident correlation, and OpenFeature-compatible SDK.
licenses:
  - MIT
platforms:
  - Go
  - Docker
  - K8S
tags:
  - Software Development - Feature Toggle
```

**PR title:** `Add Tombstone — self-hosted feature flag intelligence layer`

**PR body (fill into the PULL_REQUEST_TEMPLATE):**

```
Thanks for taking the time to suggest an addition to awesome-selfhosted!

To ensure your Pull Request is dealt with swiftly, please check the following:

- [x] Submit one item per pull request.
- [x] You have searched the repository for any relevant issues or PRs, including closed ones.
- [x] Any software you are adding is not already listed at awesome-sysadmin, staticgen.com, staticsitegenerators.bevry.me, or dbdb.io.
- [x] The file you are adding is formatted as described in addition.md.
- [x] Comments and unused optional fields have been removed.
- [x] The file you are adding uses kebab-case file naming: `tombstone.yml`.
- [x] Values for `platform` match the platforms required to install and run the software (Go, Docker, K8S).
- [x] Any software project you are adding to the list is actively maintained.
- [x] Any software project you are adding was first released more than 4 months ago (first release: v1.0.0).
- [x] Any software project you are adding has working installation instructions (`make dev` in README).
- [x] You understand that your Pull Request will be merged at least ~1 week after approval.

---

**What Tombstone does:**

Tombstone is a self-hosted production intelligence layer for feature flags. It combines flag delivery (SSE streaming, OpenFeature-compatible TypeScript SDK) with operational safety: blast-radius scoring before each rollout (BLOCKED/HIGH/MEDIUM/LOW), circuit-breaker auto-rollback when error rates exceed threshold, causal incident correlation ("What Changed?" — returns flags that changed before an incident, ordered by blast radius), and automated stale-flag cleanup. MIT licensed, runs via Docker Compose or Kubernetes.

**Why it belongs in Software Development - Feature Toggle:**

The existing entries in this section (Featbit, Flagsmith, Flipt, GO Feature Flag) are flag delivery platforms. Tombstone is the only entry focused on the post-delivery reliability loop: automatic rollback, incident correlation, and blast-radius gating. It is directly comparable to those tools (same deployment pattern: self-hosted Docker/K8S, same audience: engineering teams), and complements the section by covering the operational safety dimension no other entry addresses.

**Homepage / source:** https://github.com/sairam0424/Tombstone
**License:** MIT
**Platforms:** Go, Docker, K8S
**Tag:** Software Development - Feature Toggle
```
