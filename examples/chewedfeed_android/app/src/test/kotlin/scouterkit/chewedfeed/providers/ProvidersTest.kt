package scouterkit.chewedfeed.providers

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import scouterkit.chewedfeed.ArmStatus
import scouterkit.chewedfeed.GlassesStatus
import scouterkit.chewedfeed.render.Batteries
import scouterkit.chewedfeed.render.MusicStatus
import java.time.ZoneOffset

class ProvidersTest {
    @Test
    fun theFirstPlayingTitleWins() {
        assertEquals("", nowPlaying(emptyList()))
        assertEquals("", nowPlaying(listOf(Player("a", false, "Paused song"))))
        assertEquals("", nowPlaying(listOf(Player("a", true, null), Player("b", true, "  \n "))))
        assertEquals("Second", nowPlaying(listOf(Player("a", false, "First"), Player("b", true, "Second"), Player("c", true, "Third"))))
        assertEquals("Song – Live", nowPlaying(listOf(Player("a", true, "Song\u0000 –\nLive"))))
    }

    // Fake sessions: what is subscribed, and a way to change one.
    private class Fake(var playing: Boolean, var title: String?) {
        var listener: (() -> Unit)? = null
    }

    @Test
    fun watcherSubscribesOnlyTheCurrentSessions() {
        val titles = mutableListOf<String>()
        val w = Watcher<Fake>(
            subscribe = { f, changed -> f.listener = changed; { f.listener = null } },
            read = { f -> Player(f.hashCode().toString(), f.playing, f.title) },
            onTitle = { titles += it },
        )
        val a = Fake(true, "Track A")
        val b = Fake(false, "Track B")
        w.sessions(listOf(a, b))
        assertEquals(2, w.subscribed)
        assertEquals(listOf("Track A"), titles)
        a.playing = false; a.listener!!()  // paused: the clock
        b.playing = true; b.listener!!()   // the other player starts
        assertEquals(listOf("Track A", "", "Track B"), titles)
        b.title = null; b.listener!!()     // no metadata
        assertEquals("", titles.last())
        val c = Fake(true, "Track C")
        w.sessions(listOf(c))             // the player list changed: old callbacks removed
        assertEquals(null, a.listener)
        assertEquals(null, b.listener)
        assertEquals(1, w.subscribed)
        assertEquals("Track C", titles.last())
        w.sessions(emptyList())            // access revoked (no sessions readable)
        assertEquals("", titles.last())
        w.sessions(listOf(c))
        w.stop()
        assertEquals(null, c.listener)
        assertEquals(0, w.subscribed)
        assertEquals("", titles.last())
    }

    private fun status(leftConnected: Boolean, left: Int?, right: Int?, case: Int?) = GlassesStatus(640, 480, 0, 0, case,
        ArmStatus(true, leftConnected, left, false), ArmStatus(true, true, right, true))

    @Test
    fun batteriesAreUnknownRatherThanStale() {
        LastCase.clear()
        assertEquals(Batteries(0, null, null, 100), batteries(status(true, 0, 101, -1), 100)) // 0% is a reading; no case yet
        assertEquals(Batteries(80, 70, 55, 41), batteries(status(true, 80, 70, 55), 41))
        // Left gone: unknown, not 80. The case keeps its last reading once the glasses are out of it.
        assertEquals(Batteries(null, 70, 55, null), batteries(status(false, 80, 70, null), null))
        assertEquals(Batteries(80, 70, 55, 41, hostCharging = true), batteries(status(true, 80, 70, null), 41, phoneCharging = true))
        assertEquals(false, batteries(status(true, 80, 70, null), null, phoneCharging = true).hostCharging) // no phone reading: no bolt
        assertEquals(50, batteryPercent(50, 100))
        assertEquals(100, batteryPercent(255, 255))
        assertEquals(null, batteryPercent(-1, 100))
        assertEquals(null, batteryPercent(10, 0))
    }

    // The desktop's cadence: a too-wide title scrolls one character per 250 ms, alternates with the clock after
    // three full scrolls (and at least 5 s), shows the clock for 5 s, then the title again.
    @Test
    fun scrollingCadence() {
        val chars = { s: String -> s.codePointCount(0, s.length) }
        val m = MusicStatus(ZoneOffset.UTC)
        var now = 0L
        m.setTitle("ABCDEFGHIJ", now) // 10 + 3 gap = 13 steps per scroll, at width 5
        assertEquals("ABCDE", m.line(now, 5, chars))
        var changes = 0
        val lines = mutableListOf<String>()
        while (now < 13 * 3 * 250) {
            now += 250
            if (m.advance(now, 5, chars)) changes++
            lines += m.line(now, 5, chars)
        }
        assertEquals("BCDEF", lines[0])
        assertTrue(changes >= 13 * 3 - 1)
        assertEquals("Thu 1 Jan 00:00", lines.last()) // after three scrolls: the clock
        now += MusicStatus.TIME_DISPLAY
        assertTrue(m.advance(now, 5, chars))
        assertEquals("ABCDE", m.line(now, 5, chars)) // the title again, from the start
        assertTrue(m.setTitle("", now))
        assertEquals("Thu 1 Jan 00:00", m.line(now, 5, chars)) // nothing playing: the clock
    }

    @Test
    fun caseFromCaseEvents() {
        LastCase.clear()
        LastCase.event(1 or 4 or (73 shl 8)) // in the case, charging, 73 %
        val b = batteries(status(true, 80, 70, null), null)
        assertEquals(73, b.case)
        assertTrue(b.caseCharging)
        LastCase.event(1 or (0xFF shl 8)) // battery unknown: the last reading stays
        assertEquals(73, batteries(status(true, 80, 70, null), null).case)
        LastCase.event(40 shl 8) // out of the case, not charging, 40 %
        val c = batteries(status(true, 80, 70, null), null)
        assertEquals(40, c.case)
        assertEquals(false, c.caseCharging)
        LastCase.clear()
    }
}
