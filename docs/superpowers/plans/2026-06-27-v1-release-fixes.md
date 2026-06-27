# Tombstone v1 Release Fixes

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix 3 critical and 4 major gaps identified in the honest post-missed-release audit so Tombstone v1 ships as a genuinely functional product.

**Architecture:** Three phases: (1) Critical code fixes — rollout slider, experiments honesty, command palette; (2) Polish fixes — stale data, silent failures; (3) Manual ops — deploy evaluator + intelligence to Northflank Account 2 and flip env vars. Phases 1+2 are pure frontend React 19, no backend changes needed.

**Tech Stack:** React 19, TanStack Query v5, Sonner toasts, motion/react, TypeScript 6

## Global Constraints

- Working dir: `/Users/sairamugge/Desktop/Not-Humans-World/Tombstone/workspace-dashboard`
- Branch: `feat/v1-release-fixes` (create from main before starting)
- All LOCAL imports use `.js` extension (ESM/NodeNext)
- Motion v12: import from `motion/react`
- TanStack Query v5: `useQueryClient`, `invalidateQueries({ queryKey: [...] })`, `refetchInterval`
- Sonner: `import { toast } from 'sonner'`
- Build verify: `cd workspace-dashboard && npm run build` — must pass zero errors
- TypeScript strict — no `any`, no unused locals
- PATCH endpoint body: `{ enabled: boolean, rollout_pct: number }` (both required, confirmed from flags.go)

---

## Phase 1 — Critical Code Fixes

### Task 1: Rollout % slider in FlagDetail

**Files:**
- Modify: `workspace-dashboard/src/views/FlagDetail/index.tsx`

**Interfaces:**
- Consumes: `activeEnvState.rollout_pct` (number), `flagKey` (string), `activeEnv` (Env), `activeEnvState.enabled` (boolean), `API_URL`, `SDK_TOKEN` from config
- Produces: Inline editable rollout card — user clicks, slider appears (0–100 step 5), on release PATCHes `{ enabled: currentEnabled, rollout_pct: newPct }`, shows toast on success/error, updates snapshot cache

- [ ] **Step 1: Create branch**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone
git checkout main && git pull origin main
git checkout -b feat/v1-release-fixes
```

- [ ] **Step 2: Read the current FlagDetail file**

Read the full `workspace-dashboard/src/views/FlagDetail/index.tsx` to understand where `activeEnvState.rollout_pct` is displayed (around line 695–715, inside the grid with "Rollout %" card).

- [ ] **Step 3: Add rollout editing state and handler**

After the existing `const [rollbackPending, setRollbackPending]` state declaration inside `FlagDetail` (or at the top of the component), add:

```tsx
const [editingRollout, setEditingRollout] = useState(false);
const [pendingRollout, setPendingRollout] = useState<number | null>(null);
const queryClient = useQueryClient();

const handleRolloutChange = async (newPct: number) => {
  if (!flagKey || !activeEnvState) return;
  const prev = activeEnvState.rollout_pct;
  // Optimistic update in snapshot cache
  queryClient.setQueryData(['snapshot', activeEnv], (old: Record<string, EnvStateRow> | undefined) => {
    if (!old || !flagKey) return old;
    return { ...old, [flagKey]: { ...old[flagKey], rollout_pct: newPct } };
  });
  try {
    const r = await fetch(`${API_URL}/api/v1/flags/${flagKey}/environments/${activeEnv}`, {
      method: 'PATCH',
      headers: { Authorization: `Bearer ${SDK_TOKEN}`, 'Content-Type': 'application/json' },
      body: JSON.stringify({ enabled: activeEnvState.enabled, rollout_pct: newPct }),
    });
    if (!r.ok) throw new Error(`${r.status}`);
    toast.success(`Rollout set to ${newPct}%`, { description: flagKey });
    queryClient.invalidateQueries({ queryKey: ['snapshot', activeEnv] });
  } catch (err) {
    toast.error('Rollout update failed', { description: String(err) });
    // Revert optimistic update
    queryClient.setQueryData(['snapshot', activeEnv], (old: Record<string, EnvStateRow> | undefined) => {
      if (!old || !flagKey) return old;
      return { ...old, [flagKey]: { ...old[flagKey], rollout_pct: prev } };
    });
  } finally {
    setEditingRollout(false);
    setPendingRollout(null);
  }
};
```

Also add `useQueryClient` to the TanStack Query import and `toast` from sonner (check if already imported — if not, add `import { toast } from 'sonner';`).

- [ ] **Step 4: Replace the read-only rollout card with an editable one**

Find the Rollout % card:
```tsx
<div className="rounded-lg p-3" style={{ background: '#0d0d0d', border: '1px solid #1a1a1a' }}>
  <div className="text-gray-500 text-xs mb-1.5">Rollout %</div>
  <RolloutBar pct={activeEnvState.rollout_pct} envKey={activeEnv} />
</div>
```

Replace with:
```tsx
<div
  className="rounded-lg p-3"
  style={{ background: '#0d0d0d', border: `1px solid ${editingRollout ? 'var(--color-accent, #38e1ff)' : '#1a1a1a'}`, cursor: 'pointer', transition: 'border-color 0.15s' }}
  onClick={() => { setEditingRollout(true); setPendingRollout(activeEnvState.rollout_pct); }}
  title="Click to edit rollout %"
>
  <div className="text-gray-500 text-xs mb-1.5" style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
    <span>Rollout %</span>
    {!editingRollout && <span style={{ color: 'var(--color-accent, #38e1ff)', fontSize: 10 }}>edit</span>}
  </div>
  {editingRollout ? (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }} onClick={e => e.stopPropagation()}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
        <input
          type="range"
          min={0}
          max={100}
          step={5}
          value={pendingRollout ?? activeEnvState.rollout_pct}
          onChange={e => setPendingRollout(Number(e.target.value))}
          style={{ flex: 1, accentColor: 'var(--color-accent, #38e1ff)' }}
          autoFocus
        />
        <span style={{ fontSize: 13, fontWeight: 600, color: 'var(--color-fg, #e9ecf5)', minWidth: 36, textAlign: 'right' as const }}>
          {pendingRollout ?? activeEnvState.rollout_pct}%
        </span>
      </div>
      <div style={{ display: 'flex', gap: 6 }}>
        <button
          onClick={() => handleRolloutChange(pendingRollout ?? activeEnvState.rollout_pct)}
          style={{ flex: 1, padding: '4px 0', borderRadius: 6, border: 'none', background: 'var(--color-accent, #38e1ff)', color: '#07080d', fontSize: 11, fontWeight: 600, cursor: 'pointer' }}
        >
          Save
        </button>
        <button
          onClick={() => { setEditingRollout(false); setPendingRollout(null); }}
          style={{ flex: 1, padding: '4px 0', borderRadius: 6, border: '1px solid #1a1a1a', background: 'transparent', color: '#6b7280', fontSize: 11, cursor: 'pointer' }}
        >
          Cancel
        </button>
      </div>
    </div>
  ) : (
    <RolloutBar pct={activeEnvState.rollout_pct} envKey={activeEnv} />
  )}
</div>
```

- [ ] **Step 5: Verify build**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/workspace-dashboard
npm run build 2>&1 | tail -5
```

Expected: `✓ built in X.XXs`

- [ ] **Step 6: Commit**

```bash
cd ..
git add workspace-dashboard/src/views/FlagDetail/index.tsx
git commit -m "feat(dashboard): FlagDetail — inline rollout % slider with optimistic update and toast feedback"
```

---

### Task 2: Experiments — honest demo data banner

**Files:**
- Modify: `workspace-dashboard/src/views/Experiments/index.tsx`

**Interfaces:**
- Consumes: `MOCK_EXPERIMENTS` array (lines 43-88), existing render section around line 625–655
- Produces: Yellow "Demo data" banner above the experiment catalogue; count shows "(demo)" suffix; no behavior change

- [ ] **Step 1: Read the Experiments file**

Read `workspace-dashboard/src/views/Experiments/index.tsx`. Find:
1. Where `runningCount`, `completeCount`, `draftCount` are computed from `MOCK_EXPERIMENTS` (~line 391)
2. Where the count stats are rendered (~line 625–640)
3. Where `MOCK_EXPERIMENTS.map(...)` renders the catalogue cards (~line 652)

- [ ] **Step 2: Add demo banner constant**

At the top of the file, after the imports, add a `IS_DEMO_DATA` constant:

```tsx
// MOCK_EXPERIMENTS are always shown until a real warehouse is connected.
// This flag drives the honest "demo data" banner.
const IS_DEMO_DATA = true;
```

- [ ] **Step 3: Add demo banner before the catalogue**

Find the section that renders experiment counts and the catalogue grid. Before the catalogue grid (before `{MOCK_EXPERIMENTS.map(...)}`), add:

```tsx
{IS_DEMO_DATA && (
  <div style={{
    marginBottom: 16,
    padding: '10px 14px',
    borderRadius: 8,
    background: 'color-mix(in oklab, #fbbf24 10%, transparent)',
    border: '1px solid color-mix(in oklab, #fbbf24 30%, transparent)',
    display: 'flex',
    alignItems: 'center',
    gap: 8,
    fontSize: 12,
    color: '#fbbf24',
  }}>
    <span style={{ fontSize: 14 }}>⚠</span>
    <span>
      <strong>Demo data</strong> — These are example experiments.
      Connect your warehouse DSN in the analysis panel to run real A/B tests.
    </span>
  </div>
)}
```

- [ ] **Step 4: Mark the count as demo**

Find where the experiment counts are shown (something like `{MOCK_EXPERIMENTS.length} total`). Add `(demo)` suffix:

```tsx
{MOCK_EXPERIMENTS.length} total {IS_DEMO_DATA && <span style={{ color: '#fbbf24', fontSize: 10, fontWeight: 500 }}>(demo)</span>}
```

- [ ] **Step 5: Verify build**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/workspace-dashboard
npm run build 2>&1 | tail -4
```

- [ ] **Step 6: Commit**

```bash
cd ..
git add workspace-dashboard/src/views/Experiments/index.tsx
git commit -m "feat(dashboard): Experiments — honest demo data banner, marks mock catalogue clearly"
```

---

### Task 3: CommandPalette — wire live flags

**Files:**
- Modify: `workspace-dashboard/src/App.tsx`

**Interfaces:**
- Consumes: `useFlags()` from `./hooks/useFlags.js` — returns `{ data: FlagItem[] }` where `FlagItem = { id, key, name, description, state, owner_id, flag_type }`
- Produces: CommandPalette `flags` prop populated with live flags from TanStack Query cache; Cmd+K flag search works

- [ ] **Step 1: Read App.tsx**

Read `workspace-dashboard/src/App.tsx`. Find:
1. Line ~126: `const [flags] = useState<{ key: string; name: string; state: string }[]>([]);`
2. Line ~528: `<CommandPalette open={cmdOpen} onClose={() => setCmdOpen(false)} flags={flags} />`

- [ ] **Step 2: Replace empty useState with useFlags**

Add `useFlags` to imports at the top of App.tsx:
```tsx
import { useFlags } from './hooks/useFlags.js';
```

Find and remove:
```tsx
const [flags] = useState<{ key: string; name: string; state: string }[]>([]);
```

Replace with:
```tsx
const { data: flagsData = [] } = useFlags();
const flags = flagsData.map(f => ({ key: f.key, name: f.name, state: f.state }));
```

- [ ] **Step 3: Verify build**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/workspace-dashboard
npm run build 2>&1 | tail -4
```

- [ ] **Step 4: Commit**

```bash
cd ..
git add workspace-dashboard/src/App.tsx
git commit -m "fix(dashboard): CommandPalette — populate flags from live useFlags() hook, fixes empty search"
```

---

## Phase 2 — Polish Fixes

### Task 4: ApprovalQueue — 30s auto-refresh

**Files:**
- Modify: `workspace-dashboard/src/views/ApprovalQueue/index.tsx`

**Interfaces:**
- Consumes: existing `useEffect` + `fetch` pattern
- Produces: Change requests auto-refresh every 30 seconds; no manual reload needed

- [ ] **Step 1: Read ApprovalQueue/index.tsx**

Read the full file. Note that it uses raw `useEffect + fetch` not TanStack Query. The fix is to convert the fetch to `useQuery` with `refetchInterval`.

- [ ] **Step 2: Convert to useQuery with 30s refetch**

Add imports at top of file:
```tsx
import { useQuery } from '@tanstack/react-query';
```

The existing component has a `useEffect` that fetches change requests. Find the pattern:
```tsx
useEffect(() => {
  setLoading(true);
  fetch(`${apiUrl}/api/v1/change-requests?status=PENDING`, { headers })
    .then(r => r.json())
    .then((d: { requests?: ChangeRequest[] }) => setRequests(d.requests ?? []))
    .catch(console.error)
    .finally(() => setLoading(false));
}, []);
```

Replace the `requests` state + `loading` state + `useEffect` with:
```tsx
const { data: requests = [], isLoading: loading } = useQuery({
  queryKey: ['change-requests', 'PENDING'],
  queryFn: async (): Promise<ChangeRequest[]> => {
    const r = await fetch(`${apiUrl}/api/v1/change-requests?status=PENDING`, { headers });
    if (!r.ok) return [];
    const d = await r.json() as { requests?: ChangeRequest[] };
    return d.requests ?? [];
  },
  refetchInterval: 30_000,
});
```

Remove the now-unused `useState` declarations for `requests` and `loading` (keep `acting` state if present).

- [ ] **Step 3: Verify build**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/workspace-dashboard
npm run build 2>&1 | tail -4
```

- [ ] **Step 4: Commit**

```bash
cd ..
git add workspace-dashboard/src/views/ApprovalQueue/index.tsx
git commit -m "fix(dashboard): ApprovalQueue — auto-refresh every 30s via TanStack Query refetchInterval"
```

---

### Task 5: BreakGlass — refresh list after token create

**Files:**
- Modify: `workspace-dashboard/src/views/BreakGlass/index.tsx`

**Interfaces:**
- Consumes: existing `loadTokens()` function, existing `create()` function
- Produces: After a token is successfully created, `loadTokens()` is called immediately so the new token appears in the list without a page reload

- [ ] **Step 1: Read BreakGlass/index.tsx**

Read `workspace-dashboard/src/views/BreakGlass/index.tsx`. Find the `create` function (around line 33–50). Note that after `setNewToken(data.token)` it already calls `loadTokens()`. 

Check if `loadTokens` actually works after create: trace the function. If it uses the same `useEffect` pattern it may be a closure issue. The simplest fix is to ensure `loadTokens` is called and awaited correctly.

- [ ] **Step 2: Ensure loadTokens is triggered after create**

In the `create` function, after the success block that sets `newToken`, verify `loadTokens()` is called. If it is already there but not working, the issue is likely that the `tokens` state from the initial load is a closure. Fix by adding a small delay or converting to useQuery:

Add `import { useQueryClient } from '@tanstack/react-query';` at top.

Inside the component, add:
```tsx
const queryClient = useQueryClient();
```

In the `create` function success block, after `setNewToken(data.token)`, add:
```tsx
queryClient.invalidateQueries({ queryKey: ['break-glass-tokens'] });
```

Then convert the `loadTokens` useEffect to use `useQuery` with queryKey `['break-glass-tokens']`:
```tsx
const { data: tokens = [] } = useQuery({
  queryKey: ['break-glass-tokens'],
  queryFn: async (): Promise<BGToken[]> => {
    const r = await fetch(`${apiUrl}/api/v1/break-glass/tokens`, { headers });
    if (!r.ok) return [];
    const d = await r.json() as { tokens?: BGToken[] };
    return d.tokens ?? [];
  },
});
```

Remove the old `useEffect` that called `loadTokens` and the `setTokens` state.

- [ ] **Step 3: Verify build**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/workspace-dashboard
npm run build 2>&1 | tail -4
```

- [ ] **Step 4: Commit**

```bash
cd ..
git add workspace-dashboard/src/views/BreakGlass/index.tsx
git commit -m "fix(dashboard): BreakGlass — token list auto-refreshes after create via TanStack Query invalidation"
```

---

### Task 6: GovernanceDash — error toast on apply failure + push PR

**Files:**
- Modify: `workspace-dashboard/src/views/GovernanceDash/index.tsx`

**Interfaces:**
- Consumes: existing `handleApplyRec` catch block (line ~690), `toast` from sonner
- Produces: When autonomous rollout apply fails, user sees a toast error instead of silence

- [ ] **Step 1: Read GovernanceDash handleApplyRec**

Read `workspace-dashboard/src/views/GovernanceDash/index.tsx`. Find the `handleApplyRec` function (around line 678). The catch block is:
```tsx
} catch {
  // silently ignore
}
```

- [ ] **Step 2: Check if toast is already imported**

Look for `import { toast } from 'sonner'` at the top of the file. If not present, add it.

- [ ] **Step 3: Replace silent catch with error toast**

Replace:
```tsx
} catch {
  // silently ignore
}
```

With:
```tsx
} catch (err) {
  toast.error('Failed to apply recommendation', {
    description: err instanceof Error ? err.message : 'Rollout update failed',
  });
}
```

- [ ] **Step 4: Verify build**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/workspace-dashboard
npm run build 2>&1 | tail -4
```

- [ ] **Step 5: Commit + push + create PR**

```bash
cd ..
git add workspace-dashboard/src/views/GovernanceDash/index.tsx
git commit -m "fix(dashboard): GovernanceDash — show toast.error when autonomous rollout apply fails"

export PATH="/opt/homebrew/bin:$PATH"
git push origin feat/v1-release-fixes

gh pr create \
  --title "fix(dashboard): v1 release fixes — rollout slider, demo banner, CommandPalette, stale data, silent errors" \
  --base main \
  --head feat/v1-release-fixes \
  --repo sairam0424/Tombstone \
  --body "$(cat <<'EOF'
## v1 Release Fixes — 6 items from post-release audit

### Critical
- **Rollout % slider**: FlagDetail now has an inline editable rollout slider (click the Rollout % card). PATCHes enabled+rollout_pct together with optimistic update and toast feedback.
- **Experiments demo banner**: Clear ⚠ banner marks mock catalogue as demo data. Count shows (demo) suffix. Honest to users.
- **CommandPalette flag search**: Wired to live useFlags() — Cmd+K now actually searches real flags.

### Major
- **ApprovalQueue auto-refresh**: Converted to TanStack Query with refetchInterval:30_000. No more stale data.
- **BreakGlass token refresh**: invalidateQueries after create — new token appears immediately without page reload.
- **GovernanceDash silent failure**: toast.error() in handleApplyRec catch — users know when autonomous rollout fails.

## Test plan
- [ ] FlagDetail: click Rollout % card → slider appears → drag → Save → toast "Rollout set to X%"
- [ ] Experiments: yellow demo banner visible above catalogue
- [ ] Cmd+K → type flag name → results appear
- [ ] ApprovalQueue: create a change request externally → appears in list within 30s
- [ ] BreakGlass: create token → appears in list immediately below
- [ ] GovernanceDash: with intelligence offline → apply rec → toast error shown
- [ ] npm run build passes zero errors
EOF
)"
```

---

## Phase 3 — Manual Ops: Deploy Evaluator + Intelligence

### Task 7: Deploy evaluator + intelligence to Northflank Account 2

**Files:** No code changes — this is a manual ops deployment

**What you need:**
- Northflank Account 2 (sairam056 or the second account)
- Values from `infra/.env.secrets`

- [ ] **Step 1: Deploy evaluator service**

Go to **https://app.northflank.com** on Account 2 → project `tombstone` → **Deploy a repository**:

| Field | Value |
|-------|-------|
| Name | `tombstone-evaluator` |
| Repo | `sairam0424/Tombstone` |
| Branch | `main` |
| Build context | `/services/evaluator` |
| Dockerfile | `/services/evaluator/Dockerfile` |
| Port | `8082` HTTP Public |

Runtime env vars:
```
PORT = 8082
DB_URL = [from infra/.env.secrets]
REDIS_URL = [from infra/.env.secrets]
JWT_SECRET = REDACTED_ROTATE_THIS_TOKEN
FLAG_API_URL = https://p01--tombstone--ddzvzj6b5rd6.code.run
UPSTASH_REDIS_REST_URL = [from infra/.env.secrets]
UPSTASH_REDIS_REST_TOKEN = [from infra/.env.secrets]
```

- [ ] **Step 2: Deploy intelligence service**

Same project on Account 2 → **Deploy a repository**:

| Field | Value |
|-------|-------|
| Name | `tombstone-intelligence` |
| Repo | `sairam0424/Tombstone` |
| Branch | `main` |
| Build context | `/services/intelligence` |
| Dockerfile | `/services/intelligence/Dockerfile` |
| Port | `8083` HTTP Public |

Runtime env vars:
```
PORT = 8083
DB_URL = [from infra/.env.secrets]
REDIS_URL = [from infra/.env.secrets]
EMBEDDING_BACKEND = bedrock
BEDROCK_ACCESS_KEY_ID = [from infra/.env.secrets]
BEDROCK_SECRET_ACCESS_KEY = [from infra/.env.secrets]
BEDROCK_REGION = us-east-1
CONSUMER_BACKEND = redis
TOMBSTONE_ENVIRONMENTS = production
UPSTASH_REDIS_REST_URL = [from infra/.env.secrets]
UPSTASH_REDIS_REST_TOKEN = [from infra/.env.secrets]
```

- [ ] **Step 3: Verify both services are healthy**

```bash
curl -s --max-time 10 https://p01--tombstone-evaluator--XXXXXXX.code.run/health
curl -s --max-time 10 https://p01--tombstone-intelligence--XXXXXXX.code.run/health
```

Expected: `{"status":"ok"}` from both

- [ ] **Step 4: Set Vercel env vars and redeploy**

Go to **https://vercel.com/sairams-projects-d50d7437/tombstone-workspace-dashboard-ihw7/settings/environment-variables**

Add:
```
VITE_ENABLE_INTELLIGENCE = true
VITE_ENABLE_EVALUATOR    = true
VITE_INTEL_URL           = https://p01--tombstone-intelligence--XXXXXXX.code.run
VITE_EVAL_URL            = https://p01--tombstone-evaluator--XXXXXXX.code.run
```

Then **Redeploy** the latest production deployment.

- [ ] **Step 5: Verify GovernanceDash + SLOView are now live**

Open the dashboard → confirm Governance and SLO items appear in the nav sidebar.

---

## Self-Review

**Spec coverage:**
- ✅ Rollout % slider → Task 1
- ✅ Experiments demo banner → Task 2
- ✅ CommandPalette live flags → Task 3
- ✅ ApprovalQueue 30s refresh → Task 4
- ✅ BreakGlass token refresh → Task 5
- ✅ GovernanceDash error toast → Task 6
- ✅ Deploy evaluator + intelligence → Task 7

**Placeholder scan:** All steps have concrete code. No TBD.

**Type consistency:**
- `flags` in App.tsx: `{ key, name, state }[]` — matches CommandPalette `FlagItem[]` prop
- `handleRolloutChange(newPct: number)` — takes number, sends `rollout_pct: newPct` in body
- `useQuery` in ApprovalQueue returns `ChangeRequest[]` — same type as existing `requests` state
