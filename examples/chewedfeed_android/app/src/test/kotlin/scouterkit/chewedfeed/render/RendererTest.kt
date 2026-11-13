package scouterkit.chewedfeed.render

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import scouterkit.chewedfeed.ArmLink
import scouterkit.chewedfeed.Frame
import scouterkit.chewedfeed.GlassesBridge
import scouterkit.chewedfeed.dashboard.Notification
import scouterkit.chewedfeed.dashboard.Page

private fun Byte.u() = toInt() and 0xFF

// Departure Mono at a scale, as the glasses load it: metrics and its one advance (it is monospaced, no kerning).
internal fun monoFont(scale: Double): Pair<FontMetrics, Int> {
    val b = FontScale.scale(departureMono(), scale)
    check(b[10].u() == 0 && b[11].u() == 0)
    return FontMetrics(b[12].u(), b[13].u(), b[14].u()) to b[16 + 6].u()
}

// Records what is drawn, as boxes: text from baseline - ascent to baseline + descent.
private class Recorder(override val metrics: FontMetrics, private val advance: Int) : Surface {
    data class Box(val text: String?, val x0: Int, val y0: Int, val x1: Int, val y1: Int)
    val boxes = mutableListOf<Box>()
    override fun measure(text: String) = text.codePointCount(0, text.length) * advance
    override fun fill(x: Int, y: Int, w: Int, h: Int, gray: Int) {
        assertTrue("fill $w x $h", w >= 0 && h >= 0)
        boxes += Box(null, x, y, x + w, y + h)
    }
    override fun text(text: String, x: Int, baseline: Int, gray: Int) {
        boxes += Box(text, x, baseline - metrics.ascent, x + measure(text), baseline + metrics.descent)
    }
    val texts get() = boxes.mapNotNull { it.text }
}

class RendererTest {
    // dx, dy, scale -> x y w h | contentW statusX statusW | footerBaseline bodyStart rows, from the desktop
    // (go/examples/chewedfeed newScreenScale, emulated arms); the virtual display is 640+|dx| x 480-|dy|.
    private val desktop = listOf(
        Triple(0, 0, 2.0) to listOf(20, 40, 600, 400, 460, 461, 139, 250, 2, 5),
        Triple(-10, -8, 1.5) to listOf(25, 36, 600, 400, 488, 489, 111, 273, 1, 10),
        Triple(240, 0, 1.0) to listOf(248, 40, 384, 400, 293, 294, 90, 281, 1, 17),
        Triple(-240, 120, 3.0) to listOf(248, 20, 384, 320, 178, 179, 205, 121, 1, 1),
        Triple(0, 0, 0.5) to listOf(20, 40, 600, 400, 509, 510, 90, 304, 1, 40),
    )

    private fun layout(dx: Int, dy: Int, scale: Double): Pair<Recorder, Layout> {
        val (m, adv) = monoFont(scale)
        val r = Recorder(m, adv)
        return r to Layout(640 + Math.abs(dx), 480 - Math.abs(dy), dx, scale, m, r::measure)
    }

    @Test
    fun layoutIsTheDesktops() {
        for ((c, want) in desktop) {
            val l = layout(c.first, c.second, c.third).second
            assertEquals("$c", want, listOf(l.x, l.y, l.w, l.h, l.contentW, l.statusX, l.statusW, l.footerBaseline, l.bodyStart, l.rows))
        }
    }

    private val long = "Ünïcödé 世界 🚀 " + "a-very-long-unbroken-word-".repeat(20)
    private val page = Page("projects/x/0", long, List(60) { "Line $it $long" }, "STALE Fetched 12:34")

    @Test
    fun everythingStaysInItsRegion() {
        for ((c, _) in desktop) for (popup in listOf(null, Notification("Mail 📬", long, long.repeat(5)))) {
            val (rec, l) = layout(c.first, c.second, c.third)
            Renderer(rec, l).render(page, 98, 99, false, popup, Batteries(100, 7, null, 55)) { w -> "x".repeat(500).take(w) }
            for (b in rec.boxes) assertTrue("$c $b outside the canvas", b.x0 >= 0 && b.x1 <= l.w && b.y0 >= 0 && b.x1 >= b.x0)
            if (c.third == 3.0) continue // 320 px high at scale 3: the status panel runs into the footer, as on the desktop
            val footerTop = l.footerBaseline - l.m.ascent - 5
            for (b in rec.boxes.filter { it.text != null }) when {
                b.y1 > footerTop -> assertTrue("$c footer $b", b.y0 >= footerTop && b.x1 <= l.w - TEXT_INSET)
                b.x0 < l.statusX -> assertTrue("$c content $b", b.x1 <= l.statusX - 1 - TEXT_INSET) // clear of the divider
                else -> assertTrue("$c panel $b", b.x1 <= l.w)
            }
            val content = rec.boxes.filter { it.text != null && it.x0 == TEXT_INSET && it.y1 <= footerTop }
            if (popup == null) assertEquals("$c rows", l.rows + 1, content.size)
            else assertTrue("$c popup rows ${content.size}", content.size in 2..1 + minOf(POPUP_LINES, l.rows + l.bodyStart - 1))
            if (popup != null) assertTrue(rec.texts.none { it.startsWith("Ünïcödé") && it == content[0].text })
        }
    }

    @Test
    fun statusPanelIsTruthful() {
        val (rec, l) = layout(-10, -8, 1.5)
        val r = Renderer(rec, l)
        r.render(page, 0, 3, true, null, Batteries(80, null, 15, 55)) { "Fri 13 Nov 12:00" }
        assertTrue(rec.texts.containsAll(listOf("L ", "80%", "R ", "--%", "15%", "55%", "12:34", "Page 1/3", "Fri 13 Nov 12:00")))
        rec.boxes.clear()
        r.render(Page("system/cpu", "Stats", listOf("CPU: 3%"), "Unavailable"), 0, 1, false, null, Batteries()) { "" }
        assertEquals(4, rec.texts.count { it == "--%" })
        assertFalse(rec.texts.contains("12:34"))
    }

    @Test
    fun indicators() {
        for ((status, want) in listOf(
            "Fetched 12:30" to SourceIndicator("download", "12:30", false),
            "Cached 11:55" to SourceIndicator("cache", "11:55", false),
            "Sampled 12:30:05" to SourceIndicator("sample", "12:30:05", false),
            "STALE details / Fetched 12:30" to SourceIndicator("download", "12:30", true),
            "STALE / cache unavailable" to SourceIndicator("unknown", "--", true),
            "Unavailable" to SourceIndicator("unknown", "--", false),
            "Received 13 Nov 12:00" to SourceIndicator("bell", "13 Nov 12:00", false),
        )) assertEquals(status, want, sourceIndicator(status))
        assertEquals("STALE Fetched 12:30 / HTTP 503", footerStatus("STALE details / Fetched 12:30 / HTTP 503"))
        for ((name, rows) in STATUS_ICONS) assertTrue(name, rows.size in 1..11 && rows.all { it.length == rows[0].length })
        assertTrue(STATUS_ICONS.getValue("phone")[0].length <= STATUS_ICONS.getValue("computer")[0].length) // fits statusW
    }

    // The native side, faked: what GlassesScreen asks of a session.
    private class Bridge(val live: Long = 5) : GlassesBridge {
        val calls = mutableListOf<String>()
        override fun start(link: ArmLink?, left: String, right: String, dx: Int, dy: Int, brightness: Int) = live
        override fun lastError() = ""
        override fun cancel() {}
        override fun stop() {}
        override fun loadFont(generation: Long, scale: Int) = if (generation == live) { calls += "font $scale"; 0 } else -1
        override fun loadFontBlob(generation: Long, blob: ByteArray) = if (generation == live) { calls += "blob ${blob.size}"; 1 } else -1
        override fun fontMetrics(generation: Long, font: Int) = intArrayOf(17, 5, 21)
        override fun measure(generation: Long, font: Int, utf8: ByteArray) = String(utf8, Charsets.UTF_8).length * 11
        override fun canvas(generation: Long, width: Int, height: Int) = true.also { calls += "canvas $width $height" }
        override fun clear(generation: Long, gray: Int) = true.also { calls += "clear" }
        override fun fill(generation: Long, x: Int, y: Int, w: Int, h: Int, gray: Int) = true
        override fun text(generation: Long, font: Int, utf8: ByteArray, x: Int, baseline: Int, align: Int, gray: Int) = true.also { calls += "text $font" }
        override fun present(generation: Long, x: Int, y: Int) = true.also { calls += "present $x $y" }
        override fun blank(generation: Long) = true.also { calls += "blank" }
        override fun status(generation: Long): IntArray? = null
        override fun events(generation: Long) = IntArray(0)
    }

    @Test
    fun glassesScreenUsesTheSessionsResources() {
        val b = Bridge()
        val s = GlassesScreen.open(Frame(b, 5), 650, 472, -10, 1.5, ::departureMono)!!
        assertEquals("blob 32271", b.calls[0]) // the desktop's 1.5 blob size
        assertEquals("canvas 600 400", b.calls[1])
        assertTrue(s.show(page, 0, 1, false, null, Batteries()) { "" })
        assertTrue(b.calls.contains("text 1") && b.calls.last() == "present 25 36")
        b.calls.clear()
        assertTrue(s.hide())
        assertEquals(listOf("clear", "blank"), b.calls)
        GlassesScreen.open(Frame(b, 5), 640, 480, 0, 2.0) { error("integer scales use the native loader") }
        assertTrue(b.calls.contains("font 2"))
        assertNull(GlassesScreen.open(Frame(b, 4), 640, 480, 0, 2.0, ::departureMono)) // a stopped generation
    }
}
