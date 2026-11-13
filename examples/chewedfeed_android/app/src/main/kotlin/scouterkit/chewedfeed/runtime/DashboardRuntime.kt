package scouterkit.chewedfeed.runtime

import scouterkit.chewedfeed.DisplaySettings
import scouterkit.chewedfeed.GlassesStatus
import scouterkit.chewedfeed.api.ApiConfig
import scouterkit.chewedfeed.api.ApiSnapshot
import scouterkit.chewedfeed.bluetooth.Cancellable
import scouterkit.chewedfeed.bluetooth.Scheduler
import scouterkit.chewedfeed.dashboard.Dashboard
import scouterkit.chewedfeed.dashboard.DisplayOptions
import scouterkit.chewedfeed.dashboard.Navigation
import scouterkit.chewedfeed.dashboard.Page
import scouterkit.chewedfeed.dashboard.Screen
import scouterkit.chewedfeed.dashboard.View
import scouterkit.chewedfeed.notifications.FeedEvent
import scouterkit.chewedfeed.providers.batteries
import scouterkit.chewedfeed.render.Batteries
import scouterkit.chewedfeed.render.MusicStatus
import scouterkit.chewedfeed.render.PageFormatter
import scouterkit.chewedfeed.settings.AppSettings
import java.time.Instant
import java.time.ZoneId
import java.time.format.DateTimeFormatter
import java.util.Locale

// The Chewedfeed API, asynchronously: done(snapshot, or null when cancelled or failed) on the runtime's scheduler.
fun interface SitesSource {
    fun refresh(config: ApiConfig, done: (ApiSnapshot?) -> Unit): Cancellable
}

// The text area the pages are formatted for (Layout.textW and rows, the footer label's width, the font).
data class TextArea(val width: Int, val rows: Int, val footerWidth: Int, val measure: (String) -> Int)

// The standalone dashboard (laptop off): Sites from the API, AI and Stats as laptop-only placeholders, phone
// notifications, the music/clock footer and batteries, with the Dashboard controller deciding what shows (tilt,
// case, touches, rotation, popups). All calls on the scheduler's thread. stop() cancels every producer; after it
// nothing draws, whatever arrives late.
class DashboardRuntime(
    private val display: DisplaySettings,
    private val app: AppSettings,
    private val text: TextArea,
    private val scheduler: Scheduler,
    private val clock: () -> Long, // monotonic ms (rotation, popups)
    private val wall: () -> Long,  // epoch ms (the footer clock, received times)
    private val sites: SitesSource,
    private val draw: (Screen) -> Unit,
    private val zone: ZoneId = ZoneId.systemDefault(),
) {
    private val formatter = PageFormatter(zone)
    private val music = MusicStatus(zone)
    private val received = DateTimeFormatter.ofPattern("dd MMM HH:mm", Locale.ENGLISH)
    val dashboard = Dashboard(
        Navigation(listOf(if (app.sites) LOADING else off("sites", "Sites")), listOf(placeholder("ai", "AI usage")),
            listOf(placeholder("system", "System monitor"))),
        display.displayOptions(DisplayOptions(rotateMs = app.rotateSeconds * 1000L)), clock,
    ) { h -> h.pages(text.width, text.rows, text.measure) }
    var batteries = Batteries()
        private set
    private var generation = 0L
    private var running = false
    private var refreshing: Cancellable? = null
    private var nextRefresh: Cancellable? = null
    private var scroll: Cancellable? = null
    private var last: ApiSnapshot? = null
    private var online = true
    private var missed = false // a refresh was due while offline
    private var minute = -1L

    fun start(generation: Long) {
        this.generation = generation
        running = true
        dashboard.acquired(generation)
        if (app.sites) refresh()
        render()
    }

    fun stop() {
        running = false
        refreshing?.cancel(); nextRefresh?.cancel(); scroll?.cancel()
        refreshing = null; nextRefresh = null; scroll = null
        dashboard.released()
    }

    // The footer's middle label for a width (the renderer asks).
    fun footer(width: Int) = music.line(wall(), width, text.measure)

    // The glasses' status, sampled by the service: head angle, case, batteries; also the clock's minute.
    fun sampled(st: GlassesStatus, phone: Int?) {
        if (!running) return
        batteries = batteries(st, phone)
        dashboard.pitch(generation, st.pitch)
        st.left.inCase?.let { dashboard.caseChanged(generation, 0, it) }
        st.right.inCase?.let { dashboard.caseChanged(generation, 1, it) }
        val m = wall() / 60_000
        if (m != minute) { minute = m; dashboard.invalidate() }
        render()
    }

    fun touch(arm: Int, gesture: Int) {
        if (!running) return
        if (dashboard.touch(generation, arm, gesture)) render()
    }

    fun notification(e: FeedEvent) {
        if (!running) return
        when (e) {
            is FeedEvent.Show -> dashboard.notify(e.note, Instant.ofEpochMilli(wall()).atZone(zone).format(received), e.key)
            is FeedEvent.Dismiss -> dashboard.dismissed(e.key)
        }
        render()
    }

    // The playing title ("" none): scrolled and alternated with the clock at the desktop's cadence.
    fun title(t: String) {
        if (!running) return
        music.setTitle(t, wall())
        dashboard.invalidate()
        render()
        scroll?.cancel()
        scroll = if (t.isEmpty()) null else scheduler.postDelayed(MusicStatus.SCROLL_EVERY, ::scrolled)
    }

    private fun scrolled() {
        if (!running || music.title.isEmpty()) return
        if (music.advance(wall(), text.footerWidth, text.measure)) {
            dashboard.invalidate()
            render()
        }
        scroll = scheduler.postDelayed(MusicStatus.SCROLL_EVERY, ::scrolled)
    }

    // The network came or went: data shows stale while offline, and a refresh missed meanwhile runs at once.
    fun network(available: Boolean) {
        if (available == online) return
        online = available
        if (!running || !app.sites) return
        if (!available) showSites()
        else if (missed) refresh()
    }

    private fun refresh() {
        nextRefresh?.cancel()
        nextRefresh = null
        if (!online) {
            missed = true
            showSites()
            return schedule()
        }
        missed = false
        val config = runCatching { app.apiConfig() }.getOrNull() ?: return run {
            dashboard.nav.replace(View.SITES, listOf(Page("sites/settings", "Chewedfeed", listOf("Check the API settings"), "Unavailable")))
            render()
        }
        // One request at a time: the next is scheduled only after this one answered (the API client's own time
        // limit makes sure it does).
        refreshing = sites.refresh(config) { snapshot ->
            if (!running) return@refresh
            refreshing = null
            if (snapshot != null) last = snapshot
            showSites()
            schedule()
        }
    }

    private fun schedule() {
        nextRefresh = scheduler.postDelayed(app.refreshSeconds * 1000L) { if (running) refresh() }
    }

    private fun showSites() {
        val s = last ?: return
        // Offline: what was fetched stays, marked stale (the desktop's STALE for failed sources).
        val sources = if (online) s.sources else s.sources.map { if (it.err.isEmpty()) it.copy(err = "offline") else it }
        val pages = formatter.sites(sources, text.width, text.rows, text.measure)
        dashboard.nav.replace(View.SITES, if (s.notice.isEmpty()) pages else listOf(Page("sites/notice", "Chewedfeed", listOf(s.notice), "Unavailable")) + pages)
        render()
    }

    private fun render() {
        if (!running) return
        dashboard.poll()?.let(draw)
    }

    private fun placeholder(id: String, title: String) =
        if (id == "ai" && !app.ai || id == "system" && !app.stats) off(id, title)
        else Page("$id/laptop", title, listOf("Shown from the laptop only"), "Unavailable")

    companion object {
        val LOADING = Page("sites/loading", "Chewedfeed", listOf("Fetching…"), "Unavailable")
        fun off(id: String, title: String) = Page("$id/off", title, listOf("Turned off in Settings"), "Unavailable")
    }
}
