package io.tombstone.actions

import com.intellij.notification.NotificationGroupManager
import com.intellij.notification.NotificationType
import com.intellij.openapi.actionSystem.AnAction
import com.intellij.openapi.actionSystem.AnActionEvent
import com.intellij.openapi.application.ApplicationManager
import io.tombstone.FlagCacheHolder

// ---------------------------------------------------------------------------
// RefreshFlagsAction — force reload flag state from the API.
// Invalidates the flag cache so the next inlay-hint render and ToolWindow
// both pick up fresh data.
// ---------------------------------------------------------------------------

class RefreshFlagsAction : AnAction("Tombstone: Refresh Flags") {

    override fun actionPerformed(event: AnActionEvent) {
        val project = event.project ?: return
        FlagCacheHolder.invalidate(project)
        NotificationGroupManager.getInstance()
            .getNotificationGroup("Tombstone")
            .createNotification("Tombstone: Flags refreshed.", NotificationType.INFORMATION)
            .notify(project)
    }
}
