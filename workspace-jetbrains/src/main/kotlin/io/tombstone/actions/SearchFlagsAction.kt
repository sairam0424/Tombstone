package io.tombstone.actions

import com.intellij.openapi.actionSystem.AnAction
import com.intellij.openapi.actionSystem.AnActionEvent
import com.intellij.openapi.application.ApplicationManager
import com.intellij.openapi.project.Project
import com.intellij.openapi.ui.InputValidator
import com.intellij.openapi.ui.Messages
import io.tombstone.NlpSearchResult
import io.tombstone.TombstoneClientHolder
import io.tombstone.TombstoneConfig
import java.awt.Desktop
import java.net.URI

// ---------------------------------------------------------------------------
// SearchFlagsAction
//
// Shows an NLP search input, calls GET /api/v1/search?q=... on a pooled
// thread, then presents results as a chooser dialog.  Selecting a result
// opens the flag in the browser dashboard.
// ---------------------------------------------------------------------------

class SearchFlagsAction : AnAction("Tombstone: Search Flags") {

    override fun actionPerformed(event: AnActionEvent) {
        val project = event.project ?: return

        val query = Messages.showInputDialog(
            project,
            "Describe the flag you are looking for (natural language):",
            "Tombstone: Search Flags",
            Messages.getQuestionIcon(),
            "",
            object : InputValidator {
                override fun checkInput(value: String) = value.trim().isNotEmpty()
                override fun canClose(value: String) = value.trim().isNotEmpty()
            }
        )?.trim() ?: return

        ApplicationManager.getApplication().executeOnPooledThread {
            val results: List<NlpSearchResult>
            try {
                val client = TombstoneClientHolder.get(project)
                results = client.searchFlagsNlp(query)
            } catch (e: Exception) {
                ApplicationManager.getApplication().invokeLater {
                    Messages.showErrorDialog(project, "Search failed: ${e.message}", "Tombstone")
                }
                return@executeOnPooledThread
            }

            ApplicationManager.getApplication().invokeLater {
                if (results.isEmpty()) {
                    Messages.showInfoMessage(
                        project,
                        "No flags matched \"$query\".",
                        "Tombstone Search"
                    )
                    return@invokeLater
                }

                val items = results.map { r ->
                    "${r.flag_key} — ${r.name}  (score: ${"%.2f".format(r.score)})"
                }.toTypedArray()

                val choice = Messages.showEditableChooseDialog(
                    "Found ${results.size} result(s) for \"$query\".\nSelect a flag to open in the dashboard:",
                    "Tombstone Search Results",
                    Messages.getInformationIcon(),
                    items,
                    items.firstOrNull(),
                    null
                ) ?: return@invokeLater

                val idx = items.indexOf(choice)
                if (idx >= 0) {
                    openInDashboard(project, results[idx].flag_key)
                }
            }
        }
    }

    // -------------------------------------------------------------------------
    // Helpers
    // -------------------------------------------------------------------------

    private fun openInDashboard(project: Project, flagKey: String) {
        val config = TombstoneConfig.load(project)
        val dashboardBase = config.apiUrl
            .replace(Regex(":8081$"), ":3000")
            .replace(Regex("/api.*$"), "")
        val uri = URI("$dashboardBase/flags/${java.net.URLEncoder.encode(flagKey, "UTF-8")}")
        if (Desktop.isDesktopSupported()) {
            Desktop.getDesktop().browse(uri)
        }
    }
}
