package scouterkit.chewedfeed.service

// Where the glasses service stands. Only ACTIVE holds the glasses (an open native session and GATT links).
enum class Phase(val text: String) {
    STOPPED("Stopped"),
    STANDBY("Standby: another device has the glasses"),
    CONNECTING("Connecting to the glasses…"),
    ACTIVE("Connected to the glasses"),
    RETRY_WAIT("Glasses not reachable: trying again soon"),
    FAILED("Glasses not reachable: open the app to try again"),
    WAITING_BLUETOOTH("Waiting for Bluetooth"),
    NEEDS_PERMISSION("Needs the Nearby devices permission"),
    NEEDS_SETUP("Glasses not paired: pair them in the app"),
}

// What the controller asks of the service.
interface LifecycleActions {
    fun connect(attempt: Long)                 // open the session; report connected/failed with this attempt
    fun disconnect()                           // blank, close the session and GATT, cancel a connect in progress
    fun schedule(ms: Long, attempt: Long)      // call retryDue(attempt) after ms (replaces an earlier one)
    fun cancelRetry()
    fun phase(p: Phase, detail: String)        // the status notification and the app
    fun stopService()
    fun saveWanted(wanted: Boolean)            // survives process death
}

// The service's decisions, without Android: the user's start/stop (persisted, so a sticky restart after a stop
// stays stopped), Bluetooth, permission, pairing and ownership (this device should hold the glasses; the
// coordinator decides, and after process death only it can say so again). Connects only when all allow it;
// failures retry with a capped backoff, then wait for a new trigger. Every connect has an attempt number, so a
// result arriving after a disconnect is ignored.
class Lifecycle(private val a: LifecycleActions, private val maxAttempts: Int = 8) {
    var phase = Phase.STOPPED
        private set
    private var wanted = false
    private var bluetooth = false
    private var permission = false
    private var paired = false
    private var owner = false
    private var attempt = 0L      // the current connect; 0: none
    private var failures = 0

    // The app's Start / the notification's Stop. Stop holds until the next Start.
    fun userStart() {
        wanted = true
        failures = 0
        a.saveWanted(true)
        evaluate()
    }

    fun userStop() {
        wanted = false
        a.saveWanted(false)
        evaluate()
    }

    // The process came back (a sticky restart) with what was saved. Ownership starts false: the coordinator must
    // confirm it (reconcile with the peer) before this device connects again.
    fun restored(wanted: Boolean) {
        this.wanted = wanted
        owner = false
        evaluate()
    }

    fun conditions(bluetooth: Boolean, permission: Boolean, paired: Boolean) {
        val again = bluetooth && !this.bluetooth || paired && !this.paired || permission && !this.permission
        this.bluetooth = bluetooth; this.permission = permission; this.paired = paired
        if (again) failures = 0
        evaluate()
    }

    fun ownership(owner: Boolean) {
        if (owner && !this.owner) failures = 0
        this.owner = owner
        evaluate()
    }

    fun connected(attempt: Long) {
        if (attempt != this.attempt || phase != Phase.CONNECTING) return
        failures = 0
        set(Phase.ACTIVE)
    }

    // A connect failed or an active session was lost. setup: the arms need pairing again.
    fun failed(attempt: Long, setup: Boolean = false, why: String = "") {
        if (attempt != this.attempt || phase != Phase.CONNECTING && phase != Phase.ACTIVE) return
        drop()
        if (setup) {
            paired = false
            return set(Phase.NEEDS_SETUP, why)
        }
        failures++
        if (failures >= maxAttempts) return set(Phase.FAILED, why)
        val next = ++this.attempt
        a.schedule(backoff(failures), next)
        set(Phase.RETRY_WAIT, why)
    }

    fun retryDue(attempt: Long) {
        if (attempt != this.attempt || phase != Phase.RETRY_WAIT) return
        evaluate(force = true)
    }

    private fun evaluate(force: Boolean = false) {
        val blocked = when {
            !wanted -> Phase.STOPPED
            !permission -> Phase.NEEDS_PERMISSION
            !bluetooth -> Phase.WAITING_BLUETOOTH
            !paired -> Phase.NEEDS_SETUP
            !owner -> Phase.STANDBY
            else -> null
        }
        if (blocked != null) {
            drop()
            set(blocked)
            if (blocked == Phase.STOPPED) a.stopService()
            return
        }
        if (!force && (phase == Phase.CONNECTING || phase == Phase.ACTIVE || phase == Phase.RETRY_WAIT)) return
        if (!force && phase == Phase.FAILED && failures >= maxAttempts) return
        a.cancelRetry()
        val n = ++attempt
        set(Phase.CONNECTING)
        a.connect(n)
    }

    // Releases everything this device holds and forgets the attempt (late results are ignored).
    private fun drop() {
        a.cancelRetry()
        if (phase == Phase.CONNECTING || phase == Phase.ACTIVE) a.disconnect()
        attempt++
    }

    private fun set(p: Phase, detail: String = "") {
        phase = p
        a.phase(p, detail)
    }

    companion object {
        // 2 s, 4 s, ... capped at 60 s.
        fun backoff(failures: Int) = minOf(60_000L, 2_000L shl minOf(failures - 1, 5))
    }
}
