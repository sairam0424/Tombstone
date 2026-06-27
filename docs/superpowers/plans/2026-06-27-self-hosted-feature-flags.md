# Self-Hosted Feature Flags — Gate Undeployed Services

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Gate Tombstone's 4 undeployed-service views behind real Tombstone feature flags so deploying a service only requires flipping a flag — no code changes, no redeployment.

**Architecture:** Create 3 feature flags in the live flag-api DB (disabled by default). Add a `useFeatureFlags()` hook that reads the production environment snapshot from the flag-api and returns a map of `{ [key]: boolean }`. Wire the 4 affected views and the navigation sidebar to check these flags instead of the current localhost URL heuristic. When a service deploys, flipping its flag to enabled instantly unlocks the corresponding view.

**Tech Stack:** React 19, TanStack Query v5, flag-api REST (live), TypeScript 6

## Global Constraints

- Working directory: `/Users/sairamugge/Desktop/Not-Humans-World/Tombstone/workspace-dashboard`
- All LOCAL imports use `.js` extension (ESM/NodeNext)
- Flag-api live at `https://p01--tombstone--ddzvzj6b5rd6.code.run`
- SDK token: `REDACTED_ROTATE_THIS_TOKEN` (service token, not JWT)
- TanStack Query v5 — use `useQuery` with `queryKey` arrays
- Branch: `feat/self-hosted-feature-flags`
- Verify build after every task: `cd workspace-dashboard && npm run build`
- TypeScript strict — no `any`, no unused locals

---

## Task 1: Create the 3 feature flags in the live flag-api

**Files:**
- No code files — this is a one-time API call to the live flag-api

**Interfaces:**
- Produces: 3 flags in the DB with keys `feature-intelligence-service`, `feature-evaluator-service`, `feature-marketplace-service`, all ACTIVE state, all disabled in production environment

- [ ] **Step 1: Create feat branch**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone
git checkout main && git pull origin main
git checkout -b feat/self-hosted-feature-flags
```

- [ ] **Step 2: Create feature-intelligence-service flag**

```bash
curl -s -X POST \
  -H "Authorization: Bearer REDACTED_ROTATE_THIS_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"key":"feature-intelligence-service","name":"Intelligence Service","description":"Gates GovernanceDash and Experiments. Enable when evaluator+intelligence are deployed to Northflank Account 2.","flag_type":"BOOLEAN","owner_id":"sdk:dashboard-sdk"}' \
  https://p01--tombstone--ddzvzj6b5rd6.code.run/api/v1/flags
```

Expected: `{"key":"feature-intelligence-service","state":"ACTIVE",...}`

- [ ] **Step 3: Create feature-evaluator-service flag**

```bash
curl -s -X POST \
  -H "Authorization: Bearer REDACTED_ROTATE_THIS_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"key":"feature-evaluator-service","name":"Evaluator Service","description":"Gates SLOView and SLO links in FlagDetail. Enable when evaluator is deployed.","flag_type":"BOOLEAN","owner_id":"sdk:dashboard-sdk"}' \
  https://p01--tombstone--ddzvzj6b5rd6.code.run/api/v1/flags
```

Expected: `{"key":"feature-evaluator-service","state":"ACTIVE",...}`

- [ ] **Step 4: Create feature-marketplace-service flag**

```bash
curl -s -X POST \
  -H "Authorization: Bearer REDACTED_ROTATE_THIS_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"key":"feature-marketplace-service","name":"Marketplace Service","description":"Gates Marketplace view. Enable when marketplace service is deployed.","flag_type":"BOOLEAN","owner_id":"sdk:dashboard-sdk"}' \
  https://p01--tombstone--ddzvzj6b5rd6.code.run/api/v1/flags
```

Expected: `{"key":"feature-marketplace-service","state":"ACTIVE",...}`

- [ ] **Step 5: Verify all 3 flags exist and are disabled**

```bash
curl -s \
  -H "Authorization: Bearer REDACTED_ROTATE_THIS_TOKEN" \
  "https://p01--tombstone--ddzvzj6b5rd6.code.run/api/v1/environments/snapshot?environment=production" | python3 -c "
import json,sys
flags = json.load(sys.stdin).get('flags',[])
feature_flags = [f for f in flags if f['flag_key'].startswith('feature-')]
for f in feature_flags:
    print(f['flag_key'], '→ enabled:', f['enabled'])
"
```

Expected:
```
feature-intelligence-service → enabled: False
feature-evaluator-service → enabled: False
feature-marketplace-service → enabled: False
```

Note: These flags default to disabled because we never called PATCH /environments/production to enable them.

---

## Task 2: useFeatureFlags hook

**Files:**
- Create: `workspace-dashboard/src/hooks/useFeatureFlags.ts`

**Interfaces:**
- Produces:
  - `useFeatureFlags(): Record<string, boolean>` — returns map of all flag keys to their enabled state in production. Returns empty object while loading or on error (fail-open — show content if flag-api is unreachable).
  - `useFeatureFlag(key: string): boolean` — convenience wrapper, defaults to `false` if key not found.

- [ ] **Step 1: Create src/hooks/useFeatureFlags.ts**

```ts
// workspace-dashboard/src/hooks/useFeatureFlags.ts
import { useQuery } from '@tanstack/react-query';
import { API_URL, SDK_TOKEN } from '../config.js';

interface EnvFlagState {
  flag_key: string;
  enabled: boolean;
  rollout_pct: number;
}

/**
 * Fetches the production environment snapshot and returns a map of
 * feature flag keys to their enabled state.
 *
 * Fail-CLOSED: if the flag-api is unreachable, feature flags default to false
 * (undeployed services stay hidden). This is intentional — don't show a broken view.
 */
export function useFeatureFlags(): Record<string, boolean> {
  const { data } = useQuery({
    queryKey: ['feature-flags', 'production'],
    queryFn: async (): Promise<Record<string, boolean>> => {
      const r = await fetch(
        `${API_URL}/api/v1/environments/snapshot?environment=production`,
        { headers: { Authorization: `Bearer ${SDK_TOKEN}` } },
      );
      if (!r.ok) return {};
      const d = await r.json() as { flags?: EnvFlagState[] };
      const map: Record<string, boolean> = {};
      for (const f of (d.flags ?? [])) {
        if (f.flag_key.startsWith('feature-')) {
          map[f.flag_key] = f.enabled;
        }
      }
      return map;
    },
    staleTime: 60_000,    // re-check every 60s
    gcTime:    300_000,
    retry: 1,
  });
  return data ?? {};
}

/**
 * Returns whether a single feature flag is enabled.
 * Defaults to false if the flag doesn't exist or the snapshot hasn't loaded.
 */
export function useFeatureFlag(key: string): boolean {
  const flags = useFeatureFlags();
  return flags[key] ?? false;
}
```

- [ ] **Step 2: Verify build**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/workspace-dashboard
npm run build 2>&1 | tail -4
```

Expected: `✓ built in X.XXs`

- [ ] **Step 3: Commit**

```bash
cd ..
git add workspace-dashboard/src/hooks/useFeatureFlags.ts
git commit -m "feat(dashboard): useFeatureFlags + useFeatureFlag hooks — reads live flag-api snapshot, 60s cache"
```

---

## Task 3: Update GovernanceDash to use feature flag

**Files:**
- Modify: `workspace-dashboard/src/views/GovernanceDash/index.tsx`

**Interfaces:**
- Consumes: `useFeatureFlag('feature-intelligence-service')` from `../../hooks/useFeatureFlags.js`
- Produces: GovernanceDash shows offline EmptyState when `feature-intelligence-service` is false; shows full content when true

- [ ] **Step 1: Read the current GovernanceDash to find the isIntelAvailable block**

Read `workspace-dashboard/src/views/GovernanceDash/index.tsx` — find line with `const isIntelAvailable`.

- [ ] **Step 2: Replace isIntelAvailable with useFeatureFlag**

Find this import at the top:
```tsx
import { API_URL, INTEL_URL, SDK_TOKEN } from '../../config.js';
```

Add `useFeatureFlag` to imports:
```tsx
import { useFeatureFlag } from '../../hooks/useFeatureFlags.js';
```

Find inside the component:
```tsx
const isIntelAvailable = !INTEL_URL.includes('localhost');
```

Replace with:
```tsx
const isIntelAvailable = useFeatureFlag('feature-intelligence-service');
```

- [ ] **Step 3: Update the offline EmptyState message**

Find the existing EmptyState block (triggered when `!isIntelAvailable`). Update the body text to:
```tsx
<EmptyState
  heading="Intelligence service offline"
  body="Enable the 'feature-intelligence-service' flag in Tombstone when the intelligence service is deployed to unlock this view."
/>
```

- [ ] **Step 4: Verify build**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/workspace-dashboard
npm run build 2>&1 | tail -4
```

- [ ] **Step 5: Commit**

```bash
cd ..
git add workspace-dashboard/src/views/GovernanceDash/index.tsx
git commit -m "feat(dashboard): GovernanceDash gated by feature-intelligence-service flag"
```

---

## Task 4: Update SLOView + Experiments to use feature flags

**Files:**
- Modify: `workspace-dashboard/src/views/SLOView/index.tsx`
- Modify: `workspace-dashboard/src/views/Experiments/index.tsx`

**Interfaces:**
- Consumes: `useFeatureFlag` from `../../hooks/useFeatureFlags.js`

- [ ] **Step 1: Update SLOView**

Read `workspace-dashboard/src/views/SLOView/index.tsx`.

Add import:
```tsx
import { useFeatureFlag } from '../../hooks/useFeatureFlags.js';
```

Find:
```tsx
const isEvalAvailable = !EVAL_URL.includes('localhost');
```

Replace with:
```tsx
const isEvalAvailable = useFeatureFlag('feature-evaluator-service');
```

Update the offline EmptyState body:
```tsx
<EmptyState
  heading="Evaluator service offline"
  body="Enable the 'feature-evaluator-service' flag in Tombstone when the evaluator is deployed."
/>
```

- [ ] **Step 2: Update Experiments**

Read `workspace-dashboard/src/views/Experiments/index.tsx`.

Add import:
```tsx
import { useFeatureFlag } from '../../hooks/useFeatureFlags.js';
```

Find the top of the Experiments component function. Add right after the existing hooks:
```tsx
const isIntelAvailable = useFeatureFlag('feature-intelligence-service');
```

Find the existing empty/offline section (where the component renders when intel is unavailable — currently it falls through to mock data). Add an early return before the existing mock data rendering:

```tsx
if (!isIntelAvailable) {
  return (
    <div style={{ padding: '32px 40px' }}>
      <EmptyState
        heading="Intelligence service offline"
        body="Enable the 'feature-intelligence-service' flag in Tombstone when the intelligence service is deployed."
      />
    </div>
  );
}
```

Place this AFTER the hooks but BEFORE the JSX return. The exact location: after `const isIntelAvailable = ...` and before the first `return (`.

- [ ] **Step 3: Verify build**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/workspace-dashboard
npm run build 2>&1 | tail -4
```

- [ ] **Step 4: Commit**

```bash
cd ..
git add workspace-dashboard/src/views/SLOView/index.tsx workspace-dashboard/src/views/Experiments/index.tsx
git commit -m "feat(dashboard): SLOView + Experiments gated by evaluator/intelligence feature flags"
```

---

## Task 5: Update Marketplace to use feature flag

**Files:**
- Modify: `workspace-dashboard/src/views/Marketplace/index.tsx`

**Interfaces:**
- Consumes: `useFeatureFlag('feature-marketplace-service')` from `../../hooks/useFeatureFlags.js`
- Produces: Marketplace shows a clean offline EmptyState instead of "Error: Failed to fetch" when flag is false

- [ ] **Step 1: Read current Marketplace/index.tsx**

Read `workspace-dashboard/src/views/Marketplace/index.tsx` to understand the component structure.

- [ ] **Step 2: Add feature flag check**

Add imports at the top:
```tsx
import { useFeatureFlag } from '../../hooks/useFeatureFlags.js';
import { EmptyState } from '../../components/ui/index.js';
import { Puzzle } from 'lucide-react';
```

Add inside the Marketplace component (after existing hooks, before the JSX return):
```tsx
const isMarketplaceAvailable = useFeatureFlag('feature-marketplace-service');

if (!isMarketplaceAvailable) {
  return (
    <div style={{ padding: '32px 40px' }}>
      <EmptyState
        icon={<Puzzle size={40} />}
        heading="Marketplace service offline"
        body="Enable the 'feature-marketplace-service' flag in Tombstone when the marketplace service is deployed."
      />
    </div>
  );
}
```

This replaces the current "Error: Failed to fetch" experience with a clear, actionable message.

- [ ] **Step 3: Verify build**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/workspace-dashboard
npm run build 2>&1 | tail -4
```

- [ ] **Step 4: Commit**

```bash
cd ..
git add workspace-dashboard/src/views/Marketplace/index.tsx
git commit -m "feat(dashboard): Marketplace gated by feature-marketplace-service flag — clean offline state replaces error banner"
```

---

## Task 6: Update navigation sidebar to hide disabled views + push PR

**Files:**
- Modify: `workspace-dashboard/src/App.tsx`

**Interfaces:**
- Consumes: `useFeatureFlags()` from `./hooks/useFeatureFlags.js`
- Produces: Navigation sidebar hides Governance, Experiments, and Marketplace nav items when their feature flags are disabled. When flags are enabled, nav items reappear automatically.

- [ ] **Step 1: Read App.tsx nav config**

Read `workspace-dashboard/src/App.tsx` — find the `nav` array (around line 96) with INTELLIGENCE section containing governance, experiments, marketplace items.

- [ ] **Step 2: Add useFeatureFlags to App.tsx**

Add import at top of App.tsx:
```tsx
import { useFeatureFlags } from './hooks/useFeatureFlags.js';
```

Inside the `App()` function, add after the existing state:
```tsx
const featureFlags = useFeatureFlags();
```

- [ ] **Step 3: Filter nav items based on feature flags**

Find the `nav` array definition (the `const nav: ...` at the top level, outside the component). Change it to a function inside the component that accepts `featureFlags`:

Replace the static `nav` constant with a computed value inside `App()`:

```tsx
// Inside App() function, after featureFlags declaration:
const nav = [
  {
    heading: 'FLAGS',
    items: [{ to: '/', label: 'All Flags', icon: <IconFlag />, end: true }],
  },
  {
    heading: 'OPERATIONS',
    items: [
      { to: '/incident',    label: 'What Changed?', icon: <IconLightning /> },
      { to: '/approvals',   label: 'Approvals',     icon: <IconCheckCircle /> },
      { to: '/break-glass', label: 'Break-Glass',   icon: <IconShield /> },
    ],
  },
  {
    heading: 'INTELLIGENCE',
    items: [
      { to: '/graph',       label: 'Causal Graph',  icon: <IconShare /> },
      ...(featureFlags['feature-intelligence-service']
        ? [
            { to: '/governance',  label: 'Governance',   icon: <IconBarChart /> },
            { to: '/experiments', label: 'Experiments',  icon: <IconBeaker /> },
          ]
        : []),
      ...(featureFlags['feature-marketplace-service']
        ? [{ to: '/marketplace', label: 'Marketplace', icon: <IconPuzzle /> }]
        : []),
    ],
  },
];
```

Also add SLO to FlagDetail when evaluator is available — but that's a follow-up; skip for now to keep scope tight.

- [ ] **Step 4: Remove the static nav constant at module level**

Find the `const nav: { heading: string; items: NavItem[] }[] = [` at module level and delete it (since we now define it inside the component).

- [ ] **Step 5: Verify build**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/workspace-dashboard
npm run build 2>&1 | tail -4
```

Expected: `✓ built in X.XXs` zero TypeScript errors.

- [ ] **Step 6: Commit + push + create PR**

```bash
cd ..
git add workspace-dashboard/src/App.tsx
git commit -m "feat(dashboard): nav sidebar hides intelligence/marketplace items when feature flags disabled"

export PATH="/opt/homebrew/bin:$PATH"
git push origin feat/self-hosted-feature-flags

gh pr create \
  --title "feat(dashboard): self-hosted feature flags — gate undeployed services via live flag-api" \
  --base main \
  --head feat/self-hosted-feature-flags \
  --body "$(cat <<'EOF'
## Summary
Tombstone now uses its own feature flags to gate its own undeployed services. No more hardcoded localhost URL heuristics — the dashboard asks the flag-api whether each service feature is enabled.

**3 feature flags created in live DB (all disabled by default):**
- \`feature-intelligence-service\` → gates GovernanceDash + Experiments
- \`feature-evaluator-service\` → gates SLOView
- \`feature-marketplace-service\` → gates Marketplace

**To unlock a view when its service deploys:**
1. Go to the dashboard → All Flags
2. Find \`feature-intelligence-service\` (or evaluator/marketplace)
3. Enable it → the view unlocks instantly for all users, no code change, no redeployment

**Changes:**
- New \`useFeatureFlags()\` + \`useFeatureFlag(key)\` hooks — queries production snapshot, 60s cache
- GovernanceDash, SLOView, Experiments: replaced localhost URL check with \`useFeatureFlag()\`
- Marketplace: replaced error banner with clean EmptyState + feature flag gate
- App.tsx nav sidebar: dynamically hides disabled views

## Test plan
- [ ] Open dashboard — Governance/Experiments/Marketplace show clean offline states (not errors)
- [ ] Nav sidebar hides Governance, Experiments, Marketplace items
- [ ] Enable feature-intelligence-service flag → Governance + Experiments nav items appear
- [ ] Enable feature-marketplace-service flag → Marketplace nav item appears
- [ ] Disable flags again → items hide
- [ ] Build passes zero errors
EOF
)"
```

---

## Self-Review

**Spec coverage:**
- ✅ Create 3 flags in DB → Task 1
- ✅ `useFeatureFlags` + `useFeatureFlag` hook → Task 2
- ✅ GovernanceDash gated → Task 3
- ✅ SLOView gated → Task 4
- ✅ Experiments gated → Task 4
- ✅ Marketplace gated → Task 5
- ✅ Navigation sidebar hides disabled items → Task 6
- ✅ PR created → Task 6

**Placeholder scan:** All steps have complete code. No TBD.

**Type consistency:**
- `useFeatureFlags()` returns `Record<string, boolean>` — consumed as `featureFlags['feature-*']` in App.tsx (boolean indexing is correct)
- `useFeatureFlag(key)` returns `boolean` — consumed as `const isIntelAvailable = useFeatureFlag(...)` (boolean assignment matches existing pattern)
