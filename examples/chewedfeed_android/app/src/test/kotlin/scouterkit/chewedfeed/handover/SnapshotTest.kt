package scouterkit.chewedfeed.handover

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test
import scouterkit.chewedfeed.api.ApiJson
import scouterkit.chewedfeed.dashboard.Navigation
import scouterkit.chewedfeed.dashboard.Page
import scouterkit.chewedfeed.dashboard.View
import java.io.File

// tests/handover, shared with the Go and Python checks.
private fun shared(name: String): String {
    var dir: File? = File("").absoluteFile
    while (dir != null && !File(dir, "tests/handover/$name").isFile) dir = dir.parentFile
    return File(dir ?: error("tests/handover not found"), "tests/handover/$name").readText()
}

// go/examples/chewedfeed snapshot_test.go, with the phone's own fallback for laptop-only views.
class SnapshotTest {
    private fun nav() = Navigation(
        listOf(Page("summary"), Page("projects"), Page("site-one"), Page("site-two")),
        listOf(Page("codex"), Page("claude")),
        listOf(Page("system/cpu"), Page("system/memory")),
    )

    private fun phoneNav() = Navigation(listOf(Page("summary"), Page("projects/one/0")),
        listOf(Page("ai/laptop", "AI usage", listOf("Shown from the laptop only"), "Unavailable")),
        listOf(Page("system/off", "System monitor", listOf("Turned off in Settings"), "Unavailable")))

    private val good = """{"schema_version":1,"revision":3,"view":"sites","page_id":"summary","project":null,"rotation_paused":false,"rotation_ms":12000}"""

    @Test
    fun roundTrip() {
        val from = nav()
        from.select(View.AI, "claude")
        from.paused = true
        val s = UiSnapshot.decode(UiSnapshot.export(from, "alpha", 15_000, 7).encode())
        val to = nav()
        val (rotate, project) = s.apply(to, UiSnapshot.Overrides(), 12_000, "")
        assertEquals(View.AI, to.selected)
        assertEquals(1, to.index(View.AI))
        assertTrue(to.paused)
        assertEquals(15_000L to "alpha", rotate to project)
        // Notification history stays local: only the empty page travels.
        from.replace(View.NOTIFICATIONS, listOf(Page("notifications/4/0"), Page("notifications/3/0")))
        from.select(View.NOTIFICATIONS, "notifications/3/0")
        val n = UiSnapshot.export(from, "", 0, 8)
        assertEquals(UiSnapshot.NOTIFICATION_PAGE, n.pageId)
        assertNull(n.problem())
    }

    @Test
    fun goToKotlinToGoIsByteForByte() {
        val canonical = (ApiJson(shared("snapshot_vectors.json")).parse() as Map<*, *>)["canonical"] as List<*>
        assertTrue(canonical.size >= 4)
        for (c in canonical) assertEquals(c, UiSnapshot.decode(c as String).encode())
    }

    @Test
    fun onlyAllowedFieldsLeave() {
        val policy = (ApiJson(shared("policy.json")).parse() as Map<*, *>)["snapshot"] as Map<*, *>
        val allow = policy["allow"] as List<*>
        val n = nav()
        n.replace(View.SITES, listOf(Page("summary", "Chewedfeed", listOf("Customers: 12"), "Fetched 12:00"))) // API data
        val out = ApiJson(UiSnapshot.export(n, "", 1000, 1).encode()).parse() as Map<*, *>
        assertEquals(allow.toSet(), out.keys)
        for (f in policy["forbid"] as List<*>) assertFalse(f.toString(), f in out.keys)
        // The model has nothing else to leak: its fields are the allowed ones.
        assertEquals(allow.size, UiSnapshot::class.java.declaredFields.count { !java.lang.reflect.Modifier.isStatic(it.modifiers) } + 1) // + schema_version
    }

    @Test
    fun sharedMessageFixtures() {
        val f = ApiJson(shared("messages.json")).parse() as Map<*, *>
        var n = 0
        for ((kind, ok) in listOf("valid" to true, "invalid" to false)) for (c in f[kind] as List<*>) {
            c as Map<*, *>
            if (!(c["name"] as String).startsWith("snapshot")) continue
            val body = (c["message"] as Map<*, *>)["body"] as Map<*, *>
            val snapshot = body["snapshot"]
            val text = if (snapshot == null) "null" else json(snapshot)
            val result = runCatching { UiSnapshot.decode(text) }
            assertEquals(c["name"].toString(), ok, result.isSuccess)
            n++
        }
        assertTrue("only $n snapshot fixtures", n >= 7)
    }

    // Back to JSON for the fixtures (Doubles that are whole numbers as integers, as they were written).
    private fun json(v: Any?): String = when (v) {
        null -> "null"
        is String -> "\"" + v.replace("\\", "\\\\").replace("\"", "\\\"") + "\""
        is Double -> if (v == Math.floor(v) && !v.isInfinite() && kotlin.math.abs(v) < 1e15) v.toLong().toString() else v.toString()
        is Boolean -> v.toString()
        is Map<*, *> -> v.entries.joinToString(",", "{", "}") { json(it.key.toString()) + ":" + json(it.value) }
        is List<*> -> v.joinToString(",", "[", "]") { json(it) }
        else -> error("unexpected $v")
    }

    @Test
    fun rejectsWithBoundedReasons() {
        for ((name, text) in mapOf(
            "version 2" to good.replace("\"schema_version\":1", "\"schema_version\":2"),
            "missing view" to good.replace("\"view\":\"sites\",", ""),
            "unknown view" to good.replace("\"sites\"", "\"settings\""),
            "long page" to good.replace("\"summary\"", "\"" + "a".repeat(129) + "\""),
            "path in page" to good.replace("\"summary\"", "\"../home/someone\""),
            "bad project" to good.replace("\"project\":null", "\"project\":\"Has Spaces\""),
            "geometry" to good.replace("}", ",\"font_scale\":2}"),
            "truncated" to good.dropLast(5),
            "not an object" to "[1,2]",
            "too large" to good.replace("}", ",\"x\":\"" + "a".repeat(UiSnapshot.MAX_BYTES) + "\"}"),
            "negative" to good.replace("\"rotation_ms\":12000", "\"rotation_ms\":-1"),
            "fractional" to good.replace("\"revision\":3", "\"revision\":3.5"),
            "notification history" to good.replace("\"view\":\"sites\"", "\"view\":\"notifications\"").replace("\"summary\"", "\"notifications/4/0\""),
        )) {
            val e = assertThrows(name, IllegalArgumentException::class.java) { UiSnapshot.decode(text) }
            assertTrue("$name: ${e.message}", (e.message ?: "").length in 1..64)
        }
        assertEquals(3L, UiSnapshot.decode(good).revision)
    }

    @Test
    fun fallbacks() {
        val n = nav()
        UiSnapshot(0, "stats", "system/gpu", null, false, 0).apply(n, UiSnapshot.Overrides(), 0, "") // a page this phone lacks
        assertEquals(View.STATS, n.selected)
        assertEquals(0, n.index(View.STATS))
        // The laptop was on AI (or Stats), which this phone shows only as a placeholder: the first Sites page.
        val p = phoneNav()
        p.select(View.SITES, "projects/one/0")
        UiSnapshot(1, "ai", "ai/claude", null, false, 12_000).apply(p, UiSnapshot.Overrides(), 12_000, "")
        assertEquals(View.SITES, p.selected)
        assertEquals(0, p.index(View.SITES))
        UiSnapshot(1, "stats", "system/cpu", null, false, 12_000).apply(p, UiSnapshot.Overrides(), 12_000, "")
        assertEquals(View.SITES, p.selected)
        UiSnapshot(1, "notifications", UiSnapshot.NOTIFICATION_PAGE, null, false, 12_000).apply(p, UiSnapshot.Overrides(), 12_000, "")
        assertEquals(View.NOTIFICATIONS, p.selected)
        // Navigation keeps working afterwards.
        p.move(1)
        assertTrue(p.touch(2, false))
    }

    @Test
    fun localSettingsWin() {
        val s = UiSnapshot(0, "sites", "site-two", "alpha", false, 30_000)
        assertEquals(5_000L to "beta", s.apply(nav(), UiSnapshot.Overrides(rotate = true, project = true), 5_000, "beta"))
        assertEquals(30_000L to "alpha", s.apply(nav(), UiSnapshot.Overrides(), 5_000, "beta"))
        for (local in listOf(UiSnapshot.Overrides(), UiSnapshot.Overrides(rotate = true))) {
            val n = nav()
            val (rotate, _) = s.copy(rotationMs = 0).apply(n, local, 0, "")
            assertEquals(0L, rotate)
            assertTrue("zero rotation was enabled by a snapshot", n.paused)
        }
    }

    @Test
    fun gate() {
        val g = SnapshotGate()
        val s = UiSnapshot(5, "sites", "summary", null, false, 0)
        assertNotNull(g.accept("sess-a", "t1", s, 0)) // no request
        g.requested("t1", 3)
        assertNotNull(g.accept("sess-a", "t2", s, 3)) // another transfer
        assertNotNull(g.accept("sess-a", "t1", s, 4)) // a local change since the request
        assertNull(g.accept("sess-a", "t1", s, 3))
        assertNotNull(g.accept("sess-a", "t1", s, 3)) // replayed
        assertNotNull(g.accept("sess-a", "t1", s.copy(revision = 4), 3)) // older, delayed
        assertNull(g.accept("sess-a", "t1", s.copy(revision = 6), 3))
        assertNull(g.accept("sess-b", "t1", s.copy(revision = 1), 3)) // a new sender session is not compared
    }

    private fun body(s: UiSnapshot) = "{\"snapshot\":${s.encode()}}"

    @Test
    fun bridgeCommitAndFences() {
        var woken = 0
        val b = SnapshotBridge { woken++ }
        var s = UiSnapshot.export(nav(), "alpha", 12_000, 4)
        val body = body(s)
        assertFalse(b.receive("session-a", "t1", body)) // unsolicited
        b.requested("session-a", "t1")
        for ((session, transfer, bad) in listOf(
            Triple("session-b", "t1", body), Triple("session-a", "t2", body),
            Triple("session-a", "t1", "{\"snapshot\":{}}"), Triple("session-a", "t1", "{\"snapshot\":{},\"extra\":true}"),
            Triple("session-a", "t1", " ".repeat(UiSnapshot.MAX_BYTES + 33)),
        )) assertFalse(b.receive(session, transfer, bad))
        assertTrue(b.receive("session-a", "t1", body))
        assertNull(b.take()) // before the release proof
        assertFalse(b.receive("session-a", "t1", body)) // replay
        s = s.copy(revision = s.revision + 1)
        assertTrue(b.receive("session-a", "t1", body(s)))
        b.finish(true)
        assertEquals(1, woken)
        assertEquals(s.revision, b.take()?.revision)
        assertNull(b.take()) // once
        assertFalse(b.receive("session-a", "t1", body(s.copy(revision = s.revision + 1)))) // late, after the proof

        // A local edit after the request, even after staging, wins.
        b.requested("session-a", "t2")
        assertTrue(b.receive("session-a", "t2", body))
        b.edited()
        b.finish(true)
        assertNull(b.take())
        b.requested("session-a", "t3")
        b.edited()
        assertFalse(b.receive("session-a", "t3", body))

        // A retired connection drops what was staged, even after the proof.
        b.requested("session-a", "t4")
        assertTrue(b.receive("session-a", "t4", body))
        b.finish(true)
        b.finish(false)
        assertNull(b.take())
        b.requested("session-b", "t5")
        assertFalse(b.receive("session-a", "t5", body))
        assertTrue(b.receive("session-b", "t5", body(s.copy(revision = 1))))
    }

    @Test
    fun bridgePublishesRevisionsOnlyOnChange() {
        val b = SnapshotBridge()
        assertNull(b.export())
        val n = nav()
        b.publish(UiSnapshot.export(n, "alpha", 12_000, 0))
        val first = b.export()!!
        assertEquals(1L, first.revision)
        b.publish(UiSnapshot.export(n, "alpha", 12_000, 0))
        assertEquals(first.revision, b.export()!!.revision) // a poll is not a change
        n.move(1)
        b.edited()
        b.publish(UiSnapshot.export(n, "alpha", 12_000, 0))
        val changed = b.export()!!
        assertEquals(first.revision + 1, changed.revision)
        assertTrue(changed.pageId != first.pageId)
    }
}
