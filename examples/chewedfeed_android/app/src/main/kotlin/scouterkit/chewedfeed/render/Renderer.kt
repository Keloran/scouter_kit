package scouterkit.chewedfeed.render

import scouterkit.chewedfeed.Frame
import scouterkit.chewedfeed.dashboard.Notification
import scouterkit.chewedfeed.dashboard.Page
import scouterkit.chewedfeed.dashboard.fitLine
import scouterkit.chewedfeed.dashboard.notificationLines
import kotlin.math.abs
import kotlin.math.roundToInt

data class FontMetrics(val ascent: Int, val descent: Int, val lineSpacing: Int)

// What the renderer draws on: one font and a gray canvas (Frame on the glasses, a bitmap in tests).
interface Surface {
    val metrics: FontMetrics
    fun measure(text: String): Int
    fun fill(x: Int, y: Int, w: Int, h: Int, gray: Int)
    fun text(text: String, x: Int, baseline: Int, gray: Int) // left-aligned
}

// Percentages; null: not reported. host is this device (the phone).
data class Batteries(val left: Int? = null, val right: Int? = null, val case: Int? = null, val host: Int? = null)

// Original pixel icons (go/examples/chewedfeed footer.go), drawn directly so no emoji coverage is needed.
val STATUS_ICONS: Map<String, List<String>> = mapOf(
    "computer" to listOf(" ########### ", " #         # ", " #         # ", " #         # ", " #         # ", " ########### ", "     ###     ", "  #########  "),
    "phone" to listOf(" ####### ", "#       #", "# ##### #", "# #   # #", "# #   # #", "# #   # #", "# ##### #", "#   #   #", " ####### "),
    "case" to listOf("  #########  ", " #         # ", "#############", "#           #", "#     #     #", "#           #", " #         # ", "  #########  "),
    "glasses" to listOf("#           #", "#           #", " ##### ##### ", " #   ###   # ", " #   # #   # ", " #   # #   # ", " ##### ##### "),
    "refresh" to listOf("   #####     ", "  #     #  # ", " #      # ## ", " #      #### ", "             ", " ####      # ", " ## #      # ", " #  #     #  ", "     #####   "),
    "hand" to listOf("     #     ", "   # # #   ", "   # # # # ", "   # # # # ", "   ####### ", " # #     # ", " ###     # ", " #       # ", "  #     #  ", "   #####   "),
    "download" to listOf("    #    ", "    #    ", "    #    ", " #  #  # ", "  # # #  ", "   ###   ", "    #    ", "#       #", "#########"),
    "cache" to listOf("#########", "#       #", "#########", " #     # ", " # ### # ", " #     # ", " #     # ", " ####### "),
    "sample" to listOf("      ## ", "      ## ", "   ## ## ", "   ## ## ", "## ## ## ", "## ## ## ", "## ## ## ", "#########"),
    "warning" to listOf("     #     ", "    # #    ", "   # # #   ", "   # # #   ", "  #  #  #  ", "  #     #  ", " #   #   # ", "#         #", "###########"),
    "unknown" to listOf(" ##### ", "#     #", "      #", "    ## ", "   #   ", "       ", "   #   "),
    "bell" to listOf("    ###    ", "   #   #   ", "  #     #  ", "  #     #  ", "  #     #  ", " #       # ", " #       # ", "###########", "    # #    ", "     #     "),
)

const val TEXT_INSET = 4
const val LOW_BATTERY_AT = 20
private const val FETCH_GAP = 10 // px between the mode icon and the fetch time

private fun iconScale(m: FontMetrics) = maxOf(0.1, (m.ascent + m.descent) / 11.0)
private fun r(v: Double) = v.roundToInt() // Go's math.Round for the non-negative values here

fun iconSize(name: String, m: FontMetrics): Pair<Int, Int> {
    val icon = STATUS_ICONS.getValue(name)
    return r(icon[0].length * iconScale(m)) to r(icon.size * iconScale(m))
}

// The dashboard's place on the virtual display (newScreenScale): vw x vh is the library's virtual display, which
// already includes the eye offsets; dx only keeps the content clear of the side the shift crops.
class Layout(vw: Int, vh: Int, dx: Int, scale: Double, val m: FontMetrics, measure: (String) -> Int) {
    val w = maxOf(1, minOf(600, vw - 2 * abs(dx) - 16))
    val h = maxOf(1, minOf(400, vh - 40))
    val x = maxOf(abs(dx) + 8, (vw - w) / 2)
    val y = (vh - h) / 2
    val statusW: Int
    val contentW: Int
    val statusX: Int
    val footerBaseline = m.ascent + ((h / m.lineSpacing) - 1 - 2) * m.lineSpacing - 80
    val bodyStart = if (scale == 2.0) 2 else 1
    val rows: Int

    init {
        val fetchW = iconSize("download", m).first + measure(" 00:00") + 2 * TEXT_INSET // the Sites fetch time
        val batteryW = iconSize("computer", m).first + 2 * TEXT_INSET +
            maxOf(measure(" 100%"), measure(" 20% ") + iconSize("warning", m).first) // "100%", or low "20%" with its warning
        statusW = maxOf(90, w / 7, fetchW, batteryW)
        contentW = maxOf(1, w - statusW - 1)
        statusX = contentW + 1
        rows = maxOf(1, (footerBaseline - m.ascent - 8 - m.ascent - m.descent) / m.lineSpacing - bodyStart + 1)
    }
}

// footerStatus: the part of a status the footer shows (a stale mark and the newest time).
fun footerStatus(status: String): String {
    if (!status.startsWith("STALE")) return status
    for (marker in listOf("Fetched ", "Cached ", "Sampled ", "Received ")) {
        val i = status.lastIndexOf(marker)
        if (i >= 0) return "STALE " + status.substring(i)
    }
    return "STALE"
}

data class SourceIndicator(val icon: String, val text: String, val stale: Boolean)

fun sourceIndicator(status0: String): SourceIndicator {
    var status = footerStatus(status0)
    val stale = status.startsWith("STALE")
    status = status.removePrefix("STALE").trim()
    for ((prefix, icon) in listOf("Fetched " to "download", "Cached " to "cache", "Sampled " to "sample", "Received " to "bell")) {
        if (status.startsWith(prefix)) return SourceIndicator(icon, status.removePrefix(prefix), stale)
    }
    return SourceIndicator("unknown", if (status == "Unavailable" || status.isEmpty()) "--" else status, stale)
}

// Draws one dashboard frame (main.go render, footer.go): the page or a notification popup on the left, the
// status panel on the right, page number and footer label (music or time) along the bottom.
class Renderer(private val s: Surface, val layout: Layout) {
    private val m = s.metrics

    // footer: the middle label for a width (MusicStatus.line).
    fun render(page: Page, index: Int, total: Int, paused: Boolean, popup: Notification?, batteries: Batteries, footer: (Int) -> String) {
        val l = layout
        s.fill(0, 0, l.w, l.h, 0)
        fun line(text: String, n: Int) = s.text(fitLine(text, l.contentW - TEXT_INSET, s::measure), TEXT_INSET, m.ascent + n * m.lineSpacing, 255)
        if (popup == null) {
            line(page.title, 0)
            page.lines.take(l.rows).forEachIndexed { i, text -> line(text, i + l.bodyStart) }
        } else {
            line(popup.app.ifEmpty { "Notification" }, 0)
            notificationLines(popup, l.contentW - TEXT_INSET, l.rows + l.bodyStart - 1, s::measure).forEachIndexed { i, text -> line(text, i + 1) }
        }
        statusPanel(paused, page.status, batteries)
        navigationRow(index, total, footer)
    }

    private fun drawIcon(name: String, x: Int, baseline: Int, gray: Int): Int {
        val icon = STATUS_ICONS.getValue(name)
        val (w, h) = iconSize(name, m)
        val sc = iconScale(m)
        val top = baseline + minOf(m.descent, m.lineSpacing - m.ascent) - h
        for ((row, line) in icon.withIndex()) for ((col, c) in line.withIndex()) {
            if (c != '#') continue
            val x0 = r(col * sc); val x1 = r((col + 1) * sc)
            val y0 = r(row * sc); val y1 = r((row + 1) * sc)
            s.fill(x + x0, top + y0, x1 - x0, y1 - y0, gray)
        }
        return w
    }

    private fun text(t: String, x: Int, baseline: Int, gray: Int): Int {
        s.text(t, x, baseline, gray)
        return s.measure(t)
    }

    // The width the footer's middle label gets (MusicStatus scrolls a title wider than this).
    fun footerWidth() = (layout.w - TEXT_INSET) - (footerSplit() + TEXT_INSET)

    private fun footerSplit(): Int {
        val need = TEXT_INSET + s.measure("Page 99/99") + TEXT_INSET
        return minOf(maxOf(layout.w / 3, need), layout.w * 2 / 3)
    }

    private fun navigationRow(index: Int, total: Int, footer: (Int) -> String) {
        val l = layout
        val baseline = l.footerBaseline
        val top = baseline - m.ascent - 5
        val split = footerSplit()
        s.fill(split, top, 1, baseline + m.descent - top, 153)
        s.text(fitLine("Page ${index + 1}/$total", maxOf(0, split - TEXT_INSET), s::measure), TEXT_INSET, baseline, 153)
        var x = split + TEXT_INSET
        val limit = l.w - TEXT_INSET
        val label = footer(limit - x)
        val width = s.measure(label)
        if (width < limit - x) x += (limit - x - width) / 2
        s.text(fitLine(label, maxOf(0, limit - x), s::measure), x, baseline, 153)
    }

    private fun statusPanel(paused: Boolean, status: String, b: Batteries) {
        val l = layout
        val footerTop = l.footerBaseline - m.ascent - 5
        s.fill(l.statusX - 1, 0, 1, footerTop, 153)
        s.fill(0, footerTop, l.w, 1, 153)
        val (iconW, iconH) = iconSize("glasses", m)
        val iconBaseline = m.ascent + iconH + 2
        val rightBaseline = iconBaseline + 2 * m.lineSpacing
        val panelMid = rightBaseline + m.descent + 5
        val panelLower = (panelMid + footerTop) / 2
        s.fill(l.statusX, panelMid, l.w - l.statusX, 1, 153)
        s.fill(l.statusX, panelLower, l.w - l.statusX, 1, 153)

        drawIcon("glasses", l.statusX + (l.statusW - iconW) / 2, iconBaseline, 255)
        val x0 = l.statusX + TEXT_INSET
        percent(x0 + text("L ", x0, iconBaseline + m.lineSpacing, 255), b.left, iconBaseline + m.lineSpacing)
        percent(x0 + text("R ", x0, rightBaseline, 255), b.right, rightBaseline)

        // Case and this device: icon, then the battery, one row each.
        val caseBaseline = panelMid + (panelLower - panelMid - 2 * m.lineSpacing) / 2 + m.ascent
        percent(x0 + drawIcon("case", x0, caseBaseline, 255) + s.measure(" "), b.case, caseBaseline)
        val hostBaseline = caseBaseline + m.lineSpacing
        percent(x0 + drawIcon("phone", x0, hostBaseline, 255) + s.measure(" "), b.host, hostBaseline)

        // Rotation mode, and for fetched data (Sites download, AI cache) its time under it.
        val mode = if (paused) "hand" else "refresh"
        val src = sourceIndicator(status)
        val fetched = src.icon == "download" || src.icon == "cache"
        val bottom = footerTop - panelLower
        val (modeW, modeH) = iconSize(mode, m)
        val gap = minOf(FETCH_GAP, maxOf(0, bottom - modeH - m.lineSpacing))
        val content = modeH + if (fetched) gap + m.lineSpacing else 0
        val modeBaseline = panelLower + (bottom - content) / 2 + modeH - minOf(m.descent, m.lineSpacing - m.ascent)
        drawIcon(mode, l.statusX + (l.statusW - modeW) / 2, modeBaseline, 255)
        if (fetched) {
            val space = s.measure(" ")
            var x = l.statusX + (l.statusW - iconSize(src.icon, m).first - space - s.measure(src.text)) / 2
            val baseline = modeBaseline + gap + m.lineSpacing
            x += drawIcon(src.icon, x, baseline, 255) + space
            s.text(src.text, x, baseline, 255)
        }
    }

    // The percentage, with a warning icon when low.
    private fun percent(x: Int, p: Int?, baseline: Int) {
        val w = text(if (p == null) "--%" else "$p%", x, baseline, 255)
        if (p != null && p <= LOW_BATTERY_AT) drawIcon("warning", x + w + s.measure(" "), baseline, 255)
    }
}

// A session's dashboard screen on the glasses: the font at the configured scale and a canvas, both native
// resources of that session's generation (gone with it). Run on the session thread (GlassesSession.draw).
class GlassesScreen private constructor(private val frame: Frame, private val surface: FrameSurface, val renderer: Renderer) {
    fun show(page: Page, index: Int, total: Int, paused: Boolean, popup: Notification?, batteries: Batteries, footer: (Int) -> String): Boolean {
        renderer.render(page, index, total, paused, popup, batteries, footer)
        return surface.ok && frame.present(renderer.layout.x, renderer.layout.y)
    }

    fun measure(text: String) = surface.measure(text)

    // Head level, in the case, or released: black and off; the canvas is cleared so no text stays behind.
    fun hide(): Boolean = frame.clear(0) and frame.blank()

    private class FrameSurface(private val frame: Frame, private val font: Int, override val metrics: FontMetrics) : Surface {
        var ok = true
        override fun measure(text: String) = maxOf(0, frame.measure(font, text))
        override fun fill(x: Int, y: Int, w: Int, h: Int, gray: Int) {
            if (w > 0 && h > 0) ok = frame.fill(x, y, w, h, gray) && ok
        }
        override fun text(text: String, x: Int, baseline: Int, gray: Int) {
            if (text.isNotEmpty()) ok = frame.text(font, text, x, baseline, Frame.ALIGN_LEFT, gray) && ok
        }
    }

    companion object {
        // null when the generation stopped or the font did not load. blob: the built-in font, for fractional scales.
        fun open(frame: Frame, width: Int, height: Int, dx: Int, scale: Double, blob: () -> ByteArray): GlassesScreen? {
            val font = if (scale == Math.rint(scale)) frame.loadFont(scale.toInt()) else frame.loadFontBlob(FontScale.scale(blob(), scale))
            if (font < 0) return null
            val v = frame.fontMetrics(font) ?: return null
            val surface = FrameSurface(frame, font, FontMetrics(v[0], v[1], maxOf(1, v[2])))
            val layout = Layout(width, height, dx, scale, surface.metrics, surface::measure)
            if (!frame.canvas(layout.w, layout.h)) return null
            return GlassesScreen(frame, surface, Renderer(surface, layout))
        }
    }
}
