package scouterkit.chewedfeed.dashboard

// The desktop's display choices (go/examples/chewedfeed: -show-above, -show-below, -always-show-notifications,
// -rotate): the dashboard shows while the head pitch is above showAbove or below showBelow (degrees, + looking
// up); in between only notification popups show, if alwaysShowNotifications. rotateMs 0 starts paused.
data class DisplayOptions(
    val showAbove: Int = 9,
    val showBelow: Int = -30,
    val alwaysShowNotifications: Boolean = true,
    val rotateMs: Long = 12000,
    val popupMs: Long = 5000,
)

// What the glasses should show.
sealed class Screen {
    data object Hidden : Screen()
    data class Shown(val view: View, val page: Page, val index: Int, val total: Int, val paused: Boolean, val popup: Notification?) : Screen()
}

// Navigation and visibility for the glasses, without any I/O: the session feeds it arm events, head angles,
// notifications and ticks; poll() says what to show, only when that changed. Only the current ownership
// generation's events count; releasing cancels popups and rotation. Time comes from `clock` (monotonic ms).
class Dashboard(
    val nav: Navigation,
    private val options: DisplayOptions = DisplayOptions(),
    private val clock: () -> Long,
    // Formats the history as Notifications pages (the renderer knows the font and the rows).
    private val historyPages: (NotificationHistory) -> List<Page> = { NotificationHistory.EMPTY },
) {
    val history = NotificationHistory()
    var generation = 0L
        private set
    private val inCase = BooleanArray(2)
    private var level = false
    private var popup: Notification? = null
    private var popupKey: String? = null
    private var popupUntil = 0L
    private var nextRotation = 0L
    private var last: Screen? = null
    private val rotateMs = if (options.rotateMs > 0) options.rotateMs else 12000

    init {
        if (options.rotateMs <= 0) nav.paused = true
    }

    val visible get() = popupVisible && !level
    val popupVisible get() = !inCase[0] && !inCase[1]

    // This host owns the glasses (a new session): repaint in full, no popup from before.
    fun acquired(generation: Long) {
        this.generation = generation
        inCase.fill(false)
        level = false
        popup = null
        nextRotation = clock() + rotateMs
        last = null
    }

    // Ownership ended (released, suspended, lost): nothing more to show; pending popup and rotation dropped.
    fun released() {
        generation = 0
        popup = null
        last = null
    }

    // A classified touch from an arm (0 left, 1 right). False when ignored (another generation, in the case,
    // or a gesture that does nothing).
    fun touch(generation: Long, arm: Int, gesture: Int): Boolean {
        if (!current(generation) || !popupVisible) return false
        if (!nav.touch(gesture, arm == 0)) return false
        if (gesture == 1 || gesture == 2) popup = null
        nextRotation = clock() + rotateMs
        return true
    }

    fun caseChanged(generation: Long, arm: Int, isInCase: Boolean) {
        if (!current(generation) || arm !in 0..1 || inCase[arm] == isInCase) return
        inCase[arm] = isInCase
        if (!popupVisible) popup = null
        nextRotation = clock() + rotateMs
    }

    // The head's pitch in degrees, or null when the arm does not know it (then the dashboard stays on).
    fun pitch(generation: Long, degrees: Int?) {
        if (!current(generation)) return
        val now = degrees != null && degrees <= options.showAbove && degrees >= options.showBelow
        if (now != level) {
            level = now
            nextRotation = clock() + rotateMs
        }
    }

    // A phone notification: into the history, and a popup when the glasses would show it. key: its source
    // identity (an update replaces the old entry).
    fun notify(note: Notification, receivedLabel: String, key: String? = null) {
        history.add(note, receivedLabel, key)
        nav.replace(View.NOTIFICATIONS, historyPages(history))
        if (generation != 0L && (visible || options.alwaysShowNotifications && popupVisible)) {
            popup = note
            popupKey = key
            popupUntil = clock() + options.popupMs
        }
    }

    // The source dismissed a notification: out of the history, and its popup ends.
    fun dismissed(key: String) {
        if (popupKey == key) popup = null
        if (history.remove(key)) nav.replace(View.NOTIFICATIONS, historyPages(history))
    }

    // Forces the next poll to report the screen (e.g. the clock in the footer changed).
    fun invalidate() {
        last = null
    }

    // Advances time: ends a popup at its deadline, rotates when visible, unpaused and without a popup.
    fun tick() {
        val now = clock()
        if (popup != null && now >= popupUntil) {
            popup = null
            nextRotation = now + rotateMs
        }
        if (generation != 0L && !nav.paused && visible && popup == null && now >= nextRotation) {
            nav.move(1)
            nextRotation = now + rotateMs
        }
    }

    fun screen(): Screen {
        if (generation == 0L || !(visible || popup != null && popupVisible)) return Screen.Hidden
        val pages = nav.pages(nav.selected)
        return Screen.Shown(nav.selected, nav.page, nav.index(nav.selected), pages.size, nav.paused, popup)
    }

    // tick(), then the screen if it differs from the last one returned (null: send nothing).
    fun poll(): Screen? {
        tick()
        val s = screen()
        if (s == last) return null
        last = s
        return s
    }

    private fun current(generation: Long) = generation != 0L && generation == this.generation
}
