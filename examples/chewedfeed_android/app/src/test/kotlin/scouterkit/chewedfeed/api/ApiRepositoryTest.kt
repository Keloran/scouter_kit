package scouterkit.chewedfeed.api

import org.junit.Assert.*
import org.junit.Test
import scouterkit.chewedfeed.render.SourceResult
import java.io.StringReader
import java.util.concurrent.CancellationException
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicInteger

internal class MemoryCredentials(@Volatile var token: SessionToken? = null) : SessionCredentialStore {
    override fun load(): SessionToken? = token
    override fun save(token: SessionToken?) { this.token = token }
}

class ApiRepositoryTest {
    @Test fun cancelledOldRefreshCannotOverwriteNewerData() {
        val entered = CountDownLatch(1)
        val release = CountDownLatch(1)
        val returned = CountDownLatch(1)
        val calls = AtomicInteger()
        val fetcher = ApiFetcher { _, _, _, _ ->
            if (calls.incrementAndGet() == 1) {
                entered.countDown()
                // Deliberately emulate I/O which ignores interruption.
                while (release.count > 0) { try { release.await() } catch (_: InterruptedException) { } }
                returned.countDown()
                listOf(SourceResult("old", atMs = 1))
            } else listOf(SourceResult("new", atMs = 2))
        }
        ApiRepository(MemoryCredentials(), fetcher).use { repository ->
            try {
                val config = ApiConfig.create("https://example.invalid")
                val old = repository.refresh(config)
                assertTrue(entered.await(2, TimeUnit.SECONDS))
                val current = repository.refresh(config).await(3, TimeUnit.SECONDS)!!
                assertEquals("new", current.sources.single().key)
                release.countDown()
                assertTrue(returned.await(2, TimeUnit.SECONDS))
                try { old.await(2, TimeUnit.SECONDS); fail("Cancelled refresh returned data") } catch (_: CancellationException) { }
                assertEquals(current, repository.snapshot())
            } finally { release.countDown() }
        }
    }

    @Test fun explicitCancelAndCloseRetirePendingCallbacks() {
        val entered = CountDownLatch(1)
        val release = CountDownLatch(1)
        val repository = ApiRepository(MemoryCredentials(), ApiFetcher { _, _, _, _ ->
            entered.countDown()
            try { release.await() } catch (_: InterruptedException) { }
            listOf(SourceResult("late", atMs = 1))
        })
        try {
            val refresh = repository.refresh(ApiConfig.create("https://example.invalid"))
            assertTrue(entered.await(2, TimeUnit.SECONDS))
            refresh.cancel()
            release.countDown()
            try { refresh.await(2, TimeUnit.SECONDS); fail("Cancelled refresh committed") } catch (_: CancellationException) { }
            assertTrue(repository.snapshot().sources.isEmpty())
            repository.close()
            try { repository.refresh(ApiConfig.create("https://example.invalid")); fail("Closed repository restarted") } catch (_: IllegalStateException) { }
        } finally { release.countDown(); repository.close() }
    }

    @Test fun cacheNeverCrossesCredentialOrProjectSelections() {
        val credentials = MemoryCredentials(SessionToken.parse("test-session"))
        val calls = AtomicInteger()
        val fetcher = ApiFetcher { _, _, previous, _ ->
            if (calls.incrementAndGet() == 1) listOf(SourceResult("summary", listOf(mapOf("projects" to 2.0)), 1))
            else previous.ifEmpty { listOf(SourceResult("summary", err = "request failed")) }
        }
        ApiRepository(credentials, fetcher).use { repository ->
            val config = ApiConfig.create("https://example.invalid")
            assertTrue(repository.refresh(config).await(2, TimeUnit.SECONDS)!!.sources.single().rows.isNotEmpty())
            repository.saveSession(null)
            assertTrue(repository.snapshot().sources.isEmpty())
            assertTrue(repository.refresh(config).await(2, TimeUnit.SECONDS)!!.sources.single().rows.isEmpty())
            repository.importSession(StringReader("replacement-session\n"))
            assertTrue(repository.refresh(ApiConfig.create("https://example.invalid", project = "other")).await(2, TimeUnit.SECONDS)!!.sources.single().rows.isEmpty())
        }
    }

    @Test fun authenticationFailuresAreActionableAndRedacted() {
        MockApi { request ->
            when (request.requestURI.path) {
                "/api/projects" -> reply(request, body = "[]")
                "/stats" -> reply(request, 401, "private failure body")
                else -> reply(request, 403, "private failure body")
            }
        }.use { server ->
            ApiRepository(MemoryCredentials(SessionToken.parse("test-session"))).use { repository ->
                val result = repository.refresh(server.config(details = false)).await(3, TimeUnit.SECONDS)!!
                assertEquals(ApiSessionState.EXPIRED, result.session)
                assertTrue(result.notice.contains("re-enter"))
                assertFalse(result.toString().contains("test-session"))
                assertFalse(result.toString().contains("private failure body"))
            }
        }
    }

    @Test fun unavailableKeystoreNeverFallsBackToPlaintextOrPublicRequests() {
        val calls = AtomicInteger()
        val credentials = object : SessionCredentialStore {
            override fun load(): SessionToken? = throw IllegalStateException("private key detail")
            override fun save(token: SessionToken?) { }
        }
        ApiRepository(credentials, ApiFetcher { _, _, _, _ -> calls.incrementAndGet(); emptyList() }).use { repository ->
            val state = repository.refresh(ApiConfig.create("https://example.invalid")).await(2, TimeUnit.SECONDS)!!
            assertEquals(0, calls.get())
            assertEquals(ApiSessionState.UNAVAILABLE, state.session)
            assertTrue(state.notice.contains("re-enter"))
            assertFalse(state.toString().contains("private key detail"))
        }
    }
}
