# Tombstone — Directory Listing Copy-Paste Sheet

Standard fields to copy-paste into every directory submission form.

---

## Standard Fields

**Name:** Tombstone

**Tagline (60 chars):** Self-hosted blast-radius gates for feature flags

**Short description (150 chars):**
Production intelligence layer for feature flags. Self-hosted, MIT. Circuit-breaker auto-rollback, blast-radius gates, causal incident correlation.

**Long description (500 chars):**
Tombstone treats your feature flag inventory as a live causal graph of production behavior — not just configuration. Before any rollout, blast-radius scoring shows the tier of impact (BLOCKED/HIGH/MEDIUM/LOW). After a change, circuit-breaker auto-rollback disables the flag automatically when error rates spike. The "What Changed?" query answers which flag caused an incident in milliseconds. ML-driven rollout (Thompson Sampling + LinUCB) advances rollout percentages automatically. Self-hosted via Docker Compose. MIT licensed. No per-seat pricing.

**GitHub URL:** https://github.com/sairam0424/Tombstone

**Website:** https://github.com/sairam0424/Tombstone

**License:** MIT

**Platforms:** Linux, Docker, Kubernetes, Self-Hosted

**Alternative to:** LaunchDarkly, Unleash, Flagsmith, Flipt, GrowthBook

**Categories:** Developer Tools, DevOps, Feature Flags, Infrastructure, Platform Engineering

**Tags:** feature-flags, circuit-breaker, blast-radius, golang, python, typescript, self-hosted, openfeature, devops, kubernetes

---

## Per-Platform Variations

### selfh.st (selfh.st/submit)
- Category: Software Development / Feature Flags
- Self-hostable: Yes
- Quick-start: `git clone https://github.com/sairam0424/Tombstone && cd Tombstone && cp infra/.env.example infra/.env && make dev`

### openalternative.co (openalternative.co/submit)
- Frame as: "Open source LaunchDarkly alternative"
- Alternative to: LaunchDarkly (primary), then Unleash, Flagsmith, Flipt
- Differentiator: "The only OSS flag platform with circuit-breaker auto-rollback and blast-radius scoring"

### StackShare (stackshare.io/contribute/tool)
- Category: Feature Flags
- Supported platforms: Linux, Docker, Kubernetes
- Pricing: Free / Open Source

### devhunt.org
- Categories: DevOps, Infrastructure, Open Source
- Tags: feature-flags, golang, self-hosted, circuit-breaker
- Description hook: Start with the Knight Capital story (440M loss from feature flag)

### G2 (sell.g2.com)
- Category: Feature Flag Management
- Pricing: Free (open source, self-hosted)
- Deployment: Self-hosted

### Capterra (vendors.capterra.com)
- Category: Feature Flag Management
- Pricing model: Free/Open Source

---

## JetBrains Marketplace

Submit at: https://plugins.jetbrains.com/author/me
Plugin name: Tombstone Feature Flags
Category: Tools Integration
Tags: feature-flags, devops

Build command:
```bash
cd workspace-jetbrains && ./gradlew buildPlugin
# Produces: build/distributions/Tombstone-Feature-Flags-*.zip
```

---

## VS Code Marketplace

Submit at: https://marketplace.visualstudio.com/manage

Prerequisites:
```bash
# 1. Create publisher at marketplace.visualstudio.com/manage/createpublisher
# 2. Install vsce
npm install -g @vscode/vsce

# 3. Login
vsce login tombstone

# 4. Convert icon SVG to PNG
rsvg-convert -w 128 -h 128 workspace-vscode-ext/images/icon.svg > workspace-vscode-ext/images/icon.png

# 5. Package and publish
cd workspace-vscode-ext
vsce package
vsce publish
```

---

## Published Articles

| Platform | URL | Date |
|----------|-----|------|
| dev.to (intro post) | https://dev.to/sai_ram_0000/i-built-a-self-hosted-feature-flag-platform-that-auto-rolls-back-bad-flags-heres-why-2m0k | 2026-06-28 |
