---
kind: architecture
title: Knowledge-base architecture
type: decision
status: adopted
---

# Knowledge-base architecture

How this repo is organized as the operating substrate for a long-lived, autonomous agent
(and its humans). Everything is plain **markdown + frontmatter in git** — diffable, reviewable,
agent-writable. This doc is the durable record of the model and the options rejected, so the
shape stays intentional as it grows.

**Product:** Tombstone — production intelligence layer for feature flags.

---

## The model (v1 — deliberately minimal)

Two ideas only:

1. **Artifacts** are global, foldered by **kind**; `domain:` is a **field (a list)**, not a folder.
   Each artifact has exactly one home (by *what it is*). Cross-cutting is handled by tags + links
   — never by duplicating or by nesting inside a domain.
2. **Domains** are "loops" — a thread of work with a charter, cadence, and metrics. A domain
   folder holds only its **README (charter)** + **machinery** (metrics, collectors). It **links**
   artifacts; it never contains them.

### Kinds (start with just these two)

| kind | what it is | folder | key frontmatter |
|---|---|---|---|
| `signal` | evidence: feedback / idea / observation (deduped, frequency-counted) | `signals/` | `category, frequency, sources[], domain[], status` |
| `doc` | durable knowledge: an analysis, a decision, a thing you learned | `docs/` | `domain[], status?, links` |

That's enough to run almost any loop. Each folder's `README` is its schema — read it before
adding artifacts of that kind. Committed work doesn't need its own kind to start: a loop's
to-dos live inline as a backlog in its domain `README`. Promote them to a `task` kind only
once you've earned it (below).

### Earning a new kind

Default to an existing kind. Add a new one **only** when it has all three of: its own status
machine **and** queryable frontmatter fields **and** a distinct body shape. Otherwise it's a
`doc` or a `signal` with a tag, or a backlog line in a domain README.

---

## Tombstone Domains (loops)

These four domain loops are the primary workstreams for Tombstone's intelligence layer:

| Domain | Goal | Cadence | Collector |
|---|---|---|---|
| `flag-cleanup` | Eliminate stale flags before they become incidents | daily | `flag-api /api/v1/stale` |
| `incident-response` | Correlate production incidents to causal flags; auto-rollback within SLO | on-trigger | `evaluator /api/v1/flags/{key}/slo` |
| `rollout-advisor` | Maximize experiment velocity and minimize collision risk | daily | `intelligence /api/v1/anomaly/{key}` |
| `governance` (active) — weekly health loop | Enforce approval workflows, OPA policy coverage, and audit trail completeness | weekly | `flag-api /api/v1/compliance/evidence`, `intelligence /api/v1/stale` |

### Collectors (data sources)

Deterministic collectors write numeric data. Agents read and interpret.

| Collector | Endpoint | Data written |
|---|---|---|
| stale-flag collector | `intelligence :8083/api/v1/stale` | list of flags not evaluated in >30 days |
| SLO collector | `evaluator :8082/api/v1/flags/{key}/slo` | error rate, p99, circuit-breaker state |
| anomaly collector | `intelligence :8083/api/v1/anomaly/{key}` | ensemble anomaly score, drift delta |
| audit collector | `flag-api :8081/api/v1/audit` | approval lag, coverage gaps, tombstone count |

---

## Rules (DRY + MECE)

1. **One concept = one home** (by kind). Everyone else links via `[[slug]]`.
2. **`domain:` is a field (list), not a folder.** Cross-cutting = multi-tag + multi-link.
3. **Collectors write data; agents write knowledge.** Don't pay an LLM to fetch numbers.
4. **Frontmatter = anything you'd query.** Prose for everything else.

---

## Logs & data

- **`LOG.md`** (root) — global activity feed: one line per ship/ingest. Detail lives in each
  artifact's `## Timeline`. Append one entry right before the commit that ships a bulk of work.
- **No separate `daily`/`journal` kind.** A domain's run-log is its `README`'s `## Timeline`
  (one terse dated line per run); rich per-item detail lives in the items it links. So there are
  exactly two log surfaces: per-artifact `## Timeline` + the global `LOG.md`.
- **`domains/<x>/metrics/*.jsonl`** — numeric time-series, written by **deterministic collectors**
  (code/skills, *not* the LLM). Agents read & interpret. Scorecards are generated from these.

---

## Deferred — add only when the need is real (do NOT pre-build)

| Later | Trigger to add it |
|---|---|
| `trigger:` field (cron / webhook / event) | first non-manual automation (e.g. a server-down webhook) |
| recursive `thread` + `parent:` relation | a domain needs sub-threads (e.g. strategy → tasks) |
| `task` kind | backlogs outgrow inline domain README checklists |
| derived index (sqlite / vector) | retrieval volume outgrows ripgrep (~10⁴⁺ artifacts) |
| reconcile / consolidation daemon | autonomous volume creates dupes / contradictions |
| autonomy / guardrails / budget formalization | agents act without human review |

---

## Options considered, and why not

1. **Folder-by-domain** (everything for a loop under its own folder). Cross-cutting artifacts
   have no single home — an analysis spanning two loops, or a signal feeding multiple domains,
   can't live in one folder. Forces duplication.
2. **Folder-by-kind only, no domains.** Loses the thread-of-work + cadence cohesion;
   "where's the flag-cleanup loop?" has no home.
3. **Half-nested** (some kinds global, some under domains). The asymmetry *is* the bug.
4. **Pure database** (Notion-style). We want the data to live in *this* repo
   (code-adjacent, diffable, reviewable). Forward-compatible: a DB can be derived later.
5. **Heavy taxonomy (8 kinds upfront).** Premature; every kind you can't justify causes
   friction. Start with 2, earn more.

---

## Map (where things live)

| I want to… | Go to |
|---|---|
| record a fact / insight we learned | `docs/` |
| capture feedback / a signal (with frequency) | `signals/` |
| track a piece of committed work | backlog line in the domain `README` |
| read a deep analysis | `docs/` |
| see why we chose something | `docs/` (a decision) |
| see a loop's goal / cadence / state | `domains/<x>/README.md` |
| see metrics over time | `domains/<x>/metrics/*.jsonl` |
| spin up a new loop | run the `new-loop` skill |
| see what's changed across all loops | `LOG.md` |
