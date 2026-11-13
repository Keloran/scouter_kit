package scouterkit.chewedfeed.api

import scouterkit.chewedfeed.render.SourceResult
import java.io.Reader
import java.security.MessageDigest
import java.util.Collections
import java.util.concurrent.ExecutorService
import java.util.concurrent.Executors
import java.util.concurrent.FutureTask
import java.util.concurrent.TimeUnit

enum class ApiSessionState { PUBLIC, CONFIGURED, EXPIRED, UNAVAILABLE }
data class ApiSnapshot(val sources: List<SourceResult> = emptyList(), val session: ApiSessionState = ApiSessionState.PUBLIC, val notice: String = "")

class ApiRefresh internal constructor(
    private val cancellation: ApiCancellation,
    internal val task: FutureTask<ApiSnapshot?>,
    private val invalidate: () -> Unit,
) {
    // A cancelled or superseded refresh never publishes over newer data.
    fun cancel() { invalidate(); task.cancel(true); cancellation.cancel() }
    fun await(timeout: Long, unit: TimeUnit): ApiSnapshot? = task.get(timeout, unit)
}

// No Activity, Bluetooth, settings UI or lifecycle dependency. Consumers render
// snapshot(); await is for their worker thread. Credential import/save are I/O.
class ApiRepository(
    private val credentials: SessionCredentialStore,
    private val fetcher: ApiFetcher = ApiClient(),
    private val executor: ExecutorService = Executors.newFixedThreadPool(2),
) : AutoCloseable {
    private val lock = Any()
    private val credentialLock = Any()
    private var generation = 0L
    private var closed = false
    private var active: ApiRefresh? = null
    private var current = ApiSnapshot()
    private var cacheKey: String? = null // internal digest, never logged or published

    fun snapshot(): ApiSnapshot = synchronized(lock) { current }

    fun refresh(config: ApiConfig): ApiRefresh {
        val (old, next) = synchronized(lock) {
            check(!closed) { "API repository closed" }
            val id = ++generation
            val cancellation = ApiCancellation(config.refreshTimeoutMs)
            val task = FutureTask<ApiSnapshot?> { fetch(id, config, cancellation) }
            val refresh = ApiRefresh(cancellation, task) {
                synchronized(lock) { if (generation == id) { generation++; active = null } }
            }
            val previous = active
            active = refresh
            previous to refresh
        }
        old?.cancel()
        try { executor.execute(next.task) }
        catch (_: Exception) { next.cancel() }
        return next
    }

    private fun fetch(id: Long, config: ApiConfig, cancellation: ApiCancellation): ApiSnapshot? {
        cancellation.check()
        val credential = try { synchronized(credentialLock) { credentials.load() } } catch (_: Exception) {
            return commit(id, null, ApiSnapshot(session = ApiSessionState.UNAVAILABLE, notice = "Saved session unavailable; re-enter or clear the session cookie"))
        }
        cancellation.check()
        val fingerprint = credential?.let { token -> MessageDigest.getInstance("SHA-256").digest(token.cookie.toByteArray(Charsets.UTF_8)).joinToString("") { "%02x".format(it) } } ?: "public"
        val key = listOf(config.base.toASCIIString(), config.project, config.details.toString(), fingerprint).joinToString("\n")
        val previous = synchronized(lock) {
            if (id != generation || closed) return null
            if (cacheKey == key) current.sources else {
                current = ApiSnapshot(session = if (credential == null) ApiSessionState.PUBLIC else ApiSessionState.CONFIGURED)
                emptyList()
            }
        }
        val data = fetcher.fetch(config, credential, previous, cancellation)
        cancellation.check()
        val expired = data.any { it.err.startsWith("HTTP 401 ") || it.err.startsWith("HTTP 403 ") }
        val session = when { expired -> ApiSessionState.EXPIRED; credential == null -> ApiSessionState.PUBLIC; else -> ApiSessionState.CONFIGURED }
        return commit(id, key, ApiSnapshot(Collections.unmodifiableList(data.toList()), session,
            if (expired) "Session missing or expired; re-enter the session cookie or use public mode" else ""))
    }

    private fun commit(id: Long, key: String?, value: ApiSnapshot): ApiSnapshot? = synchronized(lock) {
        if (id != generation || closed) return null
        current = value
        cacheKey = key
        active = null
        value
    }

    private fun invalidate() {
        val old = synchronized(lock) {
            check(!closed) { "API repository closed" }
            generation++
            cacheKey = null
            current = ApiSnapshot()
            active.also { active = null }
        }
        old?.cancel()
    }

    fun saveSession(token: SessionToken?) = synchronized(credentialLock) {
        invalidate()
        try { credentials.save(token) } finally { invalidate() }
    }
    fun importSession(reader: Reader) = synchronized(credentialLock) {
        invalidate()
        try { credentials.import(reader) } finally { invalidate() }
    }

    override fun close() {
        val old = synchronized(lock) { closed = true; generation++; active.also { active = null } }
        old?.cancel()
        executor.shutdownNow()
    }
}
