package scouterkit.chewedfeed.render

import scouterkit.chewedfeed.dashboard.Page
import scouterkit.chewedfeed.dashboard.fitLine
import java.time.Instant
import java.time.OffsetDateTime
import java.time.ZoneId
import java.time.format.DateTimeFormatter
import java.util.Locale

// Go's %.<n>f: the exact binary value rounded half to even (Java's String.format rounds 42.25 up to 42.3), and
// Go's "-0" for small negatives.
fun fixed(v: Double, n: Int): String {
    if (v.isNaN()) return "NaN"
    if (v.isInfinite()) return if (v > 0) "+Inf" else "-Inf"
    val t = java.math.BigDecimal(v).setScale(n, java.math.RoundingMode.HALF_EVEN).toPlainString()
    return if (java.lang.Double.doubleToRawLongBits(v) < 0 && !t.startsWith("-")) "-$t" else t
}

// One API source's latest data (go/examples/chewedfeed client.go result): rows are JSON objects as decoded (numbers
// as Double, strings, booleans, null, nested lists and maps); atMs is when it last succeeded (null: never); err is a
// sanitized error ("" when the last fetch worked). Filled by the API client (CF-43).
data class SourceResult(val key: String, val rows: List<Map<String, Any?>> = emptyList(), val atMs: Long? = null, val err: String = "")

// The Sites pages, as go/examples/chewedfeed pages.go makes them: only listed fields reach the glasses, so internal
// IDs, customer records and credentials never do.
class PageFormatter(private val zone: ZoneId = ZoneId.systemDefault()) {
    private val hhmm = DateTimeFormatter.ofPattern("HH:mm", Locale.ENGLISH)
    private val dayTime = DateTimeFormatter.ofPattern("dd MMM HH:mm", Locale.ENGLISH)

    fun value(v: Any?): String = when (v) {
        null -> ""
        is String -> v.split(Regex("[\\s\\p{Cntrl}]+")).filter { it.isNotEmpty() }.joinToString(" ")
        is Double -> fixed(v, 2)
        is Number -> fixed(v.toDouble(), 2)
        is Boolean -> if (v) "yes" else "no"
        else -> json(v)
    }

    private fun json(v: Any?): String = when (v) {
        null -> "null"
        is String -> "\"" + v.replace("\\", "\\\\").replace("\"", "\\\"") + "\""
        is Number, is Boolean -> v.toString()
        is Map<*, *> -> v.entries.sortedBy { it.key.toString() }.joinToString(",", "{", "}") { json(it.key.toString()) + ":" + json(it.value) }
        is List<*> -> v.joinToString(",", "[", "]") { json(it) }
        else -> json(v.toString())
    }

    fun fieldValue(key: String, v: Any?): String {
        if (v is Number) {
            val n = v.toDouble()
            if (key == "uptime_24h" || key == "uptime_30d") return fixed(n * 100, 2) + "%"
            if (n == Math.rint(n) && !n.isInfinite()) return fixed(n, 0)
            return fixed(n, 1)
        }
        if (v is String && key.endsWith("_at")) {
            runCatching { return OffsetDateTime.parse(v).atZoneSameInstant(zone).format(dayTime) }
        }
        return value(v)
    }

    fun sourceStatus(r: SourceResult): String {
        var status = if (r.atMs == null) "Unavailable" else "Fetched " + Instant.ofEpochMilli(r.atMs).atZone(zone).format(hhmm)
        if (r.err.isNotEmpty()) status = "STALE $status"
        return status
    }

    // {"backlog": 2, "columns": [{"name": "To do", "count": 1}, ...]} as ["Backlog 2", "To do 1", ...]; empty
    // without a TrackLines board.
    fun ticketCounts(row: Map<String, Any?>?): List<String> {
        fun count(v: Any?) = if (v is Number) fixed(v.toDouble(), 0) else "?"
        val columns = row?.get("columns") as? List<*> ?: emptyList<Any?>()
        if (row?.get("backlog") == null && columns.isEmpty()) return emptyList()
        return listOf("Backlog " + count(row?.get("backlog"))) +
            columns.filterIsInstance<Map<*, *>>().map { value(it["name"]) + " " + count(it["count"]) }
    }

    private fun sitePage(row: Map<String, Any?>, id: String, status0: String, data: List<SourceResult>, width: Int, rows: Int, measure: (String) -> Int): Page {
        var status = status0
        val title = value(row["name"]).ifEmpty { value(row["slug"]) }.ifEmpty { "Unnamed project" }
        fun field(r: Map<String, Any?>?, key: String) = fieldValue(key, r?.get(key)).ifEmpty { "unknown" }
        fun score(key: String) = if (row[key] == null) "unknown" else field(row, key) + "/100"
        val slug = value(row["slug"])
        var uptime: Map<String, Any?>? = null
        var tickets: Map<String, Any?>? = null
        var customers: Int? = null
        for (r in data) {
            val relevant = r.key == "$slug/uptime" || r.key == "$slug/ticket-counts" || r.key == "customers"
            if (slug.isEmpty() || !relevant) continue
            if (r.err.isNotEmpty() && !status.startsWith("STALE")) status = "STALE $status"
            if (r.atMs == null && r.err.isNotEmpty()) continue
            if (r.key == "$slug/uptime" && r.rows.isNotEmpty()) uptime = r.rows[0]
            if (r.key == "$slug/ticket-counts" && r.rows.isNotEmpty()) tickets = r.rows[0]
            if (r.key == "customers") customers = r.rows.count { it["product_slug"] == slug }
        }
        val uptimeText = if (uptime?.get("uptime_24h") != null || uptime?.get("uptime_30d") != null)
            "24h " + field(uptime, "uptime_24h") + " / 30d " + field(uptime, "uptime_30d") else "unknown"
        val lines = mutableListOf(
            "PRs: " + field(row, "github_open_prs"),
            "Action: " + field(row, "attention_mode"),
            "Stage: " + field(row, "stage"),
            "Overall status: " + field(uptime, "overall_status"),
            "Health: " + score("health_score"),
            "Uptime: $uptimeText",
            "Velocity: " + score("task_velocity_score"),
        )
        customers?.let { lines += "Customers: $it" }
        // Whole columns per row, so "In progress 0" never splits.
        for ((i, part) in ticketCounts(tickets).withIndex()) {
            if (i > 0 && measure(lines.last() + " | " + part) <= width) lines[lines.size - 1] = lines.last() + " | " + part
            else lines += part
        }
        val kept = lines.take(minOf(maxOf(1, rows), lines.size)).map { fitLine(it, width, measure) }
        return Page("projects/$id/0", title, kept, status)
    }

    fun sites(data: List<SourceResult>, width: Int, rows: Int, measure: (String) -> Int): List<Page> {
        val pages = mutableListOf<Page>()
        for (r in data) when (r.key) {
            "summary" -> {
                val row = r.rows.firstOrNull()
                val lines = listOf("projects" to "Projects", "customers" to "Customers", "paid_customers" to "Paid customers").map { (key, label) ->
                    fitLine("$label: " + fieldValue(key, row?.get(key)).ifEmpty { "unknown" }, width, measure)
                }
                pages += Page("summary", "Chewedfeed", lines.take(minOf(maxOf(1, rows), lines.size)), sourceStatus(r))
            }
            "projects" -> {
                if (r.rows.isEmpty()) {
                    pages += Page("projects", "Projects", listOf(if (r.err.isNotEmpty()) "Projects unavailable" else "No projects"), sourceStatus(r))
                }
                for ((i, row) in r.rows.withIndex()) {
                    val id = value(row["id"]).ifEmpty { value(row["slug"]) }.ifEmpty { "$i" }
                    pages += sitePage(row, id, sourceStatus(r), data, width, rows, measure)
                }
            }
        }
        return pages.ifEmpty { listOf(Page("summary", "Chewedfeed", listOf("No data available"), "Unavailable")) }
    }
}

// One AI provider's usage, as the desktop reads it from ai-usagebar's cache (ai_usage.go): updatedMs null when no
// readable cache. Filled by whatever supplies usage on this device; only these fields are shown.
data class UsageWindow(val name: String, val used: Double?, val resetMs: Long?)
data class AiUsage(val id: String, val title: String, val updatedMs: Long?, val windows: List<UsageWindow> = emptyList(), val extra: List<String> = emptyList())

fun shortDuration(ms: Long): String {
    if (ms < 60_000) return "<1m"
    val m = ms / 60_000
    if (m >= 24 * 60) return "${m / (24 * 60)}d ${m / 60 % 24}h"
    if (m >= 60) return "${m / 60}h ${m % 60}m"
    return "${m}m"
}

// aiUsagePages: a page per provider; an unreadable cache keeps the last good lines, marked stale.
fun aiPages(providers: List<AiUsage>, previous: List<Page>, nowMs: Long, zone: ZoneId = ZoneId.systemDefault()): List<Page> =
    providers.map { p ->
        val at = { ms: Long, pattern: String -> Instant.ofEpochMilli(ms).atZone(zone).format(DateTimeFormatter.ofPattern(pattern, Locale.ENGLISH)) }
        if (p.updatedMs == null) {
            val old = previous.firstOrNull { it.id == p.id && it.status != "Unavailable" }
            return@map if (old != null) Page(p.id, p.title, old.lines, "STALE / cache unavailable")
            else Page(p.id, p.title, listOf("No readable local usage cache", "Run ai-usagebar to refresh usage"), "Unavailable")
        }
        var status = "Cached " + at(p.updatedMs, "HH:mm")
        if (nowMs - p.updatedMs > 5 * 60_000) status = "STALE $status"
        val lines = mutableListOf<String>()
        for (w in p.windows) {
            if (w.resetMs != null && nowMs >= w.resetMs && p.updatedMs < w.resetMs) {
                lines += "${w.name}: awaiting cache refresh"
                if (!status.startsWith("STALE")) status = "STALE $status"
                continue
            }
            val used = w.used
            lines += if (used == null || used.isNaN() || used.isInfinite() || used < 0) "${w.name}: usage unknown"
            else "${w.name}: ${fixed(used, 1)}% used (${fixed(maxOf(0.0, 100 - used), 1)}% left)"
            var reset = "Reset: unknown"
            if (w.resetMs != null) {
                reset = "Reset: " + at(w.resetMs, "dd MMM HH:mm")
                if (nowMs < w.resetMs) reset += " (" + shortDuration(w.resetMs - nowMs) + ")"
            }
            lines += reset
        }
        if (p.windows.isEmpty()) lines += "No usage windows reported"
        Page(p.id, p.title, lines + p.extra, status)
    }

// A system sample (system_usage.go): null fields are what this device cannot read.
data class ProcessUsage(val pid: Int, val name: String, val cpu: Double?, val memory: Long)
data class SystemSample(
    val atMs: Long, val cpu: Double?, val cores: Int,
    val memoryUsed: Long, val memoryTotal: Long, val swapUsed: Long, val swapTotal: Long,
    val load: String, val processes: List<ProcessUsage> = emptyList(),
)

// systemPages: one page; stale when the last sample failed.
fun statsPages(sample: SystemSample?, stale: Boolean, rows: Int, zone: ZoneId = ZoneId.systemDefault()): List<Page> {
    if (sample == null) return listOf(Page("system/cpu", "System monitor", listOf("System information unavailable"), "Unavailable"))
    var status = "Sampled " + Instant.ofEpochMilli(sample.atMs).atZone(zone).format(DateTimeFormatter.ofPattern("HH:mm:ss", Locale.ENGLISH))
    if (stale) status = "STALE $status"
    fun percent(n: Double?) = if (n == null) "sampling" else fixed(n, 1) + "%"
    fun gib(n: Long) = n / (1024.0 * 1024 * 1024)
    val lines = mutableListOf(
        "CPU: ${percent(sample.cpu)} (${sample.cores} cores)",
        "RAM: ${fixed(gib(sample.memoryUsed), 1)}/${fixed(gib(sample.memoryTotal), 1)}G  Swap: ${fixed(gib(sample.swapUsed), 1)}/${fixed(gib(sample.swapTotal), 1)}G",
        "Load: ${sample.load}",
        "CPU%   RAM    PID NAME",
    )
    val sorted = sample.processes.sortedWith(compareByDescending<ProcessUsage> { it.cpu ?: -1.0 }.thenByDescending { it.memory }.thenBy { it.pid })
    for (p in sorted.take(5)) {
        if (lines.size >= rows) break
        val usage = if (p.cpu == null) "   -" else fixed(p.cpu, 1).padStart(4)
        lines += "$usage ${fixed(p.memory / (1024.0 * 1024), 0).padStart(5)}M ${p.pid.toString().padStart(6)} ${p.name}"
    }
    return listOf(Page("system/cpu", "System / top 5 CPU", lines.take(minOf(lines.size, maxOf(1, rows))), status))
}
