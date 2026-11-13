package scouterkit.chewedfeed.api

import scouterkit.chewedfeed.render.SourceResult
import java.io.ByteArrayOutputStream
import java.net.HttpURLConnection
import java.net.SocketTimeoutException
import java.net.URI
import java.nio.ByteBuffer
import java.nio.charset.CodingErrorAction
import java.util.Collections
import java.util.concurrent.CancellationException
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import java.util.concurrent.TimeoutException

internal const val MAX_API_BODY = 4 * 1024 * 1024

data class ApiResponse(val status: Int, val body: ByteArray = byteArrayOf())

fun interface ApiTransport {
    fun get(url: URI, credential: SessionToken?, timeoutMs: Int, cancellation: ApiCancellation): ApiResponse
}

class UrlConnectionTransport : ApiTransport {
    override fun get(url: URI, credential: SessionToken?, timeoutMs: Int, cancellation: ApiCancellation): ApiResponse {
        val connection = url.toURL().openConnection() as HttpURLConnection
        val call = AutoCloseable { connection.disconnect() }
        try {
            cancellation.attach(call)
            connection.requestMethod = "GET"
            connection.instanceFollowRedirects = false
            connection.useCaches = false
            connection.connectTimeout = timeoutMs
            connection.readTimeout = timeoutMs
            connection.setRequestProperty("Accept", "application/json")
            credential?.let { connection.setRequestProperty("Cookie", "session=" + it.cookie) }
            val status = connection.responseCode
            cancellation.check()
            // Error/redirect bodies can contain private server details; do not read them.
            if (status != HttpURLConnection.HTTP_OK) return ApiResponse(status)
            require(connection.contentLengthLong <= MAX_API_BODY) { "API body exceeds limit" }
            val body = ByteArrayOutputStream()
            connection.inputStream.use { input ->
                val buffer = ByteArray(8192)
                while (true) {
                    cancellation.check()
                    cancellation.remainingMs()
                    val n = input.read(buffer)
                    if (n < 0) break
                    require(body.size() + n <= MAX_API_BODY) { "API body exceeds limit" }
                    body.write(buffer, 0, n)
                }
            }
            return ApiResponse(status, body.toByteArray())
        } finally {
            cancellation.detach(call)
            connection.disconnect()
        }
    }
}

fun interface ApiFetcher {
    fun fetch(config: ApiConfig, credential: SessionToken?, previous: List<SourceResult>, cancellation: ApiCancellation): List<SourceResult>
}

private enum class Kind { SUMMARY, PROJECTS, CUSTOMERS, UPTIME, TICKETS }
private data class ApiSource(val key: String, val path: String, val kind: Kind, val list: Boolean = false)

class ApiClient(private val transport: ApiTransport = UrlConnectionTransport(), private val nowMs: () -> Long = System::currentTimeMillis) : ApiFetcher {
    override fun fetch(config: ApiConfig, credential: SessionToken?, previous: List<SourceResult>, cancellation: ApiCancellation): List<SourceResult> {
        cancellation.check()
        // The project list uses the same deadline and pool as all detail calls.
        val pool = Executors.newFixedThreadPool(4)
        val futures = mutableListOf<java.util.concurrent.Future<SourceResult>>()
        fun submit(source: ApiSource) = pool.submit<SourceResult> { get(config, credential, source, cancellation) }.also { futures += it }
        try {
            val projectsSource = ApiSource("projects", "/api/projects", Kind.PROJECTS, true)
            var projects = retain(projectsSource, wait(projectsSource, submit(projectsSource), cancellation), previous)
            if (config.project.isNotEmpty()) {
                val rows = projects.rows.filter { it["slug"] == config.project }
                projects = projects.copy(rows = rows, err = if (rows.isEmpty() && projects.err.isEmpty()) "project slug not found" else projects.err)
            }
            projects = projects.copy(rows = projects.rows.sortedBy {
                when (it["attention_mode"]) { "fix" -> 0; "revive" -> 1; else -> 2 }
            })
            val sources = mutableListOf(ApiSource("summary", "/stats", Kind.SUMMARY))
            if (credential != null) sources += ApiSource("customers", "/api/product-customers", Kind.CUSTOMERS, true)
            if (config.details) {
                for (project in projects.rows) {
                    val slug = project["slug"] as? String ?: continue
                    if (!ApiConfig.SLUG.matches(slug)) continue
                    val path = "/api/projects/" + ApiConfig.pathSegment(slug)
                    if (!(project["uptime_kuma_slug"] as? String).isNullOrEmpty()) sources += ApiSource("$slug/uptime", "$path/uptime", Kind.UPTIME)
                    sources += ApiSource("$slug/ticket-counts", "$path/ticket-counts", Kind.TICKETS)
                }
            }
            val results = sources.map { source -> source to submit(source) }.map { (source, future) ->
                var result = retain(source, wait(source, future, cancellation), previous)
                if (source.kind == Kind.CUSTOMERS && config.project.isNotEmpty()) result = result.copy(rows = result.rows.filter { it["product_slug"] == config.project })
                result
            }
            cancellation.check()
            return Collections.unmodifiableList((listOf(results.first(), projects) + results.drop(1)).map {
                it.copy(rows = Collections.unmodifiableList(it.rows.toList()))
            })
        } finally {
            futures.forEach { it.cancel(true) }
            pool.shutdownNow()
            cancellation.closeCalls()
        }
    }

    private fun wait(source: ApiSource, future: java.util.concurrent.Future<SourceResult>, cancellation: ApiCancellation): SourceResult {
        cancellation.check()
        return try {
            if (future.isDone) future.get() else future.get(cancellation.remainingMs(), TimeUnit.MILLISECONDS)
        } catch (e: CancellationException) { throw e
        } catch (_: InterruptedException) {
            Thread.currentThread().interrupt()
            throw CancellationException("Refresh cancelled")
        } catch (_: TimeoutException) {
            future.cancel(true)
            SourceResult(source.key, err = "request timed out")
        } catch (_: Exception) {
            cancellation.check()
            SourceResult(source.key, err = "request failed")
        }
    }

    private fun get(config: ApiConfig, credential: SessionToken?, source: ApiSource, cancellation: ApiCancellation): SourceResult {
        try {
            cancellation.check()
            val timeout = minOf(config.requestTimeoutMs.toLong(), cancellation.remainingMs()).toInt()
            val response = transport.get(URI(config.base.toASCIIString() + source.path), credential, timeout, cancellation)
            cancellation.check()
            if (response.status == 404 && source.kind == Kind.TICKETS) return SourceResult(source.key, atMs = nowMs())
            if (response.status != 200) {
                val extra = if (response.status == 401 || response.status == 403) " (session missing or expired; re-enter session cookie)" else ""
                return SourceResult(source.key, err = "HTTP ${response.status}$extra")
            }
            require(response.body.size <= MAX_API_BODY)
            val text = Charsets.UTF_8.newDecoder().onMalformedInput(CodingErrorAction.REPORT).onUnmappableCharacter(CodingErrorAction.REPORT)
                .decode(ByteBuffer.wrap(response.body)).toString()
            val parsed = ApiJson(text).parse()
            val rows: List<*> = if (source.list) parsed as? List<*> ?: error("list required") else listOf(parsed as? Map<*, *> ?: error("object required"))
            require(rows.size <= if (source.kind == Kind.PROJECTS) 128 else 4096)
            require(rows.all { it is Map<*, *> })
            val filtered = rows.map { allowed(source.kind, it as Map<*, *>) }
            return SourceResult(source.key, Collections.unmodifiableList(filtered), nowMs())
        } catch (e: CancellationException) { throw e
        } catch (_: SocketTimeoutException) { return SourceResult(source.key, err = "request timed out")
        } catch (_: TimeoutException) { return SourceResult(source.key, err = "request timed out")
        } catch (_: InterruptedException) {
            Thread.currentThread().interrupt()
            throw CancellationException("Refresh cancelled")
        } catch (_: Exception) {
            cancellation.check()
            return SourceResult(source.key, err = "response invalid, unavailable, or over limits")
        }
    }

    private fun retain(source: ApiSource, result: SourceResult, previous: List<SourceResult>): SourceResult {
        if (result.err.isEmpty()) return result // successful empty data clears the old rows
        val old = previous.firstOrNull { it.key == source.key && it.atMs != null } ?: return result
        return result.copy(rows = Collections.unmodifiableList(old.rows.map { allowed(source.kind, it) }), atMs = old.atMs)
    }

    private fun allowed(kind: Kind, row: Map<*, *>): Map<String, Any?> {
        val strings = when (kind) {
            Kind.PROJECTS -> setOf("id", "slug", "name", "attention_mode", "stage", "uptime_kuma_slug")
            Kind.CUSTOMERS -> setOf("product_slug")
            Kind.UPTIME -> setOf("overall_status")
            else -> emptySet()
        }
        val numbers = when (kind) {
            Kind.SUMMARY -> setOf("projects", "customers", "paid_customers")
            Kind.PROJECTS -> setOf("github_open_prs", "health_score", "task_velocity_score")
            Kind.UPTIME -> setOf("uptime_24h", "uptime_30d")
            Kind.TICKETS -> setOf("backlog")
            else -> emptySet()
        }
        val result = linkedMapOf<String, Any?>()
        for (key in strings) (row[key] as? String)?.let { value ->
            if (key != "slug" || ApiConfig.SLUG.matches(value)) result[key] = bounded(value, if (key == "name") 512 else 128)
        }
        for (key in numbers) (row[key] as? Number)?.toDouble()?.takeIf { it.isFinite() }?.let { result[key] = it }
        if (kind == Kind.PROJECTS && row["id"] is Number) result["id"] = (row["id"] as Number).toDouble()
        if (kind == Kind.TICKETS) {
            (row["columns"] as? List<*>)?.let { columns ->
                result["columns"] = Collections.unmodifiableList(columns.take(128).filterIsInstance<Map<*, *>>().map { column ->
                    val clean = linkedMapOf<String, Any?>()
                    (column["name"] as? String)?.let { clean["name"] = bounded(it, 128) }
                    (column["count"] as? Number)?.toDouble()?.takeIf { it.isFinite() }?.let { clean["count"] = it }
                    Collections.unmodifiableMap(clean)
                })
            }
        }
        return Collections.unmodifiableMap(result)
    }

    private fun bounded(value: String, limit: Int): String {
        if (value.length <= limit) return value
        val end = if (value[limit - 1].isHighSurrogate()) limit - 1 else limit
        return value.substring(0, end)
    }
}
