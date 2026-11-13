package scouterkit.chewedfeed.dashboard

// One page of a view: id is stable across refreshes, so the reading position survives them.
data class Page(val id: String, val title: String = "", val lines: List<String> = emptyList(), val status: String = "")

enum class View(val title: String) { SITES("Sites"), AI("AI"), NOTIFICATIONS("Notifications"), STATS("Stats") }

// The views and their reading positions (go/examples/chewedfeed navigation.go). Every view always has a page.
class Navigation(sites: List<Page>, ai: List<Page>, stats: List<Page>, var paused: Boolean = false) {
    private val pages = mutableMapOf(
        View.SITES to sites.ifEmpty { listOf(placeholder(View.SITES)) },
        View.AI to ai.ifEmpty { listOf(placeholder(View.AI)) },
        View.NOTIFICATIONS to NotificationHistory.EMPTY,
        View.STATS to stats.ifEmpty { listOf(placeholder(View.STATS)) },
    )
    private val index = View.entries.associateWith { 0 }.toMutableMap()
    var selected = View.SITES

    fun pages(view: View): List<Page> = pages.getValue(view)
    fun index(view: View): Int = index.getValue(view)
    val page get() = pages(selected)[index(selected)]

    // New pages for a view, keeping the selected page by id (else the first). An empty list changes nothing.
    fun replace(view: View, list: List<Page>) {
        if (list.isEmpty()) return
        val id = pages(view)[index(view)].id
        pages[view] = list
        index[view] = list.indexOfFirst { it.id == id }.coerceAtLeast(0)
    }

    // Selects a page by id in a view; false (first page) when it is gone.
    fun select(view: View, id: String): Boolean {
        selected = view
        val i = pages(view).indexOfFirst { it.id == id }
        index[view] = i.coerceAtLeast(0)
        return i >= 0
    }

    fun move(step: Int) {
        val n = pages(selected).size
        index[selected] = ((index(selected) + step) % n + n) % n
    }

    // A classified gesture from an arm (1 single, 2 double, 3 triple tap; the firmware sends no single tap
    // before a double). Single: page; double: view; right triple: pause rotation. False when it did nothing.
    fun touch(gesture: Int, left: Boolean): Boolean {
        val step = if (left) -1 else 1
        when (gesture) {
            1 -> move(step)
            2 -> selected = View.entries[((selected.ordinal + step) % View.entries.size + View.entries.size) % View.entries.size]
            3 -> if (left) return false else paused = !paused
            else -> return false
        }
        return true
    }

    companion object {
        // A view whose data source (the laptop, for AI and Stats) has nothing yet: still navigable.
        fun placeholder(view: View) = Page("${view.name.lowercase()}/unavailable", view.title, listOf("No data yet"), "Unavailable")
    }
}
