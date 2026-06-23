package io.tombstone.actions

import com.intellij.notification.NotificationGroupManager
import com.intellij.notification.NotificationType
import com.intellij.openapi.actionSystem.AnAction
import com.intellij.openapi.actionSystem.AnActionEvent
import com.intellij.openapi.application.ApplicationManager
import com.intellij.openapi.project.Project
import com.intellij.openapi.ui.InputValidator
import com.intellij.openapi.ui.Messages
import io.tombstone.FlagCacheHolder
import io.tombstone.TombstoneClientHolder

// ---------------------------------------------------------------------------
// KillSwitchAction
//
// Shows two dialogs:
//   1. Flag key input
//   2. Reason input (minimum 10 characters enforced)
//   3. Confirmation warning
// Then calls POST /api/v1/flags/{key}/kill on a pooled thread and notifies.
// ---------------------------------------------------------------------------

class KillSwitchAction : AnAction("Tombstone: Kill Switch") {

    override fun actionPerformed(event: AnActionEvent) {
        val project = event.project ?: return

        // Step 1: ask for the flag key
        val flagKey = Messages.showInputDialog(
            project,
            "Enter the flag key to disable immediately:",
            "Tombstone Kill Switch",
            Messages.getWarningIcon(),
            "",
            object : InputValidator {
                override fun checkInput(value: String) = value.trim().isNotEmpty()
                override fun canClose(value: String) = value.trim().isNotEmpty()
            }
        )?.trim() ?: return

        // Step 2: ask for the reason (min 10 chars)
        val reason = Messages.showInputDialog(
            project,
            "Reason for disabling \"$flagKey\" (minimum 10 characters):",
            "Kill Switch — $flagKey",
            Messages.getWarningIcon(),
            "",
            object : InputValidator {
                override fun checkInput(value: String) = value.trim().length >= 10
                override fun canClose(value: String) = value.trim().length >= 10
            }
        )?.trim() ?: return

        // Step 3: final confirmation
        val confirm = Messages.showOkCancelDialog(
            project,
            "Kill switch will immediately disable \"$flagKey\" in the configured environment. This cannot be undone without a manual re-enable.\n\nReason: $reason\n\nContinue?",
            "Confirm Kill Switch",
            "Kill Flag",
            "Cancel",
            Messages.getWarningIcon()
        )
        if (confirm != Messages.OK) return

        // Step 4: execute on background thread
        ApplicationManager.getApplication().executeOnPooledThread {
            try {
                val client = TombstoneClientHolder.get(project)
                client.killSwitch(flagKey, reason)
                // Invalidate cache so the next render shows updated state
                FlagCacheHolder.invalidate(project)
                notifySuccess(project, "Flag \"$flagKey\" has been killed.")
            } catch (e: Exception) {
                notifyError(project, "Kill switch failed for \"$flagKey\": ${e.message}")
            }
        }
    }

    // -------------------------------------------------------------------------
    // Helpers
    // -------------------------------------------------------------------------

    private fun notifySuccess(project: Project, message: String) {
        ApplicationManager.getApplication().invokeLater {
            NotificationGroupManager.getInstance()
                .getNotificationGroup("Tombstone")
                .createNotification(message, NotificationType.INFORMATION)
                .notify(project)
        }
    }

    private fun notifyError(project: Project, message: String) {
        ApplicationManager.getApplication().invokeLater {
            NotificationGroupManager.getInstance()
                .getNotificationGroup("Tombstone")
                .createNotification(message, NotificationType.ERROR)
                .notify(project)
        }
    }
}
