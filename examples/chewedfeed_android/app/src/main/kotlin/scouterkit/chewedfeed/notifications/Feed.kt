package scouterkit.chewedfeed.notifications

import scouterkit.chewedfeed.dashboard.Notification

// A phone notification as the listener sees it, before anything is decided about it. No contents are logged.
data class Posted(
    val key: String,
    val packageName: String,
    val app: String, // the app's label
    val title: CharSequence?,
    val text: CharSequence?,
    val bigText: CharSequence? = null,
    val ongoing: Boolean = false,
    val groupSummary: Boolean = false,
    val foregroundService: Boolean = false,
    val secret: Boolean = false, // VISIBILITY_SECRET: the app asked for it never to show outside the phone's own UI
)

data class NotificationSettings(val enabled: Boolean = true, val excluded: Set<String> = emptySet())

sealed class FeedEvent {
    data class Show(val key: String, val note: Notification) : FeedEvent()
    data class Dismiss(val key: String) : FeedEvent()
}

// Decides which phone notifications reach the glasses, and cleans their text as the desktop does
// (go/examples/chewedfeed notifications.go notificationText). Bounded: a burst beyond maxBurst per windowMs is
// dropped, at most MAX_KEYS keys are remembered (for repeats and dismissals), and text is cut at MAX_TEXT.
class Feed(
    private val ownPackage: String,
    private val settings: () -> NotificationSettings,
    private val clock: () -> Long,
    private val maxBurst: Int = 10,
    private val windowMs: Long = 10_000,
) {
    private val shown = object : LinkedHashMap<String, Notification>(16, 0.75f, true) {
        override fun removeEldestEntry(eldest: MutableMap.MutableEntry<String, Notification>?) = size > MAX_KEYS
    }
    private val recent = ArrayDeque<Long>()

    fun posted(p: Posted): FeedEvent? {
        val s = settings()
        if (!s.enabled || p.packageName == ownPackage || p.packageName in s.excluded) return null
        // Ongoing (media, calls, downloads), foreground-service and group-summary entries are status, not news;
        // SECRET ones stay on the phone.
        if (p.ongoing || p.foregroundService || p.groupSummary || p.secret) return null
        val note = Notification(clean(p.app).replace('\n', ' ').ifEmpty { "Notification" },
            clean(p.title?.toString() ?: ""), clean((p.bigText ?: p.text)?.toString() ?: ""))
        if (note.summary.isEmpty() && note.body.isEmpty()) return null
        if (shown[p.key] == note) return null // re-posted unchanged (a sound or progress refresh)
        val now = clock()
        while (recent.isNotEmpty() && now - recent.first() >= windowMs) recent.removeFirst()
        if (recent.size >= maxBurst) return null
        recent.addLast(now)
        shown[p.key] = note
        return FeedEvent.Show(p.key, note)
    }

    // Only for keys that were shown: others never reached the glasses.
    fun removed(key: String): FeedEvent? = if (shown.remove(key) != null) FeedEvent.Dismiss(key) else null

    // Access revoked or the listener disconnected: nothing is remembered across it.
    fun reset() {
        shown.clear()
        recent.clear()
    }

    companion object {
        const val MAX_TEXT = 8192
        const val MAX_KEYS = 64
        private val BREAKS = Regex("(?i)<br\\s*/?>|</p\\s*>")
        private val TAGS = Regex("<[^>]*>")
        private val ENTITY = Regex("&(#[0-9]{1,7}|#[xX][0-9a-fA-F]{1,6}|[a-zA-Z]+);")
        private val NAMED = mapOf("amp" to "&", "lt" to "<", "gt" to ">", "quot" to "\"", "apos" to "'", "nbsp" to " ")

        // ponytail: the common named entities only (Go's html.UnescapeString knows all); others stay as written.
        private fun unescape(s: String) = ENTITY.replace(s) { m ->
            val e = m.groupValues[1]
            when {
                e.startsWith("#x") || e.startsWith("#X") -> e.substring(2).toIntOrNull(16)
                e.startsWith("#") -> e.substring(1).toIntOrNull()
                else -> null
            }?.takeIf { Character.isValidCodePoint(it) && it != 0 }?.let { String(Character.toChars(it)) } ?: NAMED[e] ?: m.value
        }

        fun clean(s0: String): String {
            var s = s0
            if (s.codePointCount(0, s.length) > MAX_TEXT) s = s.substring(0, s.offsetByCodePoints(0, MAX_TEXT)) + "..."
            s = unescape(TAGS.replace(BREAKS.replace(s, "\n"), ""))
            return s.split('\n').joinToString("\n") { line ->
                line.split(Regex("[\\s\\p{Cntrl}]+")).filter { it.isNotEmpty() }.joinToString(" ")
            }.trim()
        }
    }
}
