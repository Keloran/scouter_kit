package scouterkit.chewedfeed.bluetooth

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

// A scheduler the test runs by hand, with a fake clock for delayed work.
private class ManualScheduler : Scheduler {
    var now = 0L
    private val tasks = mutableListOf<Triple<Long, Int, () -> Unit>>()
    private var seq = 0

    override fun post(task: () -> Unit) = postDelayed(0, task).let {}
    override fun postDelayed(ms: Long, task: () -> Unit): Cancellable {
        val entry = Triple(now + ms, seq++, task)
        tasks += entry
        return object : Cancellable { override fun cancel() { tasks.remove(entry) } }
    }

    // Runs everything due, including what the tasks post.
    fun run() {
        while (true) {
            val next = tasks.filter { it.first <= now }.minWithOrNull(compareBy({ it.first }, { it.second })) ?: return
            tasks.remove(next)
            next.third()
        }
    }

    fun advance(ms: Long) { now += ms; run() }
    val pending get() = tasks.size
}

private class FakeConnection(val arm: Int, val cb: BleCallbacks) : BleConnection {
    var closed = false
    var mtuAsked = 0
    val writes = mutableListOf<Pair<Int, ByteArray>>()
    var busy = false
    var enableOk = true
    override fun requestMtu(mtu: Int) { mtuAsked++ }
    override fun preferTwoMegabit() {}
    override fun discoverServices() {}
    override fun enableEvents() = enableOk
    override fun write(channel: Int, data: ByteArray): Boolean {
        if (busy) return false
        writes += channel to data
        return true
    }
    override fun close() { closed = true }
}

private class FakeAdapter : BleAdapter {
    override var enabled = true
    var permission = true
    val bonded = mutableMapOf("L" to true, "R" to true)
    val connections = mutableListOf<FakeConnection>()
    override fun device(address: String): BleDevice {
        if (!permission) throw SecurityException("no BLUETOOTH_CONNECT")
        return object : BleDevice {
            override val bonded get() = this@FakeAdapter.bonded[address] == true
            override fun connect(background: Boolean, callbacks: BleCallbacks) =
                FakeConnection(if (address == "L") 0 else 1, callbacks).also { connections += it }
        }
    }
    fun last(arm: Int) = connections.last { it.arm == arm }
}

private class Recorder : NativeLinkEvents {
    val log = mutableListOf<String>()
    override fun linkUp(arm: Int, mtu: Int) { log += "up $arm $mtu" }
    override fun linkDown(arm: Int) { log += "down $arm" }
    override fun linkData(arm: Int, data: ByteArray) { log += "data $arm ${data.size}" }
    override fun linkFound(arm: Int, address: String) { log += "found $arm" }
    override fun linkProblem(code: Int, text: String) { log += "problem $code" }
}

class ArmsLinkTest {
    private val sched = ManualScheduler()
    private val adapter = FakeAdapter()
    private val events = Recorder()
    private val link = ArmsLink(adapter, arrayOf("L", "R"), events, sched, maxQueue = 4)

    private fun bringUp(arm: Int, mtu: Int = 247) {
        val c = adapter.last(arm)
        c.cb.onConnectionState(true, 0); sched.run()
        c.cb.onMtu(mtu, true); sched.run()
        c.cb.onServices(true, true); sched.run()
        c.cb.onEventsEnabled(0); sched.run()
    }

    private fun connectBoth() {
        link.connectBoth(); sched.run()
        bringUp(0); bringUp(1)
    }

    @Test
    fun connectsBothAndSerializesWrites() {
        connectBoth()
        assertEquals(LinkStatus.CONNECTED, link.status)
        assertEquals(listOf("found 0", "up 0 247", "found 1", "up 1 247"), events.log)
        val c = adapter.last(0)
        assertTrue(link.write(0, 0, byteArrayOf(1)))
        assertTrue(link.write(0, 1, byteArrayOf(2)))
        sched.run()
        assertEquals(1, c.writes.size) // one in flight per arm
        c.cb.onWriteDone(); sched.run()
        assertEquals(listOf(0, 1), c.writes.map { it.first })
        c.cb.onData(byteArrayOf(9, 9)); sched.run()
        assertEquals("data 0 2", events.log.last())
    }

    @Test
    fun queueIsBounded() {
        connectBoth()
        adapter.last(1).busy = true
        repeat(4) { assertTrue(link.write(1, 0, byteArrayOf(it.toByte()))) }
        assertFalse(link.write(1, 0, byteArrayOf(5))) // full: the library retries later
    }

    @Test
    fun releaseClearsQueueAndStaleCallbacksDoNothing() {
        connectBoth()
        val old = adapter.last(0)
        old.busy = true
        link.write(0, 0, byteArrayOf(1)); link.write(0, 0, byteArrayOf(2))
        link.releaseBoth(); sched.run()
        assertTrue(adapter.connections.all { it.closed })
        assertEquals(LinkStatus.IDLE, link.status)
        assertFalse(link.write(0, 0, byteArrayOf(3)))
        val before = events.log.size
        val asked = old.mtuAsked
        // Late callbacks of the closed connection: no link comes back.
        old.cb.onConnectionState(true, 0); old.cb.onMtu(247, true); old.cb.onEventsEnabled(0); old.cb.onWriteDone(); old.cb.onData(byteArrayOf(1))
        sched.run()
        assertEquals(asked, old.mtuAsked)
        assertEquals(before, events.log.size)
        assertEquals(LinkStatus.IDLE, link.status)
        sched.advance(60_000)
        assertEquals(2, adapter.connections.size) // no reconnect attempts
    }

    @Test
    fun callbackReorderingAndStaleGeneration() {
        link.connectBoth(); sched.run()
        val first = adapter.last(0)
        // The link drops before it was up; it reconnects (autoConnect) with a new connection.
        first.cb.onConnectionState(false, 8); sched.run()
        val second = adapter.last(0)
        assertTrue(first.closed && second !== first)
        // The first connection's subscription answer arrives late: ignored.
        first.cb.onEventsEnabled(0); sched.run()
        assertFalse(events.log.contains("up 0 247"))
        bringUp(0)
        assertTrue(events.log.contains("up 0 247"))
        assertEquals(LinkStatus.PARTIAL, link.status)
    }

    @Test
    fun mtuFailureRetriesThenGivesUp() {
        link.connectBoth(); sched.run()
        repeat(ArmsLink.MAX_MTU_FAILURES) {
            val c = adapter.last(0)
            c.cb.onConnectionState(true, 0); sched.run()
            c.cb.onMtu(23, false); sched.run()
            sched.advance(ArmsLink.RETRY_MS)
        }
        assertEquals(LinkStatus.FAILED, link.status)
        assertTrue(adapter.connections.all { it.closed })
        val n = adapter.connections.size
        sched.advance(60_000)
        assertEquals(n, adapter.connections.size)
    }

    @Test
    fun encryptionFailureAfterOneArmReleasesBothAndNeverRepairs() {
        link.connectBoth(); sched.run()
        bringUp(0)
        adapter.last(1).cb.onConnectionState(false, 5); sched.run() // the right arm refuses encryption
        assertEquals(LinkStatus.ENCRYPTION_FAILED, link.status)
        assertTrue(events.log.containsAll(listOf("down 0", "problem 3")))
        assertTrue(adapter.connections.all { it.closed })
        val n = adapter.connections.size
        link.connect(0, 0); link.connect(1, 0); sched.advance(60_000) // the library asks again: nothing until setup
        assertEquals(n, adapter.connections.size)
        link.connectBoth(); sched.run() // the user resolved it in setup
        assertEquals(n + 2, adapter.connections.size)
    }

    @Test
    fun partialAcquisitionWithLostBond() {
        adapter.bonded["R"] = false
        link.connectBoth(); sched.run()
        assertEquals(LinkStatus.SETUP_NEEDED, link.status)
        assertTrue(adapter.connections.all { it.closed })
        // A bond lost while connected is the same setup state.
        adapter.bonded["R"] = true
        connectBoth()
        link.bondLost("R"); sched.run()
        assertEquals(LinkStatus.SETUP_NEEDED, link.status)
        assertTrue(events.log.containsAll(listOf("down 0", "down 1")))
    }

    @Test
    fun bluetoothOffWithoutCallbacksAndBack() {
        connectBoth()
        link.bluetoothOff(); sched.run()
        assertEquals(LinkStatus.BLUETOOTH_OFF, link.status)
        assertTrue(events.log.containsAll(listOf("down 0", "down 1")))
        assertFalse(link.write(0, 0, byteArrayOf(1)))
        adapter.enabled = true
        link.bluetoothOn(); sched.run()
        assertEquals(4, adapter.connections.size) // both reopen
        bringUp(0); bringUp(1)
        assertEquals(LinkStatus.CONNECTED, link.status)
    }

    @Test
    fun bluetoothOffAtConnect() {
        adapter.enabled = false
        link.connectBoth(); sched.run()
        assertEquals(LinkStatus.BLUETOOTH_OFF, link.status)
        assertTrue(adapter.connections.isEmpty())
    }

    @Test
    fun permissionRevokedThenGranted() {
        adapter.permission = false
        link.connectBoth(); sched.run()
        assertEquals(LinkStatus.PERMISSION_NEEDED, link.status)
        adapter.permission = true
        connectBoth()
        assertEquals(LinkStatus.CONNECTED, link.status)
    }

    @Test
    fun holdReconnectsLaterUnlessReleased() {
        connectBoth()
        link.hold(0, 300); sched.run()
        assertEquals(2, adapter.connections.size)
        sched.advance(300)
        assertEquals(3, adapter.connections.size)
        link.hold(1, 300); sched.run()
        link.releaseBoth(); sched.run()
        sched.advance(1000)
        assertEquals(3, adapter.connections.size) // the release cancelled the pending reconnect
    }

    @Test
    fun wrongFirmware() {
        link.connectBoth(); sched.run()
        val c = adapter.last(0)
        c.cb.onConnectionState(true, 0); c.cb.onMtu(247, true); c.cb.onServices(true, false); sched.run()
        assertEquals(LinkStatus.WRONG_FIRMWARE, link.status)
        assertTrue(events.log.contains("problem 4"))
    }

    @Test
    fun oneArmOnly() {
        val one = ArmsLink(adapter, arrayOf("L", "none"), events, sched)
        one.connectBoth(); sched.run()
        assertEquals(1, adapter.connections.size)
        bringUp(0)
        assertEquals(LinkStatus.CONNECTED, one.status)
        assertNull(adapter.connections.firstOrNull { it.arm == 1 })
    }
}

class PairRecordTest {
    private fun record(flags: Int, id: String) = byteArrayOf(1, flags.toByte()) + id.toByteArray()

    @Test
    fun parsesAndGroupsByPairId() {
        assertEquals(PairRecord("A1", left = true, hasPanel = true), PairRecord.parse(record(1, "A1")))
        assertEquals(PairRecord("A1", left = false, hasPanel = false), PairRecord.parse(record(2, "A1")))
        assertNull(PairRecord.parse(null))
        assertNull(PairRecord.parse(byteArrayOf(2, 1, 65))) // other format
        assertNull(PairRecord.parse(byteArrayOf(1, 1))) // no id
        val f = PairFinder()
        f.add("aa", PairRecord.parse(record(1, "A1")))
        f.add("bb", PairRecord.parse(record(0, "A1")))
        f.add("cc", PairRecord.parse(record(1, "B2")))
        f.add("dd", PairRecord.parse(record(2, "B2"))) // a board without a panel: never part of a pair
        f.add("ee", null)
        assertEquals(listOf(PairFinder.Pair("A1", "aa", "bb"), PairFinder.Pair("B2", "cc", null)), f.pairs)
        assertTrue(f.pairs[0].complete)
        assertFalse(f.pairs[1].complete)
    }
}
