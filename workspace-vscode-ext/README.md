# Tombstone VS Code Extension

Manage feature flags directly from your editor. The Tombstone extension connects to your running flag-api and intelligence service to surface flag state inline, let you act on flags without leaving VS Code, and generate cleanup PRs for stale flags.

Requires **Tombstone v2.0.1** or later.

---

## Features

### CodeLens Annotations
Inline annotations appear above every flag evaluation call in your code. Each lens shows:
- Enabled/disabled state (green check or slash circle)
- Current rollout percentage
- Flag owner
- Evaluation reason from the v2 5-step pipeline: `RULE_MATCH`, `FALLTHROUGH`, `KILLED`, `PREREQUISITES_UNMET`, or `TARGETING_MATCH`
- A warning when a flag has been fully rolled out to 100% and may be ready for removal

Supported call patterns: `evaluate()`, `isEnabled()`, `is_enabled()`, `getFlag()`, `flagEnabled()`, `checkFlag()`

### Kill Switch Command
`Tombstone: Kill Switch — Disable Flag Immediately`

Instantly disables a flag in the target environment. Requires a reason of at least 10 characters. Prompts for confirmation before acting.

### NLP Flag Search
`Tombstone: Search Flags (NLP)`

Describe what you are looking for in plain English. Results are powered by the v2 intelligence service's pgvector semantic search — embeddings stored in PostgreSQL via pgvector give more accurate relevance ranking than keyword search. Select a result to open it directly in the dashboard.

### Cleanup PR Generation
`Tombstone: Generate Cleanup PR for Stale Flag`

Lists all stale flags (flags that have been at 100% rollout for an extended period). Pick one and the extension generates a PR title, body, and branch name — displayed as a Markdown preview document you can copy into your SCM tool. The generated spec includes:
- Flag key, owner, and age-at-100%
- An ast-rewriter command block (`tombstone ast-rewriter remove <flag-key>`) pre-filled with the correct flag key for automated code removal
- Affected file list sourced from the intelligence service's impact analysis
- Standard checklist (remove call sites, update tests, archive the flag)

### Feature Flag Sidebar
The Tombstone activity bar panel lists all flags sorted by urgency (killed flags first, then disabled, then enabled). Click any flag to open it in the dashboard.

---

## v2 Evaluation Pipeline

The v2 evaluator resolves a flag through five ordered steps. CodeLens annotations now surface the winning step as the evaluation reason so you can tell at a glance why a flag resolved the way it did:

| Reason | Meaning |
|---|---|
| `KILLED` | Flag was force-disabled via Kill Switch |
| `PREREQUISITES_UNMET` | One or more prerequisite flags are not enabled for this context |
| `TARGETING_MATCH` | A targeting rule matched the request context |
| `RULE_MATCH` | A percentage or attribute rule matched |
| `FALLTHROUGH` | No rule matched; default (fallthrough) value was returned |

---

## Setup

1. Install the extension from the VS Code Marketplace or build it locally with `npm run package`.

2. Open **Settings** (`Cmd+,` on macOS) and configure:

   | Setting | Description | Default |
   |---|---|---|
   | `tombstone.apiUrl` | Base URL of the flag-api service | `http://localhost:8081` |
   | `tombstone.apiToken` | Bearer token for API authentication | *(empty)* |
   | `tombstone.environment` | Target environment (`development` / `staging` / `production`) | `production` |
   | `tombstone.intelligenceApiUrl` | Base URL of the v2 intelligence service (NLP search, impact analysis, stale-flag detection) | `http://localhost:8083` |
   | `tombstone.enableCodeLens` | Toggle inline CodeLens annotations | `true` |

3. Make sure your flag-api and intelligence services are running and reachable at the configured URLs. The intelligence service must be the v2 build (port 8083 by default) for NLP search and cleanup PR generation to work.

4. Open any source file — CodeLens annotations will appear within a few seconds.

---

## Supported Languages

TypeScript, JavaScript (including React/TSX/JSX variants), Python, Go, Java, Ruby

---

## Commands

| Command | Palette title |
|---|---|
| `tombstone.refreshFlags` | Tombstone: Refresh Flags |
| `tombstone.killSwitch` | Tombstone: Kill Switch — Disable Flag Immediately |
| `tombstone.searchFlags` | Tombstone: Search Flags (NLP) |
| `tombstone.generateCleanupPR` | Tombstone: Generate Cleanup PR for Stale Flag |
| `tombstone.openFlagInDashboard` | Tombstone: Open Flag in Dashboard |

---

## Version Compatibility

| Extension version | Tombstone backend |
|---|---|
| 2.x | v2.0.1+ (required) |
| 1.x | v1.x (legacy, no longer maintained) |

The v2 extension will not function correctly against a v1 backend. The evaluation-reason field and pgvector NLP search endpoint (`/api/v2/intelligence/search`) are absent in v1.

---

## Development

```bash
# Install dependencies
npm install

# Compile TypeScript
npm run compile

# Watch mode
npm run watch

# Package as .vsix
npm run package
```

Open the project in VS Code and press `F5` to launch an Extension Development Host for local testing.
