import * as vscode from "vscode";
import { listFlags, FlagState } from "./api";

// ---------------------------------------------------------------------------
// FlagTreeItem
// ---------------------------------------------------------------------------
export class FlagTreeItem extends vscode.TreeItem {
  public readonly flagKey: string;

  constructor(flag: FlagState) {
    super(flag.name || flag.key, vscode.TreeItemCollapsibleState.None);

    this.flagKey = flag.key;
    this.tooltip = [
      `Key: ${flag.key}`,
      `Type: ${flag.flag_type}`,
      `State: ${flag.state}`,
      flag.description ? `Description: ${flag.description}` : null,
      `Owner: ${flag.owner_id || "unassigned"}`,
    ]
      .filter(Boolean)
      .join("\n");

    // Description line shown in the tree below the label
    const statusLabel = flag.enabled ? "enabled" : "disabled";
    this.description = `${statusLabel} · ${flag.rollout_pct}%`;

    // Icon: green check when enabled, circle-slash when disabled or killed
    if (flag.state === "killed") {
      this.iconPath = new vscode.ThemeIcon(
        "circle-slash",
        new vscode.ThemeColor("errorForeground")
      );
    } else if (flag.enabled) {
      this.iconPath = new vscode.ThemeIcon(
        "check",
        new vscode.ThemeColor("testing.iconPassed")
      );
    } else {
      this.iconPath = new vscode.ThemeIcon(
        "circle-slash",
        new vscode.ThemeColor("disabledForeground")
      );
    }

    // Clicking the item opens the flag in the dashboard
    this.command = {
      command: "tombstone.openFlagInDashboard",
      title: "Open in Dashboard",
      arguments: [flag.key],
    };

    // Context value enables view/item/context menu contributions
    this.contextValue = flag.state === "killed" ? "flagKilled" : "flag";
  }
}

// ---------------------------------------------------------------------------
// TombstoneTreeProvider
// ---------------------------------------------------------------------------
export class TombstoneTreeProvider
  implements vscode.TreeDataProvider<FlagTreeItem>
{
  private flags: FlagState[] = [];
  private isLoading = false;

  private readonly changeEmitter =
    new vscode.EventEmitter<FlagTreeItem | undefined | null | void>();
  public readonly onDidChangeTreeData: vscode.Event<
    FlagTreeItem | undefined | null | void
  > = this.changeEmitter.event;

  /** Re-fetch flags from the API and re-render the tree. */
  public async refresh(): Promise<void> {
    if (this.isLoading) {
      return;
    }
    this.isLoading = true;

    try {
      this.flags = await listFlags();
    } catch (err) {
      // Surface the error as a VS Code notification; keep existing tree data.
      void vscode.window.showErrorMessage(
        `Tombstone: Failed to refresh flags — ${(err as Error).message}`
      );
    } finally {
      this.isLoading = false;
      this.changeEmitter.fire();
    }
  }

  // -------------------------------------------------------------------------
  // vscode.TreeDataProvider
  // -------------------------------------------------------------------------
  public getTreeItem(element: FlagTreeItem): vscode.TreeItem {
    return element;
  }

  public getChildren(_element?: FlagTreeItem): FlagTreeItem[] {
    // Flat list — no nesting for now
    if (_element) {
      return [];
    }

    if (this.isLoading) {
      const loading = new vscode.TreeItem(
        "Loading flags…",
        vscode.TreeItemCollapsibleState.None
      );
      loading.iconPath = new vscode.ThemeIcon("loading~spin");
      return [];
    }

    if (this.flags.length === 0) {
      return [];
    }

    // Sort: killed first (attention needed), then by enabled state, then name
    const sorted = [...this.flags].sort((a, b) => {
      if (a.state === "killed" && b.state !== "killed") return -1;
      if (b.state === "killed" && a.state !== "killed") return 1;
      if (a.enabled !== b.enabled) return a.enabled ? -1 : 1;
      return (a.name || a.key).localeCompare(b.name || b.key);
    });

    return sorted.map((f) => new FlagTreeItem(f));
  }

  public dispose(): void {
    this.changeEmitter.dispose();
  }
}
