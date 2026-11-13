package scouterkit.chewedfeed.api

import java.net.URI
import java.net.URLEncoder
import java.nio.charset.StandardCharsets
import java.util.Locale
import java.util.concurrent.CancellationException
import java.util.concurrent.TimeoutException

class ApiConfig private constructor(
    val base: URI,
    val project: String,
    val details: Boolean,
    val requestTimeoutMs: Int,
    val refreshTimeoutMs: Int,
) {
    companion object {
        // Loopback HTTP is explicitly enabled only by host tests/development.
        fun create(base: String, project: String = "", details: Boolean = true,
                   requestTimeoutMs: Int = 15_000, refreshTimeoutMs: Int = 45_000,
                   allowLoopbackHttp: Boolean = false): ApiConfig {
            val uri = try { URI(base) } catch (_: Exception) { throw IllegalArgumentException("Invalid API base URL") }
            val scheme = uri.scheme?.lowercase(Locale.ROOT)
            require(uri.host != null && uri.rawUserInfo == null && uri.rawQuery == null && uri.rawFragment == null && !uri.isOpaque) {
                "Use an API base URL without credentials, query, or fragment"
            }
            require(scheme == "https" || allowLoopbackHttp && scheme == "http" && uri.host == "localhost") { "Use HTTPS for the API" }
            require(uri.port == -1 || uri.port in 1..65535) { "Invalid API port" }
            require(project.isEmpty() || SLUG.matches(project)) { "Invalid project slug" }
            require(requestTimeoutMs in 50..30_000 && refreshTimeoutMs in requestTimeoutMs..120_000) { "Invalid request time limits" }
            return ApiConfig(URI(uri.toASCIIString().trimEnd('/')), project, details, requestTimeoutMs, refreshTimeoutMs)
        }

        internal val SLUG = Regex("[a-z0-9][a-z0-9_-]{0,63}")
        internal fun pathSegment(value: String): String = URLEncoder.encode(value, StandardCharsets.UTF_8.name()).replace("+", "%20")
    }
}

// Cancellations close outstanding I/O and never hold the repository state lock.
class ApiCancellation internal constructor(timeoutMs: Int) {
    private val lock = Any()
    private val deadline = System.nanoTime() + timeoutMs * 1_000_000L
    private val calls = mutableSetOf<AutoCloseable>()
    @Volatile var cancelled = false
        private set

    internal fun check() {
        if (cancelled || Thread.currentThread().isInterrupted) throw CancellationException("Refresh cancelled")
    }

    internal fun remainingMs(): Long {
        check()
        val remaining = deadline - System.nanoTime()
        if (remaining <= 0) throw TimeoutException("Refresh deadline")
        return maxOf(1, remaining / 1_000_000)
    }

    internal fun attach(call: AutoCloseable) {
        synchronized(lock) {
            check()
            calls += call
        }
    }

    internal fun detach(call: AutoCloseable) { synchronized(lock) { calls -= call } }

    internal fun closeCalls() {
        val old = synchronized(lock) { calls.toList().also { calls.clear() } }
        old.forEach { runCatching { it.close() } }
    }

    fun cancel() {
        synchronized(lock) { cancelled = true }
        closeCalls()
    }
}
