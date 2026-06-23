package io.tombstone

import com.google.gson.Gson
import com.google.gson.reflect.TypeToken
import com.intellij.credentialStore.CredentialAttributes
import com.intellij.credentialStore.generateServiceName
import com.intellij.ide.passwordSafe.PasswordSafe
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import java.io.IOException
import java.util.concurrent.TimeUnit

// ---------------------------------------------------------------------------
// Domain types — mirror of VS Code api.ts
// ---------------------------------------------------------------------------

data class FlagState(
    val id: String = "",
    val key: String = "",
    val name: String = "",
    val description: String = "",
    val state: String = "inactive",   // "active" | "inactive" | "killed" | "archived"
    val owner_id: String = "",
    val enabled: Boolean = false,
    val rollout_pct: Int = 0,
    val flag_type: String = "release" // "release" | "experiment" | "kill_switch" | "operational"
)

data class StaleFlag(
    val flag_key: String = "",
    val days_at_100_pct: Int = 0,
    val stale_score: Double = 0.0,
    val recommended_action: String = "review" // "remove" | "archive" | "review"
)

data class NlpSearchResult(
    val flag_key: String = "",
    val name: String = "",
    val score: Double = 0.0,
    val snippet: String = ""
)

data class CleanupPRResult(
    val pr_title: String = "",
    val pr_body: String = "",
    val branch_name: String = ""
)

// ---------------------------------------------------------------------------
// Credential key for PasswordSafe
// ---------------------------------------------------------------------------

private const val CREDENTIAL_SERVICE = "io.tombstone.flags"
private const val CREDENTIAL_KEY = "apiToken"

fun loadApiToken(): String {
    val attrs = CredentialAttributes(generateServiceName(CREDENTIAL_SERVICE, CREDENTIAL_KEY))
    return PasswordSafe.instance.getPassword(attrs) ?: ""
}

fun saveApiToken(token: String) {
    val attrs = CredentialAttributes(generateServiceName(CREDENTIAL_SERVICE, CREDENTIAL_KEY))
    PasswordSafe.instance.setPassword(attrs, token)
}

// ---------------------------------------------------------------------------
// TombstoneApiClient
// ---------------------------------------------------------------------------

class TombstoneApiClient(
    private val apiUrl: String,
    private val intelligenceUrl: String,
    apiToken: String
) {
    private val gson = Gson()
    private val json = "application/json; charset=utf-8".toMediaType()

    private val http = OkHttpClient.Builder()
        .connectTimeout(10, TimeUnit.SECONDS)
        .readTimeout(30, TimeUnit.SECONDS)
        .build()

    private val authHeader: String? = apiToken.ifBlank { null }?.let { "Bearer $it" }

    // -------------------------------------------------------------------------
    // Core request helper
    // -------------------------------------------------------------------------

    @Throws(IOException::class)
    private fun get(baseUrl: String, path: String): String {
        val req = Request.Builder()
            .url("$baseUrl$path")
            .apply { authHeader?.let { header("Authorization", it) } }
            .build()

        http.newCall(req).execute().use { resp ->
            if (!resp.isSuccessful) {
                val body = resp.body?.string() ?: "(no body)"
                throw IOException("Tombstone API ${resp.code} ${resp.message} — $baseUrl$path: $body")
            }
            return resp.body?.string() ?: ""
        }
    }

    @Throws(IOException::class)
    private fun post(baseUrl: String, path: String, payload: Any): String {
        val body = gson.toJson(payload).toRequestBody(json)
        val req = Request.Builder()
            .url("$baseUrl$path")
            .post(body)
            .apply { authHeader?.let { header("Authorization", it) } }
            .build()

        http.newCall(req).execute().use { resp ->
            if (!resp.isSuccessful) {
                val bodyText = resp.body?.string() ?: "(no body)"
                throw IOException("Tombstone API ${resp.code} ${resp.message} — $baseUrl$path: $bodyText")
            }
            return resp.body?.string() ?: ""
        }
    }

    // -------------------------------------------------------------------------
    // Flag-API endpoints (flag-api, default :8081)
    // -------------------------------------------------------------------------

    fun listFlags(): List<FlagState> {
        val raw = get(apiUrl, "/api/v1/flags")
        // Support both array and {flags:[]} envelope
        return try {
            val type = object : TypeToken<List<FlagState>>() {}.type
            gson.fromJson(raw, type)
        } catch (_: Exception) {
            data class Envelope(val flags: List<FlagState>?)
            gson.fromJson(raw, Envelope::class.java).flags ?: emptyList()
        }
    }

    fun getFlag(key: String): FlagState {
        val raw = get(apiUrl, "/api/v1/flags/${encodeComponent(key)}")
        return gson.fromJson(raw, FlagState::class.java)
    }

    fun killSwitch(flagKey: String, reason: String) {
        post(apiUrl, "/api/v1/flags/${encodeComponent(flagKey)}/kill", mapOf("reason" to reason))
    }

    // -------------------------------------------------------------------------
    // Intelligence service endpoints (intelligence, default :8082)
    // -------------------------------------------------------------------------

    fun listStaleFlags(): List<StaleFlag> {
        val raw = get(intelligenceUrl, "/api/v1/stale")
        return try {
            val type = object : TypeToken<List<StaleFlag>>() {}.type
            gson.fromJson(raw, type)
        } catch (_: Exception) {
            data class Envelope(val stale_flags: List<StaleFlag>?)
            gson.fromJson(raw, Envelope::class.java).stale_flags ?: emptyList()
        }
    }

    fun searchFlagsNlp(query: String): List<NlpSearchResult> {
        val raw = get(intelligenceUrl, "/api/v1/search?q=${encodeComponent(query)}")
        return try {
            val type = object : TypeToken<List<NlpSearchResult>>() {}.type
            gson.fromJson(raw, type)
        } catch (_: Exception) {
            data class Envelope(val results: List<NlpSearchResult>?)
            gson.fromJson(raw, Envelope::class.java).results ?: emptyList()
        }
    }

    fun generateCleanupPR(flagKey: String): CleanupPRResult {
        val raw = post(intelligenceUrl, "/api/v1/cleanup/generate-pr", mapOf("flag_key" to flagKey))
        return gson.fromJson(raw, CleanupPRResult::class.java)
    }

    // -------------------------------------------------------------------------
    // Helpers
    // -------------------------------------------------------------------------

    private fun encodeComponent(value: String): String =
        java.net.URLEncoder.encode(value, "UTF-8").replace("+", "%20")
}
