
# Tombstone Submission Guide

**Project state as of 2026-06-27:** v2.2.0, repo created 2026-06-21 (6 days old), MIT licensed, self-hosted, Go 1.25 / Python 3.12 / TypeScript monorepo.

**Critical naming facts grounded in the actual repo:**
- Go module: `github.com/tombstone/flag-api` (does NOT match GitHub repo path `sairam0424/Tombstone` — this is a blocker for pkg.go.dev and awesome-go)
- npm packages: `@tombstone/core`, `@tombstone/react`, `@tombstone/edge`, `@tombstone/browser`, `@tombstone/eval`, `@tombstone/cli`, `@tombstone/mcp`
- PyPI SDK name in pyproject.toml: `tombstone` (taken on PyPI by an unrelated debug tool — must rename before publishing)
- Ruby gem name in gemspec: `tombstone` (taken on RubyGems)
- Java group: `io.tombstone`, artifact: unnamed (needs artifactId)
- .NET package: `Tombstone.Client`
- JetBrains plugin: `Tombstone Feature Flags`, group `io.tombstone`
- VS Code extension: publisher `tombstone`, name `tombstone-vscode`
- Helm chart: name `tombstone`, keywords include `feature-flags`, `circuit-breaker`, `blast-radius`
- Docker: no custom images pushed yet (compose uses upstream images for dependencies)
- MCP server: `@tombstone/mcp` v0.1.0, 8 tools, Streamable HTTP transport

---

## SECTION 1: IMMEDIATE — Do Today, Under 1 Hour Each

### 1.1 AlternativeTo

**URL:** https://alternativeto.net/software/launchdarkly/

**What to do:** On the LaunchDarkly page, click "Add Alternatives" at the top. Fill in:

- Name: Tombstone
- Website: https://github.com/sairam0424/Tombstone
- Platforms: Linux, Self-Hosted
- License: Open Source (MIT)
- Description (keep it under 250 chars): Production intelligence layer for feature flags. Self-hosted alternative with blast-radius gates, circuit-breaker auto-rollback, causal incident correlation, and ML-driven rollout recommendations. No per-seat pricing.

Also add Tombstone as an alternative to: Unleash (https://alternativeto.net/software/unleash-feature-toggle-system/), Flagsmith, and Flipt — same drill on each page.

**Estimated time:** 15 minutes.

**Blockers:** None.

---

### 1.2 CNCF Slack — Join #openfeature

**URL:** https://slack.cncf.io (invite) → then https://cloud-native.slack.com/archives/C0344AANLA1

**What to do:**

1. Register at https://slack.cncf.io — free, email-based.
2. Join channel `#openfeature` (ID: C0344AANLA1).
3. Post this message (do not post yet — wait until the OpenFeature provider PR is open, see Section 2.3, then post together):

> Hey #openfeature — I just opened a PR to register Tombstone as an OpenFeature provider. Tombstone is a self-hosted production intelligence layer for feature flags: blast-radius gates, circuit-breaker auto-rollback, and causal incident correlation. The TypeScript provider is at [link to PR]. Happy to answer any questions!

**Estimated time:** 10 minutes to join and draft.

**Blockers:** None to join. Hold the actual post until the PR is open.

---

### 1.3 CNCF Slack — Join #wg-platforms

**URL:** https://cloud-native.slack.com/archives/C020RHD43BP

**What to do:** After joining CNCF Slack (step 1.2 above), also join `#wg-platforms`. Post once the project has its first real feature (e.g., when blast-radius demo is working end-to-end):

> Sharing a self-hosted tool we built for platform teams: Tombstone — a feature flag intelligence layer with blast-radius gates and circuit-breaker auto-rollback. Aimed at teams running 1,000+ flags in production. https://github.com/sairam0424/Tombstone

**Estimated time:** 5 minutes additional after 1.2.

---

### 1.4 Platform Engineering Slack

**URL:** https://platformengineering.org/slack-rd

**What to do:** Join the free community Slack. Post in `#show-and-tell` or `#tools`:

> We built Tombstone — a self-hosted feature flag platform that treats your flag inventory as a causal graph of production behavior. Key differentiators vs Unleash/Flipt: blast-radius scoring before every rollout, circuit-breaker auto-rollback on SLO breach, ML-driven rollout recommendations (Thompson Sampling), and "What Changed?" incident correlation. Apache/MIT, no per-seat pricing. GitHub: https://github.com/sairam0424/Tombstone — curious what pain points platform teams have with flag hygiene at scale.

**Estimated time:** 10 minutes.

**Blockers:** None.

---

### 1.5 seifrajhi/awesome-platform-engineering-tools

**URL:** https://github.com/seifrajhi/awesome-platform-engineering-tools/pulls

**What to do:** Fork the repo, add one line to the "Feature flags and change management" section in alphabetical order:

```
- [Tombstone](https://github.com/sairam0424/Tombstone) - Self-hosted production intelligence layer for 5,000+ feature flags — blast-radius gates, circuit-breaker auto-rollback, causal incident correlation, and OpenFeature-compatible.
```

PR title: `feat: add Tombstone to feature flags section`

PR body: Keep it to 2–3 sentences. Say what it is, why it belongs in this section, and note it is actively maintained.

**Estimated time:** 20 minutes.

**Blockers:** None stated by this list. No age requirement. The list already has OpenFeature and Unleash — Tombstone slots in directly.

---

### 1.6 shospodarets/awesome-platform-engineering

**URL:** https://github.com/shospodarets/awesome-platform-engineering/pulls

**Same process as 1.5.** Find the "Feature flags, environments and change management" section, insert in alphabetical order:

```
- [Tombstone](https://github.com/sairam0424/Tombstone) - Self-hosted feature flag platform with blast-radius gating, circuit-breaker auto-rollback, and causal incident correlation for production-scale flag inventory.
```

PR title: `add Tombstone — self-hosted feature flag intelligence layer`

**Estimated time:** 15 minutes.

**Blockers:** None.

---

### 1.7 tech-and-finance/awesome-feature-flags

**URL:** https://github.com/tech-and-finance/awesome-feature-flags/pulls

**What to do:** Fork, find "Open Source (Self-Hosted)" section, add:

```
- [Tombstone](https://github.com/sairam0424/Tombstone) - Production intelligence layer for 5,000+ feature flags. Self-hosted. MIT. Circuit-breaker auto-rollback, blast-radius gates, causal incident correlation, ML rollout recommendations.
```

**Estimated time:** 15 minutes.

**Blockers:** None. This list has no age requirement and is the most targeted match for Tombstone's exact use case. Low stars but highest topical relevance.

---

### 1.8 rootsongjc/awesome-cloud-native

**URL:** https://github.com/rootsongjc/awesome-cloud-native/pulls

**What to do:** Find the "Configuration & Policy Automation" section (which already lists OpenFeature and Unleash). Add Tombstone in alphabetical order:

```
- [Tombstone](https://github.com/sairam0424/Tombstone) - Self-hosted feature flag platform with OpenFeature compatibility, blast-radius gating, and circuit-breaker auto-rollback. Go + Python + TypeScript.
```

**Estimated time:** 15 minutes.

**Blockers:** None. The existing entries (OpenFeature, Unleash) confirm this section is the right place.

---

## SECTION 2: SHORT-TERM — This Week, 1–4 Hours Each

### 2.1 GitHub Container Registry (ghcr.io) — Push Service Images

**URL:** https://github.com/features/packages

**What to do:** Add a GitHub Actions workflow at `.github/workflows/docker-publish.yml` that builds and pushes all service images to `ghcr.io/sairam0424/tombstone-*`. Use the `docker/build-push-action@v7` action.

Required steps per service image:
1. Add `LABEL org.opencontainers.image.source=https://github.com/sairam0424/Tombstone` to each service's `Dockerfile`.
2. Push with tag strategy: `ghcr.io/sairam0424/tombstone-flag-api:v2.2.0` and `ghcr.io/sairam0424/tombstone-flag-api:latest` simultaneously.
3. After first push, go to GitHub → Packages → set each package visibility to Public.

Images to build: `tombstone-flag-api`, `tombstone-gateway`, `tombstone-evaluator`, `tombstone-intelligence`, `tombstone-gitops-sync`, `tombstone-ast-rewriter`, `tombstone-marketplace`, `tombstone-dashboard`.

**Auth in Actions:**
```yaml
- uses: docker/login-action@v4
  with:
    registry: ghcr.io
    username: ${{ github.repository_owner }}
    password: ${{ secrets.GITHUB_TOKEN }}
```

**Estimated time:** 2–3 hours (writing the workflow, fixing Dockerfile labels, verifying push).

**Blockers:** Dockerfiles must exist per service. Check each `services/<svc>/Dockerfile`.

---

### 2.2 npm — Publish TypeScript Packages

**URL:** https://www.npmjs.com/signup

**Exact packages to publish (all at v0.1.0):**
- `@tombstone/core` — Node.js SDK
- `@tombstone/react` — React hooks
- `@tombstone/edge` — Cloudflare Workers SDK
- `@tombstone/browser` — browser bundle
- `@tombstone/eval` — WASM eval engine
- `@tombstone/cli` — Commander CLI
- `@tombstone/mcp` — MCP server

**Steps:**
1. Register at https://www.npmjs.com/signup. Enable 2FA (mandatory for publishing).
2. Create the `@tombstone` org scope at https://www.npmjs.com/org/create (if not already created).
3. Generate a granular access token: Account Settings → Access Tokens → Generate New Token → Granular → scope to each package → `read and write` permissions.
4. For each package: `cd packages/sdks/@tombstone/<name> && npm run build && npm publish --access public`
5. For `@tombstone/eval`: `cd packages/sdk-wasm && npm run build && npm publish --access public`
6. For `@tombstone/cli`: `cd workspace-cli && npm run build && npm publish --access public`
7. For `@tombstone/mcp`: `cd workspace-mcp && npm run build && npm publish --access public`

**package.json fields to add/verify in each package before publishing:**
```json
{
  "repository": {"type": "git", "url": "https://github.com/sairam0424/Tombstone"},
  "homepage": "https://github.com/sairam0424/Tombstone",
  "bugs": {"url": "https://github.com/sairam0424/Tombstone/issues"},
  "keywords": ["feature-flags", "openfeature", "tombstone", "circuit-breaker"]
}
```

**Estimated time:** 2 hours (setup + build verification + publish pipeline).

**Blockers:** 2FA required. Each package must build cleanly before publish. The `@tombstone` npm org scope may need to be reserved — check https://www.npmjs.com/org/tombstone first.

---

### 2.3 OpenFeature Ecosystem PR

**URL:** https://github.com/open-feature/openfeature.dev

**What to do:** This is the single highest-credibility submission this week. The TypeScript provider already exists at `packages/sdks/@tombstone/core/src/provider.ts`.

**Steps:**

1. Fork https://github.com/open-feature/openfeature.dev.
2. Create file `/src/datasets/providers/tombstone.ts` with this content:
```typescript
import TombstoneSvg from '@site/static/img/tombstone-no-fill.svg';
import { Provider } from '.';

export const Tombstone: Provider = {
  name: 'Tombstone',
  logo: TombstoneSvg,
  technologies: [
    {
      technology: 'JavaScript',
      vendorOfficial: true,
      href: 'https://github.com/sairam0424/Tombstone/tree/main/packages/sdks/@tombstone/core',
      category: ['Server'],
    },
    {
      technology: 'JavaScript',
      vendorOfficial: true,
      href: 'https://github.com/sairam0424/Tombstone/tree/main/packages/sdks/@tombstone/react',
      category: ['Client'],
    },
    {
      technology: 'Python',
      vendorOfficial: true,
      href: 'https://github.com/sairam0424/Tombstone/tree/main/packages/sdks/tombstone-python-sdk',
      category: ['Server'],
    },
  ],
  description: 'Self-hosted production intelligence layer for feature flags — blast-radius gates, circuit-breaker auto-rollback, and causal incident correlation.',
};
```
3. Add an SVG logo (no fills, clean paths) to `/static/img/tombstone-no-fill.svg`.
4. Add `Tombstone` to the `PROVIDERS` array in `/src/datasets/providers/index.ts`, alphabetically.
5. `git commit --signoff` (the `--signoff` flag is mandatory per their CONTRIBUTING.md).
6. Open the PR. Title: `feat: add Tombstone provider`.

**Estimated time:** 1.5–2 hours (SVG creation is the slowest part — design a clean mark).

**Blockers:** Need a proper SVG logo. The `--signoff` requirement means you must use Git commit signoff, not just a normal commit.

---

### 2.4 MCP Server Registry

**URL:** https://registry.modelcontextprotocol.io / https://github.com/modelcontextprotocol/registry

**This is the fastest high-impact submission available.** The `workspace-mcp` directory already has a working MCP server with 8 tools.

**Steps:**

1. First, publish `@tombstone/mcp` to npm (step 2.2 above must complete first).
2. Add `mcpName` to `workspace-mcp/package.json`:
```json
{
  "mcpName": "io.github.sairam0424/tombstone"
}
```
3. Install the CLI: `brew install mcp-publisher` (or download binary from GitHub releases).
4. Run `mcp-publisher init` in `workspace-mcp/` — edit the generated `server.json`:
```json
{
  "version": "0.1.0",
  "identifier": "io.github.sairam0424/tombstone",
  "name": "Tombstone Feature Flags",
  "description": "MCP server for Tombstone — manage feature flags, query blast-radius, trigger kill switches, and search stale flags from any MCP-compatible AI client.",
  "tools": 8
}
```
5. Authenticate: `mcp-publisher login github` (device flow).
6. Publish: `mcp-publisher publish`.
7. Verify: `curl "https://registry.modelcontextprotocol.io/v0.1/servers?search=io.github.sairam0424/tombstone"`

**Estimated time:** 1 hour (after npm publish is done).

**Blockers:** npm publish (step 2.2) must complete first. The `mcpName` namespace `io.github.sairam0424/tombstone` requires GitHub auth as `sairam0424`.

---

### 2.5 selfh.st Submit

**URL:** https://selfh.st/submit

**What to do:** Submit via the form at https://selfh.st/submit. Fill in:

- Tool name: Tombstone
- Category: Software Development / Feature Flags
- Description: Self-hosted production intelligence layer for feature flags. Treats 5,000+ flags as a live causal graph — blast-radius gating before each rollout, circuit-breaker auto-rollback on SLO breach, "What Changed?" incident correlation, and automated stale-flag cleanup. MIT licensed. Deployable via Docker Compose in under 5 minutes.
- GitHub URL: https://github.com/sairam0424/Tombstone
- License: MIT
- Self-hostable: Yes (Docker Compose)

**Estimated time:** 20 minutes.

**Blockers:** None.

---

### 2.6 OpenAlternative Submit

**URL:** https://openalternative.co/submit

**What to do:** Fill out the submission form:

- Tool name: Tombstone
- Tagline: Self-hosted LaunchDarkly alternative with blast-radius gates and auto-rollback
- Description: Tombstone is a self-hosted production intelligence layer for feature flags. It treats your flag inventory as a live causal graph of production behavior — providing blast-radius scoring before every rollout, circuit-breaker auto-rollback when error rates spike, "What Changed?" causal incident correlation, and ML-driven rollout recommendations. MIT licensed. No per-seat pricing. Deploys in under 5 minutes via Docker Compose.
- GitHub URL: https://github.com/sairam0424/Tombstone
- Alternative to: LaunchDarkly (primary), Unleash, Flagsmith, Flipt
- Categories: Developer Tools, DevOps, Feature Flags

Also click "Suggest an alternative" directly on the LaunchDarkly page at openalternative.co/launchdarkly.

**Estimated time:** 20 minutes.

**Blockers:** None. Free submission. This is one of the highest-value quick wins given it directly intercepts users searching for LaunchDarkly alternatives.

---

### 2.7 StackShare

**URL:** https://stackshare.io/contribute/tool

**What to do:** Create an account at stackshare.io, then navigate to https://stackshare.io/contribute/tool. Fill in:

- Name: Tombstone
- GitHub URL: https://github.com/sairam0424/Tombstone
- Website: https://github.com/sairam0424/Tombstone
- Category: Feature Flags
- Description: Self-hosted production intelligence layer for feature flags — blast-radius gating, circuit-breaker auto-rollback, causal incident correlation, and ML rollout recommendations. Go + Python + TypeScript. MIT licensed.
- Supported platforms: Linux, Docker, Kubernetes

**Estimated time:** 20 minutes.

**Blockers:** None. StackShare is heavily used for tech stack research — engineering teams evaluating LaunchDarkly alternatives will encounter it here.

---

### 2.8 PyPI — Fix Name and Publish Python SDK

**URL:** https://pypi.org/account/register/

**BLOCKER TO RESOLVE FIRST:** The Python SDK at `packages/sdks/tombstone-python-sdk/pyproject.toml` currently has `name = "tombstone"` — this name is taken on PyPI by an unrelated debug tool. Before doing anything else for this step, rename it.

**Steps:**

1. Edit `packages/sdks/tombstone-python-sdk/pyproject.toml`: change `name = "tombstone"` to `name = "flagmind"` (short, clean, not taken as of research date — verify at https://pypi.org/project/flagmind/ first).
2. Update the description in pyproject.toml to add required fields:
```toml
[project]
name = "flagmind"
version = "0.1.0"
description = "Tombstone Python SDK — server-side feature flag evaluation"
readme = "README.md"
requires-python = ">=3.10"
license = "MIT"
license-files = ["LICENSE"]
authors = [{ name = "Tombstone", email = "you@example.com" }]
keywords = ["feature-flags", "tombstone", "openfeature"]
classifiers = [
  "Programming Language :: Python :: 3",
  "Operating System :: OS Independent",
]
dependencies = ["httpx>=0.27.0", "mmh3>=4.0.0"]

[project.urls]
Homepage = "https://github.com/sairam0424/Tombstone"
Issues = "https://github.com/sairam0424/Tombstone/issues"

[build-system]
requires = ["hatchling>=1.26"]
build-backend = "hatchling.build"
```
3. Add a `LICENSE` file if missing in the SDK directory.
4. Register at https://pypi.org/account/register/ — verify email, enable 2FA.
5. Generate API token at pypi.org under Account Settings.
6. Build: `cd packages/sdks/tombstone-python-sdk && python -m pip install build && python -m build`
7. Test upload: `python -m pip install twine && python -m twine upload --repository testpypi dist/*`
8. Verify at https://test.pypi.org/project/flagmind/, then: `python -m twine upload dist/*`

**Estimated time:** 2 hours (name decision + pyproject.toml edit + build + publish).

**Blockers:** Verify the name `flagmind` is not taken before committing to it. The SDK at `packages/sdks/tombstone-python-sdk/` has a subdirectory named `tombstone/` internally — verify the import path after rename.

---

### 2.9 VS Code Extension — Publish

**URL:** https://marketplace.visualstudio.com/manage

**This is the fastest marketplace win.** No manual review — extensions go live almost immediately.

**Steps:**

1. Create a Microsoft account / Azure DevOps org at https://dev.azure.com.
2. Generate a Personal Access Token (PAT): https://dev.azure.com → User Settings → Personal Access Tokens → New Token → Scopes: Marketplace (Manage).
3. Install vsce: `npm install -g @vscode/vsce`
4. Create publisher: `vsce create-publisher tombstone` (or reuse if already exists).
5. Login: `vsce login tombstone`
6. Build and package: `cd workspace-vscode-ext && npm run build && vsce package`
7. Publish: `vsce publish`

**Before publishing, verify `workspace-vscode-ext/package.json` has:**
- `"publisher": "tombstone"` — must match the publisher created above
- `"repository"` field pointing to the GitHub repo
- `"icon"` field (128x128 PNG) — required for marketplace listing
- `"categories"` — currently `["Other"]`, consider adding `["Linters"]` or keeping as is
- `"galleryBanner"` — optional but recommended: `{"color": "#1a1a2e", "theme": "dark"}`

**Estimated time:** 1.5 hours (including Entra ID / Azure DevOps setup, which is the slowest part).

**Blockers:** Need a 128x128 PNG icon. The publisher name `tombstone` needs to be registered at https://marketplace.visualstudio.com/manage/createpublisher.

---

### 2.10 JetBrains Plugin Marketplace — Publish

**URL:** https://plugins.jetbrains.com/author/me

**Steps:**

1. Sign in to https://plugins.jetbrains.com with a JetBrains account (create one at account.jetbrains.com).
2. Click "Add new plugin" → Upload a file.
3. Build the plugin JAR: `cd workspace-jetbrains && ./gradlew buildPlugin` → produces `build/distributions/Tombstone-Feature-Flags-0.1.0.zip`.
4. Upload the ZIP via the marketplace UI.
5. Fill in: Plugin name: "Tombstone Feature Flags", Category: "Tools Integration", Tags: "feature-flags, devops"

**plugin.xml must include** (verify it exists in `workspace-jetbrains/src/main/resources/META-INF/plugin.xml`):
- `<vendor>` tag with email
- `<description>` of at least 40 words
- `<change-notes>` for this version

**Estimated time:** 1.5 hours.

**Blockers:** JetBrains account required. Plugin signing recommended but not required for initial publish. Verify `build.gradle.kts` `sinceBuild`/`untilBuild` ranges are set correctly (currently `243` to `251.*` per the file content above).

---

### 2.11 devhunt.org — Launch Listing

**URL:** https://devhunt.org

**What to do:** Create account, submit Tombstone as a new tool. Fill in:
- Name: Tombstone
- Tagline: Self-hosted blast-radius gates and auto-rollback for 5,000+ feature flags
- Description: 3–4 paragraphs covering the Knight Capital problem framing, what makes Tombstone different (causal graph vs. simple delivery), the core features, and the Docker Compose quick start.
- GitHub link: https://github.com/sairam0424/Tombstone
- Categories: DevOps, Infrastructure, Open Source
- Tags: feature-flags, golang, self-hosted, circuit-breaker

**Estimated time:** 30 minutes.

**Blockers:** Account creation required. No approval process described.

---

## SECTION 3: MEDIUM-TERM — This Month, Quality Gates Required

### 3.1 Fix Go Module Path — Required for Everything Go-Related

**THIS IS A PREREQUISITE FOR:** pkg.go.dev, awesome-go, CNCF Landscape, Go Report Card accuracy.

**Current state:** `services/flag-api/go.mod` declares `module github.com/tombstone/flag-api`. This does NOT match the GitHub repo path `github.com/sairam0424/Tombstone`.

**What to do:** Decide on the canonical Go module path. Two options:

Option A (if the GitHub org is `Not-Humans-World`): Change to `module github.com/Not-Humans-World/Tombstone/services/flag-api` — but this requires updating all internal imports across the Go workspace.

Option B (simpler): Keep the `tombstone` GitHub username or create a `tombstone` GitHub org and transfer the repo there, making the module path `github.com/tombstone/tombstone/services/flag-api` or similar.

**After fixing:** Tag the first SemVer release (`v0.1.0` or `v2.2.0`) and push to GitHub. pkg.go.dev will auto-index within 24 hours of the tag if the module path matches the repo path. No account needed for pkg.go.dev.

**Estimated time:** 2–4 hours depending on scope of import path refactoring.

---

### 3.2 awesome-selfhosted

**URL:** https://github.com/awesome-selfhosted/awesome-selfhosted-data/new/master/software

**This unlocks LibHunt auto-indexing automatically.**

**Eligibility check:** awesome-selfhosted requires the first release to be at least 4 months old. Tombstone is 6 days old as of today. **Do not submit until October 2026 at earliest** (4 months from creation). However, prepare everything now so you can submit the moment eligibility is met.

**What to prepare now:** Create a draft YAML file at `scratch-pad/awesome-selfhosted-tombstone.yml`:

```yaml
name: Tombstone
description: Production intelligence layer for 5,000+ feature flags — blast-radius
  gates, circuit-breaker auto-rollback, causal incident correlation, and ML-driven
  rollout recommendations. Alternative to LaunchDarkly.
website_url: https://github.com/sairam0424/Tombstone
source_code_url: https://github.com/sairam0424/Tombstone
license: MIT
tags:
  - feature-flags
demo_url:
depends_on_3rd_party_services: false
```

When eligible (October 2026): fork `awesome-selfhosted/awesome-selfhosted-data`, save the file as `software/tombstone.yml`, open a PR. The PR description must be written by a human — the ban on LLM-generated contributions is explicit and enforced.

**Estimated time to prep now:** 15 minutes to draft and save. PR submission in October: 30 minutes.

---

### 3.3 G2 — Create Vendor Profile

**URL:** https://sell.g2.com/create-a-profile

**Requirements from G2:** B2B software only, not in alpha/beta, website must be accessible, not a pure plugin/integration. Tombstone qualifies as a standalone B2B platform.

**What to do:**
1. Create a seller account at sell.g2.com.
2. Submit product under category: "Feature Flags Management."
3. Fill in all required fields: product name (Tombstone), website, description, pricing (Free/Open Source), deployment type (Self-hosted).
4. Wait for G2 research team to verify and create the profile (3–5 business days).
5. Once live, claim the profile (1–3 business days review).

**Key things the G2 submission needs:**
- A live product website or GitHub page that is publicly accessible and functional
- A clear description of the value proposition (not marketing copy — what it does technically)
- Pricing: list as "Free" (open source, self-hosted)

**Estimated time:** 1 hour for submission, then waiting.

---

### 3.4 CNCF Landscape PR

**URL:** https://github.com/cncf/landscape/pulls

**Eligibility:** Minimum 300 GitHub stars. Tombstone has 0 right now. **Hold this PR until the 300-star milestone.**

**What to prepare now:**

1. Design the Tombstone SVG logo to CNCF specs: must be square-ish, work on white background, no text embedded.
2. Save as `hosted_logos/tombstone.svg` locally.
3. Identify exact position in `landscape.yml` — the "Feature Flagging" category under "Observability and Analysis."
4. Draft the landscape.yml entry:
```yaml
- item:
  name: Tombstone
  homepage_url: https://github.com/sairam0424/Tombstone
  logo: tombstone.svg
  repo_url: https://github.com/sairam0424/Tombstone
  project: ''
  description: Production intelligence layer for feature flags — blast-radius gates, circuit-breaker auto-rollback, causal incident correlation
```

**When to submit:** After reaching 300 stars. The CNCF landscape regenerates daily — after merge, Tombstone appears within 24 hours.

**Estimated time:** 2 hours total (logo finalization + PR, spread over two sessions).

---

### 3.5 pheature-flags/awesome-feature-flags

**URL:** https://github.com/pheature-flags/awesome-feature-flags/pulls

Low stars (6) but targeted audience. The list currently has no self-hosted section — propose adding one. In the PR:

1. Add a new `## Self-Hosted` section under Services.
2. Add Tombstone as the first entry.
3. Add Flipt and Flagsmith also (to give the section substance — a section with 1 entry is weaker than one with 3).

PR description: "This PR adds a Self-Hosted subsection to distinguish open-source self-deployed options from SaaS products. Starting with Tombstone, Flipt, and Flagsmith."

**Estimated time:** 30 minutes.

---

### 3.6 Docker Hub — Push Images

**URL:** https://app.docker.com/signup

**After completing ghcr.io push (step 2.1), also push to Docker Hub for wider discovery.**

**Steps:**
1. Register at https://app.docker.com/signup. Create org: `tombstone` (check if taken first).
2. Extend the GitHub Actions workflow from step 2.1 to push to Docker Hub simultaneously:
```yaml
tags: |
  tombstone/tombstone-flag-api:latest
  tombstone/tombstone-flag-api:v2.2.0
  ghcr.io/sairam0424/tombstone-flag-api:latest
  ghcr.io/sairam0424/tombstone-flag-api:v2.2.0
```
3. Add repository secrets: `DOCKERHUB_USERNAME` and `DOCKERHUB_TOKEN`.

**Note on rate limits:** Free Docker Hub tier limits unauthenticated pulls to 100/6h. For an OSS project this matters once it gains traction — ghcr.io has no pull rate limits for public images, so it is the better primary distribution point.

**Estimated time:** 1 hour (mostly the Actions workflow extension from step 2.1).

---

### 3.7 Capterra — Free Listing

**URL:** https://vendors.capterra.com

**Steps:**
1. Go to https://vendors.capterra.com and create a vendor account.
2. Submit Tombstone under "Feature Flag Management."
3. Select "Free/Open Source" pricing.
4. Description must highlight: self-hosted, MIT licensed, no per-seat pricing.

**Estimated time:** 1 hour.

**Blockers:** Capterra requires a product website (GitHub page is acceptable). A dedicated landing page at, e.g., a Tombstone subdomain would increase acceptance probability.

---

### 3.8 ArtifactHub.io — Helm Chart

**URL:** https://artifacthub.io/sign-up

**What to do:**

1. Host the Helm chart as an OCI artifact on ghcr.io:
```bash
helm package infra/helm/flagmind
helm push tombstone-0.1.0.tgz oci://ghcr.io/sairam0424/helm-charts
```
2. Create `artifacthub-repo.yml` in the chart directory:
```yaml
repositoryID: <uuid-generated-by-artifacthub>
owners:
  - name: Tombstone
    email: you@example.com
```
3. Register at https://artifacthub.io/sign-up.
4. Add repository: Type "Helm Charts (OCI)", URL `oci://ghcr.io/sairam0424/helm-charts`.
5. ArtifactHub will auto-index the chart from the OCI registry.

**Estimated time:** 2 hours.

---

### 3.9 dr.to/awesome-feature-flags Channels (remaining low-effort awesome lists)

**dastergon/awesome-sre** (https://github.com/dastergon/awesome-sre/pulls):

Find the "SRE Tools" section. Add:
```
- [Tombstone](https://github.com/sairam0424/Tombstone) - Self-hosted production intelligence for feature flags with blast-radius gating and circuit-breaker auto-rollback.
```

Frame this PR around reliability, not feature delivery. The SRE framing: "circuit-breaker auto-rollback on SLO breach" and "causal incident correlation (what flag caused this outage)."

**adriannovegil/awesome-observability** (https://github.com/adriannovegil/awesome-observability/pulls):

The observability framing: Tombstone's incident correlation, anomaly detection (3-model ensemble), and "What Changed?" functionality make it defensible here. Add under "General Tools":
```
- [Tombstone](https://github.com/sairam0424/Tombstone) - Feature flag incident correlation and anomaly detection — identifies which of 5,000+ active flags caused a production incident.
```

Include a clear justification in the PR body for why this belongs under observability.

**Estimated time:** 30 minutes each.

---

## SECTION 4: LONG-TERM — 3–6 Months, Requires Adoption Evidence

### 4.1 avelino/awesome-go

**URL:** https://github.com/avelino/awesome-go/pulls

**BLOCKERS (all must be resolved):**
1. Repo age: must be at least 5 months old. Tombstone was created 2026-06-21. Earliest eligible: November 2026.
2. Go module path mismatch: `github.com/tombstone/flag-api` does not match the GitHub org path. Must be fixed (see step 3.1) and a working pkg.go.dev page must exist.
3. Test coverage: awesome-go requires a link to a coverage report (e.g., Codecov or coveralls.io badge showing 80%+).
4. Go Report Card: must show A grade at https://goreportcard.com/report/github.com/sairam0424/Tombstone (or the corrected module path).
5. A feature-flags category does not currently exist in awesome-go and requires 3 qualifying projects to create. Tombstone alone cannot create it — it would need to be added to an adjacent category like "DevOps Tools."

**What to do when eligible:** PR title format (exact): `feat: add Tombstone to DevOps Tools section`. Include: repo URL, pkg.go.dev URL (not just GitHub), Go Report Card link, coverage badge URL, README screenshot.

**Estimated time to prepare:** 4 hours when eligible (November 2026+).

---

### 4.2 Hacker News Show HN

**URL:** https://news.ycombinator.com/showhn.html

**Eligibility check before posting:** The project should be non-trivial and ready for users to try. With Docker Compose working and the dashboard live, this criterion is met architecturally. The real question is whether the demo flow is smooth enough for a first-time visitor to get value in under 10 minutes.

**What to prepare before posting:**
- A 3–5 minute demo GIF or video showing: `git clone` → `make dev` → dashboard live → create flag → kill switch → circuit-breaker trigger.
- A dedicated landing page or at minimum a clean README with screenshots.
- Self-hosted playground (e.g., a publicly accessible Fly.io instance) so HN readers can try it without running Docker locally. The `services/intelligence/fly.toml` exists — deploy the full stack there.

**Exact title:** `Show HN: Tombstone – self-hosted blast-radius gates and circuit-breaker auto-rollback for feature flags`

**First comment (post immediately after the title):** Write a 60–80 word founder comment covering: the problem (Knight Capital, 3am alarms from stale flags), what Tombstone does differently (causal graph vs. simple delivery), current state (v2.2.0, MIT, Docker Compose), and a specific question for the community (e.g., "What's the most painful thing about managing feature flags at scale in your org?").

**Timing:** Post on a weekday between 8–11 AM US Eastern time. Do NOT post on a weekend.

**Estimated time:** 3–5 hours of prep to get the demo and landing page right; 30 minutes to write the HN post.

---

### 4.3 Product Hunt Launch

**URL:** https://www.producthunt.com/posts/new

**Prep timeline (start 4–6 weeks before launch day):**
1. Create a Product Hunt account and be active — upvote and comment on others' tools for 4 weeks before your own launch.
2. Find a hunter with a developer tools track record to hunt you. Check https://www.producthunt.com/hunters for hunters who have previously featured Go/DevOps/infrastructure tools.
3. Prepare: product name (40 char max: "Tombstone"), tagline (60 char max: "Self-hosted blast-radius gates for feature flags"), 3–5 screenshots (1200x630), a 30-second demo video.
4. Tag as: Developer Tools, Open Source, DevOps.

**Launch day:** Post on Tuesday or Wednesday between 12:01 AM and 6 AM Pacific time. Reply to every comment on launch day — every single one.

**Estimated time:** 4–6 weeks of prep, 1 day of active management on launch day.

---

### 4.4 Slack App Directory

**URL:** https://api.slack.com/apps

**Eligibility gate:** At least 5 workspaces actively using the Tombstone Slack integration. This is the threshold before Slack will begin the review process.

**What to prepare now:**
- The marketplace service already integrates with Slack. Document the OAuth flow at `services/marketplace/`.
- Create a Tombstone Slack app at https://api.slack.com/apps with: bot token scopes `chat:write`, `channels:read`, incoming webhooks, and interactive components (for blast-radius alerts with approve/rollback buttons).
- The app must respond to Slack's URL verification challenge and handle action payloads for the kill-switch button.

**When ready to submit:** Use the "Submit to Slack Marketplace" option in the app's configuration page. Review takes up to 10 weeks for first-time submissions — plan accordingly.

**Estimated time:** 4–6 hours to formalize the Slack app OAuth + submission materials, after getting 5 real users.

---

### 4.5 PagerDuty Technology Partner

**URL:** https://www.pagerduty.com/partner-with-pagerduty/

**What to do when ready:** Submit the partner inquiry form. Select "Technology Partner." The integration (`services/marketplace/`) already has PagerDuty support. Before applying:
- Ensure the PagerDuty integration sends properly formatted Events API v2 payloads.
- Build a test suite that exercises the PagerDuty integration end-to-end.
- Have at least one production user using the PagerDuty integration.

**No specific eligibility criteria are published** — PagerDuty discloses requirements after the inquiry. Contact pagerduty.com/contact-us/ to get the technical requirements before building.

**Estimated time:** 2–3 hours for the application itself when ready.

---

### 4.6 Datadog integrations-extras

**URL:** https://github.com/DataDog/integrations-extras/compare

**Eligibility:** Working integration, willingness to be the long-term maintainer.

**Required files per Datadog integration PR:**
1. `manifest.json` — app_uuid (generate via `uuidgen`), app_id: `tombstone`, display_on_public_website: true, classifier_tags: `["Supported OS::Linux", "Category::Configuration & Deployment"]`
2. `README.md` with `## Overview`, `## Setup`, `## Support` sections
3. `CHANGELOG.md` — format exactly as described in the research data above
4. `metadata.csv` — one row per custom metric emitted (or empty if none)
5. `assets/service_checks.json`
6. `assets/dashboards/tombstone_overview.json` — a proper Datadog dashboard JSON

The `services/marketplace/` integration with Datadog is already built. The PR work is purely documentation and packaging.

**Estimated time:** 6–8 hours (building the Datadog dashboard JSON is the slowest part).

---

### 4.7 ThoughtWorks Technology Radar

**URL:** https://www.thoughtworks.com/radar

**No submission process exists.** This is purely organic. The only viable path:

1. Get Tombstone adopted on real projects where ThoughtWorks consultants work.
2. Speak at conferences where ThoughtWorks technologists attend: KubeCon, QCon, Devoxx, SREcon.
3. Be cited in articles by ThoughtWorks authors on martinfowler.com — the "What Changed?" incident correlation angle and the CUPED experimentation approach are both blog-post-worthy topics that would attract this audience.

**Target timeline:** 12–18 months minimum from a standing start.

---

### 4.8 Maven Central (Java SDK)

**URL:** https://central.sonatype.com/register

**Current state:** `packages/sdks/tombstone-java-sdk/build.gradle` has `group = "io.tombstone"` but no `artifactId` explicitly named.

**Steps when ready:**
1. Register at https://central.sonatype.com (new Central Portal, not deprecated OSSRH).
2. Verify namespace `io.tombstone` via DNS TXT record on the `tombstone.io` domain, or via GitHub proof if switching to `io.github.sairam0424`.
3. Generate and upload a GPG key to `keyserver.ubuntu.com`.
4. Add `central-publishing-gradle-plugin` to `tombstone-java-sdk/build.gradle`.
5. Add explicit `artifactId`: `flagmind-java`.
6. Run `./gradlew publishToCentralPortal`.

**Estimated time:** 3–4 hours (the GPG + namespace verification steps are the slow part).

---

### 4.9 NuGet.org (.NET SDK)

**URL:** https://www.nuget.org/users/account/LogOn

**Current state:** `packages/sdks/tombstone-dotnet-sdk/src/FlagMind/FlagMind.csproj` has `<PackageId>Tombstone.Client</PackageId>`.

**Steps:**
1. Register at nuget.org.
2. Generate an API key with scope limited to `Tombstone.Client`.
3. Pack: `dotnet pack --configuration Release`
4. Push: `dotnet nuget push Tombstone.Client.0.1.0.nupkg --api-key $NUGET_API_KEY --source https://api.nuget.org/v3/index.json`

**Estimated time:** 1 hour.

---

### 4.10 RubyGems (Ruby SDK)

**BLOCKER:** `packages/sdks/tombstone-ruby-sdk/flagmind.gemspec` has `s.name = "tombstone"` — this name is taken on RubyGems. Rename to `flagmind-ruby` before publishing.

**Steps after renaming:**
1. Register at https://rubygems.org/users/new.
2. Build: `cd packages/sdks/tombstone-ruby-sdk && gem build flagmind.gemspec` → produces `flagmind-ruby-0.1.0.gem`
3. Push: `gem push flagmind-ruby-0.1.0.gem`

**Estimated time:** 1 hour after the rename is done.

---

### 4.11 Console.dev

**Email:** hello@console.dev

**Requirements from their own criteria:** developer-first tool, self-service (Docker Compose qualifies), power-user features (CLI, API, dark mode in dashboard), fast, actively maintained. Tombstone meets all of these.

**Email content (keep under 150 words):**
> Subject: Tombstone — self-hosted feature flag intelligence layer for production
> 
> I built Tombstone: a self-hosted alternative to LaunchDarkly/Unleash that adds blast-radius gates, circuit-breaker auto-rollback, and causal incident correlation. The core idea: treat 5,000 active flags as a causal graph of production behavior so you can answer "which flag caused this incident?" rather than "what's the flag value?"
>
> - MIT licensed, self-hostable via Docker Compose (`make dev`)
> - CLI (`@tombstone/cli`), REST API, MCP server (8 tools), VS Code + JetBrains plugins
> - Dashboard with dark mode at localhost:3000
> - Go + Python + TypeScript, v2.2.0
> - GitHub: https://github.com/sairam0424/Tombstone
>
> Let me know if it's a fit for the newsletter.

**Estimated time:** 20 minutes to write and send.

---

### 4.12 Changelog Podcast

**URLs:** https://changelog.com/news/submit (news item) | https://changelog.com/request (podcast)

**News submission (submit once a meaningful blog post or release announcement exists):**
- Must be OSS and developer-interesting. Submit the GitHub repo link with a 1-sentence description.
- Do NOT submit commercial content — Tombstone as MIT OSS qualifies.

**Podcast request (longer lead time):**
- Create an account at changelog.com, then fill out https://changelog.com/request.
- Select show: "Changelog Interviews."
- Pitch text: "I built Tombstone — a self-hosted production intelligence layer for feature flags. The motivation is the Knight Capital Group incident: a stale feature flag destroyed $440M in 45 minutes. Tombstone adds blast-radius gates, circuit-breaker auto-rollback, and causal incident correlation so teams can answer 'which flag caused this?' in real time. I'd love to talk about how feature flags have evolved from simple toggles to a production reliability primitive, and what self-hosted tooling for this looks like."

**Estimated time:** 30 minutes for the news submission, 1 hour for a polished podcast request.

---

### 4.13 DevOps Weekly (Gareth Rushgrove)

**Email:** gareth@morethanseven.net

**Keep it under 3 sentences:**
> Tombstone is a self-hosted production intelligence layer for feature flags — blast-radius gating before rollouts, circuit-breaker auto-rollback on SLO breach, and "What Changed?" causal incident correlation. MIT, Docker Compose, Go + Python + TypeScript. GitHub: https://github.com/sairam0424/Tombstone.

Send when there is a substantive release (v3.0 or a major new capability) to give the editor a news peg.

**Estimated time:** 10 minutes.

---

### 4.14 Golang Weekly (Cooper Press)

**URL:** https://cooperpress.com/submit

**Submit once Tombstone has a blog post or significant Go architectural detail to share.** The Go Weekly editorial team responds to: new libraries, technical deep-dives on Go concurrency or performance, and benchmark results. A post titled "How we built a circuit-breaker-backed flag evaluator in Go with 5ms p99 latency" would be a direct fit.

**Estimated time:** 15 minutes for the form submission (after writing the blog post).

---

### 4.15 noted.lol

**URL:** https://noted.lol/contribute

**What to do:** Contact Jeremy via the noted.lol Discord or Contact page. Introduce yourself as a developer with an OSS self-hosted project and ask about contributing a guest post introducing Tombstone. The post should cover: what Tombstone is, why you built it (Knight Capital motivation), how to deploy it, and what makes it different from Flagsmith/Flipt.

**Estimated time:** 1 hour to write the intro message, 3–4 hours to write the article if accepted.

---

### 4.16 GitHub Trending (Algorithmic)

**URL:** https://github.com/trending/go

**Not a submission — a coordinated launch strategy.** To reach the Go daily trending page, the goal is 100+ stars in a 24-hour window. Achieve this by:

1. Post Show HN (step 4.2) — typically drives 50–200 stars on a good day.
2. Post on r/golang, r/devops, and r/sre on the same day as Show HN (cross-post same content, don't duplicate).
3. Send the Changelog news submission on the same day.
4. Notify #openfeature and #wg-platforms CNCF Slack on the same day.
5. Post a Twitter/X thread on the same day.

All five simultaneously creates the star velocity spike needed for trending.

---

## SECTION 5: NAMING BLOCKERS — FIX BEFORE ANY PUBLISHING

This section collects all naming conflicts identified in the repo files that must be resolved before publishing to registries. Publish with a conflicting name and you cannot fix it without a breaking rename.

| Registry | Current name in repo | Conflict? | Recommended name |
|----------|---------------------|-----------|-----------------|
| PyPI (SDK) | `tombstone` (`tombstone-python-sdk/pyproject.toml`) | YES — taken | `flagmind` |
| RubyGems | `tombstone` (`flagmind.gemspec`) | YES — taken | `flagmind-ruby` |
| Go module | `github.com/tombstone/flag-api` | YES — mismatches GitHub path | `github.com/sairam0424/Tombstone/services/flag-api` or transfer repo to `tombstone` org |
| npm | `@tombstone/*` | Unverified — check `npmjs.com/org/tombstone` | `@tombstone/*` if scope unclaimed |
| NuGet | `Tombstone.Client` | Unverified | `Tombstone.Client` (check nuget.org) |
| JetBrains plugin | `Tombstone Feature Flags` | Unverified | As-is if unclaimed |
| VS Code publisher | `tombstone` | Unverified | As-is if unclaimed |

**Recommended order of resolution:** Go module path fix first (it unblocks pkg.go.dev, Go Report Card, and awesome-go). Then npm scope claim. Then PyPI rename. Then Ruby rename.

---

## SUMMARY PRIORITY TABLE

| Platform | Impact | Effort | When | Key Blocker |
|----------|--------|--------|------|-------------|
| AlternativeTo | HIGH | 15 min | Today | None |
| OpenAlternative | HIGH | 20 min | Today | None |
| awesome-platform-engineering-tools | MEDIUM | 20 min | Today | None |
| awesome-platform-engineering | MEDIUM | 15 min | Today | None |
| tech-and-finance/awesome-feature-flags | MEDIUM | 15 min | Today | None |
| rootsongjc/awesome-cloud-native | MEDIUM | 15 min | Today | None |
| CNCF Slack join | HIGH | 10 min | Today | None |
| selfh.st | MEDIUM | 20 min | Today | None |
| StackShare | MEDIUM | 20 min | This week | None |
| npm publish | HIGH | 2 hr | This week | 2FA setup |
| OpenFeature PR | HIGH | 2 hr | This week | SVG logo |
| MCP Registry | HIGH | 1 hr | This week | npm publish first |
| VS Code Extension | MEDIUM | 1.5 hr | This week | Icon PNG + Azure DevOps |
| JetBrains Plugin | MEDIUM | 1.5 hr | This week | JetBrains account |
| PyPI (flagmind) | HIGH | 2 hr | This week | Rename pyproject.toml first |
| devhunt.org | MEDIUM | 30 min | This week | None |
| ghcr.io images | HIGH | 2 hr | This week | Dockerfile labels |
| Docker Hub images | HIGH | 1 hr | This month | ghcr.io first |
| G2 listing | HIGH | 1 hr | This month | Live product URL |
| Capterra listing | MEDIUM | 1 hr | This month | Live product URL |
| ArtifactHub (Helm) | MEDIUM | 2 hr | This month | OCI chart push |
| awesome-selfhosted | HIGH | 30 min | Oct 2026 | 4-month age rule |
| CNCF Landscape | HIGH | 2 hr | 300 stars | Star threshold |
| awesome-go | HIGH | 4 hr | Nov 2026 | Age + Go module fix + coverage |
| Show HN | HIGH | 5 hr | After demo ready | Live playground |
| Product Hunt | MEDIUM | 6 wk | After Show HN | Community warm-up |
| Slack App Directory | HIGH | 6 hr | After 5 workspaces | User adoption |
| Datadog integrations-extras | HIGH | 8 hr | After adoption | Dashboard JSON |
| Changelog Podcast | HIGH | 1 hr | After adoption | Editorial acceptance |