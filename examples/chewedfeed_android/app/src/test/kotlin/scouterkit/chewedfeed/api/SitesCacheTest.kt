package scouterkit.chewedfeed.api

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder
import scouterkit.chewedfeed.render.SourceResult
import java.io.File

class SitesCacheTest {
    @get:Rule val tmp = TemporaryFolder()
    private var now = 1_800_000_000_000L
    private val file get() = File(tmp.root, "sites-cache.json")
    private fun cache() = SitesCache(file) { now }

    private val sources = listOf(
        SourceResult("summary", listOf(mapOf("projects" to 2.0, "customers" to 5.0)), now - 1000),
        SourceResult("one/ticket-counts", listOf(mapOf("backlog" to 3.0, "columns" to listOf(mapOf("name" to "To do", "count" to 3.0)))), now - 1000),
        SourceResult("customers", listOf(mapOf("product_slug" to "one")), now - 1000), // needs the session: never on disk
        SourceResult("uptime", emptyList(), null, "HTTP 500"), // never fetched: nothing to keep
    )

    @Test
    fun roundTripKeepsOnlyAllowedData() {
        assertTrue(cache().save("k", sources))
        val back = cache().load("k")!!
        assertEquals(listOf("summary", "one/ticket-counts"), back.map { it.key })
        assertEquals(sources[0].rows, back[0].rows)
        assertEquals(sources[1].rows, back[1].rows)
        assertEquals(now - 1000, back[0].atMs)
        assertFalse(file.readText().contains("product_slug"))
    }

    @Test
    fun anotherSessionOrProjectFindsNothingAndClearsIt() {
        cache().save("k", sources)
        assertNull(cache().load("other project or session"))
        assertFalse(file.exists())
    }

    @Test
    fun corruptOldOrForeignFilesAreDiscarded() {
        for (bad in listOf("", "{", "not json", """{"version":2,"key":"x","saved":1,"sources":[]}""", "[1,2]",
            """{"version":1,"key":"x","saved":"soon","sources":[]}""")) {
            file.writeText(bad)
            assertNull(bad, cache().load("k"))
            assertFalse(bad, file.exists())
        }
        file.writeBytes(ByteArray(SitesCache.MAX_BYTES + 1) { ' '.code.toByte() })
        assertNull(cache().load("k"))
        // A valid file with a wrong row type (schema change) is discarded whole.
        cache().save("k", sources)
        file.writeText(file.readText().replace("\"rows\":[{", "\"rows\":[7,{"))
        assertNull(cache().load("k"))
    }

    @Test
    fun ageBounds() {
        cache().save("k", sources)
        now += SitesCache.MAX_AGE_MS + 1 // the file itself too old
        assertNull(cache().load("k"))
        now -= SitesCache.MAX_AGE_MS + 1
        val old = SourceResult("projects", listOf(mapOf("slug" to "one")), now - SitesCache.MAX_AGE_MS - 1) // kept failing
        cache().save("k", sources + old)
        assertEquals(listOf("summary", "one/ticket-counts"), cache().load("k")!!.map { it.key })
    }

    @Test
    fun failedOrInterruptedWritesKeepThePreviousFile() {
        cache().save("k", sources)
        val before = file.readText()
        // Interrupted: a temporary file left behind is ignored and replaced by the next write.
        File(tmp.root, "sites-cache.json.tmp").writeText("{partial")
        assertEquals(2, cache().load("k")!!.size)
        // Cannot write (a directory where the temporary file goes, like a full disk): false, previous file intact.
        File(tmp.root, "sites-cache.json.tmp").apply { delete(); mkdir() }
        assertFalse(cache().save("k", sources.take(1)))
        assertEquals(before, file.readText())
        // Too large to keep: refused, previous file intact.
        File(tmp.root, "sites-cache.json.tmp").delete()
        val huge = SourceResult("projects", List(5000) { mapOf("name" to "x".repeat(100)) }, now)
        assertFalse(cache().save("k", listOf(huge)))
        assertEquals(before, file.readText())
    }

    @Test
    fun clear() {
        cache().save("k", sources)
        cache().clear()
        assertNull(cache().load("k"))
    }
}
