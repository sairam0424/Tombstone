import * as vscode from "vscode";
import { listFlags, FlagState } from "./api";

// ---------------------------------------------------------------------------
// Patterns: match flag evaluation calls across common languages.
// Covers single quotes, double quotes, AND template literal backticks.
// Examples matched:
//   evaluate("my-flag")         evaluate('my-flag')      evaluate(`my-flag`)
//   isEnabled("my-flag")        isEnabled('my-flag')     isEnabled(`my-flag`)
//   is_enabled("my-flag")       is_enabled('my-flag')    is_enabled(`my-flag`)
//   getFlag("my-flag")          getFlag('my-flag')       getFlag(`my-flag`)
//   client.isEnabled('x')       flags.evaluate("beta-ui")
// Note: flag keys with dot-notation (a.b.c) are matched by [a-zA-Z0-9._-]+
// ---------------------------------------------------------------------------
export const FLAG_PATTERNS: RegExp[] = [
  /\bevaluate\(\s*['"`]([a-zA-Z0-9._-]+)['"`]/g,
  /\bisEnabled\(\s*['"`]([a-zA-Z0-9._-]+)['"`]/g,
  /\bis_enabled\(\s*['"`]([a-zA-Z0-9._-]+)['"`]/g,
  /\bgetFlag\(\s*['"`]([a-zA-Z0-9._-]+)['"`]/g,
  /\bflagEnabled\(\s*['"`]([a-zA-Z0-9._-]+)['"`]/g,
  /\bcheckFlag\(\s*['"`]([a-zA-Z0-9._-]+)['"`]/g,
];

const CACHE_TTL_MS = 30_000;

// ---------------------------------------------------------------------------
// TombstoneCodeLensProvider
// ---------------------------------------------------------------------------
export class TombstoneCodeLensProvider implements vscode.CodeLensProvider {
  private flagCache: Map<string, FlagState> = new Map();
  private lastFetch = 0;

  private readonly changeEmitter = new vscode.EventEmitter<void>();
  public readonly onDidChangeCodeLenses: vscode.Event<void> =
    this.changeEmitter.event;

  /** Force a cache refresh and re-render all CodeLenses. */
  public refresh(): void {
    this.lastFetch = 0;
    this.changeEmitter.fire();
  }

  // -------------------------------------------------------------------------
  // vscode.CodeLensProvider
  // -------------------------------------------------------------------------
  public async provideCodeLenses(
    document: vscode.TextDocument
  ): Promise<vscode.CodeLens[]> {
    const config = vscode.workspace.getConfiguration("tombstone");
    const enabled = config.get<boolean>("enableCodeLens", true);
    if (!enabled) {
      return [];
    }

    await this.ensureFlagCacheLoaded();

    const text = document.getText();
    const lenses: vscode.CodeLens[] = [];

    for (const pattern of FLAG_PATTERNS) {
      // Reset the lastIndex so the RegExp can be reused across documents.
      pattern.lastIndex = 0;
      let match: RegExpExecArray | null;

      while ((match = pattern.exec(text)) !== null) {
        const flagKey = match[1];
        const matchIndex = match.index;
        const position = document.positionAt(matchIndex);
        const range = new vscode.Range(position, position);

        const flagState = this.flagCache.get(flagKey);
        const lens = this.buildCodeLens(range, flagKey, flagState);
        lenses.push(lens);
      }
    }

    return lenses;
  }

  // -------------------------------------------------------------------------
  // Helpers
  // -------------------------------------------------------------------------

  private buildCodeLens(
    range: vscode.Range,
    flagKey: string,
    state: FlagState | undefined
  ): vscode.CodeLens {
    let title: string;

    if (!state) {
      title = `$(question) Tombstone: ${flagKey} — unknown flag`;
    } else {
      const circle = state.enabled ? "$(circle-filled)" : "$(circle-slash)";
      const pct = `${state.rollout_pct}%`;
      const owner = state.owner_id ? ` · ${state.owner_id}` : "";
      const staleWarning =
        state.rollout_pct >= 100 && state.enabled
          ? " ⚠ fully rolled out — consider cleanup"
          : "";

      title = `${circle} ${flagKey} [${pct}${owner}]${staleWarning}`;
    }

    return new vscode.CodeLens(range, {
      title,
      command: "tombstone.openFlagInDashboard",
      arguments: [flagKey],
      tooltip: state
        ? `State: ${state.state} | Type: ${state.flag_type} | ${state.description || "no description"}`
        : `Flag "${flagKey}" not found in Tombstone`,
    });
  }

  private async ensureFlagCacheLoaded(): Promise<void> {
    const now = Date.now();
    if (now - this.lastFetch < CACHE_TTL_MS) {
      return;
    }

    try {
      const flags = await listFlags();
      this.flagCache.clear();
      for (const flag of flags) {
        this.flagCache.set(flag.key, flag);
      }
      this.lastFetch = Date.now();
    } catch (err) {
      // Don't crash CodeLens on API failures — just show stale/empty data.
      // Keep lastFetch at 0 so the next document open retries.
      console.error("[Tombstone] CodeLens cache refresh failed:", err);
    }
  }

  public dispose(): void {
    this.changeEmitter.dispose();
  }
}
