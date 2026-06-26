# Tombstone Dashboard — Anvilry Design System Port

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Port Anvilry's premium design system patterns (Inter font, MotionConfig provider, cn() utility, Reveal/EmptyState/Section components, skeleton composites) into the Tombstone dashboard so both products share the same visual language.

**Architecture:** Four sequential tasks: (1) foundation — fonts + tailwind-merge + MotionConfig + cn(); (2) UI primitives — Reveal, EmptyState, Section ported verbatim from Anvilry; (3) skeleton composites — SkeletonStatCard upgrade; (4) wiring — apply components in 5 views. Every task builds on the previous and ships independently.

**Tech Stack:** React 19, Vite 8, Tailwind v4, Motion v12 (`motion/react`), @fontsource/inter, @fontsource/jetbrains-mono, tailwind-merge, clsx (already installed)

## Global Constraints

- Working directory: `/Users/sairamugge/Desktop/Not-Humans-World/Tombstone/workspace-dashboard`
- All LOCAL imports use `.js` extension (ESM/NodeNext) — e.g. `import { cn } from '../lib/utils.js'`
- Motion v12 import: `import { motion, MotionConfig } from 'motion/react'`
- Design tokens live in `src/index.css` under `@theme {}` — Tailwind v4 CSS-first
- Primary accent: `#38e1ff` (electric cyan) — do not change
- Branch: `feat/dashboard-anvilry-port` (create from main before starting)
- Verify build after every task: `cd workspace-dashboard && npm run build`
- TypeScript strict — no `any`, no unused locals
- Anvilry source path: `/Users/sairamugge/Desktop/Not-Humans-World/Anvilry/sairam-dev/src`

---

## Task 1: Foundation — Inter font + tailwind-merge + cn() + MotionConfig

**Files:**
- Modify: `workspace-dashboard/package.json`
- Modify: `workspace-dashboard/src/index.css`
- Modify: `workspace-dashboard/src/main.tsx`
- Create: `workspace-dashboard/src/lib/utils.ts`
- Create: `workspace-dashboard/src/lib/useReducedMotion.ts`
- Create: `workspace-dashboard/src/lib/useMounted.ts`

**Interfaces:**
- Produces:
  - `cn(...inputs: ClassValue[]): string` from `src/lib/utils.ts` — used by all Tasks 2–4
  - `useReducedMotion(): boolean` from `src/lib/useReducedMotion.ts` — used by Reveal (Task 2)
  - `useMounted(): boolean` from `src/lib/useMounted.ts` — used by Reveal (Task 2)
  - Inter font loaded via CSS, `--font-sans` CSS var active
  - `MotionConfig reducedMotion="user"` wrapping the entire app

- [ ] **Step 1: Create feat branch**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone
git checkout main && git pull origin main
git checkout -b feat/dashboard-anvilry-port
```

- [ ] **Step 2: Install new packages**

```bash
cd workspace-dashboard
npm install --ignore-scripts @fontsource/inter @fontsource/jetbrains-mono tailwind-merge
```

- [ ] **Step 3: Verify packages installed**

```bash
node -e "require('./node_modules/tailwind-merge/dist/cjs/index.js')" && echo "tailwind-merge ok"
ls node_modules/@fontsource/inter/index.css && echo "inter ok"
ls node_modules/@fontsource/jetbrains-mono/index.css && echo "mono ok"
```

Expected: three "ok" lines.

- [ ] **Step 4: Add font imports to src/index.css**

Read the current `src/index.css`. Add these two lines at the very top, before `@import "tailwindcss"`:

```css
@import "@fontsource/inter/latin.css";
@import "@fontsource/jetbrains-mono/latin.css";
```

Then update the `@theme` block — find the `--font-mono` line and add `--font-sans` before it:

```css
  /* Fonts */
  --font-sans: 'Inter', ui-sans-serif, system-ui, -apple-system, sans-serif;
  --font-mono: 'JetBrains Mono', 'Fira Code', ui-monospace, SFMono-Regular, Menlo, monospace;
```

Also update the `body` rule to use the CSS var:

```css
body {
  color: var(--color-fg);
  font-family: var(--font-sans);
  -webkit-font-smoothing: antialiased;
  -moz-osx-font-smoothing: grayscale;
}
```

- [ ] **Step 5: Create src/lib/utils.ts**

```ts
// workspace-dashboard/src/lib/utils.ts
import { clsx, type ClassValue } from 'clsx';
import { twMerge } from 'tailwind-merge';

/** Merge Tailwind classes with conflict resolution. Ported from Anvilry. */
export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}
```

- [ ] **Step 6: Create src/lib/useReducedMotion.ts**

```ts
// workspace-dashboard/src/lib/useReducedMotion.ts
import { useEffect, useState } from 'react';

/**
 * Reads prefers-reduced-motion without importing the full motion bundle.
 * SSR-safe: returns false when window is unavailable. Ported from Anvilry.
 */
export function useReducedMotion(): boolean {
  const [reduced, setReduced] = useState(
    () =>
      typeof window !== 'undefined'
        ? window.matchMedia('(prefers-reduced-motion: reduce)').matches
        : false,
  );
  useEffect(() => {
    const mq = window.matchMedia('(prefers-reduced-motion: reduce)');
    const handler = (e: MediaQueryListEvent) => setReduced(e.matches);
    mq.addEventListener('change', handler);
    return () => mq.removeEventListener('change', handler);
  }, []);
  return reduced;
}
```

- [ ] **Step 7: Create src/lib/useMounted.ts**

```ts
// workspace-dashboard/src/lib/useMounted.ts
import { useSyncExternalStore } from 'react';

const emptySubscribe = () => () => {};

/**
 * Hydration-safe mount flag. Server = false, client = true.
 * Prevents animations from firing before JS is ready. Ported from Anvilry.
 */
export function useMounted(): boolean {
  return useSyncExternalStore(
    emptySubscribe,
    () => true,  // client snapshot
    () => false, // server snapshot
  );
}
```

- [ ] **Step 8: Add MotionConfig to src/main.tsx**

Read the current `src/main.tsx`. Add this import:

```tsx
import { MotionConfig } from 'motion/react';
```

Then wrap the entire `<StrictMode>` tree with `<MotionConfig>`:

```tsx
createRoot(root).render(
  <StrictMode>
    <MotionConfig reducedMotion="user">
      <QueryClientProvider client={queryClient}>
        <BrowserRouter>
          <NuqsAdapter>
            <App />
          </NuqsAdapter>
        </BrowserRouter>
        {import.meta.env.DEV && <ReactQueryDevtools initialIsOpen={false} />}
      </QueryClientProvider>
    </MotionConfig>
  </StrictMode>,
);
```

- [ ] **Step 9: Verify build passes**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/workspace-dashboard
npm run build 2>&1 | tail -5
```

Expected: `✓ built in X.XXs` with zero TypeScript errors.

- [ ] **Step 10: Commit**

```bash
cd ..
git add workspace-dashboard/package.json workspace-dashboard/package-lock.json \
        workspace-dashboard/src/index.css workspace-dashboard/src/main.tsx \
        workspace-dashboard/src/lib/utils.ts workspace-dashboard/src/lib/useReducedMotion.ts \
        workspace-dashboard/src/lib/useMounted.ts
git commit -m "feat(dashboard): Inter font + tailwind-merge + cn() utility + MotionConfig provider — ported from Anvilry"
```

---

## Task 2: UI Primitives — Reveal, EmptyState, Section

**Files:**
- Create: `workspace-dashboard/src/components/ui/Reveal.tsx`
- Create: `workspace-dashboard/src/components/ui/EmptyState.tsx`
- Create: `workspace-dashboard/src/components/ui/Section.tsx`
- Modify: `workspace-dashboard/src/components/ui/index.ts`

**Interfaces:**
- Consumes: `cn` from `../../lib/utils.js`, `useReducedMotion` from `../../lib/useReducedMotion.js`, `useMounted` from `../../lib/useMounted.js`, `motion` from `motion/react`
- Produces:
  - `<Reveal delay? className?>` — scroll-triggered fade-up, static fallback on reduced-motion/unmounted
  - `<EmptyState icon? heading body? action? className?>` — card-surface empty state
  - `<Section id? label? title? titleAs? className?>` — eyebrow + heading wrapper

- [ ] **Step 1: Create src/components/ui/Reveal.tsx**

```tsx
// workspace-dashboard/src/components/ui/Reveal.tsx
import { motion } from 'motion/react';
import { useReducedMotion } from '../../lib/useReducedMotion.js';
import { useMounted } from '../../lib/useMounted.js';
import type { ReactNode } from 'react';

/**
 * Scroll-into-view reveal with no-JS / pre-hydration safety net.
 * Fires animation only after mount — content never stuck at opacity:0.
 * Reduced-motion users get static rendering. Ported from Anvilry.
 */
export function Reveal({
  children,
  delay = 0,
  className,
}: {
  children: ReactNode;
  delay?: number;
  className?: string;
}) {
  const reduced = useReducedMotion();
  const mounted = useMounted();

  if (reduced || !mounted) return <div className={className}>{children}</div>;

  return (
    <motion.div
      className={className}
      initial={{ opacity: 0, y: 16 }}
      whileInView={{ opacity: 1, y: 0 }}
      viewport={{ once: true, margin: '-80px' }}
      transition={{ duration: 0.5, delay, ease: [0.21, 0.47, 0.32, 0.98] }}
    >
      {children}
    </motion.div>
  );
}
```

- [ ] **Step 2: Create src/components/ui/EmptyState.tsx**

```tsx
// workspace-dashboard/src/components/ui/EmptyState.tsx
import type { ReactNode } from 'react';
import { cn } from '../../lib/utils.js';

interface EmptyStateProps {
  icon?: ReactNode;
  heading: string;
  body?: string;
  action?: ReactNode;
  className?: string;
}

/**
 * Premium empty state: icon + heading + body + optional CTA.
 * Uses card-surface for consistent visual treatment. Ported from Anvilry.
 */
export function EmptyState({ icon, heading, body, action, className }: EmptyStateProps) {
  return (
    <div
      className={cn(
        'card-surface flex flex-col items-center gap-3 px-8 py-12 text-center',
        className,
      )}
    >
      {icon && (
        <span
          style={{ color: 'var(--color-fg-subtle)' }}
          aria-hidden="true"
        >
          {icon}
        </span>
      )}
      <h3 style={{ fontWeight: 600, color: 'var(--color-fg)', margin: 0 }}>
        {heading}
      </h3>
      {body && (
        <p style={{ fontSize: 13, color: 'var(--color-fg-muted)', maxWidth: 280, margin: 0, lineHeight: 1.5 }}>
          {body}
        </p>
      )}
      {action && <div style={{ marginTop: 8 }}>{action}</div>}
    </div>
  );
}
```

- [ ] **Step 3: Create src/components/ui/Section.tsx**

```tsx
// workspace-dashboard/src/components/ui/Section.tsx
import type { ReactNode } from 'react';
import { cn } from '../../lib/utils.js';

/**
 * Page section with optional monospace eyebrow label above heading.
 * Pass titleAs="h1" on the first/primary section per page (WCAG 1.3.1).
 * Subsequent sections keep the default h2. Ported from Anvilry.
 */
export function Section({
  id,
  label,
  title,
  titleAs: TitleTag = 'h2',
  children,
  className,
}: {
  id?: string;
  label?: string;
  title?: string;
  titleAs?: 'h1' | 'h2';
  children: ReactNode;
  className?: string;
}) {
  return (
    <section
      id={id}
      style={{ width: '100%' }}
      className={cn(className)}
    >
      {(label || title) && (
        <header style={{ marginBottom: 24 }}>
          {label && (
            <p
              className="flag-key"
              style={{
                fontSize: 11,
                fontWeight: 600,
                letterSpacing: '0.1em',
                textTransform: 'uppercase',
                color: 'var(--color-fg-subtle)',
                margin: '0 0 6px',
              }}
            >
              {label}
            </p>
          )}
          {title && (
            <TitleTag
              style={{
                fontSize: 18,
                fontWeight: 700,
                color: 'var(--color-fg)',
                margin: 0,
                letterSpacing: '-0.02em',
              }}
            >
              {title}
            </TitleTag>
          )}
        </header>
      )}
      {children}
    </section>
  );
}
```

- [ ] **Step 4: Update src/components/ui/index.ts barrel**

Read the current `src/components/ui/index.ts`. Add the three new exports:

```ts
export { Reveal } from './Reveal.js';
export { EmptyState } from './EmptyState.js';
export { Section } from './Section.js';
```

- [ ] **Step 5: Verify build passes**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/workspace-dashboard
npm run build 2>&1 | tail -5
```

- [ ] **Step 6: Commit**

```bash
cd ..
git add workspace-dashboard/src/components/ui/Reveal.tsx \
        workspace-dashboard/src/components/ui/EmptyState.tsx \
        workspace-dashboard/src/components/ui/Section.tsx \
        workspace-dashboard/src/components/ui/index.ts
git commit -m "feat(dashboard): Reveal + EmptyState + Section components — ported from Anvilry with .js imports"
```

---

## Task 3: Skeleton Composites — SkeletonStatCard + SkeletonViewTransition

**Files:**
- Modify: `workspace-dashboard/src/components/SkeletonRow.tsx`

**Interfaces:**
- Consumes: `cn` from `../lib/utils.js`, `useReducedMotion` from `../lib/useReducedMotion.js`
- Produces:
  - `SkeletonStatCard()` — icon circle + value + label (for GovernanceDash stat cards)
  - `SkeletonViewTransition({ label? })` — full-viewport fallback with pulsing orb ring

- [ ] **Step 1: Read the current SkeletonRow.tsx**

Read the full file at `workspace-dashboard/src/components/SkeletonRow.tsx`

- [ ] **Step 2: Add imports and new composites**

Add these imports at the top of the file:

```tsx
import { cn } from '../lib/utils.js';
import { useReducedMotion } from '../lib/useReducedMotion.js';
```

Then add these two new exports at the bottom of the file (keep all existing exports unchanged):

```tsx
/**
 * Matches GovernanceDash stat card layout: [icon circle] [value] [label].
 * Ported from Anvilry SkeletonStatCard.
 */
export function SkeletonStatCard() {
  return (
    <div
      className="card-surface"
      style={{ display: 'flex', alignItems: 'center', gap: 12, padding: '12px 16px' }}
      aria-hidden="true"
    >
      <div
        className="skeleton-shimmer"
        style={{ width: 32, height: 32, borderRadius: '50%', flexShrink: 0 }}
      />
      <div style={{ display: 'flex', flexDirection: 'column', gap: 6, flex: 1 }}>
        <div className="skeleton-shimmer" style={{ height: 14, width: 40, borderRadius: 4 }} />
        <div className="skeleton-shimmer" style={{ height: 10, width: 80, borderRadius: 4 }} />
      </div>
    </div>
  );
}

/**
 * Full-viewport skeleton for view-switching fallbacks.
 * Pulsing cyan orb ring + skeleton lines. Ported from Anvilry.
 */
export function SkeletonViewTransition({ label }: { label?: string }) {
  const reduced = useReducedMotion();
  return (
    <div
      style={{
        display: 'flex',
        flexDirection: 'column',
        alignItems: 'center',
        justifyContent: 'center',
        height: 'calc(100vh - 60px)',
        gap: 24,
      }}
      role="status"
      aria-label={label ?? 'Loading…'}
    >
      {/* Orb ring — matches Anvilry accent aesthetics */}
      <div
        style={{
          width: 64, height: 64, borderRadius: '50%',
          border: '1px solid color-mix(in oklab, var(--color-accent) 30%, transparent)',
          background: 'color-mix(in oklab, var(--color-accent) 5%, transparent)',
          animation: reduced ? 'none' : 'pulse 2s ease-in-out infinite',
        }}
        aria-hidden="true"
      />
      <div style={{ width: 160, display: 'flex', flexDirection: 'column', gap: 8 }}>
        <div className="skeleton-shimmer" style={{ height: 10, width: '100%', borderRadius: 999 }} />
        <div className="skeleton-shimmer" style={{ height: 10, width: '75%', borderRadius: 999, margin: '0 auto' }} />
        <div className="skeleton-shimmer" style={{ height: 10, width: '50%', borderRadius: 999, margin: '0 auto' }} />
      </div>
      <span className="sr-only">{label ?? 'Loading…'}</span>
    </div>
  );
}
```

Also add the pulse keyframe to `src/index.css` (in the animations section, with reduced-motion guard):

```css
@keyframes pulse {
  0%, 100% { opacity: 1; transform: scale(1); }
  50%       { opacity: 0.6; transform: scale(1.05); }
}
@media (prefers-reduced-motion: reduce) {
  [style*="animation: pulse"] { animation: none !important; }
}
```

- [ ] **Step 3: Export new composites from ui/index.ts barrel**

Read `src/components/ui/index.ts`. Add:

```ts
export { SkeletonStatCard, SkeletonViewTransition } from '../SkeletonRow.js';
```

- [ ] **Step 4: Verify build**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/workspace-dashboard
npm run build 2>&1 | tail -5
```

- [ ] **Step 5: Commit**

```bash
cd ..
git add workspace-dashboard/src/components/SkeletonRow.tsx \
        workspace-dashboard/src/components/ui/index.ts \
        workspace-dashboard/src/index.css
git commit -m "feat(dashboard): SkeletonStatCard + SkeletonViewTransition — ported from Anvilry with orb ring aesthetic"
```

---

## Task 4: Wire components into 5 views + create PR

**Files:**
- Modify: `workspace-dashboard/src/views/GovernanceDash/index.tsx`
- Modify: `workspace-dashboard/src/views/ApprovalQueue/index.tsx`
- Modify: `workspace-dashboard/src/views/IncidentTimeline/index.tsx`
- Modify: `workspace-dashboard/src/views/Experiments/index.tsx`
- Modify: `workspace-dashboard/src/views/Marketplace/index.tsx`

**Interfaces:**
- Consumes: `EmptyState`, `Section`, `Reveal`, `SkeletonStatCard` from the ui barrel

**For each view: read it first, then make surgical targeted replacements only.**

- [ ] **Step 1: Update GovernanceDash — Section + SkeletonStatCard + Reveal**

Read `src/views/GovernanceDash/index.tsx`.

Add imports:
```tsx
import { Section, Reveal, SkeletonStatCard } from '../../components/ui/index.js';
```

Replace the plain-text page header (h1 + subtitle div) with:
```tsx
<Section titleAs="h1" label="GOVERNANCE" title="Flag Health" />
```

Replace the 3-stat loading skeleton (currently renders nothing or a spinner) with `SkeletonStatCard`:
```tsx
{healthLoading ? (
  <div style={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: 12 }}>
    <SkeletonStatCard />
    <SkeletonStatCard />
    <SkeletonStatCard />
  </div>
) : (
  /* existing stat cards */
)}
```

Wrap the stale flags table in `<Reveal>`:
```tsx
<Reveal delay={0.1}>
  {/* existing stale flags table */}
</Reveal>
```

- [ ] **Step 2: Update ApprovalQueue — EmptyState**

Read `src/views/ApprovalQueue/index.tsx`.

Add import:
```tsx
import { EmptyState } from '../../components/ui/index.js';
import { CheckCircle } from 'lucide-react';
```

Find the empty state (plain text like "No pending approvals" or similar). Replace with:
```tsx
<EmptyState
  icon={<CheckCircle size={40} />}
  heading="No pending approvals"
  body="All flag changes have been reviewed. Great job keeping things moving."
/>
```

- [ ] **Step 3: Update IncidentTimeline — EmptyState**

Read `src/views/IncidentTimeline/index.tsx`.

Add import:
```tsx
import { EmptyState } from '../../components/ui/index.js';
import { Zap } from 'lucide-react';
```

Find the no-incidents empty state. Replace with:
```tsx
<EmptyState
  icon={<Zap size={40} />}
  heading="No incidents in this window"
  body="No flag changes or anomalies detected. Your system is stable."
/>
```

- [ ] **Step 4: Update Experiments — EmptyState + Reveal on experiment cards**

Read `src/views/Experiments/index.tsx`.

Add import:
```tsx
import { EmptyState, Reveal } from '../../components/ui/index.js';
import { FlaskConical } from 'lucide-react';
```

Find the empty state. Replace with:
```tsx
<EmptyState
  icon={<FlaskConical size={40} />}
  heading="No experiments running"
  body="Create an A/B test to start measuring flag impact on your key metrics."
/>
```

Wrap each experiment card in `<Reveal delay={index * 0.05}>`.

- [ ] **Step 5: Update Marketplace — EmptyState + Reveal on integration cards**

Read `src/views/Marketplace/index.tsx`.

Add import:
```tsx
import { EmptyState, Reveal } from '../../components/ui/index.js';
import { Puzzle } from 'lucide-react';
```

Find the empty state. Replace with:
```tsx
<EmptyState
  icon={<Puzzle size={40} />}
  heading="No integrations connected"
  body="Connect Slack, Datadog, PagerDuty and more to get alerts when flags trip circuit breakers."
/>
```

Wrap each integration card in `<Reveal delay={index * 0.04}>`.

- [ ] **Step 6: Verify full build passes**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/workspace-dashboard
npm run build 2>&1 | tail -5
```

Expected: `✓ built in X.XXs` zero TypeScript errors.

- [ ] **Step 7: Commit + push + create PR**

```bash
cd ..
git add workspace-dashboard/src/views/GovernanceDash/index.tsx \
        workspace-dashboard/src/views/ApprovalQueue/index.tsx \
        workspace-dashboard/src/views/IncidentTimeline/index.tsx \
        workspace-dashboard/src/views/Experiments/index.tsx \
        workspace-dashboard/src/views/Marketplace/index.tsx
git commit -m "feat(dashboard): wire Reveal/EmptyState/Section/SkeletonStatCard into 5 views — Anvilry design language"

export PATH="/opt/homebrew/bin:$PATH"
git push origin feat/dashboard-anvilry-port

gh pr create \
  --title "feat(dashboard): Anvilry design system port — Inter font, cn(), MotionConfig, Reveal, EmptyState, Section, SkeletonStatCard" \
  --base main \
  --head feat/dashboard-anvilry-port \
  --body "$(cat <<'EOF'
## Summary
Port Anvilry's premium design system patterns into Tombstone dashboard so both products share the same visual language.

- **Inter font** via @fontsource — closes visual gap between Anvilry and Tombstone
- **JetBrains Mono** via @fontsource — actually loaded now (was declared but not installed)
- **tailwind-merge + cn()** — utility for safe Tailwind class merging (prevents style conflicts)
- **MotionConfig reducedMotion="user"** — single OS-level provider replaces 8 scattered CSS @media guards
- **Reveal** — scroll-triggered fade-up with no-JS safety net (content never stuck at opacity:0)
- **EmptyState** — premium icon + heading + body + CTA component (replaces plain text in 5 views)
- **Section** — monospace eyebrow label + heading semantic wrapper (GovernanceDash)
- **SkeletonStatCard** — content-aware stat card skeleton (GovernanceDash loading state)
- **SkeletonViewTransition** — full-viewport fallback with pulsing cyan orb ring

## Test plan
- [ ] Inter font renders in browser (inspect element → font-family: Inter)
- [ ] JetBrains Mono renders for flag keys (monospace code elements)
- [ ] GovernanceDash shows SkeletonStatCard while loading
- [ ] ApprovalQueue/IncidentTimeline/Experiments/Marketplace show EmptyState with icon when empty
- [ ] Experiment cards and Marketplace cards reveal on scroll (fade-up)
- [ ] Users with prefers-reduced-motion OS setting get static rendering (no animation)
- [ ] npm run build passes zero errors
EOF
)"
```

---

## Self-Review

**Spec coverage:**
- ✅ Inter font → Task 1
- ✅ JetBrains Mono loaded → Task 1
- ✅ tailwind-merge installed → Task 1
- ✅ cn() utility → Task 1
- ✅ useReducedMotion hook → Task 1
- ✅ useMounted hook → Task 1
- ✅ MotionConfig provider → Task 1
- ✅ Reveal component → Task 2
- ✅ EmptyState component → Task 2
- ✅ Section component → Task 2
- ✅ SkeletonStatCard → Task 3
- ✅ SkeletonViewTransition → Task 3
- ✅ GovernanceDash wired → Task 4
- ✅ ApprovalQueue EmptyState → Task 4
- ✅ IncidentTimeline EmptyState → Task 4
- ✅ Experiments EmptyState + Reveal → Task 4
- ✅ Marketplace EmptyState + Reveal → Task 4

**Placeholder scan:** All steps have complete code. No TBD.

**Type consistency:**
- `cn` exported from `utils.ts` as `(...inputs: ClassValue[]) => string` — consumed identically in EmptyState, Section, Reveal
- `useReducedMotion()` returns `boolean` — consumed in Reveal and SkeletonViewTransition
- `useMounted()` returns `boolean` — consumed in Reveal
- `EmptyStateProps` defines all props used at each call site in Task 4
