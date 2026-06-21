import * as vscode from "vscode";
import { TombstoneCodeLensProvider } from "./codelens";
import { TombstoneTreeProvider } from "./treeview";
import {
  getConfig,
  killSwitch,
  searchFlagsNlp,
  listStaleFlags,
  generateCleanupPR,
} from "./api";

// Languages in which CodeLens annotations are shown
const CODELENS_LANGUAGES = [
  "typescript",
  "javascript",
  "typescriptreact",
  "javascriptreact",
  "python",
  "go",
  "java",
  "ruby",
];

// ---------------------------------------------------------------------------
// activate
// ---------------------------------------------------------------------------
export function activate(context: vscode.ExtensionContext): void {
  const codeLensProvider = new TombstoneCodeLensProvider();
  const treeProvider = new TombstoneTreeProvider();

  // -------------------------------------------------------------------------
  // CodeLens — register for all target languages
  // -------------------------------------------------------------------------
  for (const lang of CODELENS_LANGUAGES) {
    context.subscriptions.push(
      vscode.languages.registerCodeLensProvider(
        { language: lang },
        codeLensProvider
      )
    );
  }

  // -------------------------------------------------------------------------
  // Sidebar tree view
  // -------------------------------------------------------------------------
  const treeView = vscode.window.createTreeView("tombstone.flagList", {
    treeDataProvider: treeProvider,
    showCollapseAll: false,
  });
  context.subscriptions.push(treeView);

  // Trigger initial load
  void treeProvider.refresh();

  // -------------------------------------------------------------------------
  // Command: refreshFlags
  // -------------------------------------------------------------------------
  context.subscriptions.push(
    vscode.commands.registerCommand("tombstone.refreshFlags", async () => {
      codeLensProvider.refresh();
      await treeProvider.refresh();
      void vscode.window.showInformationMessage("Tombstone: Flags refreshed.");
    })
  );

  // -------------------------------------------------------------------------
  // Command: killSwitch
  // -------------------------------------------------------------------------
  context.subscriptions.push(
    vscode.commands.registerCommand("tombstone.killSwitch", async () => {
      const flagKey = await vscode.window.showInputBox({
        title: "Tombstone Kill Switch",
        prompt: "Enter the flag key to disable immediately",
        placeHolder: "e.g. new-checkout-flow",
        validateInput: (v) => (v.trim() ? null : "Flag key cannot be empty"),
      });

      if (!flagKey) {
        return;
      }

      const reason = await vscode.window.showInputBox({
        title: `Kill Switch — ${flagKey}`,
        prompt: "Reason for disabling (minimum 10 characters)",
        placeHolder: "e.g. Causing payment failures in production",
        validateInput: (v) =>
          v.trim().length >= 10
            ? null
            : `Reason must be at least 10 characters (current: ${v.trim().length})`,
      });

      if (!reason) {
        return;
      }

      const confirm = await vscode.window.showWarningMessage(
        `Kill switch will immediately disable "${flagKey}" in ${getConfig().environment}. Continue?`,
        { modal: true },
        "Kill Flag"
      );

      if (confirm !== "Kill Flag") {
        return;
      }

      try {
        await killSwitch(flagKey.trim(), reason.trim());
        codeLensProvider.refresh();
        await treeProvider.refresh();
        void vscode.window.showInformationMessage(
          `Tombstone: Flag "${flagKey}" has been killed.`
        );
      } catch (err) {
        void vscode.window.showErrorMessage(
          `Tombstone Kill Switch failed: ${(err as Error).message}`
        );
      }
    })
  );

  // -------------------------------------------------------------------------
  // Command: searchFlags (NLP)
  // -------------------------------------------------------------------------
  context.subscriptions.push(
    vscode.commands.registerCommand("tombstone.searchFlags", async () => {
      const query = await vscode.window.showInputBox({
        title: "Tombstone: Search Flags",
        prompt: "Describe the flag you are looking for (natural language)",
        placeHolder: "e.g. flags related to payment checkout rollout",
        validateInput: (v) =>
          v.trim() ? null : "Search query cannot be empty",
      });

      if (!query) {
        return;
      }

      let results;
      try {
        results = await searchFlagsNlp(query.trim());
      } catch (err) {
        void vscode.window.showErrorMessage(
          `Tombstone search failed: ${(err as Error).message}`
        );
        return;
      }

      if (results.length === 0) {
        void vscode.window.showInformationMessage(
          `Tombstone: No flags matched "${query}".`
        );
        return;
      }

      const items: vscode.QuickPickItem[] = results.map((r) => ({
        label: r.flag_key,
        description: r.name,
        detail: r.snippet,
      }));

      const selected = await vscode.window.showQuickPick(items, {
        title: `Tombstone Search — ${results.length} result(s)`,
        placeHolder: "Select a flag to open in the dashboard",
        matchOnDescription: true,
        matchOnDetail: true,
      });

      if (selected) {
        await vscode.commands.executeCommand(
          "tombstone.openFlagInDashboard",
          selected.label
        );
      }
    })
  );

  // -------------------------------------------------------------------------
  // Command: openFlagInDashboard
  // -------------------------------------------------------------------------
  context.subscriptions.push(
    vscode.commands.registerCommand(
      "tombstone.openFlagInDashboard",
      async (flagKey?: string) => {
        const key =
          flagKey ??
          (await vscode.window.showInputBox({
            title: "Tombstone: Open Flag in Dashboard",
            prompt: "Enter the flag key",
            placeHolder: "e.g. new-checkout-flow",
          }));

        if (!key) {
          return;
        }

        // Derive dashboard URL from the API URL (replace port 8081 → 3000)
        const { apiUrl } = getConfig();
        const dashboardBase = apiUrl
          .replace(/:8081$/, ":3000")
          .replace(/\/api.*$/, "");

        const uri = vscode.Uri.parse(`${dashboardBase}/flags/${encodeURIComponent(key)}`);
        await vscode.env.openExternal(uri);
      }
    )
  );

  // -------------------------------------------------------------------------
  // Command: generateCleanupPR
  // -------------------------------------------------------------------------
  context.subscriptions.push(
    vscode.commands.registerCommand("tombstone.generateCleanupPR", async () => {
      let staleFlags;
      try {
        staleFlags = await listStaleFlags();
      } catch (err) {
        void vscode.window.showErrorMessage(
          `Tombstone: Failed to load stale flags — ${(err as Error).message}`
        );
        return;
      }

      if (staleFlags.length === 0) {
        void vscode.window.showInformationMessage(
          "Tombstone: No stale flags detected. Everything looks clean!"
        );
        return;
      }

      const items: vscode.QuickPickItem[] = staleFlags.map((f) => ({
        label: f.flag_key,
        description: `${f.days_at_100_pct} days at 100% · score ${f.stale_score}`,
        detail: `Recommended action: ${f.recommended_action}`,
      }));

      const selected = await vscode.window.showQuickPick(items, {
        title: "Tombstone: Generate Cleanup PR",
        placeHolder: "Select a stale flag to generate a cleanup PR for",
        matchOnDescription: true,
      });

      if (!selected) {
        return;
      }

      void vscode.window.showInformationMessage(
        `Tombstone: Generating cleanup PR for "${selected.label}"…`
      );

      let prResult;
      try {
        prResult = await generateCleanupPR(selected.label);
      } catch (err) {
        void vscode.window.showErrorMessage(
          `Tombstone: Cleanup PR generation failed — ${(err as Error).message}`
        );
        return;
      }

      // Show the PR body as a new untitled markdown document
      const markdown = [
        `# ${prResult.pr_title}`,
        "",
        `**Branch:** \`${prResult.branch_name}\``,
        "",
        "---",
        "",
        prResult.pr_body,
      ].join("\n");

      const doc = await vscode.workspace.openTextDocument({
        language: "markdown",
        content: markdown,
      });

      await vscode.window.showTextDocument(doc, { preview: true });

      void vscode.window.showInformationMessage(
        `Tombstone: Cleanup PR draft ready — branch: ${prResult.branch_name}`
      );
    })
  );

  // -------------------------------------------------------------------------
  // Config change listener — refresh on apiUrl / apiToken / environment change
  // -------------------------------------------------------------------------
  context.subscriptions.push(
    vscode.workspace.onDidChangeConfiguration((event) => {
      if (
        event.affectsConfiguration("tombstone.apiUrl") ||
        event.affectsConfiguration("tombstone.apiToken") ||
        event.affectsConfiguration("tombstone.environment") ||
        event.affectsConfiguration("tombstone.intelligenceApiUrl")
      ) {
        codeLensProvider.refresh();
        void treeProvider.refresh();
      }
    })
  );

  // -------------------------------------------------------------------------
  // Status bar item
  // -------------------------------------------------------------------------
  const statusBar = vscode.window.createStatusBarItem(
    vscode.StatusBarAlignment.Right,
    100
  );
  statusBar.command = "tombstone.refreshFlags";
  statusBar.tooltip = "Tombstone — click to refresh flags";

  const { apiUrl } = getConfig();
  statusBar.text = `$(symbol-boolean) Tombstone: ${apiUrl}`;
  statusBar.show();
  context.subscriptions.push(statusBar);

  // Dispose providers when the extension is deactivated
  context.subscriptions.push(codeLensProvider);
  context.subscriptions.push(treeProvider);
}

// ---------------------------------------------------------------------------
// deactivate
// ---------------------------------------------------------------------------
export function deactivate(): void {
  // VS Code disposes all context.subscriptions automatically.
}
