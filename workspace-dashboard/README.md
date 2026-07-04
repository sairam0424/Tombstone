# Tombstone Dashboard

React 19 + Vite + Tailwind v4 management UI for Tombstone. Connects to `flag-api` (`:8081`) for CRUD and governance operations and to `gateway` (`:8080`) for live SSE flag-state updates.

## Views

| View | Route | Purpose |
|------|-------|---------|
| **FlagList** | `/` | Paginated list of all flags with search, filter by status/environment, and bulk actions |
| **FlagDetail** | `/flags/:key` | Full flag config — targeting rules, segments, variants, scheduling, and audit history |
| **IncidentTimeline** | `/incidents` | Causal correlation view: maps flag changes to production incidents on a shared timeline |
| **GovernanceDash** | `/governance` | Policy-gate status, stale-flag alerts, Knight Capital-style tombstone queue |
| **ApprovalQueue** | `/approvals` | Pending change requests awaiting owner/admin sign-off before promotion |
| **BreakGlass** | `/break-glass` | Emergency override panel — bypasses approval workflow with mandatory audit reason |
| **DependencyGraph** | `/dependencies` | Force-directed graph of flag prerequisite chains and blast-radius relationships |
| **Experiments** | `/experiments` | A/B experiment configuration, metric binding, and statistical significance tracking |
| **Marketplace** | `/marketplace` | Flag template library — import community or internal flag patterns |
| **SLO** | `/slo` | SLO health tiles per flag cohort with burn-rate alerts and auto-rollback thresholds |

## Dev commands

```bash
# Install dependencies
npm install

# Start Vite dev server (proxies API calls to :8081 / :8080)
npm run dev        # http://localhost:3000

# Run tests (Vitest)
npm run test

# Production build (output to dist/)
npm run build

# Preview production build locally
npm run preview
```

## Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `VITE_API_URL` | `http://localhost:8081` | flag-api base URL |
| `VITE_GATEWAY_URL` | `http://localhost:8080` | gateway SSE base URL |

Set these in a `.env.local` file (git-ignored) for local overrides.

## Stack

- **React 19** — concurrent rendering, use client/server component boundary
- **Vite** — ESM-native dev server, fast HMR
- **Tailwind v4** — utility-first CSS, JIT
- **Vitest** — unit and component tests
- **TypeScript** — strict mode, ESM-only

## Project structure

```
src/
├── views/          # One directory per route (FlagList, FlagDetail, …)
├── components/     # Shared UI primitives
├── hooks/          # Custom React hooks (useFlags, useSSE, …)
├── types.ts        # Shared TypeScript types mirroring proto contracts
├── App.tsx         # Router + layout shell
└── main.tsx        # Entry point
```

## Connecting to a running stack

Run `make dev` from the repo root first — it starts PostgreSQL, Redis, flag-api, gateway, and seeds sample flags. The Vite proxy (`vite.config.ts`) forwards `/api` → `:8081` and `/events` → `:8080` so no CORS configuration is needed during local development.
