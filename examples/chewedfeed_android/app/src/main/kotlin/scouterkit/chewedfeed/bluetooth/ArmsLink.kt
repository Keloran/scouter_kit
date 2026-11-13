package scouterkit.chewedfeed.bluetooth

import scouterkit.chewedfeed.ArmLink

// Why the link is or is not up; the setup states need the user, nothing here retries them.
enum class LinkStatus {
    IDLE,
    CONNECTING,
    PARTIAL,           // one arm up, the other still connecting
    CONNECTED,
    BLUETOOTH_OFF,     // reconnects when Bluetooth comes back
    PERMISSION_NEEDED, // Nearby devices permission missing or revoked
    SETUP_NEEDED,      // an arm is not bonded any more: pair again from setup
    ENCRYPTION_FAILED, // an arm refused encryption: pair again from setup
    WRONG_FIRMWARE,    // no scouter service on an arm
    FAILED,            // the link kept failing (e.g. MTU)
}

// The two arms over BLE for the native transport (ArmLink, called on the library's Bluetooth thread) and for the
// app (connectBoth, releaseBoth, cancel, status). It connects only to the bonded arms it was given, asks for MTU
// 247 and the 2M PHY, subscribes to EVT and writes CMD/CTRL one at a time per arm from a bounded queue. Every
// connection has its own generation: callbacks of a closed one are ignored, so nothing after a release or cancel
// brings a link back. A lost bond or failed encryption stops everything (both arms) until setup: it never pairs
// by itself, which could cost another host its bond slot on the arm. Link events go to `events`.
class ArmsLink(
    private val adapter: BleAdapter,
    private val addresses: Array<String>, // left, right; "" or "none": that arm is not used
    private val events: NativeLinkEvents,
    private val scheduler: Scheduler,
    private val maxQueue: Int = 256,
    private val onStatus: (LinkStatus, String) -> Unit = { _, _ -> },
) : ArmLink {
    private inner class Arm(val index: Int) {
        val address get() = addresses[index]
        val used get() = address.isNotEmpty() && address != "none"
        var conn: BleConnection? = null
        var wanted = false
        var up = false
        var mtu = 23
        var writing = false
        var mtuFailures = 0
        var retry: Cancellable? = null
        val queue = ArrayDeque<Pair<Int, ByteArray>>()
    }

    private val arms = arrayOf(Arm(0), Arm(1))
    @Volatile var status = LinkStatus.IDLE
        private set
    @Volatile var reason = ""
        private set
    private var stopped = false // a setup state: nothing connects until connectBoth

    // ---------------------------------------------------------------- the app's operations

    // Connects both arms (again, after a setup state was resolved).
    fun connectBoth() = scheduler.post {
        stopped = false
        set(LinkStatus.CONNECTING, "")
        for (a in arms) if (a.used) open(a, false)
    }

    // Both arms down: retry timers cancelled first, then both connections closed. Nothing reconnects.
    fun releaseBoth() = scheduler.post { closeAll(); set(LinkStatus.IDLE, "") }

    // Abandons a connection attempt in progress (the same as a release).
    fun cancel() = releaseBoth()

    // Android dropped both links without callbacks: down here, back when Bluetooth is.
    fun bluetoothOff() = scheduler.post {
        for (a in arms) {
            val keep = a.wanted
            close(a)
            a.wanted = keep
        }
        set(LinkStatus.BLUETOOTH_OFF, "Bluetooth is off")
    }

    fun bluetoothOn() = scheduler.post {
        if (stopped) return@post
        for (a in arms) if (a.wanted) open(a, true)
    }

    // A bond went away (ACTION_BOND_STATE_CHANGED): setup is needed for that pair.
    fun bondLost(address: String) = scheduler.post {
        val a = arms.firstOrNull { it.used && it.address.equals(address, ignoreCase = true) } ?: return@post
        giveUp(LinkStatus.SETUP_NEEDED, ERROR_BONDING, "the ${NAMES[a.index]} arm is not bonded any more")
    }

    // ---------------------------------------------------------------- ArmLink (the library's Bluetooth thread)

    override fun connect(arm: Int, unused: Int) = scheduler.post { if (!stopped) open(arms[arm], false) }

    override fun disconnect(arm: Int, unused: Int) = scheduler.post { close(arms[arm]) }

    override fun hold(arm: Int, ms: Int) = scheduler.post {
        val a = arms[arm]
        close(a)
        if (stopped) return@post
        a.wanted = true
        a.retry = scheduler.postDelayed(ms.toLong()) { a.retry = null; if (a.wanted && a.conn == null) open(a, false) }
    }

    override fun stop(unused: Int, unused2: Int) = releaseBoth()

    // Queued for the arm's link; false when it is not up or its queue is full (the library retries).
    override fun write(arm: Int, channel: Int, data: ByteArray): Boolean {
        val a = arms[arm]
        synchronized(a) {
            if (!a.up || a.queue.size >= maxQueue) return false
            a.queue.addLast(channel to data)
        }
        scheduler.post { pump(a) }
        return true
    }

    // ---------------------------------------------------------------- the link

    private fun open(a: Arm, background: Boolean) {
        if (!a.used) return
        a.wanted = true
        if (a.conn != null || stopped) return
        if (!adapter.enabled) {
            set(LinkStatus.BLUETOOTH_OFF, "Bluetooth is off")
            return events.linkProblem(ERROR_BLUETOOTH, "Bluetooth is off")
        }
        val conn = try {
            val device = adapter.device(a.address)
            if (!device.bonded) return giveUp(LinkStatus.SETUP_NEEDED, ERROR_BONDING, "the ${NAMES[a.index]} arm is not bonded")
            if (status != LinkStatus.PARTIAL && status != LinkStatus.CONNECTED) set(LinkStatus.CONNECTING, "")
            lateinit var c: BleConnection
            c = device.connect(background, callbacks(a) { a.conn === c })
            c
        } catch (e: SecurityException) {
            return giveUp(LinkStatus.PERMISSION_NEEDED, ERROR_BLUETOOTH, "the Nearby devices permission is missing")
        }
        a.conn = conn
    }

    private fun close(a: Arm) {
        a.wanted = false
        a.retry?.cancel()
        a.retry = null
        val c = a.conn
        a.conn = null
        runCatching { c?.close() }
        down(a)
    }

    private fun closeAll() {
        for (a in arms) { a.retry?.cancel(); a.retry = null; a.wanted = false } // timers first: none may reopen
        for (a in arms) close(a)
    }

    private fun down(a: Arm) {
        val was = synchronized(a) {
            val was = a.up
            a.up = false
            a.queue.clear()
            was
        }
        a.writing = false
        if (was) events.linkDown(a.index)
        refresh()
    }

    private fun giveUp(s: LinkStatus, code: Int, why: String) {
        stopped = true
        closeAll()
        set(s, why)
        events.linkProblem(code, why)
    }

    private fun refresh() {
        if (stopped || status == LinkStatus.BLUETOOTH_OFF) return
        val used = arms.filter { it.used }
        val up = used.count { it.up }
        when {
            used.isNotEmpty() && up == used.size -> set(LinkStatus.CONNECTED, "")
            up > 0 -> set(LinkStatus.PARTIAL, "")
            used.any { it.conn != null || it.wanted } -> set(LinkStatus.CONNECTING, "")
            else -> set(LinkStatus.IDLE, "")
        }
    }

    private fun set(s: LinkStatus, why: String) {
        if (status == s && reason == why) return
        status = s
        reason = why
        onStatus(s, why)
    }

    private fun pump(a: Arm) {
        val conn = a.conn ?: return
        if (a.writing) return
        val next = synchronized(a) { if (a.up) a.queue.removeFirstOrNull() else null } ?: return
        a.writing = conn.write(next.first, next.second)
        if (!a.writing) {
            synchronized(a) { if (a.up) a.queue.addFirst(next) }
            scheduler.postDelayed(5) { pump(a) }
        }
    }

    // Callbacks for one connection: `current` is false once it is closed or replaced, and then they do nothing.
    private fun callbacks(a: Arm, current: () -> Boolean) = object : BleCallbacks {
        override fun onConnectionState(connected: Boolean, status: Int) {
            if (!current()) return
            val conn = a.conn ?: return
            if (connected) return conn.requestMtu(247)
            a.conn = null
            runCatching { conn.close() }
            down(a)
            val bonded = try {
                adapter.device(a.address).bonded
            } catch (e: SecurityException) {
                return giveUp(LinkStatus.PERMISSION_NEEDED, ERROR_BLUETOOTH, "the Nearby devices permission is missing")
            }
            when {
                status in SECURITY -> giveUp(LinkStatus.ENCRYPTION_FAILED, ERROR_BONDING, "the ${NAMES[a.index]} arm refused encryption (status $status)")
                !bonded -> giveUp(LinkStatus.SETUP_NEEDED, ERROR_BONDING, "the ${NAMES[a.index]} arm is not bonded any more")
                a.wanted -> open(a, true)
            }
        }

        override fun onMtu(mtu: Int, ok: Boolean) {
            if (!current()) return
            val conn = a.conn ?: return
            if (!ok) {
                if (++a.mtuFailures >= MAX_MTU_FAILURES) {
                    return giveUp(LinkStatus.FAILED, ERROR_BLUETOOTH, "the ${NAMES[a.index]} arm's MTU exchange keeps failing")
                }
                close(a)
                a.wanted = true
                a.retry = scheduler.postDelayed(RETRY_MS) { a.retry = null; if (a.wanted && a.conn == null) open(a, false) }
                return
            }
            a.mtu = mtu
            conn.preferTwoMegabit()
            conn.discoverServices()
        }

        override fun onServices(ok: Boolean, scouter: Boolean) {
            if (!current()) return
            val conn = a.conn ?: return
            if (!ok || !scouter) return giveUp(LinkStatus.WRONG_FIRMWARE, ERROR_FIRMWARE, "the ${NAMES[a.index]} arm does not run the scouter firmware")
            if (!conn.enableEvents()) giveUp(LinkStatus.FAILED, ERROR_BLUETOOTH, "cannot subscribe to the ${NAMES[a.index]} arm")
        }

        override fun onEventsEnabled(status: Int) {
            if (!current()) return
            if (status != 0) {
                return if (status in SECURITY) giveUp(LinkStatus.ENCRYPTION_FAILED, ERROR_BONDING, "the ${NAMES[a.index]} arm refused encryption (status $status)")
                else giveUp(LinkStatus.FAILED, ERROR_BLUETOOTH, "the ${NAMES[a.index]} arm's subscription failed (status $status)")
            }
            synchronized(a) { a.up = true }
            a.mtuFailures = 0
            events.linkFound(a.index, a.address)
            events.linkUp(a.index, a.mtu)
            refresh()
        }

        override fun onWriteDone() {
            if (!current()) return
            a.writing = false
            pump(a)
        }

        override fun onData(data: ByteArray) {
            if (current() && a.up) events.linkData(a.index, data)
        }
    }

    companion object {
        val NAMES = arrayOf("left", "right")
        // ScouterKit's InitError codes for linkProblem.
        const val ERROR_BLUETOOTH = 1
        const val ERROR_BONDING = 3
        const val ERROR_FIRMWARE = 4
        // HCI authentication failure (also GATT insufficient authentication), PIN or key missing, MIC failure,
        // GATT insufficient encryption, Android's GATT_AUTH_FAIL.
        val SECURITY = setOf(5, 6, 0x3D, 15, 137)
        const val MAX_MTU_FAILURES = 3
        const val RETRY_MS = 1000L
    }
}
