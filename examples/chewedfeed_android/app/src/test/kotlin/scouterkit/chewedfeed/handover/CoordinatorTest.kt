package scouterkit.chewedfeed.handover

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import scouterkit.chewedfeed.bluetooth.Cancellable
import scouterkit.chewedfeed.bluetooth.Scheduler

// The phone's ownership controller with fakes: a manual clock and scheduler, fake glasses, a fake laptop link
// whose messages are checked as the real one checks them (every send must pass Wire.decode).
class CoordinatorTest {
    private var now = 0L
    private val timers = mutableListOf<Triple<Long, () -> Unit, BooleanArray>>()
    private val posted = ArrayDeque<() -> Unit>()
    private val scheduler = object : Scheduler {
        override fun post(task: () -> Unit) { posted += task }
        override fun postDelayed(ms: Long, task: () -> Unit): Cancellable {
            val c = BooleanArray(1)
            timers += Triple(now + ms, task, c)
            return object : Cancellable { override fun cancel() { c[0] = true } }
        }
    }

    private fun run() { while (posted.isNotEmpty()) posted.removeFirst()() }

    private fun advance(ms: Long) {
        val end = now + ms
        while (true) {
            run()
            val next = timers.filter { !it.third[0] && it.first <= end }.minByOrNull { it.first } ?: break
            timers.remove(next)
            now = next.first
            next.second()
        }
        now = end
        run()
    }

    // The glasses: acquire and release complete when the test says so.
    private inner class Glasses : OwnershipNative {
        val calls = mutableListOf<String>()
        var acquiring: ((Boolean) -> Unit)? = null
        var releasing: (() -> Unit)? = null
        override fun acquire(done: (Boolean) -> Unit) { calls += "acquire"; acquiring = done }
        override fun release(done: () -> Unit) { calls += "release"; releasing = done }
        override fun abort() { calls += "abort" }
        fun acquired(ok: Boolean = true) { acquiring!!(ok); acquiring = null; run() }
        fun released() { releasing!!(); releasing = null; run() }
    }

    private val ids = "a".repeat(64) to "b".repeat(64) // laptop, phone

    private inner class Laptop : OwnershipLink {
        override val peerSession = "c".repeat(32)
        val sent = mutableListOf<Message>()
        var closed = false
        private val out = Outbox(ids.second, ids.first, "d".repeat(32)).apply { peerSession = this@Laptop.peerSession }
        override fun negotiated(capability: String) = true
        override fun send(kind: String, epoch: Long, transfer: String, body: Map<String, Any?>): Boolean {
            sent += Wire.decode(Wire.encode(out.next(kind, epoch, transfer, body))) // as the wire checks it
            return true
        }
        override fun expect(transfer: String) {}
        override fun close() { closed = true }
        fun kinds() = sent.map { it.kind }

        // The laptop's messages to the phone.
        private val from = Outbox(ids.first, ids.second, peerSession).apply { peerSession = "d".repeat(32) }
        fun msg(kind: String, transfer: String, body: Map<String, Any?>, epoch: Long = 0) =
            Wire.decode(Wire.encode(from.next(kind, epoch, transfer, body)))
    }

    private val glasses = Glasses()
    private val bridge = SnapshotBridge()
    private val statuses = mutableListOf<OwnershipStatus>()
    private var n = 0
    private fun coordinator(enrolled: Boolean) =
        Coordinator(enrolled, glasses, scheduler, { now }, bridge, { statuses += it }, nonce = { "%032x".format(++n) }, jitter = { 0.0 })

    private val laptopActive = linkedMapOf("role" to "laptop", "phase" to "active", "eligible" to true, "pinned" to false,
        "arms" to linkedMapOf("left" to true, "right" to true), "renderer_ready" to true, "transfer" to "e".repeat(32), "ready_epoch" to 1)

    @Test
    fun withoutALaptopThePhoneTakesTheGlasses() {
        val c = coordinator(enrolled = false)
        c.start(eligible = true, pinned = false)
        assertEquals(listOf("acquire"), glasses.calls)
        glasses.acquired()
        assertEquals(Phase.ACTIVE, c.state.phase)
        assertEquals("this", statuses.last().owner)
    }

    @Test
    fun anActiveLaptopKeepsTheGlasses() {
        val c = coordinator(enrolled = true)
        c.start(eligible = true, pinned = false)
        val l = Laptop()
        c.linkUp(l)
        c.message(l, l.msg("status", "", laptopActive, epoch = 1))
        advance(60_000) // cold start and any probe delays pass
        assertTrue("never takes them from a healthy owner", "acquire" !in glasses.calls)
        assertEquals("peer", c.status().owner)
        assertTrue("status" in l.kinds())
    }

    @Test
    fun lidClosedHandsTheGlassesToThePhoneWithThePage() {
        val c = coordinator(enrolled = true)
        c.start(eligible = true, pinned = false)
        val l = Laptop()
        c.linkUp(l)
        c.message(l, l.msg("status", "", laptopActive, epoch = 1))
        // The laptop's lid closes: release notice, its navigation, then released.
        val t = "f".repeat(32)
        c.message(l, l.msg("release", t, linkedMapOf("target" to "phone", "reason" to "lid_closed"), epoch = 1))
        assertEquals(Phase.REQUESTING, c.state.phase)
        val snap = UiSnapshot(4, "sites", "projects/one/0", null, true, 12_000)
        c.message(l, l.msg("snapshot", t, linkedMapOf("snapshot" to StrictJson.parse(snap.encode().toByteArray(), 8192)), epoch = 1))
        c.message(l, l.msg("released", t, linkedMapOf("target" to "phone", "arms" to linkedMapOf("left" to false, "right" to false),
            "reconnect_disabled" to true, "workers_stopped" to true), epoch = 1))
        assertEquals(listOf("acquire"), glasses.calls)
        assertEquals("projects/one/0", bridge.take()?.pageId) // the laptop's page, committed by its release
        glasses.acquired()
        assertEquals(Phase.ACTIVE, c.state.phase)
        assertTrue(l.kinds().containsAll(listOf("acquiring", "ready")))
        assertEquals("this", c.status().owner)
    }

    @Test
    fun theLaptopCannotHoldThemSoThePhoneAsks() {
        val c = coordinator(enrolled = true)
        c.start(eligible = true, pinned = false)
        val l = Laptop()
        c.linkUp(l)
        c.message(l, l.msg("availability", "", linkedMapOf("eligible" to false, "lid" to "closed", "awake" to true, "reason" to "lid_closed")))
        assertEquals(Phase.REQUESTING, c.state.phase)
        val request = l.sent.last { it.kind == "request" }
        assertEquals("phone", request.body["target"])
        assertEquals("lid_closed", request.body["reason"]) // the laptop's own reason
        assertFalse("acquire" in glasses.calls) // only its released hands them over
        // An eligible laptop, an unpinned phone: no request.
        val d = coordinator(enrolled = true)
        d.start(eligible = true, pinned = false)
        val m = Laptop()
        d.linkUp(m)
        d.message(m, m.msg("availability", "", linkedMapOf("eligible" to true, "lid" to "open", "awake" to true, "reason" to "resume")))
        assertFalse("request" in m.kinds())
        // Pinned: the phone asks even so, as manual.
        d.pin(true)
        assertEquals("manual", m.sent.last { it.kind == "request" }.body["reason"])
    }

    @Test
    fun telemetryIsPassedOnAndNeverChangesOwnership() {
        val remote = RemoteTelemetry { now }
        var changed = 0
        val c = Coordinator(true, glasses, scheduler, { now }, bridge, nonce = { "%032x".format(++n) }, jitter = { 0.0 },
            remote = remote, onTelemetry = { changed++ })
        c.start(eligible = true, pinned = false)
        val l = Laptop()
        c.linkUp(l)
        c.message(l, l.msg("status", "", laptopActive, epoch = 1))
        val before = c.state
        c.message(l, l.msg("telemetry", "", linkedMapOf("telemetry" to linkedMapOf("schema_version" to 1, "revision" to 1, "source" to "system",
            "sample_unix_ms" to 1_800_000_000_000L, "age_ms" to 0, "status" to "fresh",
            "values" to linkedMapOf("cpu_percent" to 3.5, "memory_used_bytes" to 1, "memory_total_bytes" to 2, "battery_percent" to null))), epoch = 1))
        assertEquals(1, changed)
        assertEquals(before, c.state) // ownership untouched
        assertTrue(remote.statsView(0)!!.single().lines.first() == "CPU: 3.5%")
    }

    @Test
    fun theLaptopWakesAndAsks() {
        val c = phoneActiveWithLaptop()
        val l = laptop!!
        val t = "9".repeat(32)
        l.sent.clear()
        c.message(l, l.msg("request", t, linkedMapOf("target" to "laptop", "reason" to "resume", "pinned" to false), epoch = 2))
        assertEquals("release" , glasses.calls.last())
        assertFalse("released only after both arms are down", "released" in l.kinds())
        glasses.released()
        assertTrue("released" in l.kinds())
        assertTrue(l.kinds().indexOf("release") < l.kinds().indexOf("released"))
        assertEquals(Phase.IDLE, c.state.phase)
    }

    @Test
    fun pinnedKeepsThemOnThePhone() {
        val c = phoneActiveWithLaptop()
        val l = laptop!!
        c.pin(true)
        assertTrue(c.state.pinned)
        c.message(l, l.msg("request", "9".repeat(32), linkedMapOf("target" to "laptop", "reason" to "resume", "pinned" to false), epoch = 2))
        assertEquals(Phase.ACTIVE, c.state.phase)
        assertFalse("release" in glasses.calls)
    }

    @Test
    fun aFailedAcquisitionRollsBackAndRetriesLater() {
        val c = coordinator(enrolled = false)
        c.start(eligible = true, pinned = false)
        glasses.acquired(false)
        assertTrue(c.state.phase != Phase.ACTIVE)
        if (glasses.releasing != null) glasses.released()
        advance(Policy.RETRY_MAX_MS * 2)
        assertTrue(glasses.calls.count { it == "acquire" } >= 2) // tried again after the backoff
    }

    @Test
    fun theLaptopVanishingDoesNotTakeTheGlasses() {
        val c = phoneActiveWithLaptop()
        c.linkDown(laptop!!)
        advance(60_000)
        assertEquals(Phase.ACTIVE, c.state.phase)
        assertFalse("release" in glasses.calls)
        assertEquals("this", c.status().owner)
        assertFalse(c.status().laptopConnected)
    }

    @Test
    fun stopReleasesAndLateResultsAreIgnored() {
        val c = coordinator(enrolled = false)
        c.start(eligible = true, pinned = false)
        c.stop() // during the acquire
        glasses.acquired() // it finished anyway: let go again
        assertEquals("release", glasses.calls.last())
        glasses.released()
        assertEquals(Phase.IDLE, c.state.phase)
        assertFalse(c.state.arms.any)
    }

    @Test
    fun aSecondLaptopConnectionIsClosed() {
        val c = coordinator(enrolled = true)
        c.start(eligible = true, pinned = false)
        val a = Laptop()
        val b = Laptop()
        c.linkUp(a)
        c.linkUp(b)
        assertTrue(b.closed)
        assertFalse(a.closed)
        assertNotNull(c.heartbeat()["phase"])
        assertNull(c.heartbeat()["nothing"])
    }

    private var laptop: Laptop? = null

    // The laptop handed over (lid closed) and the phone holds the glasses, still connected to the laptop.
    private fun phoneActiveWithLaptop(): Coordinator {
        val c = coordinator(enrolled = true)
        c.start(eligible = true, pinned = false)
        val l = Laptop()
        laptop = l
        c.linkUp(l)
        c.message(l, l.msg("status", "", laptopActive, epoch = 1))
        val t = "f".repeat(32)
        c.message(l, l.msg("release", t, linkedMapOf("target" to "phone", "reason" to "lid_closed"), epoch = 1))
        c.message(l, l.msg("released", t, linkedMapOf("target" to "phone", "arms" to linkedMapOf("left" to false, "right" to false),
            "reconnect_disabled" to true, "workers_stopped" to true), epoch = 1))
        glasses.acquired()
        assertEquals(Phase.ACTIVE, c.state.phase)
        return c
    }
}
