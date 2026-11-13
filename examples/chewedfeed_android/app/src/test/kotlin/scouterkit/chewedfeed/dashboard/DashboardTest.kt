package scouterkit.chewedfeed.dashboard

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

private fun pages(vararg ids: String) = ids.map { Page(it) }
private val chars: (String) -> Int = { it.codePointCount(0, it.length) }

class NavigationTest {
    // Ported from navigation_test.go.
    @Test
    fun touchRemembersPagesAcrossViews() {
        val n = Navigation(pages("a", "b", "c"), pages("codex", "claude"), pages("cpu", "memory"))
        n.touch(1, false)
        assertEquals("b", n.page.id)
        n.touch(2, false)
        assertEquals(View.AI, n.selected)
        assertEquals(0, n.index(View.AI))
        assertFalse(n.paused)
        n.touch(1, false)
        n.touch(2, true)
        assertEquals("b", n.page.id)
        n.touch(3, false)
        n.touch(2, false)
        assertEquals("claude", n.page.id)
        assertTrue(n.paused)
        n.touch(1, true)
        assertEquals("codex", n.page.id)
        for (left in listOf(false, true)) assertFalse(n.touch(4, left) || n.touch(5, left))
    }

    @Test
    fun refreshKeepsSelectionAndPageIdentity() {
        val n = Navigation(pages("a", "b"), pages("codex", "claude"), pages("cpu"))
        n.touch(1, false)
        n.touch(2, false)
        n.touch(1, false)
        n.replace(View.SITES, pages("b", "a"))
        n.replace(View.AI, pages("claude", "codex"))
        assertEquals(View.AI, n.selected)
        assertEquals("claude", n.page.id)
        assertEquals(0, n.index(View.SITES))
        n.move(1)
        assertEquals("codex", n.page.id)
        n.replace(View.AI, emptyList())
        assertEquals(2, n.pages(View.AI).size)
        // The selected page went away: the first page, no crash.
        n.replace(View.AI, pages("gemini"))
        assertEquals("gemini", n.page.id)
    }

    @Test
    fun viewCycleBothWaysAndTriple() {
        val n = Navigation(pages("site"), pages("ai"), pages("cpu", "memory"))
        for (want in listOf(View.AI, View.NOTIFICATIONS, View.STATS, View.SITES)) { n.touch(2, false); assertEquals(want, n.selected) }
        for (want in listOf(View.STATS, View.NOTIFICATIONS, View.AI, View.SITES)) { n.touch(2, true); assertEquals(want, n.selected) }
        n.touch(2, true)
        n.touch(1, false)
        n.touch(2, false)
        n.touch(2, true)
        assertEquals("memory", n.page.id)
        assertFalse(n.touch(3, true) || n.paused) // left triple does nothing
        n.touch(3, false)
        assertTrue(n.paused)
    }

    @Test
    fun viewsWithoutLaptopDataStayUsable() {
        val n = Navigation(pages("summary"), emptyList(), emptyList())
        n.touch(2, false)
        assertEquals("ai/unavailable", n.page.id)
        n.touch(1, false)
        assertEquals("ai/unavailable", n.page.id)
        assertFalse(n.select(View.STATS, "system/gpu"))
        assertEquals("stats/unavailable", n.page.id)
    }
}

class NotificationTest {
    @Test
    fun historyKeepsLatestFiveNewestFirst() {
        val h = NotificationHistory()
        for (i in 1..7) h.add(Notification("App", "Note $i"), "13 Nov 12:0$i")
        val p = h.pages(40, 4, chars)
        assertEquals(5, h.size)
        assertEquals(listOf("Note 7"), p.first().lines)
        assertEquals("Received 13 Nov 12:07", p.first().status)
        assertEquals(listOf("Note 3"), p.last().lines)
    }

    @Test
    fun longNotificationPaginatesAndPopupFits() {
        val h = NotificationHistory()
        val n = Notification("", "Build finished", "Detailed body text 世界 🙂 ".repeat(40))
        h.add(n, "now")
        val p = h.pages(25, 3, chars)
        assertTrue(p.size > 3)
        assertEquals("Notification", p.first().title)
        assertTrue(p.all { it.lines.size <= 3 && it.lines.all { l -> chars(l) <= 25 } })
        val lines = notificationLines(n, 25, 11, chars)
        assertEquals(11, lines.size)
        assertEquals("Build finished", lines.first())
        assertTrue(lines.last().endsWith("..."))
        // No split surrogate pair anywhere.
        assertTrue(lines.all { l -> l.indices.none { Character.isHighSurrogate(l[it]) && (it + 1 >= l.length || !Character.isLowSurrogate(l[it + 1])) } })
    }

    @Test
    fun fitLineAndWrap() {
        assertEquals("abc", fitLine("abc", 5, chars))
        assertEquals("ab...", fitLine("abcdefgh", 5, chars))
        assertEquals("..", fitLine("abcdefgh", 2, chars))
        assertEquals(listOf("hello", "world"), wrap("hello world", 7, chars))
        assertEquals(listOf("abcd", "efgh"), wrap("abcdefgh", 4, chars))
        assertEquals(listOf(""), wrap("", 4, chars))
    }
}

class DashboardTest {
    private var now = 1_000_000L
    private fun dashboard(options: DisplayOptions = DisplayOptions()) =
        Dashboard(Navigation(pages("a", "b", "c"), pages("codex"), pages("cpu")), options, { now }) { it.pages(40, 3, chars) }

    @Test
    fun rotationOnlyWhenVisibleUnpausedWithoutPopup() {
        val d = dashboard()
        d.acquired(1)
        assertTrue(d.poll() is Screen.Shown)
        now += 12000
        assertEquals("b", (d.poll() as Screen.Shown).page.id)
        d.pitch(1, 0) // level: dashboard off
        now += 30000
        assertEquals(Screen.Hidden, d.poll())
        assertEquals("b", d.nav.page.id)
        d.pitch(1, 20) // rotation due 12 s from here
        now += 11000
        d.notify(Notification("Mail", "Hi"), "now")
        now += 2000 // rotation due, popup still up
        val s = d.poll() as Screen.Shown
        assertEquals("b", s.page.id) // a popup holds rotation
        assertEquals("Hi", s.popup?.summary)
        d.nav.paused = true
        now += 60000
        assertEquals("b", (d.poll() as Screen.Shown).page.id)
    }

    @Test
    fun popupFiveSecondsReplacementAndClearOnNavigation() {
        val d = dashboard()
        d.acquired(1)
        d.notify(Notification("A", "First"), "now")
        now += 4999
        assertEquals("First", (d.poll() as Screen.Shown).popup?.summary)
        d.notify(Notification("A", "Second"), "now")
        now += 4999
        assertEquals("Second", (d.screen() as Screen.Shown).popup?.summary)
        now += 1
        assertNull((d.poll() as Screen.Shown).popup)
        d.notify(Notification("A", "Third"), "now")
        assertTrue(d.touch(1, 1, 1))
        assertNull((d.screen() as Screen.Shown).popup)
        assertEquals(3, d.history.size)
        assertTrue(d.nav.pages(View.NOTIFICATIONS).first().id.startsWith("notifications/3/"))
    }

    @Test
    fun angleBoundaries() {
        val d = dashboard()
        d.acquired(1)
        for ((pitch, shown) in listOf(10 to true, 9 to false, -30 to false, -31 to true, null to true)) {
            d.pitch(1, pitch)
            assertEquals("pitch $pitch", shown, d.visible)
        }
        // Level head: only popups, when the option allows.
        d.pitch(1, 0)
        d.notify(Notification("A", "Shown anyway"), "now")
        assertTrue(d.screen() is Screen.Shown)
        val strict = dashboard(DisplayOptions(alwaysShowNotifications = false))
        strict.acquired(1)
        strict.pitch(1, 0)
        strict.notify(Notification("A", "Not shown"), "now")
        assertEquals(Screen.Hidden, strict.screen())
    }

    @Test
    fun caseHidesAtOnceAndReopens() {
        val d = dashboard()
        d.acquired(1)
        d.notify(Notification("A", "x"), "now")
        d.poll()
        d.caseChanged(1, 1, true)
        assertEquals(Screen.Hidden, d.poll())
        assertFalse(d.touch(1, 0, 1)) // no navigation in the case
        d.notify(Notification("A", "in case"), "now")
        assertNull(d.poll()) // still hidden: nothing new to send
        d.caseChanged(1, 1, false)
        val s = d.poll() as Screen.Shown
        assertNull(s.popup)
    }

    @Test
    fun hiddenDoesNotRepeatFrames() {
        val d = dashboard()
        d.acquired(1)
        d.pitch(1, 0)
        assertEquals(Screen.Hidden, d.poll())
        repeat(100) { now += 1000; assertNull(d.poll()) }
        d.invalidate()
        assertEquals(Screen.Hidden, d.poll())
    }

    @Test
    fun ownershipChangeMidPopupAndStaleGenerations() {
        val d = dashboard()
        d.acquired(1)
        d.touch(1, 1, 1) // page b
        d.notify(Notification("A", "mid"), "now")
        d.released()
        assertEquals(Screen.Hidden, d.poll())
        assertFalse(d.touch(1, 1, 1)) // the old generation's events are ignored
        d.caseChanged(1, 0, true)
        d.pitch(1, 0)
        now += 60000
        d.poll()
        assertEquals("b", d.nav.page.id) // no rotation while released
        d.acquired(2)
        val s = d.poll() as Screen.Shown
        assertEquals("b", s.page.id) // the selected page is back
        assertNull(s.popup) // no stale popup
        assertFalse(d.touch(1, 1, 1))
        assertTrue(d.touch(2, 1, 1))
    }

    @Test
    fun rotateZeroStartsPaused() {
        val d = dashboard(DisplayOptions(rotateMs = 0))
        d.acquired(1)
        assertTrue(d.nav.paused)
        now += 120000
        assertEquals("a", (d.poll() as Screen.Shown).page.id)
        d.touch(1, 1, 3)
        now += 12000
        assertEquals("b", (d.poll() as Screen.Shown).page.id)
    }
}
