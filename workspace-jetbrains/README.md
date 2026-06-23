# Tombstone JetBrains Plugin

IntelliJ/JetBrains IDE plugin for Tombstone v2.0.1 — manage feature flags without leaving your editor.

## Overview

The Tombstone JetBrains plugin integrates Tombstone's production intelligence layer directly into IntelliJ IDEA and other JetBrains IDEs. View live flag states inline as you code, kill dangerous flags with a single shortcut, search flags by natural language, and manage your entire flag inventory from a dedicated sidebar — all without switching to the dashboard.

## Features

### InlayHints

The plugin injects inline hints directly above every `evaluate()` and `isEnabled()` call in your source files. Each hint shows the flag's current state in the active environment:

- `[KILLED]` — flag has been tombstoned and is permanently off
- `[enabled]` — flag is globally on
- `[disabled]` — flag is globally off
- `[alpha: 12%]` — flag is in progressive rollout with current rollout percentage

Hints refresh automatically when the local cache is invalidated and are shown for all supported languages (TypeScript, JavaScript, Python, Go, Java, Ruby).

### ToolWindow

A dedicated **Tombstone** sidebar panel (View → Tool Windows → Tombstone) displays all flags for the configured environment, sorted by operational priority:

1. Killed flags (tombstoned, shown first as cleanup candidates)
2. Enabled flags
3. Disabled flags
4. Alpha/progressive-rollout flags

Each row shows the flag key, current state, last-modified timestamp, and owning team. Click any row to open the flag's detail page in the Tombstone dashboard.

### KillSwitchAction

**Shortcut: Shift+Ctrl+Alt+K**

Kills the flag currently under the editor cursor. When triggered:

1. The plugin reads the flag key from the nearest `evaluate()` or `isEnabled()` call at the cursor position.
2. A dialog prompts for a kill reason (minimum 10 characters enforced).
3. On confirmation, the plugin calls the Tombstone API to tombstone the flag and invalidates the local cache.
4. InlayHints for that flag update immediately to `[KILLED]`.

The action is available from the editor context menu under **Tombstone → Kill Flag**.

### SearchFlagsAction

**Menu: Tools → Tombstone → Search Flags**

NLP-powered flag search backed by Tombstone's intelligence service:

1. Type a natural-language query (e.g. "payment checkout flags" or "flags changed last week").
2. Matching flags are displayed in a quick-pick popup with state and description.
3. Select a flag to open it in the Tombstone dashboard.

Queries are forwarded to the Intelligence URL configured in settings.

### RefreshFlagsAction

**Menu: Tools → Tombstone → Refresh Flags**

Forces a full cache invalidation and re-fetches all flag data from the API. Use this after bulk flag changes or when InlayHints appear stale. The ToolWindow refreshes automatically after the fetch completes.

## Installation

### Build from Source

```bash
cd workspace-jetbrains
./gradlew buildPlugin
```

The plugin archive is written to `build/distributions/tombstone-jetbrains-*.zip`.

### Install in IntelliJ IDEA

1. Open **Settings** (Cmd+, on macOS / Ctrl+Alt+S on Windows/Linux).
2. Navigate to **Plugins**.
3. Click the gear icon and select **Install Plugin from Disk...**.
4. Choose the `.zip` file from `build/distributions/`.
5. Restart the IDE when prompted.

### Requirements

- IntelliJ IDEA 2024.3 or later (Community or Ultimate)
- Tombstone v2.0.1 or later running and reachable from the IDE host
- JDK 17+ (bundled with IntelliJ IDEA 2024.3)

## Configuration

Open **Settings → Tools → Tombstone** to configure the plugin.

| Setting | Default | Description |
|---------|---------|-------------|
| API URL | `http://localhost:8081` | Base URL of the Tombstone flag-api service |
| API Token | _(empty)_ | Bearer token for API authentication. Stored securely in the IDE's PasswordSafe — never written to disk in plaintext. |
| Environment | `development` | Active environment context (`development`, `staging`, `production`). Controls which flag states are shown in InlayHints and the ToolWindow. |
| Intelligence URL | `http://localhost:8083` | Base URL of the Tombstone intelligence service, used by SearchFlagsAction for NLP queries. |

### Remote / Staging Setup

Point **API URL** and **Intelligence URL** at your remote Tombstone instances and set **API Token** to a read/write token with the `flag:kill` scope for KillSwitchAction to work. A read-only token is sufficient for InlayHints, ToolWindow, and SearchFlagsAction.

## Supported Languages

The plugin recognises `evaluate()` and `isEnabled()` calls in the following languages and injects InlayHints accordingly:

- TypeScript
- JavaScript
- Python
- Go
- Java
- Ruby

Support for additional languages can be added by contributing a PSI visitor for the target language under `src/main/kotlin/com/tombstone/jetbrains/hints/`.

## Keyboard Shortcuts

| Action | Default Shortcut |
|--------|-----------------|
| Kill Flag | Shift+Ctrl+Alt+K |
| Search Flags | _(no default — assign in Keymap settings)_ |
| Refresh Flags | _(no default — assign in Keymap settings)_ |

To reassign shortcuts: **Settings → Keymap → search "Tombstone"**.

## Troubleshooting

**InlayHints not appearing**

- Verify the API URL is reachable: `curl http://localhost:8081/health`
- Check that the API Token is set if your instance requires authentication.
- Run **Tools → Tombstone → Refresh Flags** to force a cache refresh.
- Ensure the file language is in the supported list above.

**KillSwitchAction reports "No flag found at cursor"**

- Place the cursor on the same line as the flag key string inside `evaluate()` or `isEnabled()`.
- Confirm the flag key is a string literal (variable references are not resolved).

**SearchFlagsAction returns no results**

- Verify the Intelligence URL is set and the intelligence service is running.
- The intelligence service requires the flag-api to be reachable to build its search index on startup.

## Versioning

This plugin tracks Tombstone's version. Plugin v2.0.1 is compatible with Tombstone v2.0.1+. See the project [CHANGELOG](../CHANGELOG.md) for release notes.
