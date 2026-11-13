package scouterkit.chewedfeed.dashboard

data class Notification(val app: String, val summary: String, val body: String = "")

// The last five notifications, newest first, in memory only: never logged or stored.
class NotificationHistory {
    private class Entry(val id: Long, val note: Notification, val receivedLabel: String, val key: String?)

    private var sequence = 0L
    private val entries = ArrayDeque<Entry>()

    val size get() = entries.size

    // receivedLabel: the time shown in the status line ("13 Nov 12:30"). key: the source's identity for it (an
    // Android notification key); an update with the same key replaces the old entry.
    fun add(note: Notification, receivedLabel: String, key: String? = null) {
        if (key != null) remove(key)
        entries.addFirst(Entry(++sequence, note, receivedLabel, key))
        while (entries.size > LIMIT) entries.removeLast()
    }

    // The source dismissed it: gone from the history. False when it was not kept.
    fun remove(key: String): Boolean = entries.removeAll { it.key == key }

    // One or more pages per notification, ids stable while it is kept.
    fun pages(width: Int, rows: Int, measure: (String) -> Int): List<Page> {
        if (entries.isEmpty()) return EMPTY
        val r = maxOf(1, rows)
        return entries.flatMap { e ->
            val lines = text(e.note).split("\n").flatMap { wrap(it, width, measure) }
            lines.chunked(r).mapIndexed { i, chunk ->
                Page("notifications/${e.id}/$i", e.note.app.ifEmpty { "Notification" }, chunk, "Received ${e.receivedLabel}")
            }
        }
    }

    companion object {
        const val LIMIT = 5
        val EMPTY = listOf(Page("notifications/empty", "Notifications", listOf("No notifications yet"), "History empty"))

        fun text(n: Notification) = listOf(n.summary, n.body).filter { it.isNotEmpty() }.joinToString("\n")
    }
}

// The popup's lines: as many rows as fit, the last one ending in "..." when text was left out.
fun notificationLines(n: Notification, width: Int, rows: Int, measure: (String) -> Int): List<String> {
    val lines = mutableListOf<String>()
    for (paragraph in NotificationHistory.text(n).split("\n")) {
        lines += wrap(paragraph, width, measure)
        if (lines.size > rows) break
    }
    if (lines.size <= rows) return lines
    return lines.take(rows).toMutableList().also { it[rows - 1] = fitLine(it[rows - 1] + "...", width, measure) }
}
