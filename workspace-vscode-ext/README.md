# Tombstone VS Code Extension

Manage feature flags directly from your editor. The Tombstone extension connects to your running flag-api and intelligence service to surface flag state inline, let you act on flags without leaving VS Code, and generate cleanup PRs for stale flags.

---

## Features

### CodeLens Annotations
Inline annotations appear above every flag evaluation call in your code. Each lens shows:
- Enabled/disabled state (green check or slash circle)
- Current rollout percentage
- Flag owner
- A warning when a flag has been fully rolled out to 100% and may be ready for removal

Supported call patterns: `evaluate()`, `isEnabled()`, `is_enabled()`, `getFlag()`, `flagEnabled()`, `checkFlag()`

### Kill Switch Command
`Tombstone: Kill Switch — Disable Flag Immediately`

Instantly disables a flag in the target environment. Requires a reason of at least 10 characters. Prompts for confirmation before acting.

### NLP Flag Search
`Tombstone: Search Flags (NLP)`

Describe what you are looking for in plain English. Results come from the intelligence service's semantic search endpoint. Select a result to open it directly in the dashboard.

### Cleanup PR Generation
`Tombstone: Generate Cleanup PR for Stale Flag`

Lists all stale flags (flags that have been at 100% rollout for an extended period). Pick one and the extension generates a PR title, body, and branch name — displayed as a Markdown preview document you can copy into your SCM tool.

### Feature Flag Sidebar
The Tombstone activity bar panel lists all flags sorted by urgency (killed flags first, then disabled, then enabled). Click any flag to open it in the dashboard.

---

## Setup

1. Install the extension from the VS Code Marketplace or build it locally with `npm run package`.

2. Open **Settings** (`Cmd+,` on macOS) and configure:

   | Setting | Description | Default |
   |---|---|---|
   | `tombstone.apiUrl` | Base URL of the flag-api service | `http://localhost:8081` |
   | `tombstone.apiToken` | Bearer token for API authentication | *(empty)* |
   | `tombstone.environment` | Target environment (`development` / `staging` / `production`) | `production` |
   | `tombstone.intelligenceApiUrl` | Base URL of the intelligence service | `http://localhost:8082` |
   | `tombstone.enableCodeLens` | Toggle inline CodeLens annotations | `true` |

3. Make sure your flag-api and intelligence services are running and reachable at the configured URLs.

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
