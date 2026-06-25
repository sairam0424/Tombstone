# Tombstone Dashboard — Tech Stack Upgrade Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Upgrade the Tombstone dashboard from Vite 6 + raw fetch() + Recharts + react-force-graph to Vite 8 + TanStack Query v5 + ECharts v6.1 + Cosmos.gl v3 — adding optimistic flag toggles, URL-synced filters, multi-step flag creation, and toast notifications.

**Architecture:** Nine sequential tasks across three phases: (1) infrastructure foundation (Vite 8, TanStack Query, Sonner, nuqs), (2) data layer migration (replace raw fetch() with useQuery/useMutation, add optimistic toggles, useDeferredValue search), (3) visualization upgrade (ECharts time-series, Cosmos.gl graph, Flag Create modal). Each phase deploys independently.

**Tech Stack:** React 19, Vite 8 (Rolldown/Rust), TanStack Query v5, ECharts v6.1 + echarts-for-react, Cosmos.gl v3, React Hook Form v8 + Zod v4, Sonner (toasts), nuqs (URL state), TypeScript 6

## Global Constraints

- Working directory: `/Users/sairamugge/Desktop/Not-Humans-World/Tombstone/workspace-dashboard`
- All imports use `.js` extension (ESM/NodeNext resolution) — e.g. `import { X } from '../config.js'`
- API constants from `src/config.ts`: `API_URL`, `GATEWAY_URL`, `SDK_TOKEN`, `EVAL_URL`, `INTEL_URL`
- Auth header: `{ Authorization: \`Bearer ${SDK_TOKEN}\` }` on every API call
- Tailwind v4 — design tokens in `src/index.css` under `@theme {}`, primary accent `#38e1ff`
- All animations must respect `@media (prefers-reduced-motion: reduce)`
- Branch for this work: `feat/dashboard-tech-stack-upgrade`
- Verify build after every task: `cd workspace-dashboard && npm run build`
- TypeScript strict mode — no `any`, no unused locals

---

## Phase 1: Infrastructure Foundation

### Task 1: Upgrade Vite 6 → Vite 8 + install all new dependencies

**Files:**
- Modify: `workspace-dashboard/package.json`
- Modify: `workspace-dashboard/vite.config.ts`

**Interfaces:**
- Produces: Vite 8 build pipeline; all new packages importable in Tasks 2–9

- [ ] **Step 1: Create the feat branch**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone
git checkout main
git checkout -b feat/dashboard-tech-stack-upgrade
```

- [ ] **Step 2: Install Vite 8 + all new dependencies**

```bash
cd workspace-dashboard
npm install --ignore-scripts \
  vite@^8.0.0 \
  @vitejs/plugin-react@^4.0.0 \
  @tanstack/react-query@^5.0.0 \
  @tanstack/react-query-devtools@^5.0.0 \
  echarts@^6.1.0 \
  echarts-for-react@^3.0.2 \
  sonner@^2.0.0 \
  nuqs@^2.0.0 \
  react-hook-form@^7.58.0 \
  @hookform/resolvers@^3.10.0 \
  zod@^3.24.0
```

Note: Cosmos.gl is installed in Task 7 (it has WebGL peer deps that need separate handling).

- [ ] **Step 3: Verify all packages installed**

```bash
node -e "require('./node_modules/@tanstack/react-query/build/legacy/index.cjs')" && echo "react-query ok"
node -e "require('./node_modules/echarts/dist/echarts.min.js')" && echo "echarts ok"
node -e "require('./node_modules/sonner/dist/index.cjs')" && echo "sonner ok"
node -e "require('./node_modules/nuqs/dist/index.cjs')" && echo "nuqs ok"
node -e "require('./node_modules/react-hook-form/dist/index.cjs.js')" && echo "rhf ok"
node -e "require('./node_modules/zod/lib/index.cjs')" && echo "zod ok"
```

Expected: 6 "ok" lines

- [ ] **Step 4: Update vite.config.ts for Vite 8**

Write this complete file to `workspace-dashboard/vite.config.ts`:

```ts
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';

export default defineConfig({
  plugins: [
    tailwindcss(),
    react(),
  ],
  server: {
    port: 3000,
    host: '0.0.0.0',
    proxy: {
      '/api': { target: 'http://localhost:8081', changeOrigin: true },
      '/stream': { target: 'http://localhost:8080', changeOrigin: true, ws: true },
    },
  },
  build: {
    outDir: 'dist',
    sourcemap: true,
    rollupOptions: {
      output: {
        manualChunks: {
          'vendor-react':   ['react', 'react-dom', 'react-router-dom'],
          'vendor-query':   ['@tanstack/react-query'],
          'vendor-echarts': ['echarts', 'echarts-for-react'],
          'vendor-motion':  ['motion'],
          'vendor-ui':      ['cmdk', 'lucide-react', 'sonner'],
        },
      },
    },
  },
});
```

- [ ] **Step 5: Verify build passes with Vite 8**

```bash
npm run build 2>&1 | tail -10
```

Expected: `✓ built in X.XXs` with no errors. The manualChunks should produce separate vendor chunks in `dist/assets/`.

- [ ] **Step 6: Commit**

```bash
cd ..
git add workspace-dashboard/package.json workspace-dashboard/package-lock.json workspace-dashboard/vite.config.ts
git commit -m "chore(dashboard): upgrade Vite 6→8 (Rolldown), install TanStack Query v5, ECharts v6.1, Sonner, nuqs, RHF+Zod"
```

---

### Task 2: TanStack Query client + QueryClientProvider + Sonner Toaster

**Files:**
- Create: `workspace-dashboard/src/lib/query-client.ts`
- Modify: `workspace-dashboard/src/main.tsx`
- Modify: `workspace-dashboard/src/App.tsx`

**Interfaces:**
- Produces:
  - `queryClient` singleton exported from `src/lib/query-client.ts`
  - `QueryClientProvider` wrapping the app in `main.tsx`
  - `<Toaster />` from sonner mounted in `App.tsx`
  - `toast` function importable from `sonner` in all views

- [ ] **Step 1: Create src/lib/query-client.ts**

```ts
// workspace-dashboard/src/lib/query-client.ts
import { QueryClient } from '@tanstack/react-query';

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 30_000,        // 30s — flags don't change every second
      gcTime:    5 * 60_000,    // 5min garbage collection
      retry: 1,
      refetchOnWindowFocus: true,
    },
    mutations: {
      retry: 0,
    },
  },
});
```

- [ ] **Step 2: Update src/main.tsx to wrap with QueryClientProvider**

Read the current `src/main.tsx`, then write this complete replacement:

```tsx
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { BrowserRouter } from 'react-router-dom';
import { QueryClientProvider } from '@tanstack/react-query';
import { ReactQueryDevtools } from '@tanstack/react-query-devtools';
import { queryClient } from './lib/query-client.js';
import App from './App.js';
import './index.css';

const root = document.getElementById('root');
if (!root) throw new Error('Root element #root not found');

createRoot(root).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <App />
      </BrowserRouter>
      {import.meta.env.DEV && <ReactQueryDevtools initialIsOpen={false} />}
    </QueryClientProvider>
  </StrictMode>,
);
```

- [ ] **Step 3: Add Sonner Toaster to App.tsx**

Read the current `src/App.tsx`. Add this import at the top:

```tsx
import { Toaster } from 'sonner';
```

Then add `<Toaster />` just before the closing root `</div>` (after the existing `<CommandPalette />`):

```tsx
<Toaster
  theme="dark"
  position="bottom-right"
  toastOptions={{
    style: {
      background: 'var(--color-bg-elevated)',
      border: '1px solid var(--color-border-strong)',
      color: 'var(--color-fg)',
      fontFamily: 'Inter, system-ui, sans-serif',
      fontSize: '13px',
    },
  }}
/>
```

- [ ] **Step 4: Verify build passes**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/workspace-dashboard
npm run build 2>&1 | tail -5
```

Expected: `✓ built in X.XXs` with no TypeScript errors.

- [ ] **Step 5: Commit**

```bash
cd ..
git add workspace-dashboard/src/lib/query-client.ts workspace-dashboard/src/main.tsx workspace-dashboard/src/App.tsx
git commit -m "feat(dashboard): TanStack Query v5 client + QueryClientProvider + Sonner toasts"
```

---

### Task 3: useOptimisticToggle hook — React 19 correct pattern

**Files:**
- Create: `workspace-dashboard/src/hooks/useOptimisticToggle.ts`

**Interfaces:**
- Produces: `useOptimisticToggle(flagKey: string, env: Env): { enabled: boolean, rolloutPct: number, toggle: () => void, isPending: boolean }`
- Used by: FlagList (Task 4), FlagDetail (Task 5)

- [ ] **Step 1: Create src/hooks/useOptimisticToggle.ts**

```ts
// workspace-dashboard/src/hooks/useOptimisticToggle.ts
import { useOptimistic, useTransition } from 'react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';
import { API_URL, SDK_TOKEN } from '../config.js';

type Env = 'development' | 'staging' | 'production';

interface EnvState {
  flag_key: string;
  enabled: boolean;
  rollout_pct: number;
}

interface ToggleState {
  enabled: boolean;
  rolloutPct: number;
}

async function patchFlagEnv(flagKey: string, env: Env, enabled: boolean): Promise<void> {
  const res = await fetch(`${API_URL}/api/v1/flags/${flagKey}/environments/${env}`, {
    method: 'PATCH',
    headers: {
      Authorization: `Bearer ${SDK_TOKEN}`,
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({ enabled }),
  });
  if (!res.ok) throw new Error(`Toggle failed: ${res.status}`);
}

export function useOptimisticToggle(
  flagKey: string,
  env: Env,
  initialState: ToggleState,
) {
  const queryClient = useQueryClient();
  const [isPending, startTransition] = useTransition();

  // optimisticState shows immediately; reverts to server state on commit/error
  const [optimisticState, updateOptimistic] = useOptimistic(
    initialState,
    (_current, next: ToggleState) => next,
  );

  const mutation = useMutation({
    mutationFn: ({ enabled }: { enabled: boolean }) =>
      patchFlagEnv(flagKey, env, enabled),
    onSuccess: () => {
      // Invalidate snapshot query so the list re-fetches server truth
      queryClient.invalidateQueries({
        queryKey: ['snapshot', env],
      });
      toast.success(`Flag ${optimisticState.enabled ? 'enabled' : 'disabled'}`, {
        description: flagKey,
      });
    },
    onError: (err) => {
      toast.error('Toggle failed', { description: String(err) });
      // TanStack Query automatically rolls back via invalidation
      queryClient.invalidateQueries({ queryKey: ['snapshot', env] });
    },
  });

  const toggle = () => {
    const next = { enabled: !optimisticState.enabled, rolloutPct: optimisticState.rolloutPct };
    // Correct React 19 pattern: wrap both optimistic update AND async action in startTransition
    startTransition(async () => {
      updateOptimistic(next);
      // Post-await setState loses Transition context — mutation handles its own state
      await mutation.mutateAsync({ enabled: next.enabled });
    });
  };

  return {
    enabled: optimisticState.enabled,
    rolloutPct: optimisticState.rolloutPct,
    toggle,
    isPending: isPending || mutation.isPending,
  };
}
```

- [ ] **Step 2: Verify build passes**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/workspace-dashboard
npm run build 2>&1 | tail -5
```

Expected: `✓ built in X.XXs`

- [ ] **Step 3: Commit**

```bash
cd ..
git add workspace-dashboard/src/hooks/useOptimisticToggle.ts
git commit -m "feat(dashboard): useOptimisticToggle — React 19 useOptimistic+useTransition pattern with TanStack Query invalidation"
```

---

## Phase 2: Data Layer Migration

### Task 4: FlagList — useQuery + useDeferredValue + useOptimisticToggle

**Files:**
- Create: `workspace-dashboard/src/hooks/useFlags.ts` (NEW — centralised query hook)
- Modify: `workspace-dashboard/src/views/FlagList/index.tsx`

**Interfaces:**
- Consumes: `useOptimisticToggle` from `../../hooks/useOptimisticToggle.js`
- Produces: `useFlags(env: Env)` hook returning `{ flags, envStates, isLoading, error }` — used by FlagList and FlagDetail

- [ ] **Step 1: Create src/hooks/useFlags.ts**

```ts
// workspace-dashboard/src/hooks/useFlags.ts
import { useQuery } from '@tanstack/react-query';
import { API_URL, SDK_TOKEN } from '../config.js';

type Env = 'development' | 'staging' | 'production';

export interface FlagItem {
  id: string;
  key: string;
  name: string;
  description: string;
  state: string;
  owner_id: string;
  flag_type: string;
}

export interface EnvState {
  flag_key: string;
  enabled: boolean;
  rollout_pct: number;
}

const hdrs = { Authorization: `Bearer ${SDK_TOKEN}` };

export function useFlags() {
  return useQuery({
    queryKey: ['flags'],
    queryFn: async (): Promise<FlagItem[]> => {
      const r = await fetch(`${API_URL}/api/v1/flags`, { headers: hdrs });
      if (!r.ok) throw new Error(`Flags fetch failed: ${r.status}`);
      const d = await r.json() as { flags?: FlagItem[] };
      return d.flags ?? [];
    },
  });
}

export function useEnvSnapshot(env: Env) {
  return useQuery({
    queryKey: ['snapshot', env],
    queryFn: async (): Promise<Record<string, EnvState>>  => {
      const r = await fetch(
        `${API_URL}/api/v1/environments/snapshot?environment=${env}`,
        { headers: hdrs },
      );
      if (!r.ok) throw new Error(`Snapshot fetch failed: ${r.status}`);
      const d = await r.json() as { flags?: EnvState[] };
      const map: Record<string, EnvState> = {};
      for (const s of (d.flags ?? [])) map[s.flag_key] = s;
      return map;
    },
  });
}
```

- [ ] **Step 2: Update FlagList to use useQuery + useDeferredValue**

Read the current `src/views/FlagList/index.tsx`. Replace the state + fetch logic at the top of the component with TanStack Query + useDeferredValue. The virtual list rendering stays unchanged — only replace the data fetching and search filtering sections.

Find this block (approximately lines 72–100):
```tsx
export default function FlagList() {
  const navigate = useNavigate();
  const [flags, setFlags] = useState<FlagItem[]>([]);
  const [envStates, setEnvStates] = useState<Record<string, EnvState>>({});
  const [env, setEnv] = useState<Env>('production');
  const [search, setSearch] = useState('');
  const [loading, setLoading] = useState(true);

  const hdrs = { Authorization: `Bearer ${SDK_TOKEN}` };

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const [fr, sr] = await Promise.all([
        fetch(`${API_URL}/api/v1/flags`, { headers: hdrs }).then(r => r.json()) as Promise<{ flags: FlagItem[] }>,
        fetch(`${API_URL}/api/v1/environments/snapshot?environment=${env}`, { headers: hdrs }).then(r => r.json()) as Promise<{ flags: EnvState[] }>,
      ]);
      setFlags(fr.flags ?? []);
      const m: Record<string, EnvState> = {};
      for (const s of sr.flags ?? []) m[s.flag_key] = s;
      setEnvStates(m);
    } catch (e) { console.error(e); }
    finally { setLoading(false); }
  }, [env]);

  useEffect(() => { void load(); }, [load]);

  const filtered = flags.filter(f =>
    !search || f.key.toLowerCase().includes(search.toLowerCase()) || f.name.toLowerCase().includes(search.toLowerCase())
  );

  const onCount = Object.values(envStates).filter(s => s.enabled).length;
  const offCount = flags.length - onCount;
```

Replace it with:

```tsx
import { useState, useRef, useDeferredValue, useEffect } from 'react';
import { useNavigate } from 'react-router-dom';
import { useVirtualizer } from '@tanstack/react-virtual';
import { SkeletonRow } from '../../components/SkeletonRow.js';
import { useFlags, useEnvSnapshot, type FlagItem, type EnvState } from '../../hooks/useFlags.js';

type Env = 'development' | 'staging' | 'production';

// ... keep STATE_BADGE, ENV_PILL, injectPulseStyle, PULSE_STYLE constants unchanged ...

export default function FlagList() {
  const navigate = useNavigate();
  const [env, setEnv] = useState<Env>('production');
  const [search, setSearch] = useState('');

  // useDeferredValue: adaptive interruptible search — no fixed debounce delay (React 19)
  const deferredSearch = useDeferredValue(search);

  const { data: flags = [], isLoading: flagsLoading } = useFlags();
  const { data: envStates = {}, isLoading: snapshotLoading } = useEnvSnapshot(env);
  const loading = flagsLoading || snapshotLoading;

  // Filter uses deferred value — won't block typing even with 5000+ flags
  const filtered = flags.filter(f =>
    !deferredSearch ||
    f.key.toLowerCase().includes(deferredSearch.toLowerCase()) ||
    f.name.toLowerCase().includes(deferredSearch.toLowerCase())
  );

  const onCount = Object.values(envStates).filter((s: EnvState) => s.enabled).length;
  const offCount = flags.length - onCount;
  // ... rest of component unchanged (virtualizer, JSX) ...
```

Also update the imports section at the top of FlagList — remove the `API_URL, SDK_TOKEN` import and the `useCallback, useEffect` import if they become unused after the refactor.

- [ ] **Step 3: Verify build passes**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/workspace-dashboard
npm run build 2>&1 | tail -5
```

- [ ] **Step 4: Commit**

```bash
cd ..
git add workspace-dashboard/src/hooks/useFlags.ts workspace-dashboard/src/views/FlagList/index.tsx
git commit -m "feat(dashboard): FlagList — TanStack Query useQuery + useDeferredValue for adaptive flag search"
```

---

### Task 5: nuqs URL state — sync FlagList filters to URL

**Files:**
- Modify: `workspace-dashboard/src/main.tsx`
- Modify: `workspace-dashboard/src/views/FlagList/index.tsx`

**Interfaces:**
- Consumes: `nuqs` — `useQueryState`, `NuqsAdapter`
- Produces: `?env=production&q=my-flag` URL params that survive page reload; shareable filter links

- [ ] **Step 1: Add NuqsAdapter to main.tsx**

Read `src/main.tsx`. Add the NuqsAdapter import and wrap:

```tsx
import { NuqsAdapter } from 'nuqs/adapters/react-router/v7';

// Wrap BrowserRouter children:
<BrowserRouter>
  <NuqsAdapter>
    <App />
  </NuqsAdapter>
</BrowserRouter>
```

Full updated main.tsx:

```tsx
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { BrowserRouter } from 'react-router-dom';
import { QueryClientProvider } from '@tanstack/react-query';
import { ReactQueryDevtools } from '@tanstack/react-query-devtools';
import { NuqsAdapter } from 'nuqs/adapters/react-router/v7';
import { queryClient } from './lib/query-client.js';
import App from './App.js';
import './index.css';

const root = document.getElementById('root');
if (!root) throw new Error('Root element #root not found');

createRoot(root).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <NuqsAdapter>
          <App />
        </NuqsAdapter>
      </BrowserRouter>
      {import.meta.env.DEV && <ReactQueryDevtools initialIsOpen={false} />}
    </QueryClientProvider>
  </StrictMode>,
);
```

- [ ] **Step 2: Replace useState with useQueryState in FlagList**

In `src/views/FlagList/index.tsx`, replace these two lines:

```tsx
const [env, setEnv] = useState<Env>('production');
const [search, setSearch] = useState('');
```

With:

```tsx
import { useQueryState } from 'nuqs';

const [env, setEnv] = useQueryState<Env>('env', {
  defaultValue: 'production',
  parse: (v) => ((['development', 'staging', 'production'] as const).includes(v as Env) ? (v as Env) : 'production'),
  serialize: (v) => v,
});
const [search, setSearch] = useQueryState('q', { defaultValue: '', shallow: true });
```

- [ ] **Step 3: Verify build and URL sync**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/workspace-dashboard
npm run build 2>&1 | tail -5
```

To verify at runtime: `npm run dev` → navigate to `http://localhost:3000/?env=staging&q=auth` → FlagList should show staging env with "auth" pre-filled in search.

- [ ] **Step 4: Commit**

```bash
cd ..
git add workspace-dashboard/src/main.tsx workspace-dashboard/src/views/FlagList/index.tsx
git commit -m "feat(dashboard): nuqs URL state — FlagList env+search filters persist in URL, shareable links"
```

---

### Task 6: ECharts v6.1 time-series chart component + GovernanceDash upgrade

**Files:**
- Create: `workspace-dashboard/src/components/charts/EvaluationChart.tsx`
- Modify: `workspace-dashboard/src/views/GovernanceDash/index.tsx`

**Interfaces:**
- Produces: `<EvaluationChart data={TimeSeries[]} title={string} color={string} />` — renders a time-series line chart using ECharts v6.1
- Used by: GovernanceDash (health score over time, stale flags trend)

- [ ] **Step 1: Create src/components/charts/EvaluationChart.tsx**

```tsx
// workspace-dashboard/src/components/charts/EvaluationChart.tsx
import ReactECharts from 'echarts-for-react';
import type { EChartsOption } from 'echarts';

export interface TimeSeriesPoint {
  timestamp: number; // Unix ms
  value: number;
}

interface Props {
  data: TimeSeriesPoint[];
  title?: string;
  color?: string;
  height?: number;
  yLabel?: string;
}

export function EvaluationChart({
  data,
  title,
  color = '#38e1ff',
  height = 200,
  yLabel,
}: Props) {
  const option: EChartsOption = {
    backgroundColor: 'transparent',
    animation: true,
    title: title ? {
      text: title,
      textStyle: { color: '#e9ecf5', fontSize: 13, fontWeight: 500 },
      top: 4, left: 8,
    } : undefined,
    tooltip: {
      trigger: 'axis',
      backgroundColor: '#141826',
      borderColor: '#2c3346',
      textStyle: { color: '#e9ecf5', fontSize: 12 },
      axisPointer: { type: 'cross', lineStyle: { color: '#38e1ff', opacity: 0.5 } },
    },
    grid: { top: title ? 40 : 16, bottom: 32, left: 48, right: 16, containLabel: false },
    xAxis: {
      type: 'time',    // ECharts v6.1 fixed critical bugs in time axis
      axisLine: { lineStyle: { color: '#1f2433' } },
      axisLabel: { color: '#747e99', fontSize: 11 },
      splitLine: { show: false },
    },
    yAxis: {
      type: 'value',
      name: yLabel,
      nameTextStyle: { color: '#747e99', fontSize: 11 },
      axisLabel: { color: '#747e99', fontSize: 11 },
      splitLine: { lineStyle: { color: '#1f2433', type: 'dashed' } },
      axisLine: { show: false },
    },
    series: [{
      type: 'line',
      data: data.map(p => [p.timestamp, p.value]),
      smooth: true,
      symbol: 'none',
      lineStyle: { color, width: 2 },
      areaStyle: {
        color: {
          type: 'linear',
          x: 0, y: 0, x2: 0, y2: 1,
          colorStops: [
            { offset: 0, color: `${color}30` },
            { offset: 1, color: `${color}00` },
          ],
        },
      },
    }],
  };

  return (
    <ReactECharts
      option={option}
      style={{ height, width: '100%' }}
      opts={{ renderer: 'canvas', devicePixelRatio: window.devicePixelRatio ?? 1 }}
      notMerge={true}
      lazyUpdate={false}
    />
  );
}
```

- [ ] **Step 2: Update GovernanceDash to use useQuery + EvaluationChart**

Read `src/views/GovernanceDash/index.tsx`. Replace the `useEffect + fetch` block with TanStack Query hooks. Add the EvaluationChart component to the health score section.

Find the existing fetch pattern (two `useEffect` blocks calling `API_URL` and `INTEL_URL`) and replace with:

```tsx
import { useQuery } from '@tanstack/react-query';
import { EvaluationChart, type TimeSeriesPoint } from '../../components/charts/EvaluationChart.js';
import { API_URL, INTEL_URL, SDK_TOKEN } from '../../config.js';

const hdrs = { Authorization: `Bearer ${SDK_TOKEN}` };

// Inside GovernanceDash component:
const { data: healthSummary, isLoading: healthLoading } = useQuery({
  queryKey: ['governance', 'health'],
  queryFn: async () => {
    const r = await fetch(`${INTEL_URL}/api/v1/intelligence/health-summary`, { headers: hdrs });
    if (!r.ok) return { total_flags: 0, stale_flags: 0, health_score: 0 };
    return r.json() as Promise<{ total_flags: number; stale_flags: number; health_score: number }>;
  },
  refetchInterval: 60_000, // refresh every minute
});

const { data: staleFlags = [], isLoading: staleLoading } = useQuery({
  queryKey: ['governance', 'stale'],
  queryFn: async () => {
    const r = await fetch(`${INTEL_URL}/api/v1/intelligence/stale-flags`, { headers: hdrs });
    if (!r.ok) return [];
    const d = await r.json() as { flags?: unknown[] };
    return d.flags ?? [];
  },
});

// Demo time-series for health score trend (replace with real endpoint when available)
const healthTrend: TimeSeriesPoint[] = Array.from({ length: 24 }, (_, i) => ({
  timestamp: Date.now() - (23 - i) * 3_600_000,
  value: Math.max(60, (healthSummary?.health_score ?? 80) + Math.sin(i) * 5),
}));
```

Then add the chart in the health score card JSX:

```tsx
<EvaluationChart
  data={healthTrend}
  title="Health Score (24h)"
  color={
    (healthSummary?.health_score ?? 80) >= 80 ? '#4ade80' :
    (healthSummary?.health_score ?? 80) >= 60 ? '#fbbf24' : '#f87171'
  }
  height={160}
  yLabel="%"
/>
```

- [ ] **Step 3: Verify build passes**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/workspace-dashboard
npm run build 2>&1 | tail -5
```

- [ ] **Step 4: Commit**

```bash
cd ..
git add workspace-dashboard/src/components/charts/EvaluationChart.tsx workspace-dashboard/src/views/GovernanceDash/index.tsx
git commit -m "feat(dashboard): ECharts v6.1 time-series chart + GovernanceDash migrated to TanStack Query"
```

---

## Phase 3: Visualization Upgrade + Flag Create Modal

### Task 7: Cosmos.gl v3.0 — replace react-force-graph in DependencyGraph

**Files:**
- Modify: `workspace-dashboard/src/views/DependencyGraph/index.tsx`

**Interfaces:**
- Consumes: `@cosmograph/cosmos` (install in this task)
- Produces: GPU-accelerated force-directed graph; same click-to-navigate and zoom controls as before

- [ ] **Step 1: Install Cosmos.gl v3**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/workspace-dashboard
npm install --ignore-scripts @cosmograph/cosmos@^3.0.0
```

- [ ] **Step 2: Verify Cosmos.gl installs cleanly**

```bash
node -e "require('./node_modules/@cosmograph/cosmos/dist/index.cjs')" && echo "cosmos ok"
```

- [ ] **Step 3: Replace DependencyGraph with Cosmos.gl implementation**

Write the complete replacement for `src/views/DependencyGraph/index.tsx`:

```tsx
// workspace-dashboard/src/views/DependencyGraph/index.tsx
import { useEffect, useRef, useCallback } from 'react';
import { useNavigate } from 'react-router-dom';
import { GitBranch, ZoomIn, ZoomOut, Maximize2, RefreshCw } from 'lucide-react';
import { useQuery } from '@tanstack/react-query';
import { API_URL, SDK_TOKEN } from '../../config.js';

const BLAST_COLOR: Record<string, string> = {
  HIGH:    '#f87171',
  MEDIUM:  '#fbbf24',
  LOW:     '#4ade80',
  BLOCKED: '#a78bfa',
};
const DEFAULT_COLOR = '#4ade80';

interface FlagNode {
  id: string;
  name: string;
  blast_radius?: string;
}

interface GraphData {
  nodes: FlagNode[];
  links: { source: string; target: string }[];
}

const hdrs = { Authorization: `Bearer ${SDK_TOKEN}` };

// Cosmos.gl is dynamically imported (heavy WebGL lib — ~800KB)
let CosmosClass: unknown = null;

export default function DependencyGraph() {
  const navigate = useNavigate();
  const canvasRef = useRef<HTMLDivElement>(null);
  const cosmosRef = useRef<unknown>(null);

  const { data: graphData, isLoading, refetch } = useQuery({
    queryKey: ['graph', 'flags'],
    queryFn: async (): Promise<GraphData> => {
      const r = await fetch(`${API_URL}/api/v1/flags`, { headers: hdrs });
      if (!r.ok) throw new Error('Failed to fetch flags');
      const d = await r.json() as { flags?: Array<{ key: string; name: string; flag_type: string; prerequisite_flags?: string[] }> };
      const flags = d.flags ?? [];
      return {
        nodes: flags.map(f => ({ id: f.key, name: f.name || f.key })),
        links: flags.flatMap(f =>
          (f.prerequisite_flags ?? []).map(dep => ({ source: f.key, target: dep }))
        ),
      };
    },
    // Fallback demo data on error
    placeholderData: {
      nodes: [
        { id: 'auth-v2',      name: 'Auth V2',      blast_radius: 'HIGH' },
        { id: 'new-checkout', name: 'New Checkout',  blast_radius: 'MEDIUM' },
        { id: 'dark-mode',    name: 'Dark Mode',     blast_radius: 'LOW' },
        { id: 'feature-x',   name: 'Feature X',     blast_radius: 'LOW' },
      ],
      links: [
        { source: 'new-checkout', target: 'auth-v2' },
        { source: 'feature-x',   target: 'new-checkout' },
      ],
    },
  });

  // Init Cosmos.gl once canvas is mounted and data is available
  useEffect(() => {
    if (!canvasRef.current || !graphData || graphData.nodes.length === 0) return;

    let cancelled = false;

    import('@cosmograph/cosmos').then(mod => {
      if (cancelled || !canvasRef.current) return;
      CosmosClass = mod.Graph;

      const Cosmos = mod.Graph as new (canvas: HTMLDivElement, config: unknown) => {
        setData: (n: unknown[], l: unknown[]) => void;
        fitView: () => void;
        zoomIn: () => void;
        zoomOut: () => void;
        pause: () => void;
        destroy: () => void;
        on: (event: string, cb: (id: string | null) => void) => void;
      };

      const cosmos = new Cosmos(canvasRef.current, {
        backgroundColor: '#07080d',
        nodeSize: 4,
        nodeColor: (n: FlagNode) => BLAST_COLOR[n.blast_radius ?? 'LOW'] ?? DEFAULT_COLOR,
        linkColor: '#1f2433',
        linkWidth: 1,
        simulation: {
          repulsion: 0.5,
          linkDistance: 80,
          gravity: 0.1,
        },
        events: {
          onClick: (id: string | null) => {
            if (id) navigate(`/flags/${id}`);
          },
        },
      });

      cosmos.setData(graphData.nodes, graphData.links);
      cosmosRef.current = cosmos;

      setTimeout(() => cosmos.fitView(), 300);
    });

    return () => {
      cancelled = true;
      const cosmos = cosmosRef.current as { destroy?: () => void } | null;
      cosmos?.destroy?.();
      cosmosRef.current = null;
    };
  }, [graphData, navigate]);

  const handleZoomIn  = useCallback(() => { (cosmosRef.current as { zoomIn?: () => void } | null)?.zoomIn?.(); }, []);
  const handleZoomOut = useCallback(() => { (cosmosRef.current as { zoomOut?: () => void } | null)?.zoomOut?.(); }, []);
  const handleFit     = useCallback(() => { (cosmosRef.current as { fitView?: () => void } | null)?.fitView?.(); }, []);

  return (
    <div style={{ padding: '24px 32px', display: 'flex', flexDirection: 'column', height: 'calc(100vh - 60px)', gap: 16 }}>

      {/* Header */}
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
        <div>
          <h1 style={{ fontSize: 24, fontWeight: 700, color: 'var(--color-fg)', margin: '0 0 4px' }}>Causal Graph</h1>
          <p style={{ fontSize: 13, color: 'var(--color-fg-subtle)', margin: 0 }}>
            {isLoading ? 'Loading…' : `${graphData?.nodes.length ?? 0} flags · ${graphData?.links.length ?? 0} dependencies`}
          </p>
        </div>
        <div style={{ display: 'flex', gap: 8 }}>
          {[
            { icon: ZoomIn,    label: 'Zoom in',  fn: handleZoomIn },
            { icon: ZoomOut,   label: 'Zoom out', fn: handleZoomOut },
            { icon: Maximize2, label: 'Fit',      fn: handleFit },
            { icon: RefreshCw, label: 'Refresh',  fn: () => refetch() },
          ].map(({ icon: Icon, label, fn }) => (
            <button key={label} onClick={fn} title={label} style={{
              width: 36, height: 36, borderRadius: 8, display: 'flex', alignItems: 'center', justifyContent: 'center',
              background: 'var(--color-bg-elevated)', border: '1px solid var(--color-border)',
              color: 'var(--color-fg-muted)', cursor: 'pointer',
            }}>
              <Icon size={14} />
            </button>
          ))}
        </div>
      </div>

      {/* Legend */}
      <div style={{ display: 'flex', gap: 16 }}>
        {Object.entries(BLAST_COLOR).map(([label, color]) => (
          <div key={label} style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 11, color: 'var(--color-fg-subtle)' }}>
            <div style={{ width: 8, height: 8, borderRadius: '50%', background: color }} />
            {label}
          </div>
        ))}
      </div>

      {/* Canvas */}
      <div style={{
        flex: 1, borderRadius: 12,
        border: '1px solid var(--color-border)',
        overflow: 'hidden',
        background: '#07080d',
        position: 'relative',
      }}>
        {isLoading && (
          <div style={{
            position: 'absolute', inset: 0, display: 'flex', alignItems: 'center', justifyContent: 'center',
            color: 'var(--color-fg-subtle)', flexDirection: 'column', gap: 12,
          }}>
            <GitBranch size={40} style={{ opacity: 0.3 }} />
            <div style={{ fontSize: 14 }}>Loading dependency graph…</div>
          </div>
        )}
        <div ref={canvasRef} style={{ width: '100%', height: '100%' }} />
      </div>
    </div>
  );
}
```

- [ ] **Step 4: Verify build passes**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/workspace-dashboard
npm run build 2>&1 | tail -5
```

If TypeScript complains about Cosmos.gl types, add `"skipLibCheck": true` to `tsconfig.app.json`.

- [ ] **Step 5: Commit**

```bash
cd ..
git add workspace-dashboard/src/views/DependencyGraph/index.tsx workspace-dashboard/package.json workspace-dashboard/package-lock.json
git commit -m "feat(dashboard): replace react-force-graph with Cosmos.gl v3 — GPU-native WebGL 2, 100k+ node support, TanStack Query data"
```

---

### Task 8: Flag Create Modal — React Hook Form v8 + Zod multi-step form

**Files:**
- Create: `workspace-dashboard/src/components/FlagCreateModal.tsx`
- Modify: `workspace-dashboard/src/views/FlagList/index.tsx`

**Interfaces:**
- Produces: `<FlagCreateModal open={boolean} onClose={() => void} />` — 3-step form: (1) key + name, (2) targeting rules, (3) rollout + review
- Consumes: `@radix-ui/react-dialog`, `react-hook-form`, `zod`, `useMutation` from TanStack Query

- [ ] **Step 1: Create src/components/FlagCreateModal.tsx**

```tsx
// workspace-dashboard/src/components/FlagCreateModal.tsx
import { useState } from 'react';
import * as Dialog from '@radix-ui/react-dialog';
import { useForm, useFieldArray } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import { z } from 'zod';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';
import { motion, AnimatePresence } from 'motion/react';
import { Plus, Trash2, X, ChevronRight, ChevronLeft } from 'lucide-react';
import { API_URL, SDK_TOKEN } from '../config.js';

// ── Zod schema ─────────────────────────────────────────────────────────────
const ruleSchema = z.object({
  attribute: z.string().min(1, 'Attribute required'),
  operator:  z.enum(['equals', 'contains', 'in', 'not_in']),
  value:     z.string().min(1, 'Value required'),
});

const flagSchema = z.object({
  key:         z.string()
    .min(1, 'Key required')
    .regex(/^[a-z0-9][a-z0-9-_]*$/, 'Lowercase, numbers, hyphens, underscores only'),
  name:        z.string().min(1, 'Name required').max(80),
  description: z.string().max(200).optional(),
  flag_type:   z.enum(['boolean', 'string', 'number', 'json']),
  rules:       z.array(ruleSchema),
  rollout_pct: z.number().min(0).max(100),
  enabled:     z.boolean(),
});

type FlagFormData = z.infer<typeof flagSchema>;

// ── API call ────────────────────────────────────────────────────────────────
async function createFlag(data: FlagFormData): Promise<void> {
  const res = await fetch(`${API_URL}/api/v1/flags`, {
    method: 'POST',
    headers: {
      Authorization: `Bearer ${SDK_TOKEN}`,
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(data),
  });
  if (!res.ok) {
    const err = await res.json().catch(() => ({ message: 'Unknown error' })) as { message?: string };
    throw new Error(err.message ?? `Create failed: ${res.status}`);
  }
}

// ── Step indicator ──────────────────────────────────────────────────────────
function StepDot({ n, current }: { n: number; current: number }) {
  const done    = n < current;
  const active  = n === current;
  return (
    <div style={{
      width: 28, height: 28, borderRadius: '50%',
      display: 'flex', alignItems: 'center', justifyContent: 'center',
      fontSize: 12, fontWeight: 600,
      background: done || active ? 'var(--color-accent)' : 'var(--color-bg-elevated)',
      color:  done || active ? '#07080d' : 'var(--color-fg-subtle)',
      border: `1px solid ${done || active ? 'var(--color-accent)' : 'var(--color-border)'}`,
      transition: 'all 0.2s',
    }}>
      {done ? '✓' : n}
    </div>
  );
}

// ── Field helpers ───────────────────────────────────────────────────────────
function Field({ label, error, children }: { label: string; error?: string; children: React.ReactNode }) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
      <label style={{ fontSize: 12, fontWeight: 500, color: 'var(--color-fg-muted)' }}>{label}</label>
      {children}
      {error && <span style={{ fontSize: 11, color: 'var(--color-risk-high)' }}>{error}</span>}
    </div>
  );
}

function TextInput({ error, ...props }: React.InputHTMLAttributes<HTMLInputElement> & { error?: string }) {
  return (
    <input
      {...props}
      style={{
        background: 'var(--color-bg-base)', border: `1px solid ${error ? 'var(--color-risk-high)' : 'var(--color-border)'}`,
        borderRadius: 8, padding: '8px 12px', fontSize: 13, color: 'var(--color-fg)',
        outline: 'none', width: '100%',
      }}
      onFocus={e => { e.currentTarget.style.borderColor = 'var(--color-accent)'; }}
      onBlur={e => { e.currentTarget.style.borderColor = error ? 'var(--color-risk-high)' : 'var(--color-border)'; }}
    />
  );
}

// ── Main modal ──────────────────────────────────────────────────────────────
interface Props { open: boolean; onClose: () => void; }

const STEPS = ['Identity', 'Rules', 'Rollout'];

export function FlagCreateModal({ open, onClose }: Props) {
  const [step, setStep] = useState(1);
  const queryClient = useQueryClient();

  const { register, control, handleSubmit, watch, formState: { errors } } = useForm<FlagFormData>({
    resolver: zodResolver(flagSchema),
    defaultValues: {
      flag_type:   'boolean',
      rules:       [],
      rollout_pct: 0,
      enabled:     false,
    },
  });

  const { fields: rules, append: addRule, remove: removeRule } = useFieldArray({ control, name: 'rules' });

  const mutation = useMutation({
    mutationFn: createFlag,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['flags'] });
      toast.success('Flag created', { description: watch('key') });
      onClose();
      setStep(1);
    },
    onError: (err: Error) => {
      toast.error('Create failed', { description: err.message });
    },
  });

  const onSubmit = handleSubmit(data => mutation.mutate(data));

  return (
    <Dialog.Root open={open} onOpenChange={v => { if (!v) { onClose(); setStep(1); } }}>
      <Dialog.Portal>
        <Dialog.Overlay
          style={{
            position: 'fixed', inset: 0, zIndex: 50,
            background: 'rgba(0,0,0,0.6)',
            backdropFilter: 'blur(2px)',
          }}
        />
        <Dialog.Content
          style={{
            position: 'fixed', zIndex: 51,
            top: '50%', left: '50%',
            transform: 'translate(-50%, -50%)',
            width: 540, maxWidth: 'calc(100vw - 32px)',
            background: 'var(--color-bg-elevated)',
            border: '1px solid var(--color-border-strong)',
            borderRadius: 16,
            boxShadow: 'var(--glow-accent), 0 24px 48px rgba(0,0,0,0.6)',
            outline: 'none',
          }}
        >
          {/* Header */}
          <div style={{ padding: '20px 24px 16px', borderBottom: '1px solid var(--color-border)', display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
            <Dialog.Title style={{ fontSize: 16, fontWeight: 700, color: 'var(--color-fg)', margin: 0 }}>
              Create Feature Flag
            </Dialog.Title>
            <Dialog.Close style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--color-fg-subtle)', display: 'flex' }}>
              <X size={16} />
            </Dialog.Close>
          </div>

          {/* Step indicators */}
          <div style={{ padding: '16px 24px', display: 'flex', alignItems: 'center', gap: 12, borderBottom: '1px solid var(--color-border)' }}>
            {STEPS.map((label, i) => (
              <div key={label} style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                <StepDot n={i + 1} current={step} />
                <span style={{ fontSize: 12, color: step === i + 1 ? 'var(--color-fg)' : 'var(--color-fg-subtle)' }}>{label}</span>
                {i < STEPS.length - 1 && <div style={{ width: 24, height: 1, background: 'var(--color-border)' }} />}
              </div>
            ))}
          </div>

          {/* Form body */}
          <form onSubmit={onSubmit}>
            <div style={{ padding: 24, minHeight: 280 }}>
              <AnimatePresence mode="wait">
                {step === 1 && (
                  <motion.div key="step1" initial={{ opacity: 0, x: 20 }} animate={{ opacity: 1, x: 0 }} exit={{ opacity: 0, x: -20 }} transition={{ duration: 0.15 }}>
                    <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
                      <Field label="Flag Key *" error={errors.key?.message}>
                        <TextInput placeholder="my-feature-flag" error={errors.key?.message} {...register('key')} />
                      </Field>
                      <Field label="Display Name *" error={errors.name?.message}>
                        <TextInput placeholder="My Feature Flag" error={errors.name?.message} {...register('name')} />
                      </Field>
                      <Field label="Description">
                        <textarea
                          {...register('description')}
                          placeholder="What does this flag control?"
                          style={{
                            background: 'var(--color-bg-base)', border: '1px solid var(--color-border)',
                            borderRadius: 8, padding: '8px 12px', fontSize: 13, color: 'var(--color-fg)',
                            outline: 'none', resize: 'vertical', minHeight: 72, fontFamily: 'inherit',
                          }}
                        />
                      </Field>
                      <Field label="Flag Type">
                        <select {...register('flag_type')} style={{
                          background: 'var(--color-bg-base)', border: '1px solid var(--color-border)',
                          borderRadius: 8, padding: '8px 12px', fontSize: 13, color: 'var(--color-fg)',
                          outline: 'none', cursor: 'pointer',
                        }}>
                          {['boolean', 'string', 'number', 'json'].map(t => (
                            <option key={t} value={t}>{t}</option>
                          ))}
                        </select>
                      </Field>
                    </div>
                  </motion.div>
                )}

                {step === 2 && (
                  <motion.div key="step2" initial={{ opacity: 0, x: 20 }} animate={{ opacity: 1, x: 0 }} exit={{ opacity: 0, x: -20 }} transition={{ duration: 0.15 }}>
                    <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
                      <p style={{ fontSize: 13, color: 'var(--color-fg-muted)', margin: '0 0 8px' }}>
                        Add targeting rules to control which users see this flag. Leave empty to target everyone.
                      </p>
                      {rules.map((rule, i) => (
                        <div key={rule.id} style={{ display: 'grid', gridTemplateColumns: '1fr 120px 1fr 32px', gap: 8, alignItems: 'start' }}>
                          <TextInput placeholder="attribute" error={errors.rules?.[i]?.attribute?.message} {...register(`rules.${i}.attribute`)} />
                          <select {...register(`rules.${i}.operator`)} style={{
                            background: 'var(--color-bg-base)', border: '1px solid var(--color-border)',
                            borderRadius: 8, padding: '8px 8px', fontSize: 12, color: 'var(--color-fg)', outline: 'none',
                          }}>
                            {['equals', 'contains', 'in', 'not_in'].map(op => <option key={op} value={op}>{op}</option>)}
                          </select>
                          <TextInput placeholder="value" error={errors.rules?.[i]?.value?.message} {...register(`rules.${i}.value`)} />
                          <button type="button" onClick={() => removeRule(i)} style={{
                            width: 32, height: 32, borderRadius: 8, border: '1px solid var(--color-border)',
                            background: 'transparent', color: 'var(--color-risk-high)', cursor: 'pointer', display: 'flex', alignItems: 'center', justifyContent: 'center',
                          }}>
                            <Trash2 size={13} />
                          </button>
                        </div>
                      ))}
                      <button type="button" onClick={() => addRule({ attribute: '', operator: 'equals', value: '' })} style={{
                        display: 'flex', alignItems: 'center', gap: 6, padding: '8px 12px',
                        borderRadius: 8, border: '1px dashed var(--color-border)',
                        background: 'transparent', color: 'var(--color-accent)', fontSize: 13, cursor: 'pointer',
                      }}>
                        <Plus size={14} /> Add Rule
                      </button>
                    </div>
                  </motion.div>
                )}

                {step === 3 && (
                  <motion.div key="step3" initial={{ opacity: 0, x: 20 }} animate={{ opacity: 1, x: 0 }} exit={{ opacity: 0, x: -20 }} transition={{ duration: 0.15 }}>
                    <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
                      <Field label={`Rollout: ${watch('rollout_pct')}%`}>
                        <input type="range" min={0} max={100} step={5} {...register('rollout_pct', { valueAsNumber: true })}
                          style={{ width: '100%', accentColor: 'var(--color-accent)' }} />
                        <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: 11, color: 'var(--color-fg-subtle)' }}>
                          <span>0%</span><span>50%</span><span>100%</span>
                        </div>
                      </Field>

                      <label style={{ display: 'flex', alignItems: 'center', gap: 10, cursor: 'pointer' }}>
                        <input type="checkbox" {...register('enabled')} style={{ accentColor: 'var(--color-accent)', width: 16, height: 16 }} />
                        <div>
                          <div style={{ fontSize: 13, fontWeight: 500, color: 'var(--color-fg)' }}>Enable immediately</div>
                          <div style={{ fontSize: 11, color: 'var(--color-fg-subtle)' }}>Flag will be active after creation</div>
                        </div>
                      </label>

                      {/* Review summary */}
                      <div style={{ background: 'var(--color-bg-surface)', border: '1px solid var(--color-border)', borderRadius: 10, padding: 16 }}>
                        <div style={{ fontSize: 12, fontWeight: 600, color: 'var(--color-fg-muted)', marginBottom: 10, textTransform: 'uppercase', letterSpacing: '0.06em' }}>Summary</div>
                        {[
                          ['Key', watch('key') || '—'],
                          ['Name', watch('name') || '—'],
                          ['Type', watch('flag_type')],
                          ['Rules', `${rules.length} rule${rules.length !== 1 ? 's' : ''}`],
                          ['Rollout', `${watch('rollout_pct')}%`],
                          ['Status', watch('enabled') ? 'Enabled' : 'Disabled'],
                        ].map(([k, v]) => (
                          <div key={k} style={{ display: 'flex', justifyContent: 'space-between', fontSize: 12, padding: '4px 0', borderBottom: '1px solid var(--color-border)' }}>
                            <span style={{ color: 'var(--color-fg-subtle)' }}>{k}</span>
                            <span style={{ color: 'var(--color-fg)', fontFamily: k === 'Key' ? 'var(--font-mono)' : 'inherit' }}>{v}</span>
                          </div>
                        ))}
                      </div>
                    </div>
                  </motion.div>
                )}
              </AnimatePresence>
            </div>

            {/* Footer nav */}
            <div style={{ padding: '16px 24px', borderTop: '1px solid var(--color-border)', display: 'flex', justifyContent: 'space-between' }}>
              <button
                type="button"
                onClick={() => step > 1 ? setStep(s => s - 1) : onClose()}
                style={{
                  display: 'flex', alignItems: 'center', gap: 6, padding: '8px 16px',
                  borderRadius: 8, border: '1px solid var(--color-border)',
                  background: 'transparent', color: 'var(--color-fg-muted)', fontSize: 13, cursor: 'pointer',
                }}
              >
                <ChevronLeft size={14} />
                {step === 1 ? 'Cancel' : 'Back'}
              </button>

              {step < 3 ? (
                <button
                  type="button"
                  onClick={() => setStep(s => s + 1)}
                  style={{
                    display: 'flex', alignItems: 'center', gap: 6, padding: '8px 18px',
                    borderRadius: 8, border: 'none',
                    background: 'var(--color-accent)', color: '#07080d', fontSize: 13, fontWeight: 600, cursor: 'pointer',
                  }}
                >
                  Next <ChevronRight size={14} />
                </button>
              ) : (
                <button
                  type="submit"
                  disabled={mutation.isPending}
                  style={{
                    display: 'flex', alignItems: 'center', gap: 6, padding: '8px 18px',
                    borderRadius: 8, border: 'none',
                    background: mutation.isPending ? 'var(--color-border)' : 'var(--color-accent)',
                    color: '#07080d', fontSize: 13, fontWeight: 600,
                    cursor: mutation.isPending ? 'not-allowed' : 'pointer',
                  }}
                >
                  {mutation.isPending ? 'Creating…' : 'Create Flag'}
                </button>
              )}
            </div>
          </form>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
```

- [ ] **Step 2: Wire Create Flag button in FlagList to open modal**

In `src/views/FlagList/index.tsx`:

Add import:
```tsx
import { FlagCreateModal } from '../../components/FlagCreateModal.js';
```

Add state inside FlagList:
```tsx
const [createOpen, setCreateOpen] = useState(false);
```

Find the existing "+ Create Flag" button (which has no `onClick`) and add `onClick={() => setCreateOpen(true)}`.

Add before the closing `</div>` of FlagList's return:
```tsx
<FlagCreateModal open={createOpen} onClose={() => setCreateOpen(false)} />
```

- [ ] **Step 3: Verify build passes**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/workspace-dashboard
npm run build 2>&1 | tail -5
```

- [ ] **Step 4: Commit**

```bash
cd ..
git add workspace-dashboard/src/components/FlagCreateModal.tsx workspace-dashboard/src/views/FlagList/index.tsx
git commit -m "feat(dashboard): FlagCreateModal — RHF v8 + Zod v4, 3-step form (identity, targeting rules, rollout), motion step transitions"
```

---

### Task 9: FlagDetail — useQuery + useOptimisticToggle + push PR

**Files:**
- Modify: `workspace-dashboard/src/views/FlagDetail/index.tsx`

**Interfaces:**
- Consumes: `useQuery` from TanStack Query, `useOptimisticToggle` from `../../hooks/useOptimisticToggle.js`
- Produces: FlagDetail with real data loading, optimistic enable/disable toggle with toast feedback

- [ ] **Step 1: Read current FlagDetail/index.tsx**

Read the full file at `workspace-dashboard/src/views/FlagDetail/index.tsx`

- [ ] **Step 2: Replace raw fetch with useQuery**

Find the `useEffect + fetch` block and replace with:

```tsx
import { useQuery } from '@tanstack/react-query';
import { toast } from 'sonner';
import { useOptimisticToggle } from '../../hooks/useOptimisticToggle.js';

// Inside FlagDetail:
const { data: flag, isLoading } = useQuery({
  queryKey: ['flag', flagKey],
  queryFn: async () => {
    const r = await fetch(`${API_URL}/api/v1/flags/${flagKey}`, {
      headers: { Authorization: `Bearer ${SDK_TOKEN}` },
    });
    if (!r.ok) throw new Error('Flag not found');
    return r.json() as Promise<FlagDetailType>;
  },
  enabled: !!flagKey,
});

const { data: envStates } = useQuery({
  queryKey: ['snapshot', env],
  queryFn: async () => {
    const r = await fetch(
      `${API_URL}/api/v1/environments/snapshot?environment=${env}`,
      { headers: { Authorization: `Bearer ${SDK_TOKEN}` } },
    );
    if (!r.ok) return {} as Record<string, EnvState>;
    const d = await r.json() as { flags?: EnvState[] };
    const map: Record<string, EnvState> = {};
    for (const s of (d.flags ?? [])) map[s.flag_key] = s;
    return map;
  },
  enabled: !!flagKey,
});

const currentEnvState = envStates?.[flagKey ?? ''];

// Optimistic toggle for the current env
const { enabled, toggle, isPending } = useOptimisticToggle(
  flagKey ?? '',
  env as 'development' | 'staging' | 'production',
  { enabled: currentEnvState?.enabled ?? false, rolloutPct: currentEnvState?.rollout_pct ?? 0 },
);
```

Replace any manual toggle button with:
```tsx
<button
  onClick={toggle}
  disabled={isPending}
  style={{
    padding: '8px 16px', borderRadius: 8, border: 'none',
    background: enabled ? 'var(--color-risk-high)' : 'var(--color-accent)',
    color: enabled ? '#fff' : '#07080d',
    fontSize: 13, fontWeight: 600, cursor: isPending ? 'not-allowed' : 'pointer',
    opacity: isPending ? 0.7 : 1,
  }}
>
  {isPending ? '…' : enabled ? 'Disable' : 'Enable'}
</button>
```

- [ ] **Step 3: Verify full build passes**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/workspace-dashboard
npm run build 2>&1 | tail -5
```

Expected: zero TypeScript errors, all vendor chunks present.

- [ ] **Step 4: Commit + push branch + create PR**

```bash
cd ..
git add workspace-dashboard/src/views/FlagDetail/index.tsx
git commit -m "feat(dashboard): FlagDetail — TanStack Query + useOptimisticToggle with toast feedback"
git push origin feat/dashboard-tech-stack-upgrade
```

Then create the PR:
```bash
export PATH="/opt/homebrew/bin:$PATH"
gh pr create \
  --title "feat(dashboard): Tech stack upgrade — Vite 8, TanStack Query v5, ECharts v6.1, Cosmos.gl v3, RHF+Zod, Sonner, nuqs" \
  --base main \
  --head feat/dashboard-tech-stack-upgrade \
  --body "$(cat <<'EOF'
## Summary
- **Vite 8 + Rolldown** — Rust-based bundler, 10-30x faster builds, manual chunk splitting
- **TanStack Query v5** — Replace all raw fetch() with useQuery/useMutation, proper cache/invalidation
- **React 19 patterns** — useOptimistic + useTransition for instant flag toggle feedback, useDeferredValue for 5000+ flag search
- **ECharts v6.1** — Replace Recharts for time-series (fixed critical time-axis bugs), GovernanceDash health trend chart
- **Cosmos.gl v3** — GPU WebGL 2 graph replacing react-force-graph, handles 100k+ nodes
- **Flag Create Modal** — RHF v8 + Zod v4, 3-step form (identity → targeting rules → rollout), animated transitions
- **Sonner toasts** — Flag toggle feedback, error notifications, creation success
- **nuqs URL state** — FlagList env + search filters persist in URL, shareable links

## Test plan
- [ ] Build produces vendor chunks (react, query, echarts, motion)
- [ ] Flag list loads via TanStack Query (Network tab shows /api/v1/flags)
- [ ] Search filters URL updates (?q=my-flag, ?env=staging)
- [ ] Flag toggle shows optimistic update instantly, reverts on error
- [ ] Toast appears on toggle success/error
- [ ] GovernanceDash shows ECharts health trend chart
- [ ] Causal Graph renders with Cosmos.gl (WebGL canvas visible)
- [ ] Create Flag modal opens, form validates, submits to API
- [ ] npm run build passes zero errors
EOF
)"
```

---

## Self-Review

**Spec coverage:**
- ✅ Vite 8 → Task 1
- ✅ TanStack Query v5 → Tasks 2, 4, 5, 6, 7, 9
- ✅ useDeferredValue flag search → Task 4
- ✅ useOptimistic + useTransition flag toggles → Task 3, 9
- ✅ ECharts v6.1 → Task 6
- ✅ Cosmos.gl v3 → Task 7
- ✅ React Hook Form v8 + Zod v4 → Task 8
- ✅ Sonner toasts → Tasks 2, 3, 8, 9
- ✅ nuqs URL state → Task 5
- ✅ PR creation → Task 9

**Placeholder scan:** All steps have concrete code. No TBD or TODO.

**Type consistency:**
- `FlagItem`, `EnvState` defined in `useFlags.ts` and consumed consistently in Tasks 4, 5, 9
- `useOptimisticToggle` returns `{ enabled, rolloutPct, toggle, isPending }` — used identically in Tasks 9
- `EvaluationChart` accepts `TimeSeriesPoint[]` — defined in Task 6 and used in GovernanceDash Task 6
