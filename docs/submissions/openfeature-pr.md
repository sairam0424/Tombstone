# OpenFeature PR Guide

Two separate PRs to submit. Do both on the same day.

---

## PR 1: interested-parties.md (5 minutes)

**Repo:** open-feature/community
**Fork URL:** https://github.com/open-feature/community
**File:** interested-parties.md
**How:** Fork → add one line → open PR (no OFEP required, no vetting committee)

**Line to add (add under the table in alphabetical order):**
```markdown
| Tombstone | https://github.com/sairam0424/Tombstone | Self-hosted production intelligence layer for feature flags — blast-radius gates, circuit-breaker auto-rollback, and causal incident correlation |
```

**PR title:** `feat: add Tombstone to interested parties`

**PR body:**
```markdown
Tombstone is a self-hosted feature flag platform with an OpenFeature-compatible
TypeScript provider (packages/sdks/@flagmind/core/src/provider.ts).

The provider implements:
- All 4 typed resolvers (resolveBooleanValue, resolveStringValue, resolveNumberValue, resolveObjectValue)
- All 5 provider states (NOT_READY, READY, STALE, ERROR, FATAL)
- No-throw guarantee — all errors return ResolutionDetails with errorCode

GitHub: https://github.com/sairam0424/Tombstone
```

**Commit command:** `git commit -s -m "feat: add Tombstone to interested parties"`
(the `-s` flag adds `Signed-off-by:` which is required per their CONTRIBUTING.md)

---

## PR 2: openfeature.dev ecosystem page (1-2 hours)

**Repo:** open-feature/openfeature.dev
**Fork URL:** https://github.com/open-feature/openfeature.dev
**Files to create:**
1. `src/datasets/providers/tombstone.ts`
2. `static/img/tombstone-no-fill.svg` (copy from assets/logo-no-fill.svg in this repo)

**File 1 — src/datasets/providers/tombstone.ts:**

Check the exact TypeScript interface by reading:
https://github.com/open-feature/openfeature.dev/blob/main/src/datasets/providers/index.ts

Then create the file matching that interface. Example structure (verify against current index.ts):

```typescript
import { Provider } from '.';

export const Tombstone: Provider = {
  name: 'Tombstone',
  logo: '/img/tombstone-no-fill.svg',
  technologies: [
    {
      technology: 'JavaScript',
      vendorOfficial: true,
      href: 'https://github.com/sairam0424/Tombstone/tree/main/packages/sdks/@flagmind/core',
      category: ['Server'],
    },
    {
      technology: 'JavaScript',
      vendorOfficial: true,
      href: 'https://github.com/sairam0424/Tombstone/tree/main/packages/sdks/@flagmind/react',
      category: ['Client'],
    },
    {
      technology: 'Python',
      vendorOfficial: true,
      href: 'https://github.com/sairam0424/Tombstone/tree/main/packages/sdks/tombstone-python-sdk',
      category: ['Server'],
    },
  ],
  description:
    'Self-hosted production intelligence layer for feature flags — blast-radius gates, circuit-breaker auto-rollback, and causal incident correlation.',
};
```

**File 2:** Copy `assets/logo-no-fill.svg` → `static/img/tombstone-no-fill.svg` in the forked repo.

**Add to providers index:** In `src/datasets/providers/index.ts`, import and add `Tombstone` to the `PROVIDERS` array alphabetically.

**PR title:** `feat: add Tombstone provider`

**PR body:**
```markdown
This PR registers Tombstone as an OpenFeature provider.

**Provider details:**
- TypeScript SDK (Server + Client): packages/sdks/@flagmind/core/src/provider.ts
- Python SDK: packages/sdks/tombstone-python-sdk/
- Vendor official: true

**Compliance:**
- All 4 typed resolvers implemented
- All 5 provider states (NOT_READY, READY, STALE, ERROR, FATAL)
- No-throw guarantee on all resolvers
- Tested against OpenFeature conformance suite

**GitHub:** https://github.com/sairam0424/Tombstone
```

**Commit command:** `git commit -s -m "feat: add Tombstone provider"`
