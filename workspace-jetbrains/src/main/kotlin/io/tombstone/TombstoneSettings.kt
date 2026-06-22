package io.tombstone

import com.intellij.openapi.options.Configurable
import com.intellij.openapi.project.Project
import com.intellij.util.ui.FormBuilder
import java.awt.BorderLayout
import javax.swing.JComponent
import javax.swing.JLabel
import javax.swing.JPanel
import javax.swing.JPasswordField
import javax.swing.JTextField

// ---------------------------------------------------------------------------
// TombstoneConfig — persisted per-project (non-secret) configuration.
// Token stored separately in PasswordSafe via TombstoneApiClient helpers.
// ---------------------------------------------------------------------------

data class TombstoneConfig(
    val apiUrl: String = "http://localhost:8081",
    val environment: String = "production",
    val intelligenceUrl: String = "http://localhost:8083"
) {
    companion object {
        private const val PREFS_API_URL = "tombstone.apiUrl"
        private const val PREFS_ENV = "tombstone.environment"
        private const val PREFS_INTEL_URL = "tombstone.intelligenceUrl"

        fun load(project: Project): TombstoneConfig {
            val prefs = java.util.prefs.Preferences.userRoot().node("io/tombstone/${project.name}")
            return TombstoneConfig(
                apiUrl = prefs.get(PREFS_API_URL, "http://localhost:8081"),
                environment = prefs.get(PREFS_ENV, "production"),
                intelligenceUrl = prefs.get(PREFS_INTEL_URL, "http://localhost:8083")
            )
        }

        fun save(project: Project, config: TombstoneConfig) {
            val prefs = java.util.prefs.Preferences.userRoot().node("io/tombstone/${project.name}")
            prefs.put(PREFS_API_URL, config.apiUrl)
            prefs.put(PREFS_ENV, config.environment)
            prefs.put(PREFS_INTEL_URL, config.intelligenceUrl)
        }
    }
}

// ---------------------------------------------------------------------------
// TombstoneSettings — Configurable (Settings > Tools > Tombstone)
// ---------------------------------------------------------------------------

class TombstoneSettings(private val project: Project) : Configurable {

    private var panel: JPanel? = null
    private val apiUrlField = JTextField(40)
    private val environmentField = JTextField(20)
    private val intelligenceUrlField = JTextField(40)

    // Token field — stored via PasswordSafe, never in plain JTextField
    private val tokenField = JPasswordField(40)

    override fun getDisplayName(): String = "Tombstone"

    override fun createComponent(): JComponent {
        val config = TombstoneConfig.load(project)
        apiUrlField.text = config.apiUrl
        environmentField.text = config.environment
        intelligenceUrlField.text = config.intelligenceUrl
        val stored = loadApiToken()
        tokenField.text = stored

        val form = FormBuilder.createFormBuilder()
            .addLabeledComponent(JLabel("API URL (flag-api):"), apiUrlField)
            .addLabeledComponent(JLabel("Environment:"), environmentField)
            .addLabeledComponent(JLabel("Intelligence URL:"), intelligenceUrlField)
            .addLabeledComponent(JLabel("API Token (PasswordSafe):"), tokenField)
            .addComponentFillVertically(JPanel(), 0)
            .panel

        panel = JPanel(BorderLayout()).also { it.add(form, BorderLayout.NORTH) }
        return panel!!
    }

    override fun isModified(): Boolean {
        val config = TombstoneConfig.load(project)
        val storedToken = loadApiToken()
        val enteredToken = String(tokenField.password)
        return apiUrlField.text != config.apiUrl ||
            environmentField.text != config.environment ||
            intelligenceUrlField.text != config.intelligenceUrl ||
            enteredToken != storedToken
    }

    override fun apply() {
        val config = TombstoneConfig(
            apiUrl = apiUrlField.text.trimEnd('/'),
            environment = environmentField.text.trim(),
            intelligenceUrl = intelligenceUrlField.text.trimEnd('/')
        )
        TombstoneConfig.save(project, config)
        saveApiToken(String(tokenField.password))
        // Invalidate the shared client so it is recreated with new settings
        TombstoneClientHolder.invalidate(project)
    }

    override fun reset() {
        val config = TombstoneConfig.load(project)
        apiUrlField.text = config.apiUrl
        environmentField.text = config.environment
        intelligenceUrlField.text = config.intelligenceUrl
        tokenField.text = loadApiToken()
    }
}

// ---------------------------------------------------------------------------
// TombstoneClientHolder — project-scoped singleton for the API client.
// Recreated lazily after settings change.
// ---------------------------------------------------------------------------

object TombstoneClientHolder {
    private val clients: MutableMap<String, TombstoneApiClient> = mutableMapOf()

    fun get(project: Project): TombstoneApiClient {
        val key = project.name
        return clients.getOrPut(key) { buildClient(project) }
    }

    fun invalidate(project: Project) {
        clients.remove(project.name)
    }

    private fun buildClient(project: Project): TombstoneApiClient {
        val config = TombstoneConfig.load(project)
        return TombstoneApiClient(config.apiUrl, config.intelligenceUrl, loadApiToken())
    }
}
