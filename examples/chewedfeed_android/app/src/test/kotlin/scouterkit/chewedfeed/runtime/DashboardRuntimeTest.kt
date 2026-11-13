package scouterkit.chewedfeed.runtime

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import scouterkit.chewedfeed.ArmStatus
import scouterkit.chewedfeed.DisplaySettings
import scouterkit.chewedfeed.GlassesStatus
import scouterkit.chewedfeed.api.ApiConfig
import scouterkit.chewedfeed.api.ApiSnapshot
import scouterkit.chewedfeed.bluetooth.Cancellable
import scouterkit.chewedfeed.bluetooth.Scheduler
import scouterkit.chewedfeed.dashboard.Notification
import scouterkit.chewedfeed.dashboard.Screen
import scouterkit.chewedfeed.dashboard.View
import scouterkit.chewedfeed.notifications.FeedEvent
import scouterkit.chewedfeed.render.SourceResult
import scouterkit.chewedfeed.settings.AppSettings
import java.time.ZoneOffset

// Fakes only: a manual clock and scheduler, an API source that answers when told.
class DashboardRuntimeTest {
    private var now = 1_763_035_200_000L // 2025-11-13 12:00 UTC
    private val timers = mutableListOf<Triple<Long, () -> Unit, BooleanArray>>()
    private val scheduler = object : Scheduler {
        override fun post(task: () -> Unit) = task()
        override fun postDelayed(ms: Long, task: () -> Unit): Cancellable {
            val cancelled = BooleanArray(1)
            timers += Triple(now + ms, task, cancelled)
            return object : Cancellable { override fun cancel() { cancelled[0] = true } }
        }
    }

    private fun advance(ms: Long) {
        val end = now + ms
        while (true) {
            val next = timers.filter { !it.third[0] && it.first <= end }.minByOrNull { it.first } ?: break
            timers.remove(next)
            now = next.first
            next.second()
        }
        now = end
    }

    private val requests = mutableListOf<Pair<ApiConfig, (ApiSnapshot?) -> Unit>>()
    private var cancels = 0
    private val sites = SitesSource { config, done ->
        requests += config to done
        object : Cancellable { override fun cancel() { cancels++ } }
    }
    private val drawn = mutableListOf<Screen>()
    private val text = TextArea(40, 8, 20) { it.codePointCount(0, it.length) }

    private fun runtime(app: AppSettings = AppSettings(), display: DisplaySettings = DisplaySettings()) =
        DashboardRuntime(display, app, text, scheduler, { now }, { now }, sites, { drawn += it }, ZoneOffset.UTC)

    private val shown get() = drawn.last() as Screen.Shown

    private fun status(pitch: Int? = 20, inCase: Boolean = false) =
        GlassesStatus(640, 480, pitch, 0, null, ArmStatus(true, true, 80, inCase), ArmStatus(true, true, 70, inCase))

    private val data = ApiSnapshot(listOf(
        SourceResult("summary", listOf(mapOf("projects" to 2.0, "customers" to 5.0, "paid_customers" to 1.0)), now),
        SourceResult("projects", listOf(mapOf("id" to "one", "slug" to "one", "name" to "First site", "stage" to "live")), now),
    ))

    @Test
    fun sitesFromTheApiThenRefreshedOnSchedule() {
        val r = runtime(AppSettings(refreshSeconds = 60, rotateSeconds = 0))
        r.start(1)
        assertEquals("Fetching…", shown.page.lines[0])
        assertEquals(1, requests.size)
        requests[0].second(data)
        assertEquals("Chewedfeed", shown.page.title)
        assertEquals(listOf("summary", "projects/one/0"), r.dashboard.nav.pages(View.SITES).map { it.id })
        assertEquals("Fetched 12:00", shown.page.status)
        advance(59_999)
        assertEquals(1, requests.size)
        advance(1)
        assertEquals(2, requests.size) // every refreshSeconds
        requests[1].second(null) // failed or cancelled: the last data stays
        assertEquals(2, r.dashboard.nav.pages(View.SITES).size)
    }

    @Test
    fun laptopOnlyViewsAndSwitchedOffSources() {
        val r = runtime(AppSettings(sites = false, stats = false))
        r.start(1)
        assertTrue(requests.isEmpty()) // Sites off: no API calls
        assertEquals("Turned off in Settings", r.dashboard.nav.pages(View.SITES)[0].lines[0])
        assertEquals("Shown from the laptop only", r.dashboard.nav.pages(View.AI)[0].lines[0])
        assertEquals("Turned off in Settings", r.dashboard.nav.pages(View.STATS)[0].lines[0])
    }

    @Test
    fun offlineShowsStaleAndRefreshesWhenBack() {
        val r = runtime(AppSettings(refreshSeconds = 60))
        r.start(1)
        requests[0].second(data)
        r.network(false)
        assertEquals("STALE Fetched 12:00", shown.page.status)
        advance(60_000)
        assertEquals(1, requests.size) // offline: no request
        r.network(true)
        assertEquals(2, requests.size) // the missed refresh runs at once
        requests[1].second(data)
        assertEquals("Fetched 12:00", shown.page.status)
    }

    @Test
    fun stopCancelsEverythingAndLateResultsDoNotDraw() {
        val r = runtime(AppSettings(refreshSeconds = 60))
        r.start(1)
        r.title("A song title far longer than the footer")
        val count = drawn.size
        r.stop()
        assertEquals(1, cancels) // the refresh in flight
        requests[0].second(data) // answers anyway
        r.sampled(status(), 50)
        r.notification(FeedEvent.Show("k", Notification("Mail", "Hi")))
        r.touch(1, 1)
        r.title("Another")
        advance(600_000) // no refresh, no scroll
        assertEquals(1, requests.size)
        assertEquals(count, drawn.size)
    }

    @Test
    fun oneRequestAtATime() {
        val r = runtime(AppSettings(refreshSeconds = 60))
        r.start(1)
        r.network(false); r.network(true) // a flap while the first request is in flight
        advance(600_000)
        assertEquals(1, requests.size) // no second request until it answers
        requests[0].second(data)
        assertEquals(2, r.dashboard.nav.pages(View.SITES).size)
        advance(60_000)
        assertEquals(2, requests.size)
    }

    @Test
    fun tiltCaseTouchNotificationsMusicAndBatteries() {
        val r = runtime(AppSettings(rotateSeconds = 0), DisplaySettings(showAbove = 9, showBelow = -30))
        r.start(1)
        requests[0].second(data)
        r.sampled(status(pitch = 0), 41) // level: hidden
        assertEquals(Screen.Hidden, drawn.last())
        r.sampled(status(pitch = 20), 41)
        assertEquals(80, r.batteries.left)
        assertEquals(41, r.batteries.host)
        r.touch(1, 1) // right single tap: next page
        assertEquals(1, shown.index)
        r.notification(FeedEvent.Show("k", Notification("Mail", "Hello")))
        assertEquals("Hello", shown.popup?.summary)
        assertEquals("Received 13 Nov 12:00", r.dashboard.nav.pages(View.NOTIFICATIONS)[0].status)
        r.notification(FeedEvent.Dismiss("k"))
        assertNull(shown.popup)
        r.sampled(status(inCase = true), 41) // in the case: hidden
        assertEquals(Screen.Hidden, drawn.last())
        assertEquals("Thu 13 Nov 12:00", r.footer(20))
        r.title("Short")
        assertEquals("Short", r.footer(20))
    }

    @Test
    fun rotationFollowsTheSetting() {
        val r = runtime(AppSettings(rotateSeconds = 5))
        r.start(1)
        requests[0].second(data)
        r.sampled(status(), null)
        assertEquals(0, shown.index)
        advance(5_000)
        r.sampled(status(), null) // the service samples every 500 ms; rotation happens on these ticks
        assertEquals(1, shown.index)
    }

    @Test
    fun navigationTravelsThroughTheBridge() {
        val r = runtime(AppSettings(rotateSeconds = 12, project = "one"))
        r.start(1)
        requests[0].second(data)
        r.sampled(status(), null)
        assertEquals("summary", r.bridge.export()?.pageId) // this phone's selection, for the laptop
        assertEquals("one", r.bridge.export()?.project)
        r.bridge.requested("laptop-session", "t1")
        val incoming = """{"snapshot":{"schema_version":1,"revision":9,"view":"sites","page_id":"projects/one/0","project":null,"rotation_paused":true,"rotation_ms":30000}}"""
        assertTrue(r.bridge.receive("laptop-session", "t1", incoming))
        r.bridge.finish(true) // the release proof: restored on the runtime's thread
        assertEquals(1, shown.index)
        assertTrue(shown.paused)
        assertEquals("projects/one/0", r.bridge.export()?.pageId)
        // A local touch after a request wins over a later transfer.
        r.bridge.requested("laptop-session", "t2")
        r.touch(0, 1)
        assertTrue(!r.bridge.receive("laptop-session", "t2", incoming))
    }
}
