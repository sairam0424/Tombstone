package io.tombstone

import com.intellij.codeInsight.hints.ChangeListener
import com.intellij.codeInsight.hints.ImmediateConfigurable
import com.intellij.codeInsight.hints.InlayHintsCollector
import com.intellij.codeInsight.hints.InlayHintsProvider
import com.intellij.codeInsight.hints.InlayHintsSink
import com.intellij.codeInsight.hints.SettingsKey
import com.intellij.openapi.editor.Editor
import com.intellij.openapi.project.Project
import com.intellij.psi.PsiFile
import javax.swing.JComponent
import javax.swing.JPanel

// ---------------------------------------------------------------------------
// TombstoneInlayProvider
//
// Scans every open file for the 6 flag-evaluation patterns (via FlagPatterns),
// fetches flag state from the 30-second cache, and renders an inlay hint next
// to each call site:
//
//   ● enabled 75% | payments-team
//   ○ disabled
//   ? unknown flag
//   ✖ KILLED
// ---------------------------------------------------------------------------

class TombstoneInlayProvider : InlayHintsProvider<TombstoneInlayProvider.Settings> {

    data class Settings(val enabled: Boolean = true)

    override val key: SettingsKey<Settings> = SettingsKey("tombstone.inlay")
    override val name: String = "Tombstone flag state"
    override val previewText: String = "evaluate(\"my-flag\")"

    override fun createSettings(): Settings = Settings()

    override fun createConfigurable(settings: Settings): ImmediateConfigurable =
        object : ImmediateConfigurable {
            override fun createComponent(listener: ChangeListener): JComponent = JPanel()
        }

    override fun getCollectorFor(
        file: PsiFile,
        editor: Editor,
        settings: Settings,
        sink: InlayHintsSink
    ): InlayHintsCollector? {
        if (!settings.enabled) return null
        val project: Project = file.project
        return TombstoneInlayCollector(editor, project, sink)
    }
}

// ---------------------------------------------------------------------------
// TombstoneInlayCollector — executes per-file scan
// ---------------------------------------------------------------------------

private class TombstoneInlayCollector(
    private val editor: Editor,
    private val project: Project,
    private val sink: InlayHintsSink
) : InlayHintsCollector {

    override fun collect(element: com.intellij.psi.PsiElement, editor: Editor, sink: InlayHintsSink): Boolean {
        // Only process the file root to avoid per-element re-scanning
        if (element.parent != null) return true

        val text = editor.document.text
        val cache = FlagCacheHolder.get(project)
        val flagsSnapshot = cache.snapshot()

        // Kick off a background refresh (non-blocking)
        cache.ensureLoaded()

        val occurrences = FlagPatterns.findAll(text)
        for ((flagKey, offset) in occurrences) {
            val state: FlagState? = flagsSnapshot[flagKey]
            val hint = buildHintText(flagKey, state)
            val offset1 = minOf(offset + 1, editor.document.textLength)
            sink.addInlineElement(
                offset = offset1,
                relatesToPrecedingText = true,
                presentation = com.intellij.codeInsight.hints.presentation.PresentationFactory(editor)
                    .smallText(hint),
                placeAtEnd = false
            )
        }
        return false
    }

    private fun buildHintText(flagKey: String, state: FlagState?): String {
        if (state == null) return "  ? $flagKey unknown"
        return when {
            state.state == "killed" -> "  ✖ KILLED"
            state.enabled -> {
                val pct = "${state.rollout_pct}%"
                val owner = if (state.owner_id.isNotBlank()) " | ${state.owner_id}" else ""
                val stale = if (state.rollout_pct >= 100) " ⚠ stale?" else ""
                "  ● enabled $pct$owner$stale"
            }
            else -> "  ○ disabled"
        }
    }
}
