package io.tombstone

import com.intellij.openapi.application.ApplicationManager
import com.intellij.openapi.project.Project
import com.intellij.openapi.wm.ToolWindow
import com.intellij.openapi.wm.ToolWindowFactory
import com.intellij.ui.components.JBList
import com.intellij.ui.components.JBScrollPane
import com.intellij.ui.content.ContentFactory
import java.awt.BorderLayout
import java.awt.Desktop
import java.net.URI
import javax.swing.DefaultListModel
import javax.swing.JButton
import javax.swing.JComboBox
import javax.swing.JLabel
import javax.swing.JPanel
import javax.swing.JTextField
import javax.swing.ListSelectionModel
import javax.swing.SwingConstants

// ---------------------------------------------------------------------------
// TombstoneToolWindowFactory
//
// Right-side ToolWindow showing all flags sorted:
//   killed → enabled → disabled → alphabetical
//
// Features:
//   - Refresh button
//   - Environment filter (from settings)
//   - Search/filter text field
//   - Double-click opens flag in browser dashboard
// ---------------------------------------------------------------------------

class TombstoneToolWindowFactory : ToolWindowFactory {

    override fun createToolWindowContent(project: Project, toolWindow: ToolWindow) {
        val panel = TombstoneFlagPanel(project)
        val content = ContentFactory.getInstance()
            .createContent(panel, "Flags", false)
        toolWindow.contentManager.addContent(content)

        // Trigger initial load
        panel.refresh()
    }
}

// ---------------------------------------------------------------------------
// TombstoneFlagPanel
// ---------------------------------------------------------------------------

class TombstoneFlagPanel(private val project: Project) : JPanel(BorderLayout()) {

    private val listModel = DefaultListModel<String>()
    private val flagList = JBList(listModel).apply {
        selectionMode = ListSelectionModel.SINGLE_SELECTION
        cellRenderer = FlagListCellRenderer()
    }

    private val filterField = JTextField(20)
    private val statusLabel = JLabel("Loading…", SwingConstants.LEFT)

    private var allFlags: List<FlagState> = emptyList()

    init {
        val topBar = buildTopBar()
        add(topBar, BorderLayout.NORTH)
        add(JBScrollPane(flagList), BorderLayout.CENTER)
        add(statusLabel, BorderLayout.SOUTH)

        // Double-click opens the flag in the dashboard
        flagList.addMouseListener(object : java.awt.event.MouseAdapter() {
            override fun mouseClicked(e: java.awt.event.MouseEvent) {
                if (e.clickCount == 2) {
                    val idx = flagList.selectedIndex
                    if (idx >= 0 && idx < allFlags.size) {
                        openInDashboard(allFlags[idx].key)
                    }
                }
            }
        })

        // Filter on keypress
        filterField.document.addDocumentListener(object : javax.swing.event.DocumentListener {
            override fun changedUpdate(e: javax.swing.event.DocumentEvent) = applyFilter()
            override fun insertUpdate(e: javax.swing.event.DocumentEvent) = applyFilter()
            override fun removeUpdate(e: javax.swing.event.DocumentEvent) = applyFilter()
        })
    }

    private fun buildTopBar(): JPanel {
        val bar = JPanel()
        val refreshBtn = JButton("Refresh").apply {
            addActionListener { refresh() }
        }
        bar.add(JLabel("Filter:"))
        bar.add(filterField)
        bar.add(refreshBtn)
        return bar
    }

    fun refresh() {
        statusLabel.text = "Loading…"
        val cache = FlagCacheHolder.get(project)
        cache.invalidate()
        cache.ensureLoaded { flags ->
            ApplicationManager.getApplication().invokeLater {
                allFlags = sortFlags(flags.values.toList())
                applyFilter()
                statusLabel.text = "${allFlags.size} flag(s) loaded"
            }
        }
    }

    private fun applyFilter() {
        val query = filterField.text.trim().lowercase()
        val filtered = if (query.isEmpty()) allFlags
        else allFlags.filter { f ->
            f.key.lowercase().contains(query) ||
                f.name.lowercase().contains(query) ||
                f.owner_id.lowercase().contains(query)
        }

        listModel.clear()
        for (flag in filtered) {
            val icon = when {
                flag.state == "killed" -> "✖"
                flag.enabled -> "●"
                else -> "○"
            }
            val pct = "${flag.rollout_pct}%"
            listModel.addElement("$icon ${flag.key}  [$pct]  ${flag.name}")
        }
        // Keep allFlags in filtered order for double-click lookup
        allFlags = filtered
    }

    private fun sortFlags(flags: List<FlagState>): List<FlagState> =
        flags.sortedWith(compareBy(
            { if (it.state == "killed") 0 else 1 },
            { if (it.enabled) 0 else 1 },
            { it.name.ifBlank { it.key } }
        ))

    private fun openInDashboard(flagKey: String) {
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

// ---------------------------------------------------------------------------
// FlagListCellRenderer — colour-code killed flags red
// ---------------------------------------------------------------------------

private class FlagListCellRenderer : javax.swing.DefaultListCellRenderer() {
    override fun getListCellRendererComponent(
        list: javax.swing.JList<*>?,
        value: Any?,
        index: Int,
        isSelected: Boolean,
        cellHasFocus: Boolean
    ): java.awt.Component {
        val comp = super.getListCellRendererComponent(list, value, index, isSelected, cellHasFocus)
        val label = value?.toString() ?: ""
        if (!isSelected && label.startsWith("✖")) {
            comp.foreground = java.awt.Color(220, 50, 50)
        }
        return comp
    }
}
