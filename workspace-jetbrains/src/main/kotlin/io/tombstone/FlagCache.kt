package io.tombstone

import com.intellij.openapi.application.ApplicationManager
import com.intellij.openapi.project.Project
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.atomic.AtomicLong

/**
 * Project-scoped flag cache with 30-second TTL.
 *
 * Fetches are done on a background thread; the cache never blocks the EDT.
 * Thread-safe: multiple coroutines may call [ensureLoaded] concurrently.
 */
class FlagCache(private val project: Project) {

    private val cache = ConcurrentHashMap<String, FlagState>()
    private val lastFetch = AtomicLong(0L)
    private val cacheTtlMs = 30_000L

    @Volatile
    private var loading = false

    /** Return a snapshot of the current cache (may be empty before first load). */
    fun snapshot(): Map<String, FlagState> = HashMap(cache)

    /** Return cached flag or null. Does NOT trigger a network fetch. */
    fun get(key: String): FlagState? = cache[key]

    /**
     * Ensure flags are loaded.  If cache is fresh, returns immediately.
     * Otherwise fires a background refresh (non-blocking for the caller).
     */
    fun ensureLoaded(onLoaded: (Map<String, FlagState>) -> Unit = {}) {
        val now = System.currentTimeMillis()
        if (now - lastFetch.get() < cacheTtlMs) {
            onLoaded(snapshot())
            return
        }

        if (loading) {
            onLoaded(snapshot())
            return
        }

        loading = true
        ApplicationManager.getApplication().executeOnPooledThread {
            try {
                val client = TombstoneClientHolder.get(project)
                val flags = client.listFlags()
                cache.clear()
                flags.forEach { cache[it.key] = it }
                lastFetch.set(System.currentTimeMillis())
                onLoaded(snapshot())
            } catch (e: Exception) {
                // Swallow; callers see stale/empty data rather than crashing
                onLoaded(snapshot())
            } finally {
                loading = false
            }
        }
    }

    /** Force the next call to [ensureLoaded] to re-fetch. */
    fun invalidate() {
        lastFetch.set(0L)
    }
}

// ---------------------------------------------------------------------------
// Project-level cache holder (one FlagCache per open project)
// ---------------------------------------------------------------------------

object FlagCacheHolder {
    private val caches = ConcurrentHashMap<String, FlagCache>()

    fun get(project: Project): FlagCache =
        caches.getOrPut(project.name) { FlagCache(project) }

    fun invalidate(project: Project) {
        caches[project.name]?.invalidate()
    }
}
