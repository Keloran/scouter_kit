package scouterkit.chewedfeed.handover

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test
import java.time.ZoneOffset

// The laptop's shared readings on the phone, from the shared fixtures through the real wire checks.
class RemoteTelemetryTest {
    private var now = 1_000_000L // monotonic
    private val wall = 1_800_000_060_000L
    private val r = RemoteTelemetry { now }

    // tests/handover/messages.json's telemetry messages, decoded as the link decodes them.
    private fun fixture(name: String): Message {
        val c = (handoverFixture("messages.json")["valid"] as List<*>).map { it as Map<*, *> }.first { it["name"] == name }
        return Wire.decode(StrictJson.write(c["message"]).toByteArray())
    }

    @Test
    fun systemReadingAndItsAge() {
        val m = fixture("telemetry") // system, fresh, age 2 s, 12.5 % CPU, 4096/8192 bytes, battery 75
        assertTrue(r.accept("s1", m.body))
        val page = r.statsView(wall, ZoneOffset.UTC)!!.single()
        assertEquals("Laptop stats", page.title)
        assertEquals(listOf("CPU: 12.5%", "RAM: 0.0/0.0G", "Laptop battery: 75%"), page.lines) // the laptop's, labelled
        assertFalse(page.status.startsWith("STALE"))
        now += Policy.TELEMETRY_STALE_MS - 2000 - 1
        assertFalse(r.statsView(wall, ZoneOffset.UTC)!!.single().status.startsWith("STALE"))
        now += 1 // the sender's 2 s plus 88 s here: stale, on this phone's monotonic clock
        assertTrue(r.statsView(wall, ZoneOffset.UTC)!!.single().status.startsWith("STALE"))
    }

    @Test
    fun aiReadingsLikeTheDesktop() {
        assertTrue(r.accept("s1", fixture("ai_telemetry").body)) // codex weekly 25.5 %, sent stale
        val page = r.aiView(wall, ZoneOffset.UTC)!!.single()
        assertEquals("ai/codex", page.id) // the desktop's page id: a transferred page lands here
        assertEquals("Codex usage", page.title)
        assertEquals(listOf("Weekly: 25.5% used (74.5% left)", "Reset: unknown"), page.lines)
        assertTrue(page.status.startsWith("STALE"))
        assertTrue(r.accept("s1", fixture("unavailable_telemetry").body.withRevision(2)))
        assertEquals(listOf("No readable usage cache on the laptop", "Run ai-usagebar there to refresh usage"), r.aiView(wall)!!.single().lines)
    }

    @Test
    fun onlyNewerRevisionsPerSession() {
        val m = fixture("telemetry") // revision 4
        assertTrue(r.accept("s1", m.body))
        assertFalse(r.accept("s1", m.body)) // repeated
        assertFalse(r.accept("s1", m.body.withRevision(3))) // older
        assertTrue(r.accept("s1", m.body.withRevision(5)))
        assertTrue(r.accept("s2", m.body.withRevision(1))) // the laptop restarted: a new session counts afresh
        assertFalse(r.accept("s2", mapOf("telemetry" to mapOf("revision" to JsonNumber("9"))))) // no source
    }

    @Test
    fun disablingAndStoppedSharingClear() {
        r.accept("s1", fixture("telemetry").body)
        r.accept("s1", fixture("ai_telemetry").body)
        r.enable(ai = false, system = true)
        assertNull(r.aiView(wall))
        assertFalse(r.accept("s1", fixture("ai_telemetry").body.withRevision(9))) // ignored while off
        assertTrue(r.statsView(wall) != null)
        r.linkUp(shares = true)
        assertTrue(r.statsView(wall) != null) // a reconnect keeps the last readings
        r.linkUp(shares = false) // the laptop no longer shares: cleared
        assertNull(r.statsView(wall))
    }

    @Test
    fun malformedTelemetryNeverGetsThrough() {
        for (c in (handoverFixture("messages.json")["invalid"] as List<*>).map { it as Map<*, *> }.filter { (it["name"] as String).startsWith("telemetry") }) {
            assertThrows(c["name"].toString(), InvalidMessage::class.java) { Wire.decode(StrictJson.write(c["message"]).toByteArray()) }
        }
    }

    private fun Map<String, Any?>.withRevision(n: Long): Map<String, Any?> {
        val t = LinkedHashMap(this["telemetry"] as Map<*, *>)
        t["revision"] = JsonNumber(n.toString())
        @Suppress("UNCHECKED_CAST")
        return mapOf("telemetry" to t) as Map<String, Any?>
    }
}
