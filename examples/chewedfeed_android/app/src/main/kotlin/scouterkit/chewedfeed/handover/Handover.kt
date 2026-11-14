package scouterkit.chewedfeed.handover

// The ownership reducer of the laptop/phone handover contract (tests/handover: controller.schema.json,
// transitions.json, policy.json), the same rules as the Go one (go/examples/internal/handover/reduce.go):
// reduce(State, Event) gives the next State and the Effects to run, in order. Pure: no clock, randomness,
// Bluetooth or network. The caller injects a fresh transfer ID in Event.attemptNonce, measures timers on a
// monotonic clock and reports them as timeout events, and executes the effects.

object Policy {
    const val PEER_SUSPECT_MS = 15000L
    const val LAPTOP_STABILITY_MS = 3000L
    const val RELEASE_DEADLINE_MS = 5000L
    const val REQUEST_DEADLINE_MS = 2 * RELEASE_DEADLINE_MS // a request without its released is withdrawn (CF-58)
    const val ACQUIRE_DEADLINE_MS = 30000L
    const val PARTIAL_ARMS_MS = 2000L
    const val PHONE_COLD_START_MS = 2000L
    const val LAPTOP_COLD_START_MS = 0L
    const val RETRY_INITIAL_MS = 5000L
    const val RETRY_MAX_MS = 60000L
    const val PHONE_PROBE_EXTRA_MS = 2000L // added to the suspect and retry probe delays on the phone
    const val RETRY_JITTER_FRACTION = 0.2
    const val TELEMETRY_STALE_MS = 90000L // policy.json timers_ms.telemetry_stale (system readings)
    const val AI_STALE_MS = 300000L       // timers_ms.ai_stale

    // Delay before retry number `failures` (0 = the first): doubling from RETRY_INITIAL_MS to RETRY_MAX_MS,
    // plus jitter (0..1, the caller's random draw) times RETRY_JITTER_FRACTION. Reset failures on success.
    fun retryDelayMs(failures: Int, jitter: Double): Long {
        var d = RETRY_INITIAL_MS
        repeat(failures) { if (d < RETRY_MAX_MS) d *= 2 }
        d = minOf(d, RETRY_MAX_MS)
        return d + (d * RETRY_JITTER_FRACTION * jitter.coerceIn(0.0, 1.0)).toLong()
    }
}

enum class Role(val wire: String) { LAPTOP("laptop"), PHONE("phone") }

enum class Phase(val wire: String) {
    IDLE("idle"), REQUESTING("requesting"), YIELDING("yielding"), ACQUIRING("acquiring"),
    ACTIVE("active"), RECOVERING("recovering"), DEGRADED("degraded"),
}

// Why this host may hold the arms.
enum class Permission(val wire: String) {
    NONE("none"),
    STANDALONE("standalone"), // not enrolled: no peer to ask
    RELEASED("released"),     // the peer confirmed its release
    PROBE("probe"),           // bounded, non-preemptive try after the peer went silent
}

enum class ReleaseIntent(val wire: String) { NONE("none"), HANDOVER("handover"), ROLLBACK("rollback"), LOCAL_STOP("local_stop") }

data class Arms(val left: Boolean = false, val right: Boolean = false) {
    val both get() = left && right
    val any get() = left || right
}

// controller.schema.json's state; transfer null is JSON null.
data class State(
    val role: Role,
    val enrolled: Boolean,
    val eligible: Boolean = false,
    val pinned: Boolean = false,
    val phase: Phase = Phase.IDLE,
    val arms: Arms = Arms(),
    val rendererReady: Boolean = false,
    val generation: Long = 0,
    val transfer: String? = null,
    val peerReachable: Boolean = false,
    val peerActive: Boolean = false,
    val permission: Permission = Permission.NONE,
    val releaseIntent: ReleaseIntent = ReleaseIntent.NONE,
    val peerEligible: Boolean = false,
    val peerPinned: Boolean = false,
)

enum class Timer(val wire: String) {
    STABILITY("stability"), RELEASE("release"), ACQUIRE("acquire"), PARTIAL_ARMS("partial_arms"),
    PEER_SUSPECT("peer_suspect"), RETRY("retry"), COLD_START("cold_start"),
}

// controller.schema.json's events; generation is the State generation the event belongs to (callbacks of an
// older one are ignored); attemptNonce is a fresh transfer ID in case the event starts a new attempt.
sealed class Event {
    abstract val generation: Long
    abstract val attemptNonce: String?

    data class Start(val eligible: Boolean, override val generation: Long = 0, override val attemptNonce: String? = null) : Event()
    data class Availability(val eligible: Boolean, override val generation: Long, override val attemptNonce: String? = null) : Event()
    data class PeerStatus(val active: Boolean, val reachable: Boolean, val eligible: Boolean, val pinned: Boolean,
                          override val generation: Long, override val attemptNonce: String? = null) : Event()
    // How long this host has been continuously eligible.
    data class PreferLocal(val stableMs: Long, override val generation: Long, override val attemptNonce: String? = null) : Event()
    data class ReleaseRequest(val transfer: String, val reason: String, val target: Role, val peerPinned: Boolean,
                              override val generation: Long, override val attemptNonce: String? = null) : Event()
    // The peer let the arms go.
    data class Released(val transfer: String, override val generation: Long, override val attemptNonce: String? = null) : Event()
    data class ReleaseComplete(val bothDown: Boolean, val workersStopped: Boolean, val reconnectDisabled: Boolean,
                               override val generation: Long, override val attemptNonce: String? = null) : Event()
    data class ArmsChanged(val left: Boolean, val right: Boolean, override val generation: Long, override val attemptNonce: String? = null) : Event()
    data class Initialized(val rendererReady: Boolean, override val generation: Long, override val attemptNonce: String? = null) : Event()
    data class Timeout(val timer: Timer, val elapsedMs: Long, override val generation: Long, override val attemptNonce: String? = null) : Event()
    data class RouteLost(override val generation: Long, override val attemptNonce: String? = null) : Event()
    data class RouteRestored(override val generation: Long, override val attemptNonce: String? = null) : Event()
    data class Pin(val enabled: Boolean, override val generation: Long, override val attemptNonce: String? = null) : Event()
    data class ClockAdjusted(val wallDeltaMs: Long, override val generation: Long, override val attemptNonce: String? = null) : Event()
    data class Failed(val reason: String, override val generation: Long, override val attemptNonce: String? = null) : Event()
    data class Stop(override val generation: Long, override val attemptNonce: String? = null) : Event()
    // The peer is releasing for us.
    data class ReleaseNotice(val transfer: String, override val generation: Long, override val attemptNonce: String? = null) : Event()
}

enum class EffectKind(val wire: String) {
    SEND_REQUEST("send_request"), SEND_RELEASE("send_release"), SEND_RELEASED("send_released"),
    SEND_ACQUIRING("send_acquiring"), SEND_READY("send_ready"), SEND_ABORT("send_abort"),
    PUBLISH_STATUS("publish_status"), ACQUIRE_BOTH("acquire_both"), RELEASE_BOTH("release_both"),
    FORCE_DISCONNECT("force_disconnect"), REDRAW("redraw"), RECONCILE("reconcile"), SCHEDULE("schedule"),
    CANCEL_TIMERS("cancel_timers"), IGNORE("ignore"),
}

// controller.schema.json's effect: generation and transfer are the new State's; timer and delayMs only for SCHEDULE.
data class Effect(val kind: EffectKind, val generation: Long, val transfer: String?, val timer: Timer? = null, val delayMs: Long = 0)

data class Transition(val state: State, val effects: List<Effect>)
