package scouterkit.chewedfeed.handover

import scouterkit.chewedfeed.dashboard.Page
import scouterkit.chewedfeed.render.AiUsage
import scouterkit.chewedfeed.render.UsageWindow
import scouterkit.chewedfeed.render.aiPages
import scouterkit.chewedfeed.render.fixed
import java.time.Instant
import java.time.ZoneId
import java.time.format.DateTimeFormatter
import java.util.Locale

// The laptop's shared readings (CF-56: AI usage, aggregate system figures), the last good one per source, in memory
// only. A reading counts only when it is newer (revision) within the laptop's connection session; its age is the
// sender's plus the time since it arrived on this phone's monotonic clock (policy.json: never the wall clock), so a
// laptop that went to sleep shows stale data, never live-looking readings. Ownership never depends on it.
class RemoteTelemetry(private val clock: () -> Long) {
    class Reading(
        val source: String, val revision: Long, val session: String, val status: String,
        val sentAgeMs: Long, val receivedAt: Long, val values: Any?,
    )

    private val last = HashMap<String, Reading>()
    private var enabled = setOf("ai", "system")

    // The sources this phone shows (Settings: AI usage, Stats); a disabled one is cleared and ignored.
    @Synchronized fun enable(ai: Boolean, system: Boolean) {
        enabled = setOfNotNull("ai".takeIf { ai }, "system".takeIf { system })
        last.keys.retainAll(enabled)
    }

    // A new laptop connection: if it does not share telemetry, what was imported is cleared (the laptop stopped
    // sharing); otherwise the last readings stay until newer ones arrive.
    @Synchronized fun linkUp(shares: Boolean) {
        if (!shares) last.clear()
    }

    // A telemetry message's body from the laptop's checked link (session: its connection session). False: ignored
    // (disabled source, older or repeated revision).
    @Synchronized fun accept(session: String, body: Map<String, Any?>): Boolean {
        val t = body["telemetry"] as? Map<*, *> ?: return false
        val source = t["source"] as? String ?: return false
        if (source !in enabled) return false
        val revision = (t["revision"] as? JsonNumber)?.long ?: return false
        val prev = last[source]
        if (prev != null && prev.session == session && revision <= prev.revision) return false
        last[source] = Reading(source, revision, session, t["status"] as? String ?: "unavailable",
            (t["age_ms"] as? JsonNumber)?.long ?: 0, clock(), t["values"])
        return true
    }

    @Synchronized fun get(source: String): Reading? = last[source]

    fun ageMs(r: Reading) = r.sentAgeMs + maxOf(0, clock() - r.receivedAt)

    fun stale(r: Reading) = r.status != "fresh" || ageMs(r) >= if (r.source == "ai") Policy.AI_STALE_MS else Policy.TELEMETRY_STALE_MS

    // The AI view, as the desktop shows it (ai_usage.go aiUsagePages): one page per provider, its windows with used
    // and reset, "Cached" with the reading's time on this phone's clock, STALE when old. null: nothing received.
    fun aiView(nowMs: Long, zone: ZoneId = ZoneId.systemDefault()): List<Page>? {
        val r = get("ai") ?: return null
        if (r.status == "unavailable" || r.values !is List<*>) {
            return listOf(Page("ai/laptop", "AI usage", listOf("No readable usage cache on the laptop", "Run ai-usagebar there to refresh usage"), "Unavailable"))
        }
        val at = nowMs - ageMs(r)
        val windows = r.values.filterIsInstance<Map<*, *>>()
        return listOf("codex" to "Codex usage", "claude" to "Claude Code usage").mapNotNull { (provider, title) ->
            val ws = windows.filter { it["provider"] == provider }.map { w ->
                UsageWindow(
                    when (w["window"]) { "short" -> "5h"; "weekly" -> "Weekly"; else -> "Other" },
                    (w["used_percent"] as? JsonNumber)?.double,
                    (w["reset_unix_ms"] as? JsonNumber)?.long,
                )
            }
            if (ws.isEmpty()) null else AiUsage("ai/$provider", title, at, ws)
        }.let { providers ->
            val pages = aiPages(providers, emptyList(), nowMs, zone)
            if (stale(r)) pages.map { if (it.status.startsWith("STALE")) it else it.copy(status = "STALE ${it.status}") } else pages
        }.ifEmpty { listOf(Page("ai/laptop", "AI usage", listOf("No usage windows reported"), "Unavailable")) }
    }

    // The Stats view: the laptop's aggregate figures (no processes: the laptop does not send them, and this phone
    // cannot read other apps' either). Its battery is the laptop's, labelled so; the status panel shows the phone's.
    fun statsView(nowMs: Long, zone: ZoneId = ZoneId.systemDefault()): List<Page>? {
        val r = get("system") ?: return null
        val v = r.values as? Map<*, *>
        if (r.status == "unavailable" || v == null) return listOf(Page("system/cpu", "Laptop stats", listOf("System information unavailable"), "Unavailable"))
        fun gib(k: String) = ((v[k] as? JsonNumber)?.double ?: 0.0) / (1024.0 * 1024 * 1024)
        val cpu = (v["cpu_percent"] as? JsonNumber)?.double
        val battery = (v["battery_percent"] as? JsonNumber)?.long
        val time = Instant.ofEpochMilli(nowMs - ageMs(r)).atZone(zone).format(DateTimeFormatter.ofPattern("HH:mm:ss", Locale.ENGLISH))
        return listOf(Page("system/cpu", "Laptop stats", listOf(
            "CPU: " + if (cpu == null) "sampling" else fixed(cpu, 1) + "%",
            "RAM: ${fixed(gib("memory_used_bytes"), 1)}/${fixed(gib("memory_total_bytes"), 1)}G",
            "Laptop battery: " + if (battery == null) "--%" else "$battery%",
        ), (if (stale(r)) "STALE " else "") + "Sampled $time"))
    }
}
