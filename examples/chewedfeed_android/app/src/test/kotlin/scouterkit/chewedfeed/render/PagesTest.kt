package scouterkit.chewedfeed.render

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import java.time.ZoneOffset

// go/examples/chewedfeed pages_test.go, ai_usage and system_usage semantics, with synthetic data.
class PagesTest {
    private val f = PageFormatter(ZoneOffset.UTC)
    private val runes = { s: String -> s.codePointCount(0, s.length) }
    private val now = 1_763_035_200_000L // 2025-11-13 12:00 UTC

    @Test
    fun onlyRequestedDataIsShown() {
        val data = listOf(
            SourceResult("summary", listOf(mapOf("projects" to 2.0, "customers" to 10.0, "paid_customers" to 3.0, "score" to 99.0, "deploys" to "excluded")), now),
            SourceResult("health", listOf(mapOf("message" to "excluded-health"))),
            SourceResult("customers", listOf(mapOf("name" to "excluded-customer", "email" to "excluded-email"))),
        )
        val pages = f.sites(data, 44, 10, runes)
        assertEquals(1, pages.size)
        assertEquals("Chewedfeed", pages[0].title)
        assertEquals("Projects: 2 / Customers: 10 / Paid customers: 3", pages[0].lines.joinToString(" / "))
        assertEquals("Fetched 12:00", pages[0].status)
        assertEquals("hello world", f.value("hello\u0000\u001b\nworld"))
    }

    @Test
    fun sitesStayOnOnePageWithPriorityFields() {
        val data = listOf(
            SourceResult("summary", listOf(mapOf("projects" to 2.0))),
            SourceResult("projects", listOf(
                mapOf("id" to "one", "slug" to "one", "name" to "First site", "stage" to "launched", "github_open_prs" to 3.0,
                    "attention_mode" to "fix", "description" to "Long description ".repeat(100), "health_score" to 80.0,
                    "task_velocity_score" to 42.0, "links" to listOf(mapOf("url" to "long-url".repeat(100)))),
                mapOf("id" to "two", "slug" to "two", "name" to "Second site", "stage" to "building"),
            ), now),
            SourceResult("one/uptime", listOf(mapOf("overall_status" to "up", "uptime_24h" to 1.0, "uptime_30d" to .999)), now),
            SourceResult("customers", listOf(
                mapOf("product_slug" to "one", "plan" to "pro"), mapOf("product_slug" to "one", "plan" to "free"), mapOf("product_slug" to "two"),
            ), now),
        )
        val pages = f.sites(data, 60, 8, runes)
        assertEquals(3, pages.size)
        assertEquals("First site", pages[1].title)
        assertEquals("projects/one/0", pages[1].id)
        val first = pages[1].lines.joinToString("\n")
        for (want in listOf("PRs: 3", "Action: fix", "Stage: launched", "Overall status: up", "Health: 80/100",
            "Uptime: 24h 100.00% / 30d 99.90%", "Velocity: 42/100", "Customers: 2")) assertTrue(want, first.contains(want))
        val second = pages[2].lines.joinToString("\n")
        for (want in listOf("PRs: unknown", "Action: unknown", "Stage: building", "Overall status: unknown", "Health: unknown",
            "Uptime: unknown", "Velocity: unknown", "Customers: 1")) assertTrue(want, second.contains(want))
        for (p in pages) {
            assertTrue(p.lines.size <= 8)
            assertTrue(p.lines.all { runes(it) <= 60 })
        }
    }

    @Test
    fun detailFailuresStayVisible() {
        val data = listOf(SourceResult("projects", listOf(mapOf("slug" to "one", "name" to "Site")), now), SourceResult("one/uptime", err = "HTTP 503"))
        val pages = f.sites(data, 44, 6, runes)
        assertEquals(1, pages.size)
        assertTrue(pages[0].status.startsWith("STALE"))
        assertEquals("Projects unavailable", f.sites(listOf(SourceResult("projects", err = "HTTP 500")), 44, 6, runes)[0].lines[0])
        assertEquals("STALE Unavailable", f.sites(listOf(SourceResult("projects", err = "HTTP 500")), 44, 6, runes)[0].status)
        assertEquals(listOf("No data available"), f.sites(emptyList(), 44, 6, runes)[0].lines)
    }

    @Test
    fun numbers() {
        for (key in listOf("uptime_24h", "uptime_30d")) for ((v, want) in listOf(1.0 to "100.00%", .999 to "99.90%", .01 to "1.00%", 0.0 to "0.00%"))
            assertEquals(want, f.fieldValue(key, v))
        assertEquals("1", f.fieldValue("github_activity_score", 1.0))
        assertEquals("2.5", f.fieldValue("x", 2.5))
        assertEquals("42.2 57.8 0.12 2 -0", listOf(fixed(42.25, 1), fixed(57.75, 1), fixed(0.125, 2), fixed(2.5, 0), fixed(-0.1, 0)).joinToString(" ")) // Go's Sprintf
        assertEquals("06 Nov 10:00", f.fieldValue("last_synced_at", "2026-11-06T10:00:00Z"))
    }

    @Test
    fun ticketCountsPackWholeColumns() {
        val data = listOf(
            SourceResult("projects", listOf(mapOf("slug" to "one", "name" to "Site"), mapOf("slug" to "two", "name" to "Other")), now),
            SourceResult("one/ticket-counts", listOf(mapOf("backlog" to 2.0, "columns" to listOf(
                mapOf("name" to "To do", "count" to 1.0), mapOf("name" to "In progress", "count" to 0.0), mapOf("name" to "Done", "count" to 6.0)))), now),
            SourceResult("two/ticket-counts", atMs = now), // 404: not on TrackLines
        )
        val pages = f.sites(data, 24, 20, runes)
        assertEquals(listOf("Backlog 2 | To do 1", "In progress 0 | Done 6"), pages[0].lines.takeLast(2))
        assertFalse(pages[1].lines.joinToString().contains("Backlog") || pages[1].status.startsWith("STALE"))
    }

    @Test
    fun aiUsage() {
        val hour = 3_600_000L
        val p = aiPages(listOf(
            AiUsage("ai/claude", "Claude Code usage", now - 60_000, listOf(UsageWindow("5h", 42.25, now + 2 * hour + 5 * 60_000), UsageWindow("Week", null, null))),
            AiUsage("ai/codex", "Codex usage", now - 10 * 60_000, listOf(UsageWindow("5h", 10.0, now - 5 * 60_000))),
            AiUsage("ai/other", "Other", null),
        ), emptyList(), now, ZoneOffset.UTC)
        assertEquals(listOf("5h: 42.2% used (57.8% left)", "Reset: 13 Nov 14:05 (2h 5m)", "Week: usage unknown", "Reset: unknown"), p[0].lines)
        assertEquals("Cached 11:59", p[0].status)
        assertEquals(listOf("5h: awaiting cache refresh"), p[1].lines)
        assertEquals("STALE Cached 11:50", p[1].status)
        assertEquals("Unavailable", p[2].status)
        val kept = aiPages(listOf(AiUsage("ai/claude", "Claude Code usage", null)), p, now, ZoneOffset.UTC)[0]
        assertEquals(p[0].lines to "STALE / cache unavailable", kept.lines to kept.status)
        assertEquals(listOf("<1m", "5m", "1h 0m", "2d 3h"), listOf(59_000L, 300_000L, hour, 51 * hour).map(::shortDuration))
    }

    @Test
    fun stats() {
        val g = 1024L * 1024 * 1024
        val s = SystemSample(now, 12.34, 8, 3 * g, 16 * g, 0, 2 * g, "0.50 0.40 0.30", listOf(
            ProcessUsage(10, "idle", null, 5L shl 20), ProcessUsage(20, "busy", 50.0, 100L shl 20), ProcessUsage(30, "big", 1.0, 900L shl 20)))
        val p = statsPages(s, false, 6, ZoneOffset.UTC)[0]
        assertEquals(listOf("CPU: 12.3% (8 cores)", "RAM: 3.0/16.0G  Swap: 0.0/2.0G", "Load: 0.50 0.40 0.30", "CPU%   RAM    PID NAME",
            "50.0   100M     20 busy", " 1.0   900M     30 big"), p.lines)
        assertEquals("Sampled 12:00:00", p.status)
        assertEquals("STALE Sampled 12:00:00", statsPages(s, true, 6, ZoneOffset.UTC)[0].status)
        assertEquals("Unavailable", statsPages(null, false, 6)[0].status)
        assertEquals(1, statsPages(s, false, 0, ZoneOffset.UTC)[0].lines.size)
    }

    @Test
    fun musicFooter() {
        val m = MusicStatus(ZoneOffset.UTC)
        assertEquals("Thu 13 Nov 12:00", m.line(now, 100, runes))
        assertTrue(m.setTitle("Short song", now))
        assertFalse(m.setTitle("Short song", now))
        assertEquals("Short song", m.line(now, 100, runes))
        assertFalse(m.advance(now + 1000, 100, runes))
        assertTrue(m.advance(now + MusicStatus.MIN_TITLE, 100, runes)) // the time for a while
        assertEquals("Thu 13 Nov 12:00", m.line(now + MusicStatus.MIN_TITLE, 100, runes))
        assertTrue(m.advance(now + MusicStatus.MIN_TITLE + MusicStatus.TIME_DISPLAY, 100, runes))
        assertEquals("Short song", m.line(now, 100, runes))
        m.setTitle("A song title far too long 🎵", now)
        assertEquals("A song titl", m.line(now, 11, runes))
        m.advance(now + 2 * MusicStatus.SCROLL_EVERY, 11, runes)
        assertEquals("song title ", m.line(now, 11, runes))
        assertTrue(m.line(now, 11, runes).let(runes) <= 11)
    }
}
