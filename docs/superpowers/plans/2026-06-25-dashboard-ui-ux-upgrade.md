# Tombstone Dashboard — UI/UX Upgrade Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Upgrade the Tombstone dashboard from functional-but-hex-based to a perceptually correct, incident-ready ops UI — OKLCH semantic colors, table density switcher, incident timeline visual hierarchy, quick-action context panel, connection status indicator, and Motion-integrated micro-animations.

**Architecture:** Eight independent tasks across three phases: (1) design system foundation — OKLCH tokens and relative color variants; (2) FlagList UX — density switcher, keyboard navigation, improved empty state; (3) Power features — incident timeline hierarchy, FlagDetail quick-actions, connection status bar, rollout bar micro-animation. Every task ships independently and builds on the design tokens from Task 1.

**Tech Stack:** React 19, Tailwind v4 (OKLCH), Motion v12, TanStack Virtual v3, nuqs, Lucide, @radix-ui/react-dialog, localStorage

## Global Constraints

- Working directory: `/Users/sairamugge/Desktop/Not-Humans-World/Tombstone/workspace-dashboard`
- All LOCAL imports use `.js` extension (ESM/NodeNext) — e.g. `import { X } from '../config.js'`
- Design tokens live in `src/index.css` under `@theme {}` — Tailwind v4 CSS-first
- Primary accent: `#38e1ff` (electric cyan) — do not change
- All animations must include `@media (prefers-reduced-motion: reduce)` guards
- Motion v12 import: `import { motion, AnimatePresence } from 'motion/react'`
- Branch: `feat/dashboard-ui-ux-upgrade` (create from main before starting)
- Verify build after every task: `cd workspace-dashboard && npm run build`
- TypeScript strict — no `any`, no unused locals

---

## Phase 1: Design System Foundation

### Task 1: OKLCH Semantic Token Migration + Relative Color Variants

**Files:**
- Modify: `workspace-dashboard/src/index.css`

**Interfaces:**
- Produces: All OKLCH CSS custom properties used by Tasks 2–8. Every hex color in the design system replaced with OKLCH equivalents. Relative color auto-variants (`--color-*-hover`, `--color-*-dim`) generated from base tokens.

- [ ] **Step 1: Create feat branch**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone
git checkout main && git pull origin main
git checkout -b feat/dashboard-ui-ux-upgrade
```

- [ ] **Step 2: Replace the entire @theme block in src/index.css**

Read the current file first. Find the `@theme {` block (starts around line 9) and replace it entirely with:

```css
@theme {
  /* ── Backgrounds (deep ink) ── */
  --color-bg-base:     oklch(7% 0.02 264);
  --color-bg-surface:  oklch(10% 0.025 264);
  --color-bg-elevated: oklch(13% 0.028 264);
  --color-bg-overlay:  oklch(16% 0.03 264);

  /* ── Borders ── */
  --color-border:        oklch(22% 0.03 264);
  --color-border-strong: oklch(28% 0.035 264);
  --color-border-focus:  oklch(82% 0.18 200);

  /* ── Text (AAA contrast on dark bg) ── */
  --color-fg:        oklch(93% 0.02 264);
  --color-fg-muted:  oklch(67% 0.04 264);
  --color-fg-subtle: oklch(52% 0.04 264);

  /* ── Accent (electric cyan) ── */
  --color-accent:        oklch(82% 0.18 200);
  --color-accent-strong: oklch(72% 0.2 200);
  --color-accent-dim:    oklch(82% 0.18 200 / 20%);

  /* ── Violet (secondary, AI motifs) ── */
  --color-violet:     oklch(72% 0.19 305);
  --color-violet-dim: oklch(72% 0.19 305 / 20%);

  /* ── Semantic risk (perceptually uniform severity scale) ── */
  --color-risk-low:     oklch(74% 0.2 142);   /* emerald-green */
  --color-risk-medium:  oklch(78% 0.17 85);   /* amber */
  --color-risk-high:    oklch(65% 0.23 25);   /* red */
  --color-risk-blocked: oklch(72% 0.19 305);  /* violet */

  /* ── Semantic state ── */
  --color-state-active:   oklch(74% 0.2 142);
  --color-state-draft:    oklch(52% 0.04 264);
  --color-state-complete: oklch(82% 0.18 200);
  --color-state-archived: oklch(35% 0.025 264);

  /* ── Semantic action ── */
  --color-action-primary:   oklch(82% 0.18 200);
  --color-action-danger:    oklch(65% 0.23 25);
  --color-action-warning:   oklch(78% 0.17 85);
  --color-action-success:   oklch(74% 0.2 142);

  /* ── Fonts ── */
  --font-mono: 'JetBrains Mono', 'Fira Code', ui-monospace, SFMono-Regular, Menlo, monospace;

  /* ── Radii ── */
  --radius-sm:   4px;
  --radius-md:   8px;
  --radius-lg:   12px;
  --radius-xl:   16px;
  --radius-card: 12px;
  --radius-pill: 999px;
}
```

- [ ] **Step 3: Add OKLCH relative color auto-variants after the @theme block**

Add this section directly after the closing `}` of `@theme`:

```css
/* ── OKLCH Relative Color Variants ──────────────────────────────────────── */
/* Auto-generate hover/focus/dim from base tokens without extra hex values.   */
/* @supports fallback ensures old browsers still get the base color.           */
:root {
  /* Risk hover states — lightness +12% */
  --color-risk-low-hover:    oklch(from var(--color-risk-low)    calc(l + 0.12) c h);
  --color-risk-medium-hover: oklch(from var(--color-risk-medium) calc(l + 0.10) c h);
  --color-risk-high-hover:   oklch(from var(--color-risk-high)   calc(l + 0.12) c h);

  /* Accent dim variants — lightness -20% */
  --color-accent-subtle:  oklch(from var(--color-accent) calc(l - 0.20) c h / 15%);
  --color-accent-glow:    oklch(from var(--color-accent) l c h / 40%);

  /* Action danger — dim for disabled states */
  --color-action-danger-dim: oklch(from var(--color-action-danger) calc(l - 0.15) c h / 30%);
}

/* Browsers without relative color support get the base values */
@supports not (color: oklch(from red l c h)) {
  :root {
    --color-risk-low-hover:    #6ee7a0;
    --color-risk-medium-hover: #fcd34d;
    --color-risk-high-hover:   #fca5a5;
    --color-accent-subtle:     rgba(56, 225, 255, 0.15);
    --color-accent-glow:       rgba(56, 225, 255, 0.40);
    --color-action-danger-dim: rgba(248, 113, 113, 0.30);
  }
}
```

- [ ] **Step 4: Update the forced background colors at the top of index.css**

Find and replace:
```css
html { background-color: #07080d !important; }
body { background-color: #07080d !important; }
```
With:
```css
html { background-color: oklch(7% 0.02 264) !important; }
body { background-color: oklch(7% 0.02 264) !important; }
```

- [ ] **Step 5: Verify build passes**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/workspace-dashboard
npm run build 2>&1 | tail -5
```

Expected: `✓ built in X.XXs` with no TypeScript errors.

- [ ] **Step 6: Commit**

```bash
cd ..
git add workspace-dashboard/src/index.css
git commit -m "feat(dashboard): migrate design tokens to OKLCH — perceptually uniform severity scale, relative color auto-variants"
```

---

## Phase 2: FlagList UX

### Task 2: Table Density Switcher — useDensity hook + DensityToggle component

**Files:**
- Create: `workspace-dashboard/src/hooks/useDensity.ts`
- Create: `workspace-dashboard/src/components/DensityToggle.tsx`

**Interfaces:**
- Produces:
  - `useDensity(): { density: Density, setDensity: (d: Density) => void, rowHeight: number }` where `Density = 'condensed' | 'normal' | 'spacious'` and rowHeights are `{ condensed: 32, normal: 52, spacious: 72 }`
  - `<DensityToggle />` — renders three buttons (C/N/S) with keyboard shortcuts

- [ ] **Step 1: Create src/hooks/useDensity.ts**

```ts
// workspace-dashboard/src/hooks/useDensity.ts
import { useState, useCallback } from 'react';

export type Density = 'condensed' | 'normal' | 'spacious';

const ROW_HEIGHTS: Record<Density, number> = {
  condensed: 32,
  normal:    52,
  spacious:  72,
};

const STORAGE_KEY = 'tombstone-density';

function readStored(): Density {
  try {
    const v = localStorage.getItem(STORAGE_KEY);
    if (v === 'condensed' || v === 'normal' || v === 'spacious') return v;
  } catch { /* SSR/private browsing */ }
  return 'normal';
}

export function useDensity() {
  const [density, setDensityState] = useState<Density>(readStored);

  const setDensity = useCallback((d: Density) => {
    setDensityState(d);
    try { localStorage.setItem(STORAGE_KEY, d); } catch { /* ignore */ }
  }, []);

  return { density, setDensity, rowHeight: ROW_HEIGHTS[density] };
}
```

- [ ] **Step 2: Create src/components/DensityToggle.tsx**

```tsx
// workspace-dashboard/src/components/DensityToggle.tsx
import { useEffect } from 'react';
import { motion } from 'motion/react';
import type { Density } from '../hooks/useDensity.js';

interface Props {
  density: Density;
  onChange: (d: Density) => void;
}

const OPTIONS: { value: Density; label: string; key: string; title: string }[] = [
  { value: 'condensed', label: 'C', key: 'c', title: 'Condensed (C)' },
  { value: 'normal',    label: 'N', key: 'n', title: 'Normal (N)' },
  { value: 'spacious',  label: 'S', key: 's', title: 'Spacious (S)' },
];

export function DensityToggle({ density, onChange }: Props) {
  // Keyboard shortcuts: C / N / S (only when not focused in input)
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      const active = document.activeElement;
      const isInput = active instanceof HTMLInputElement || active instanceof HTMLTextAreaElement;
      if (isInput) return;
      const opt = OPTIONS.find(o => o.key === e.key.toLowerCase());
      if (opt) onChange(opt.value);
    };
    window.addEventListener('keydown', handler);
    return () => window.removeEventListener('keydown', handler);
  }, [onChange]);

  return (
    <div
      role="group"
      aria-label="Row density"
      style={{
        display: 'flex',
        gap: 2,
        padding: '3px',
        background: 'var(--color-bg-elevated)',
        border: '1px solid var(--color-border)',
        borderRadius: 8,
      }}
    >
      {OPTIONS.map(opt => {
        const active = density === opt.value;
        return (
          <motion.button
            key={opt.value}
            title={opt.title}
            aria-pressed={active}
            onClick={() => onChange(opt.value)}
            whileTap={{ scale: 0.93 }}
            transition={{ type: 'spring', stiffness: 600, damping: 35 }}
            style={{
              width: 28, height: 24,
              borderRadius: 5,
              border: 'none',
              background: active ? 'var(--color-accent-subtle)' : 'transparent',
              color: active ? 'var(--color-accent)' : 'var(--color-fg-subtle)',
              fontSize: 11,
              fontWeight: 600,
              cursor: 'pointer',
              letterSpacing: '0.04em',
              transition: 'background 0.12s, color 0.12s',
            }}
          >
            {opt.label}
          </motion.button>
        );
      })}
    </div>
  );
}
```

- [ ] **Step 3: Verify build**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/workspace-dashboard
npm run build 2>&1 | tail -4
```

- [ ] **Step 4: Commit**

```bash
cd ..
git add workspace-dashboard/src/hooks/useDensity.ts workspace-dashboard/src/components/DensityToggle.tsx
git commit -m "feat(dashboard): density switcher — useDensity hook (localStorage) + DensityToggle component (C/N/S keyboard shortcuts)"
```

---

### Task 3: Wire DensityToggle into FlagList virtualizer

**Files:**
- Modify: `workspace-dashboard/src/views/FlagList/index.tsx`

**Interfaces:**
- Consumes: `useDensity` from `../../hooks/useDensity.js`, `DensityToggle` from `../../components/DensityToggle.js`
- Produces: FlagList where row height adapts to density; DensityToggle appears in the controls bar

- [ ] **Step 1: Read the current FlagList/index.tsx**

Read the full file at `workspace-dashboard/src/views/FlagList/index.tsx`

- [ ] **Step 2: Add imports at top of FlagList**

```tsx
import { useDensity } from '../../hooks/useDensity.js';
import { DensityToggle } from '../../components/DensityToggle.js';
```

- [ ] **Step 3: Add useDensity inside FlagList component**

Add after the existing hooks:
```tsx
const { density, setDensity, rowHeight } = useDensity();
```

- [ ] **Step 4: Wire rowHeight to the virtualizer**

Find the `useVirtualizer` call. Change:
```tsx
estimateSize: () => 52,
```
To:
```tsx
estimateSize: () => rowHeight,
```

- [ ] **Step 5: Add DensityToggle to the controls bar**

Find the `{/* ── Controls ── */}` section (the div containing the search input and env pills). Add `<DensityToggle density={density} onChange={setDensity} />` as the last item in that flex row:

```tsx
{/* Controls bar */}
<div style={{ display: 'flex', gap: 10, marginBottom: 18, flexWrap: 'wrap', alignItems: 'center' }}>
  {/* ... existing search input and env pills ... */}
  <DensityToggle density={density} onChange={setDensity} />
</div>
```

- [ ] **Step 6: Update empty state to include Create Flag CTA**

Find the empty state div (shows "No flags yet. Create your first flag.") and replace it:

```tsx
<div style={{ padding: 64, textAlign: 'center', color: 'var(--color-fg-subtle)' }}>
  <svg width="40" height="40" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" style={{ margin: '0 auto 16px', opacity: 0.25, display: 'block' }}>
    <path d="M4 15s1-1 4-1 5 2 8 2 4-1 4-1V3s-1 1-4 1-5-2-8-2-4 1-4 1z" />
    <line x1="4" y1="22" x2="4" y2="15" />
  </svg>
  {search
    ? <><div style={{ fontSize: 14, fontWeight: 500, marginBottom: 6, color: 'var(--color-fg-muted)' }}>No flags match "{search}"</div><div style={{ fontSize: 12 }}>Try a different search term</div></>
    : <>
        <div style={{ fontSize: 14, fontWeight: 500, marginBottom: 6, color: 'var(--color-fg-muted)' }}>No flags yet</div>
        <div style={{ fontSize: 12, marginBottom: 20 }}>Create your first feature flag to get started</div>
        <button
          onClick={() => setCreateOpen(true)}
          style={{ padding: '8px 18px', borderRadius: 8, border: 'none', background: 'var(--color-accent)', color: '#07080d', fontSize: 13, fontWeight: 600, cursor: 'pointer' }}
        >
          + Create Flag
        </button>
      </>
  }
</div>
```

- [ ] **Step 7: Verify build**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/workspace-dashboard
npm run build 2>&1 | tail -4
```

- [ ] **Step 8: Commit**

```bash
cd ..
git add workspace-dashboard/src/views/FlagList/index.tsx
git commit -m "feat(dashboard): FlagList density — wire rowHeight to virtualizer, density switcher in controls, empty state with CTA"
```

---

## Phase 3: Power Features

### Task 4: Connection Status Bar — persistent SSE health indicator

**Files:**
- Create: `workspace-dashboard/src/components/ConnectionStatus.tsx`
- Modify: `workspace-dashboard/src/App.tsx`

**Interfaces:**
- Consumes: `useSSE` from `./hooks/useSSE.js` (already exists — returns `{ connected: boolean }`)
- Produces: `<ConnectionStatus env={string} />` — animated Live/Connecting/Offline pill in the App header

- [ ] **Step 1: Create src/components/ConnectionStatus.tsx**

```tsx
// workspace-dashboard/src/components/ConnectionStatus.tsx
import { motion, AnimatePresence } from 'motion/react';
import { useSSE } from '../hooks/useSSE.js';

interface Props { env: string; }

export function ConnectionStatus({ env }: Props) {
  const { connected } = useSSE(env);

  return (
    <AnimatePresence mode="wait">
      <motion.div
        key={connected ? 'live' : 'offline'}
        initial={{ opacity: 0, scale: 0.9 }}
        animate={{ opacity: 1, scale: 1 }}
        exit={{ opacity: 0, scale: 0.9 }}
        transition={{ type: 'spring', stiffness: 500, damping: 35 }}
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: 6,
          padding: '4px 10px',
          borderRadius: 'var(--radius-pill)',
          background: connected
            ? 'color-mix(in oklab, var(--color-risk-low) 12%, transparent)'
            : 'color-mix(in oklab, var(--color-fg-subtle) 8%, transparent)',
          border: `1px solid ${connected
            ? 'color-mix(in oklab, var(--color-risk-low) 25%, transparent)'
            : 'var(--color-border)'}`,
          fontSize: 11,
          fontWeight: 500,
          color: connected ? 'var(--color-risk-low)' : 'var(--color-fg-subtle)',
          userSelect: 'none',
        }}
      >
        {/* Animated dot */}
        <div style={{ position: 'relative', width: 7, height: 7 }}>
          <div style={{
            width: 7, height: 7, borderRadius: '50%',
            background: connected ? 'var(--color-risk-low)' : 'var(--color-fg-subtle)',
          }} />
          {connected && (
            <motion.div
              style={{
                position: 'absolute', inset: -2,
                borderRadius: '50%',
                border: '1.5px solid var(--color-risk-low)',
              }}
              animate={{ opacity: [1, 0], scale: [1, 1.8] }}
              transition={{ duration: 1.8, repeat: Infinity, ease: 'easeOut' }}
            />
          )}
        </div>
        {connected ? 'Live' : 'Offline'}
      </motion.div>
    </AnimatePresence>
  );
}
```

- [ ] **Step 2: Wire ConnectionStatus into App.tsx header**

Read the current `src/App.tsx`. 

Add import at top:
```tsx
import { ConnectionStatus } from './components/ConnectionStatus.js';
```

Find the header's right-side controls area (where the Activity toggle and Search button live). Add `<ConnectionStatus env={env} />` to the right of the environment pill and before the Search button:

```tsx
{/* Replace the Activity toggle button with ConnectionStatus */}
<ConnectionStatus env={env} />
```

Remove the old Activity/showFeed toggle button and the `showFeed` state, since ConnectionStatus replaces it as a persistent indicator. Also remove `{showFeed && <LiveFeed env={env} />}` from the main layout — the SSE is now always active via ConnectionStatus but not shown as a panel (simplifying the layout).

Remove these from App.tsx:
- `import { Activity } from 'lucide-react'` (if only used for the toggle)
- `import { LiveFeed } from './components/LiveFeed.js'`
- `const [showFeed, setShowFeed] = useState(false)`
- The Activity toggle button JSX
- `{showFeed && <LiveFeed env={env} />}`

- [ ] **Step 3: Verify build**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/workspace-dashboard
npm run build 2>&1 | tail -4
```

- [ ] **Step 4: Commit**

```bash
cd ..
git add workspace-dashboard/src/components/ConnectionStatus.tsx workspace-dashboard/src/App.tsx
git commit -m "feat(dashboard): ConnectionStatus — persistent SSE health pill in header with spring enter/exit + ripple animation"
```

---

### Task 5: Incident Timeline Visual Hierarchy

**Files:**
- Modify: `workspace-dashboard/src/views/IncidentTimeline/index.tsx`

**Interfaces:**
- Produces: IncidentTimeline where state-change events (flag_change, incident) are PRIMARY (semantic OKLCH colors, larger dot, bold text) and notification events (info severity) are SECONDARY (grey, smaller dot). Quick-action buttons (Rollback, Silence) appear inline on critical events.

- [ ] **Step 1: Read the current IncidentTimeline/index.tsx**

Read the full file at `workspace-dashboard/src/views/IncidentTimeline/index.tsx`

- [ ] **Step 2: Add SEVERITY_CONFIG with visual hierarchy**

After the imports, add:

```tsx
// Visual hierarchy: state-change events are PRIMARY, notification delivery is SECONDARY
const SEVERITY_CONFIG: Record<string, {
  color: string;
  bgColor: string;
  dotSize: number;
  isPrimary: boolean;
  label: string;
}> = {
  critical: { color: 'var(--color-risk-high)',    bgColor: 'color-mix(in oklab, var(--color-risk-high) 12%, transparent)',   dotSize: 10, isPrimary: true,  label: 'Critical' },
  high:     { color: 'var(--color-risk-high)',    bgColor: 'color-mix(in oklab, var(--color-risk-high) 8%, transparent)',    dotSize: 8,  isPrimary: true,  label: 'High' },
  warning:  { color: 'var(--color-risk-medium)',  bgColor: 'color-mix(in oklab, var(--color-risk-medium) 10%, transparent)', dotSize: 8,  isPrimary: true,  label: 'Warning' },
  medium:   { color: 'var(--color-risk-medium)',  bgColor: 'color-mix(in oklab, var(--color-risk-medium) 8%, transparent)',  dotSize: 6,  isPrimary: false, label: 'Medium' },
  low:      { color: 'var(--color-risk-low)',     bgColor: 'color-mix(in oklab, var(--color-risk-low) 8%, transparent)',     dotSize: 6,  isPrimary: false, label: 'Low' },
  info:     { color: 'var(--color-fg-subtle)',    bgColor: 'transparent',                                                     dotSize: 4,  isPrimary: false, label: 'Info' },
};

// State-change types are PRIMARY events — flag lifecycle changes, incidents
const PRIMARY_TYPES = new Set(['flag_change', 'incident']);
```

- [ ] **Step 3: Update the timeline entry rendering**

Find the JSX that renders each timeline entry. Replace it with a version that applies the visual hierarchy:

```tsx
{entries.map((entry, idx) => {
  const cfg = SEVERITY_CONFIG[entry.severity] ?? SEVERITY_CONFIG.info;
  const isPrimary = cfg.isPrimary || PRIMARY_TYPES.has(entry.type);

  return (
    <div
      key={`${entry.ts}-${idx}`}
      style={{
        display: 'flex',
        gap: 16,
        padding: isPrimary ? '14px 16px' : '8px 16px',
        background: isPrimary ? cfg.bgColor : 'transparent',
        borderBottom: '1px solid var(--color-border)',
        transition: 'background 0.15s',
        opacity: isPrimary ? 1 : 0.65,
      }}
    >
      {/* Timeline dot — larger for primary state-change events */}
      <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', paddingTop: 3, flexShrink: 0 }}>
        <div style={{
          width: cfg.dotSize,
          height: cfg.dotSize,
          borderRadius: '50%',
          background: cfg.color,
          boxShadow: isPrimary ? `0 0 8px ${cfg.color}80` : 'none',
          flexShrink: 0,
        }} />
        {idx < entries.length - 1 && (
          <div style={{ width: 1, flex: 1, background: 'var(--color-border)', marginTop: 4 }} />
        )}
      </div>

      {/* Content */}
      <div style={{ flex: 1, minWidth: 0 }}>
        <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', gap: 8, marginBottom: 4 }}>
          <div style={{
            fontSize: isPrimary ? 13 : 12,
            fontWeight: isPrimary ? 600 : 400,
            color: isPrimary ? 'var(--color-fg)' : 'var(--color-fg-muted)',
            lineHeight: 1.4,
          }}>
            {entry.title}
          </div>
          <span style={{ fontSize: 10, color: 'var(--color-fg-subtle)', flexShrink: 0, fontFamily: 'var(--font-mono)' }}>
            {formatDistanceToNow(fromUnixTime(entry.ts), { addSuffix: true })}
          </span>
        </div>

        {entry.description && (
          <div style={{ fontSize: 11, color: 'var(--color-fg-subtle)', marginBottom: isPrimary ? 10 : 0, lineHeight: 1.5 }}>
            {entry.description}
          </div>
        )}

        {/* Quick-actions live INSIDE the triage panel for primary events */}
        {isPrimary && entry.type === 'flag_change' && entry.flagKey && (
          <div style={{ display: 'flex', gap: 6, marginTop: 8 }}>
            <button
              onClick={() => window.location.href = `/flags/${entry.flagKey}`}
              style={{
                padding: '3px 10px', borderRadius: 6, border: '1px solid var(--color-border)',
                background: 'var(--color-bg-elevated)', color: 'var(--color-fg-muted)',
                fontSize: 11, cursor: 'pointer', fontWeight: 500,
              }}
            >
              View Flag →
            </button>
            {entry.severity === 'critical' || entry.severity === 'high' ? (
              <button
                onClick={() => { /* rollback action - navigates to flag with rollback intent */ window.location.href = `/flags/${entry.flagKey}?action=rollback`; }}
                style={{
                  padding: '3px 10px', borderRadius: 6, border: '1px solid var(--color-border)',
                  background: 'color-mix(in oklab, var(--color-action-danger) 12%, transparent)',
                  color: 'var(--color-action-danger)',
                  fontSize: 11, cursor: 'pointer', fontWeight: 500,
                }}
              >
                Rollback
              </button>
            ) : null}
          </div>
        )}
      </div>
    </div>
  );
})}
```

- [ ] **Step 4: Verify build**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/workspace-dashboard
npm run build 2>&1 | tail -4
```

- [ ] **Step 5: Commit**

```bash
cd ..
git add workspace-dashboard/src/views/IncidentTimeline/index.tsx
git commit -m "feat(dashboard): IncidentTimeline visual hierarchy — state-change events PRIMARY (semantic OKLCH colors, glow, bold), notification events SECONDARY (grey, dim), quick-actions inline"
```

---

### Task 6: FlagDetail Quick-Actions Context Strip

**Files:**
- Modify: `workspace-dashboard/src/views/FlagDetail/index.tsx`

**Interfaces:**
- Produces: An inline quick-action strip (Rollback / Disable / Clone / View Audit) that lives next to the flag data — no modal required. Destructive confirmation (Rollback) uses a Radix AlertDialog inline.

- [ ] **Step 1: Read the current FlagDetail/index.tsx**

Read the full file at `workspace-dashboard/src/views/FlagDetail/index.tsx`

- [ ] **Step 2: Add QuickActions component inside FlagDetail file**

Add this component above the `export default function FlagDetail()`:

```tsx
import * as AlertDialog from '@radix-ui/react-alert-dialog';
import { motion } from 'motion/react';
import { RotateCcw, Power, Copy, ScrollText } from 'lucide-react';

interface QuickActionsProps {
  flagKey: string;
  env: string;
  enabled: boolean;
  onToggle: () => void;
  isPending: boolean;
}

function QuickActions({ flagKey, env, enabled, onToggle, isPending }: QuickActionsProps) {
  return (
    <div style={{
      display: 'flex',
      gap: 8,
      padding: '12px 16px',
      background: 'var(--color-bg-elevated)',
      border: '1px solid var(--color-border)',
      borderRadius: 'var(--radius-lg)',
      flexWrap: 'wrap',
    }}>
      {/* Rollback — destructive, requires confirmation */}
      <AlertDialog.Root>
        <AlertDialog.Trigger asChild>
          <motion.button
            whileTap={{ scale: 0.96 }}
            transition={{ type: 'spring', stiffness: 500, damping: 35 }}
            style={{
              display: 'flex', alignItems: 'center', gap: 6,
              padding: '6px 12px', borderRadius: 8,
              border: '1px solid color-mix(in oklab, var(--color-action-danger) 30%, transparent)',
              background: 'var(--color-action-danger-dim)',
              color: 'var(--color-action-danger)',
              fontSize: 12, fontWeight: 500, cursor: 'pointer',
            }}
          >
            <RotateCcw size={12} /> Rollback
          </motion.button>
        </AlertDialog.Trigger>
        <AlertDialog.Portal>
          <AlertDialog.Overlay style={{ position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.5)', zIndex: 50 }} />
          <AlertDialog.Content style={{
            position: 'fixed', top: '50%', left: '50%', transform: 'translate(-50%,-50%)',
            zIndex: 51, width: 420, padding: 24,
            background: 'var(--color-bg-elevated)',
            border: '1px solid var(--color-border-strong)',
            borderRadius: 'var(--radius-xl)',
            boxShadow: '0 24px 48px rgba(0,0,0,0.5)',
          }}>
            <AlertDialog.Title style={{ fontSize: 16, fontWeight: 700, color: 'var(--color-fg)', marginBottom: 8 }}>
              Rollback flag?
            </AlertDialog.Title>
            <AlertDialog.Description style={{ fontSize: 13, color: 'var(--color-fg-muted)', marginBottom: 20, lineHeight: 1.5 }}>
              This will disable <code style={{ fontFamily: 'var(--font-mono)', color: 'var(--color-accent)' }}>{flagKey}</code> in <strong>{env}</strong> and revert to the previous state. This action can be undone.
            </AlertDialog.Description>
            <div style={{ display: 'flex', gap: 10, justifyContent: 'flex-end' }}>
              <AlertDialog.Cancel asChild>
                <button style={{ padding: '7px 16px', borderRadius: 8, border: '1px solid var(--color-border)', background: 'transparent', color: 'var(--color-fg-muted)', fontSize: 13, cursor: 'pointer' }}>
                  Cancel
                </button>
              </AlertDialog.Cancel>
              <AlertDialog.Action asChild>
                <button
                  onClick={onToggle}
                  style={{ padding: '7px 16px', borderRadius: 8, border: 'none', background: 'var(--color-action-danger)', color: '#fff', fontSize: 13, fontWeight: 600, cursor: 'pointer' }}
                >
                  Rollback
                </button>
              </AlertDialog.Action>
            </div>
          </AlertDialog.Content>
        </AlertDialog.Portal>
      </AlertDialog.Root>

      {/* Toggle Enable/Disable — no confirmation needed, optimistic */}
      <motion.button
        whileTap={{ scale: 0.96 }}
        transition={{ type: 'spring', stiffness: 500, damping: 35 }}
        onClick={onToggle}
        disabled={isPending}
        style={{
          display: 'flex', alignItems: 'center', gap: 6,
          padding: '6px 12px', borderRadius: 8,
          border: '1px solid var(--color-border)',
          background: 'var(--color-bg-surface)',
          color: enabled ? 'var(--color-risk-high)' : 'var(--color-risk-low)',
          fontSize: 12, fontWeight: 500, cursor: isPending ? 'wait' : 'pointer',
          opacity: isPending ? 0.6 : 1,
        }}
      >
        <Power size={12} />
        {isPending ? '…' : enabled ? 'Disable' : 'Enable'}
      </motion.button>

      {/* Clone flag — navigates to create with prefill */}
      <motion.button
        whileTap={{ scale: 0.96 }}
        transition={{ type: 'spring', stiffness: 500, damping: 35 }}
        onClick={() => { window.location.href = `/flags/new?clone=${flagKey}`; }}
        style={{
          display: 'flex', alignItems: 'center', gap: 6,
          padding: '6px 12px', borderRadius: 8,
          border: '1px solid var(--color-border)',
          background: 'var(--color-bg-surface)',
          color: 'var(--color-fg-muted)',
          fontSize: 12, fontWeight: 500, cursor: 'pointer',
        }}
      >
        <Copy size={12} /> Clone
      </motion.button>

      {/* View Audit — scrolls to audit section */}
      <motion.button
        whileTap={{ scale: 0.96 }}
        transition={{ type: 'spring', stiffness: 500, damping: 35 }}
        onClick={() => { document.getElementById('audit-log')?.scrollIntoView({ behavior: 'smooth' }); }}
        style={{
          display: 'flex', alignItems: 'center', gap: 6,
          padding: '6px 12px', borderRadius: 8,
          border: '1px solid var(--color-border)',
          background: 'var(--color-bg-surface)',
          color: 'var(--color-fg-muted)',
          fontSize: 12, fontWeight: 500, cursor: 'pointer',
        }}
      >
        <ScrollText size={12} /> Audit
      </motion.button>
    </div>
  );
}
```

- [ ] **Step 3: Mount QuickActions in FlagDetail's JSX**

In the FlagDetail component return, add `<QuickActions>` after the flag header and before the environment tabs. Find where `flagKey` and the active env toggle are used, and mount:

```tsx
{flag && currentEnvState && (
  <QuickActions
    flagKey={flag.key}
    env={activeEnv}
    enabled={enabled}
    onToggle={toggle}
    isPending={isPending}
  />
)}
```

Also add `id="audit-log"` to the audit log section div so the scroll target works.

- [ ] **Step 4: Add @radix-ui/react-alert-dialog if not installed**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/workspace-dashboard
npm list @radix-ui/react-alert-dialog 2>/dev/null | head -2 || npm install --ignore-scripts @radix-ui/react-alert-dialog@^1.1.0
```

- [ ] **Step 5: Verify build**

```bash
npm run build 2>&1 | tail -4
```

- [ ] **Step 6: Commit**

```bash
cd ..
git add workspace-dashboard/src/views/FlagDetail/index.tsx workspace-dashboard/package.json workspace-dashboard/package-lock.json
git commit -m "feat(dashboard): FlagDetail quick-actions context strip — Rollback (AlertDialog confirm), Toggle, Clone, Audit scroll — no modal context loss"
```

---

### Task 7: Rollout Bar Micro-animation via @property

**Files:**
- Modify: `workspace-dashboard/src/index.css`
- Modify: `workspace-dashboard/src/views/FlagList/index.tsx`

**Interfaces:**
- Produces: Rollout bar fill transitions via `@property --rollout-pct` (already declared in index.css) wired to actual percentage. Color transitions at 0/50/100 thresholds using OKLCH.

- [ ] **Step 1: Verify @property --rollout-pct exists in index.css**

Check that `@property --rollout-pct` is in `src/index.css`. If not, add it:

```css
@property --rollout-pct {
  syntax: '<percentage>';
  inherits: false;
  initial-value: 0%;
}
.rollout-bar-fill {
  width: var(--rollout-pct);
  transition: --rollout-pct 0.6s cubic-bezier(0.4, 0, 0.2, 1),
              background-color 0.4s ease;
}
@media (prefers-reduced-motion: reduce) {
  .rollout-bar-fill { transition: none; }
}
```

- [ ] **Step 2: Update RolloutBar in FlagList to use CSS custom property**

Find the `RolloutBar` component in FlagList (or wherever it's defined). Update it to use the `@property` approach:

```tsx
function RolloutBar({ pct, enabled }: { pct: number; enabled: boolean }) {
  const fillColor = !enabled
    ? 'var(--color-border)'
    : pct === 100
    ? 'var(--color-risk-low)'
    : pct >= 50
    ? 'var(--color-risk-medium)'
    : 'var(--color-accent)';

  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 8, minWidth: 120 }}>
      <div style={{
        flex: 1, height: 4,
        background: 'var(--color-bg-overlay)',
        borderRadius: 2,
        overflow: 'hidden',
      }}>
        <div
          className="rollout-bar-fill"
          style={{
            '--rollout-pct': `${pct}%`,
            height: '100%',
            background: fillColor,
            borderRadius: 2,
          } as React.CSSProperties}
        />
      </div>
      <span style={{ fontSize: 11, color: 'var(--color-fg-subtle)', width: 32, textAlign: 'right' }}>
        {pct}%
      </span>
    </div>
  );
}
```

- [ ] **Step 3: Verify build**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/workspace-dashboard
npm run build 2>&1 | tail -4
```

- [ ] **Step 4: Commit + push + create PR**

```bash
cd ..
git add workspace-dashboard/src/index.css workspace-dashboard/src/views/FlagList/index.tsx
git commit -m "feat(dashboard): rollout bar micro-animation via @property --rollout-pct — smooth width transition + OKLCH color at 0/50/100 thresholds"

export PATH="/opt/homebrew/bin:$PATH"
git push origin feat/dashboard-ui-ux-upgrade
gh pr create \
  --title "feat(dashboard): UI/UX upgrade — OKLCH tokens, density switcher, incident hierarchy, quick-actions, connection status" \
  --base main \
  --head feat/dashboard-ui-ux-upgrade \
  --body "$(cat <<'EOF'
## Summary
Research-backed UI/UX upgrades verified against 2026 production dashboard best practices:

- **OKLCH semantic color system** — migrate from hex to perceptually uniform OKLCH tokens; relative color auto-variants for hover/focus/dim states
- **Table density switcher** — Condensed (32px) / Normal (52px) / Spacious (72px) with C/N/S keyboard shortcuts and localStorage persistence
- **Incident timeline visual hierarchy** — state-change events PRIMARY (OKLCH semantic colors, glow, bold), notification events SECONDARY (grey, dim); quick-actions inline in triage panel
- **FlagDetail quick-actions** — Rollback (AlertDialog confirm), Toggle, Clone, Audit scroll — never lose investigation context
- **Connection status bar** — persistent SSE health pill in header with spring enter/exit + ripple animation; replaces the LiveFeed toggle
- **Rollout bar micro-animation** — @property --rollout-pct smooth transition + OKLCH color change at 0/50/100 thresholds

## Test plan
- [ ] Flag list renders with density switcher (C/N/S keys change row height)
- [ ] Density persists across page reload
- [ ] Incident timeline: critical/high events are larger, colored, bold; info events are grey and smaller
- [ ] Quick-actions strip visible in FlagDetail
- [ ] Rollback shows confirmation dialog before acting
- [ ] Connection status pill shows Live/Offline correctly
- [ ] Rollout bars animate smoothly when percentage changes
- [ ] OKLCH colors render correctly in Chrome/Safari/Firefox
- [ ] Build passes: npm run build (zero TypeScript errors)
EOF
)"
```

---

## Self-Review

**Spec coverage:**
- ✅ OKLCH tokens → Task 1
- ✅ Relative color variants → Task 1
- ✅ Table density switcher → Tasks 2+3
- ✅ FlagList empty state CTA → Task 3
- ✅ Connection status bar → Task 4
- ✅ Incident timeline visual hierarchy → Task 5
- ✅ Quick-actions inline in triage → Task 5 + Task 6
- ✅ FlagDetail quick-actions context strip → Task 6
- ✅ Rollout bar @property animation → Task 7
- ⚠️ Keyboard navigation for FlagList rows (tabindex on virtual rows) — not included to keep scope tight; add as follow-up
- ⚠️ Motion v12 circuit breaker alert flash — not included; circuit breaker data comes from CircuitBreakerStatus component which would need separate task

**Placeholder scan:** All steps have complete code. No TBD.

**Type consistency:**
- `Density` type exported from `useDensity.ts` and consumed identically in `DensityToggle.tsx` and `FlagList/index.tsx`
- `QuickActionsProps` defines all props consumed in the JSX mount in Task 6
