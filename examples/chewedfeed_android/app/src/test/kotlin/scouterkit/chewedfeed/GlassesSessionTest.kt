package scouterkit.chewedfeed

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.util.concurrent.CountDownLatch
import java.util.concurrent.LinkedBlockingQueue
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicInteger

// A native side that records where it is called from and refuses other generations, as jni.cpp does.
private class FakeBridge(var startResult: Long = 7, val startBlocks: CountDownLatch? = null) : GlassesBridge {
    @Volatile var live = 0L
    val threads = mutableSetOf<String>()
    val cancels = AtomicInteger()
    val stops = AtomicInteger()
    val drawn = mutableListOf<String>()

    private fun on() = synchronized(threads) { threads.add(Thread.currentThread().name) }

    override fun start(link: ArmLink?, left: String, right: String, dx: Int, dy: Int, brightness: Int): Long {
        on()
        startBlocks?.await(5, TimeUnit.SECONDS)
        if (cancels.get() > 0) return -StartOutcome.Failed.CANCELLED.toLong()
        if (startResult > 0) live = startResult
        return startResult
    }
    override fun lastError() = "no glasses found"
    override fun cancel() { cancels.incrementAndGet() }
    override fun stop() { on(); stops.incrementAndGet(); live = 0 }
    override fun loadFont(generation: Long, scale: Int) = if (generation == live) 0 else -1
    override fun fontMetrics(generation: Long, font: Int) = if (generation == live) intArrayOf(10, 3, 14) else null
    override fun measure(generation: Long, font: Int, utf8: ByteArray) = if (generation == live) utf8.size * 6 else -1
    override fun canvas(generation: Long, width: Int, height: Int) = generation == live
    override fun clear(generation: Long, gray: Int) = generation == live
    override fun fill(generation: Long, x: Int, y: Int, w: Int, h: Int, gray: Int) = generation == live
    override fun text(generation: Long, font: Int, utf8: ByteArray, x: Int, baseline: Int, align: Int, gray: Int): Boolean {
        on()
        if (generation != live) return false
        synchronized(drawn) { drawn.add(String(utf8, Charsets.UTF_8)) }
        return true
    }
    override fun present(generation: Long, x: Int, y: Int) = generation == live
    override fun blank(generation: Long) = generation == live
    override fun status(generation: Long) =
        if (generation == live) intArrayOf(600, 400, 5, -2, Int.MIN_VALUE, 1, 1, 80, 0, 1, 1, 75, 0) else null
    override fun events(generation: Long): IntArray {
        on()
        return if (generation == live) intArrayOf(1, GlassesEvent.INPUT, GlassesEvent.TRIGGER_TOUCH, 1, 0) else IntArray(0)
    }
}

class GlassesSessionTest {
    private val direct = java.util.concurrent.Executor { it.run() }

    @Test
    fun startReportsReadyAndRunsOffTheCallerThread() {
        val bridge = FakeBridge()
        val session = GlassesSession(bridge, direct, { _, _ -> })
        val outcome = LinkedBlockingQueue<StartOutcome>()
        session.start(StartConfig(), null) { outcome.put(it) }
        val ready = outcome.poll(5, TimeUnit.SECONDS) as StartOutcome.Ready
        assertEquals(7L, ready.generation)
        assertTrue(ready.status.ready)
        assertEquals(80, ready.status.left.battery)
        assertNull(ready.status.caseBattery)
        assertEquals(false, ready.status.right.inCase)
        assertEquals(setOf("glasses-session"), bridge.threads)
        session.close()
    }

    @Test
    fun startFailureIsStructured() {
        val bridge = FakeBridge(startResult = -StartOutcome.Failed.NO_GLASSES.toLong())
        val session = GlassesSession(bridge, direct, { _, _ -> })
        val outcome = LinkedBlockingQueue<StartOutcome>()
        session.start(StartConfig(), null) { outcome.put(it) }
        assertEquals(StartOutcome.Failed(StartOutcome.Failed.NO_GLASSES, "no glasses found"), outcome.poll(5, TimeUnit.SECONDS))
        assertEquals(0L, session.generation)
        session.close()
    }

    @Test
    fun eventsArriveUntilStopThenNoneAndNoDraws() {
        val bridge = FakeBridge()
        val events = LinkedBlockingQueue<Pair<Long, List<GlassesEvent>>>()
        val session = GlassesSession(bridge, direct, { gen, e -> events.put(gen to e) }, pollMs = 10)
        val started = CountDownLatch(1)
        session.start(StartConfig(), null) { started.countDown() }
        assertTrue(started.await(5, TimeUnit.SECONDS))
        val (gen, batch) = events.poll(5, TimeUnit.SECONDS)!!
        assertEquals(7L, gen)
        assertEquals(GlassesEvent(1, GlassesEvent.INPUT, GlassesEvent.TRIGGER_TOUCH, 1, 0), batch.first())
        assertTrue(session.draw { it.text(0, "Hello ✓ 🙂", 4, 10) }.get(5, TimeUnit.SECONDS)!!)
        assertEquals(listOf("Hello ✓ 🙂"), bridge.drawn) // UTF-8 intact, emoji included

        val stopped = CountDownLatch(1)
        session.stop { stopped.countDown() }
        assertTrue(stopped.await(5, TimeUnit.SECONDS))
        events.clear()
        Thread.sleep(100)
        assertTrue("events after stop: $events", events.isEmpty())
        assertNull(session.draw { it.present(0, 0) }.get(5, TimeUnit.SECONDS))
        // A Frame kept from the stopped generation draws nothing.
        val restarted = CountDownLatch(1)
        bridge.startResult = 8
        session.start(StartConfig(), null) { restarted.countDown() }
        assertTrue(restarted.await(5, TimeUnit.SECONDS))
        assertFalse(Frame(bridge, 7).text(0, "stale", 0, 0))
        assertEquals(1, bridge.stops.get())
        session.close()
    }

    @Test
    fun stopCancelsAStartInProgress() {
        val gate = CountDownLatch(1)
        val bridge = FakeBridge(startBlocks = gate)
        val session = GlassesSession(bridge, direct, { _, _ -> })
        val outcome = LinkedBlockingQueue<StartOutcome>()
        session.start(StartConfig(), null) { outcome.put(it) }
        Thread.sleep(50)
        session.stop()
        assertEquals(1, bridge.cancels.get()) // from the caller's thread, at once
        gate.countDown()
        val failed = outcome.poll(5, TimeUnit.SECONDS) as StartOutcome.Failed
        assertEquals(StartOutcome.Failed.CANCELLED, failed.code)
        session.close()
    }

    // A callback running on the session thread may draw and wait for it (it once deadlocked).
    @Test
    fun drawFromTheSessionThreadDoesNotDeadlock() {
        val bridge = FakeBridge()
        val session = GlassesSession(bridge, { it.run() }, { _, _ -> })
        val drawn = LinkedBlockingQueue<Boolean>()
        session.start(StartConfig(), null) { outcome ->
            if (outcome is StartOutcome.Ready) drawn.put(session.draw { it.text(0, "from the callback", 0, 10) }.get(2, TimeUnit.SECONDS) == true)
        }
        assertEquals(true, drawn.poll(5, TimeUnit.SECONDS))
        session.close()
    }

    @Test
    fun statusLayout() {
        val s = GlassesStatus.parse(intArrayOf(600, 400, Int.MIN_VALUE, 0, 55, 1, 0, Int.MIN_VALUE, 1, 0, 0, 0, 0))!!
        assertNull(s.pitch)
        assertEquals(55, s.caseBattery)
        assertEquals(ArmStatus(present = true, connected = false, battery = null, inCase = true), s.left)
        assertFalse(s.ready)
        assertNull(GlassesStatus.parse(IntArray(3)))
    }
}
