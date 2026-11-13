package scouterkit.chewedfeed.render

import java.time.Instant
import java.time.ZoneId
import java.time.format.DateTimeFormatter
import java.util.Locale

// The footer's middle: the playing title, scrolled when too wide, alternating with the date and time
// (go/examples/chewedfeed music.go). Pure: the caller supplies the title and the time (epoch ms).
class MusicStatus(private val zone: ZoneId = ZoneId.systemDefault()) {
    var title = ""
        private set
    private var showingTime = true
    private var titleStarted = 0L
    private var timeUntil = 0L
    private var nextScroll = 0L
    private var scrollOffset = 0
    private var completedScroll = 0

    // True when the title changed.
    fun setTitle(title: String, now: Long): Boolean {
        if (title == this.title) return false
        this.title = title
        showingTime = title.isEmpty()
        titleStarted = now
        timeUntil = 0
        nextScroll = now + SCROLL_EVERY
        scrollOffset = 0
        completedScroll = 0
        return true
    }

    // Moves the scroll and the title/time alternation on; true when the line changed.
    fun advance(now: Long, width: Int, measure: (String) -> Int): Boolean {
        if (title.isEmpty()) return false
        if (showingTime) {
            if (now < timeUntil) return false
            showingTime = false
            titleStarted = now
            nextScroll = now + SCROLL_EVERY
            scrollOffset = 0; completedScroll = 0
            return true
        }
        val scrolling = measure(title) > width
        var changed = false
        if (scrolling && now >= nextScroll) {
            val steps = ((now - nextScroll) / SCROLL_EVERY + 1).toInt()
            val cycle = title.codePointCount(0, title.length) + GAP
            val position = scrollOffset + steps
            completedScroll += position / cycle
            scrollOffset = position % cycle
            nextScroll += steps * SCROLL_EVERY
            changed = true
        }
        if (now >= titleStarted + MIN_TITLE && (!scrolling || completedScroll >= SCROLLS_BEFORE_TIME)) {
            showingTime = true
            timeUntil = now + TIME_DISPLAY
            return true
        }
        return changed
    }

    fun line(now: Long, width: Int, measure: (String) -> Int): String {
        if (title.isEmpty() || showingTime) return Instant.ofEpochMilli(now).atZone(zone).format(CLOCK)
        if (measure(title) <= width) return title
        val runes = title.codePoints().toArray().map { String(Character.toChars(it)) }
        var visible = 0
        while (visible < 4096 && measure("M".repeat(visible + 1)) <= width) visible++
        val cycle = runes.size + GAP
        val start = scrollOffset % cycle
        val stream = ArrayList<String>()
        while (stream.size < start + visible) { stream += runes; repeat(GAP) { stream += " " } }
        val out = StringBuilder()
        for (r in stream.subList(start, stream.size)) {
            if (measure(out.toString() + r) > width) break
            out.append(r)
        }
        return out.toString()
    }

    companion object {
        const val SCROLL_EVERY = 250L
        const val MIN_TITLE = 5_000L
        const val TIME_DISPLAY = 5_000L
        const val GAP = 3
        const val SCROLLS_BEFORE_TIME = 3
        private val CLOCK = DateTimeFormatter.ofPattern("EEE d MMM HH:mm", Locale.ENGLISH)
    }
}
