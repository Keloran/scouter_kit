package scouterkit.chewedfeed.api

import org.junit.Assert.*
import org.junit.Test
import java.io.IOException
import java.net.InetAddress
import java.net.ServerSocket
import java.net.Socket
import java.net.URI
import java.util.Locale
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.ConcurrentLinkedQueue
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicInteger

internal class TestHeaders {
    private val values = linkedMapOf<String, String>()
    fun add(name: String, value: String) { values[name.lowercase(Locale.ROOT)] = value }
    fun getFirst(name: String): String? = values[name.lowercase(Locale.ROOT)]
    fun entries(): Set<Map.Entry<String, String>> = values.entries
}

// Minimal one-request HTTP fixture on a loopback socket. No server dependency
// or JDK-only module is added to Android's test boot classpath.
internal class HttpExchange(private val socket: Socket) : AutoCloseable {
    val requestMethod: String
    val requestURI: URI
    val requestHeaders = TestHeaders()
    val responseHeaders = TestHeaders()
    val responseBody = socket.getOutputStream()
    init {
        socket.soTimeout = 3000
        val input = socket.getInputStream().bufferedReader(Charsets.US_ASCII)
        val first = input.readLine().split(" ")
        requestMethod = first[0]
        requestURI = URI(first[1])
        while (true) {
            val line = input.readLine() ?: break
            if (line.isEmpty()) break
            requestHeaders.add(line.substringBefore(':'), line.substringAfter(':').trim())
        }
    }
    fun sendResponseHeaders(status: Int, length: Long) {
        val head = buildString {
            append("HTTP/1.1 $status Test\r\nConnection: close\r\n")
            if (length >= 0) append("Content-Length: $length\r\n")
            for ((name, value) in responseHeaders.entries()) append("$name: $value\r\n")
            append("\r\n")
        }
        responseBody.write(head.toByteArray(Charsets.US_ASCII))
        responseBody.flush()
    }
    override fun close() { socket.close() }
}

internal class MockApi(handler: (HttpExchange) -> Unit) : AutoCloseable {
    private val errors = ConcurrentLinkedQueue<Throwable>()
    private val executor = Executors.newCachedThreadPool()
    private val clients = ConcurrentHashMap.newKeySet<Socket>()
    private val server = ServerSocket(0, 32, InetAddress.getLoopbackAddress())
    private val accept = Thread {
        while (!server.isClosed) {
            val socket = try { server.accept() } catch (_: IOException) { break }
            clients += socket
            executor.execute {
                try { HttpExchange(socket).use(handler) }
                catch (_: IOException) { /* expected when a client cancels/limits a body */ }
                catch (_: InterruptedException) { Thread.currentThread().interrupt() }
                catch (e: Throwable) { errors += e }
                finally { socket.close(); clients -= socket }
            }
        }
    }.apply { start() }
    val base: String get() = "http://localhost:${server.localPort}"
    fun config(project: String = "", details: Boolean = true, timeout: Int = 1000, budget: Int = 5000) =
        ApiConfig.create(base, project, details, timeout, budget, allowLoopbackHttp = true)
    override fun close() {
        server.close()
        accept.join()
        clients.forEach { it.close() }
        executor.shutdownNow()
        executor.awaitTermination(2, TimeUnit.SECONDS)
        assertTrue("Mock server assertions: $errors", errors.isEmpty())
    }
}

internal fun reply(exchange: HttpExchange, status: Int = 200, body: String = "{}") {
    val bytes = body.toByteArray(Charsets.UTF_8)
    exchange.responseHeaders.add("Content-Type", "application/json")
    exchange.sendResponseHeaders(status, bytes.size.toLong())
    exchange.responseBody.use { it.write(bytes) }
}

class ApiClientTest {
    @Test fun publicSourcesRetainFailuresAndClearSuccessfulEmptyData() {
        val mode = AtomicInteger()
        MockApi { request ->
            assertEquals("GET", request.requestMethod)
            assertNull(request.requestHeaders.getFirst("Cookie"))
            if (mode.get() == 1) { reply(request, 503, "private upstream failure"); return@MockApi }
            if (mode.get() == 3 && request.requestURI.path.endsWith("/uptime")) { reply(request,503,"private upstream failure"); return@MockApi }
            when (request.requestURI.path) {
                "/api/projects" -> reply(request, body = if (mode.get() == 2) "[]" else """[
                    {"slug":"healthy","name":"Healthy","attention_mode":"grow","ignored":"not-allowed"},
                    {"slug":"broken","name":"Broken","attention_mode":"fix","uptime_kuma_slug":"broken"}]
                """)
                "/stats" -> reply(request, body = if (mode.get() == 2) "{}" else """{"projects":2,"customers":3,"ignored":"not-allowed"}""")
                "/api/projects/broken/uptime" -> reply(request, body = """{"overall_status":"down","uptime_24h":0.8,"ignored":"not-allowed"}""")
                "/api/projects/broken/ticket-counts", "/api/projects/healthy/ticket-counts" -> reply(request, body = """{"backlog":2,"columns":[{"name":"To do","count":1,"ignored":"not-allowed"}]}""")
                else -> fail("Unexpected endpoint")
            }
        }.use { server ->
            val config = server.config()
            val client = ApiClient(nowMs = { 1000L })
            val first = client.fetch(config, null, emptyList(), ApiCancellation(5000))
            assertEquals(5, first.size)
            assertEquals("broken", first[1].rows[0]["slug"])
            assertFalse(first.toString().contains("not-allowed"))
            mode.set(3)
            val partial = client.fetch(config, null, first, ApiCancellation(5000))
            assertEquals(1, partial.count { it.err.isNotEmpty() })
            assertTrue(partial.first { it.key == "broken/uptime" }.rows.isNotEmpty())
            mode.set(1)
            val stale = client.fetch(config, null, first, ApiCancellation(5000))
            for (result in stale) {
                assertEquals(1000L, result.atMs)
                assertTrue(result.rows.isNotEmpty())
                assertTrue(result.err.startsWith("HTTP 503"))
                assertFalse(result.err.contains("upstream"))
            }
            mode.set(2)
            val empty = client.fetch(config, null, stale, ApiCancellation(5000))
            assertEquals(2, empty.size)
            assertTrue(empty[1].rows.isEmpty())
            assertTrue(empty.all { it.err.isEmpty() })
            assertTrue(first[1].rows.isNotEmpty()) // snapshots are not mutated by later fetches
        }
    }

    @Test fun privateCookieProjectFilterAndMissingIntegration() {
        val calls = AtomicInteger()
        MockApi { request ->
            calls.incrementAndGet()
            assertEquals("session=test-session", request.requestHeaders.getFirst("Cookie"))
            when (request.requestURI.path) {
                "/api/projects" -> reply(request, body = """[{"slug":"one","name":"One","uptime_kuma_slug":"one"},{"slug":"two","name":"Two"}]""")
                "/stats" -> reply(request)
                "/api/product-customers" -> reply(request, body = """[{"product_slug":"two","ignored":"not-allowed"},{"product_slug":"one","ignored":"not-allowed"}]""")
                "/api/projects/one/uptime" -> reply(request, body = """{"overall_status":"up","uptime_24h":1}""")
                "/api/projects/one/ticket-counts" -> reply(request, 404, "private body")
                else -> fail("Unexpected endpoint")
            }
        }.use { server ->
            val data = ApiClient().fetch(server.config("one"), SessionToken.parse("test-session"), emptyList(), ApiCancellation(5000))
            assertEquals(5, calls.get())
            assertTrue(data.all { it.err.isEmpty() && it.atMs != null })
            assertEquals(listOf(mapOf("product_slug" to "one")), data.first { it.key == "customers" }.rows)
            assertTrue(data.first { it.key == "one/ticket-counts" }.rows.isEmpty())
            assertEquals("one", data[1].rows.single()["slug"])
            assertFalse(data.toString().contains("not-allowed"))
        }
    }

    @Test fun redirectsNeverReceiveCookiesAtTheirDestination() {
        val destinationCalls = AtomicInteger()
        MockApi { destinationCalls.incrementAndGet(); reply(it) }.use { destination ->
            MockApi { request ->
                if (request.requestURI.path == "/api/projects") {
                    request.responseHeaders.add("Location", destination.base + "/trap")
                    reply(request, 302, "private redirect body")
                } else reply(request)
            }.use { server ->
                val data = ApiClient().fetch(server.config(details = false), SessionToken.parse("test-session"), emptyList(), ApiCancellation(5000))
                assertEquals(0, destinationCalls.get())
                assertTrue(data[1].err.startsWith("HTTP 302"))
                assertFalse(data.toString().contains("private redirect"))
            }
        }
    }

    @Test fun malformedAndOversizedResponsesAreUnavailable() {
        for (body in listOf("null", "{}", "[1]", "[", "[{slug:'one'}]", "[{\"slug\":\"one\",}]", "[{}] trailing", " ".repeat(MAX_API_BODY + 1))) {
            MockApi { request -> reply(request, body = if (request.requestURI.path == "/api/projects") body else "{}") }.use { server ->
                val data = ApiClient().fetch(server.config(details = false), null, emptyList(), ApiCancellation(5000))
                assertTrue(data[1].err.isNotEmpty())
                assertNull(data[1].atMs)
                assertTrue(data[1].rows.isEmpty())
            }
        }
    }

    @Test fun timeoutsAndProjectLimitsBoundWork() {
        MockApi { request ->
            if (request.requestURI.path == "/api/projects") Thread.sleep(400)
            reply(request, body = if (request.requestURI.path == "/api/projects") "[]" else "{}")
        }.use { server ->
            val before = System.nanoTime()
            val data = ApiClient().fetch(server.config(details = false, timeout = 100, budget = 300), null, emptyList(), ApiCancellation(300))
            assertTrue(data[1].err.contains("timed out"))
            assertTrue(TimeUnit.NANOSECONDS.toMillis(System.nanoTime() - before) < 2000)
        }
        val calls = AtomicInteger()
        MockApi { request ->
            calls.incrementAndGet()
            reply(request, body = if (request.requestURI.path == "/api/projects") (0..128).joinToString(",", "[", "]") { "{\"slug\":\"site-$it\"}" } else "{}")
        }.use { server ->
            val data = ApiClient().fetch(server.config(), null, emptyList(), ApiCancellation(5000))
            assertTrue(data[1].err.isNotEmpty())
            assertEquals(2, calls.get())
        }
    }

    @Test fun fanOutHasFourWorkers() {
        val live = AtomicInteger()
        val maximum = AtomicInteger()
        MockApi { request ->
            val count = live.incrementAndGet()
            maximum.updateAndGet { maxOf(it, count) }
            try {
                Thread.sleep(20)
            } finally { live.decrementAndGet() }
            reply(request, body = if (request.requestURI.path == "/api/projects") (0..7).joinToString(",", "[", "]") { "{\"slug\":\"site-$it\"}" } else "{}")
        }.use { server ->
            ApiClient().fetch(server.config(), null, emptyList(), ApiCancellation(5000))
            assertTrue(maximum.get() in 2..4)
        }
    }

    @Test fun streamingBodiesRespectSizeAndOverallDeadline() {
        MockApi { request ->
            if (request.requestURI.path == "/api/projects") {
                request.sendResponseHeaders(200,-1)
                request.responseBody.write(ByteArray(MAX_API_BODY+1){32})
            } else reply(request)
        }.use { server ->
            val data=ApiClient().fetch(server.config(details=false),null,emptyList(),ApiCancellation(5000))
            assertTrue(data[1].err.isNotEmpty())
            assertNull(data[1].atMs)
        }
        MockApi { request ->
            if(request.requestURI.path=="/api/projects") {
                request.sendResponseHeaders(200,64)
                request.responseBody.write('['.code)
                request.responseBody.flush()
                repeat(60){Thread.sleep(50);request.responseBody.write(' '.code);request.responseBody.flush()}
            }else reply(request)
        }.use { server ->
            val before=System.nanoTime()
            val data=ApiClient().fetch(server.config(details=false,timeout=100,budget=300),null,emptyList(),ApiCancellation(300))
            assertTrue(data[1].err.contains("timed out"))
            assertTrue(TimeUnit.NANOSECONDS.toMillis(System.nanoTime()-before)<2000)
        }
    }
}
