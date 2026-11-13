package scouterkit.chewedfeed

import java.util.concurrent.Executor
import java.util.concurrent.Executors
import java.util.concurrent.Future
import java.util.concurrent.ScheduledExecutorService
import java.util.concurrent.TimeUnit

// The native calls GlassesSession makes; Native in the app, a fake in the unit tests.
interface GlassesBridge {
    fun start(link: ArmLink?, left: String, right: String, dx: Int, dy: Int, brightness: Int): Long
    fun lastError(): String
    fun cancel()
    fun stop()
    fun loadFont(generation: Long, scale: Int): Int
    fun fontMetrics(generation: Long, font: Int): IntArray?
    fun measure(generation: Long, font: Int, utf8: ByteArray): Int
    fun canvas(generation: Long, width: Int, height: Int): Boolean
    fun clear(generation: Long, gray: Int): Boolean
    fun fill(generation: Long, x: Int, y: Int, w: Int, h: Int, gray: Int): Boolean
    fun text(generation: Long, font: Int, utf8: ByteArray, x: Int, baseline: Int, align: Int, gray: Int): Boolean
    fun present(generation: Long, x: Int, y: Int): Boolean
    fun blank(generation: Long): Boolean
    fun status(generation: Long): IntArray?
    fun events(generation: Long): IntArray
}

object NativeBridge : GlassesBridge {
    override fun start(link: ArmLink?, left: String, right: String, dx: Int, dy: Int, brightness: Int) =
        Native.start(link, left, right, dx, dy, brightness)
    override fun lastError() = Native.lastError()
    override fun cancel() = Native.cancel()
    override fun stop() = Native.stop()
    override fun loadFont(generation: Long, scale: Int) = Native.loadFont(generation, scale)
    override fun fontMetrics(generation: Long, font: Int) = Native.fontMetrics(generation, font)
    override fun measure(generation: Long, font: Int, utf8: ByteArray) = Native.measure(generation, font, utf8)
    override fun canvas(generation: Long, width: Int, height: Int) = Native.canvas(generation, width, height)
    override fun clear(generation: Long, gray: Int) = Native.clear(generation, gray)
    override fun fill(generation: Long, x: Int, y: Int, w: Int, h: Int, gray: Int) =
        Native.fill(generation, x, y, w, h, gray)
    override fun text(generation: Long, font: Int, utf8: ByteArray, x: Int, baseline: Int, align: Int, gray: Int) =
        Native.text(generation, font, utf8, x, baseline, align, gray)
    override fun present(generation: Long, x: Int, y: Int) = Native.present(generation, x, y)
    override fun blank(generation: Long) = Native.blank(generation)
    override fun status(generation: Long) = Native.status(generation)
    override fun events(generation: Long) = Native.events(generation)
}

data class StartConfig(
    val left: String = "", // bonded address; "" any bonded G1; "none" leaves the arm out
    val right: String = "",
    val dx: Int = 0,
    val dy: Int = 0,
    val brightness: Int = 21,
)

sealed class StartOutcome {
    data class Ready(val generation: Long, val status: GlassesStatus) : StartOutcome()
    data class Failed(val code: Int, val message: String) : StartOutcome() {
        companion object {
            const val BLUETOOTH = 1
            const val NO_GLASSES = 2
            const val BONDING = 3
            const val FIRMWARE = 4
            const val CANCELLED = 100
            const val BUSY = 101
        }
    }
}

data class ArmStatus(val present: Boolean, val connected: Boolean, val battery: Int?, val inCase: Boolean?)

data class GlassesStatus(
    val width: Int,
    val height: Int,
    val pitch: Int?, // degrees, + looking up
    val roll: Int?,  // degrees, + right ear down
    val caseBattery: Int?,
    val left: ArmStatus,
    val right: ArmStatus,
) {
    // Every configured arm connected.
    val ready get() = listOf(left, right).all { !it.present || it.connected } && (left.present || right.present)

    companion object {
        private fun known(v: Int) = if (v == Int.MIN_VALUE) null else v

        // native/jni.cpp Native.status layout.
        fun parse(v: IntArray): GlassesStatus? {
            if (v.size != 13) return null
            fun arm(i: Int) = ArmStatus(v[i] != 0, v[i + 1] != 0, known(v[i + 2]), known(v[i + 3])?.let { it != 0 })
            return GlassesStatus(v[0], v[1], known(v[2]), known(v[3]), known(v[4]), arm(5), arm(9))
        }
    }
}

// An arm event (scouter_kit::g1::Event): INPUT for touches and the case, NOTIFY, RESET, ...
data class GlassesEvent(val arm: Int, val type: Int, val trigger: Int, val a: Int, val b: Int) {
    companion object {
        const val ERROR = 0x02
        const val HALT = 0x03
        const val NOTIFY = 0x04
        const val INPUT = 0x07
        const val RESET = 0x80
        const val TRIGGER_CASE = 6
        const val TRIGGER_TOUCH = 8

        fun parse(v: IntArray) = List(v.size / 5) { i -> GlassesEvent(v[5 * i], v[5 * i + 1], v[5 * i + 2], v[5 * i + 3], v[5 * i + 4]) }
    }
}

// Drawing for one generation; every call fails (false, -1, null) once that generation has stopped.
class Frame internal constructor(private val bridge: GlassesBridge, val generation: Long) {
    fun loadFont(scale: Int) = bridge.loadFont(generation, scale)
    fun fontMetrics(font: Int) = bridge.fontMetrics(generation, font) // ascent, descent, line spacing
    fun measure(font: Int, text: String) = bridge.measure(generation, font, text.toByteArray(Charsets.UTF_8))
    fun canvas(width: Int, height: Int) = bridge.canvas(generation, width, height)
    fun clear(gray: Int = 0) = bridge.clear(generation, gray)
    fun fill(x: Int, y: Int, w: Int, h: Int, gray: Int) = bridge.fill(generation, x, y, w, h, gray)
    fun text(font: Int, text: String, x: Int, baseline: Int, align: Int = ALIGN_LEFT, gray: Int = 255) =
        bridge.text(generation, font, text.toByteArray(Charsets.UTF_8), x, baseline, align, gray)
    fun present(x: Int, y: Int) = bridge.present(generation, x, y)
    fun blank() = bridge.blank(generation)
    fun status() = bridge.status(generation)?.let(GlassesStatus::parse)

    companion object {
        const val ALIGN_LEFT = 0
        const val ALIGN_CENTER = 1
        const val ALIGN_RIGHT = 2
    }
}

// The glasses session: every native call runs on its own thread, never the caller's (start blocks up to 30 s).
// Each start is a new generation with fresh native resources; once stopped, its events are dropped and its
// Frame draws nothing. Events are polled every pollMs and handed to onEvents on callbacks.
class GlassesSession(
    private val bridge: GlassesBridge = NativeBridge,
    private val callbacks: Executor,
    private val onEvents: (generation: Long, events: List<GlassesEvent>) -> Unit,
    private val pollMs: Long = 50,
) {
    private val worker: ScheduledExecutorService =
        Executors.newSingleThreadScheduledExecutor { Thread(it, "glasses-session").apply { isDaemon = true } }
    @Volatile var generation = 0L
        private set
    private var poll: Future<*>? = null

    // Connects (link null: the emulated arms) and reports Ready with both arms' status, or Failed.
    fun start(config: StartConfig, link: ArmLink?, done: (StartOutcome) -> Unit) {
        worker.execute {
            val gen = bridge.start(link, config.left, config.right, config.dx, config.dy, config.brightness)
            val outcome = if (gen > 0) {
                generation = gen
                val status = bridge.status(gen)?.let(GlassesStatus::parse)
                if (status != null) StartOutcome.Ready(gen, status) else StartOutcome.Failed(StartOutcome.Failed.BLUETOOTH, "no status")
            } else {
                StartOutcome.Failed((-gen).toInt(), bridge.lastError())
            }
            if (outcome is StartOutcome.Ready) {
                poll = worker.scheduleWithFixedDelay({ pollEvents(gen) }, pollMs, pollMs, TimeUnit.MILLISECONDS)
            }
            callbacks.execute { done(outcome) }
        }
    }

    private fun pollEvents(gen: Long) {
        if (gen != generation) return
        val events = GlassesEvent.parse(bridge.events(gen))
        if (events.isNotEmpty()) callbacks.execute { if (gen == generation) onEvents(gen, events) }
    }

    // Runs block with this generation's Frame on the session thread; null when stopped.
    fun <T> draw(block: (Frame) -> T): Future<T?> = worker.submit<T?> {
        val gen = generation
        if (gen == 0L) null else block(Frame(bridge, gen))
    }

    // Cancels a start in progress at once, then releases the glasses on the session thread; done after
    // the release completed.
    fun stop(done: () -> Unit = {}) {
        bridge.cancel()
        worker.execute {
            generation = 0
            poll?.cancel(false)
            poll = null
            bridge.stop()
            callbacks.execute(done)
        }
    }

    // Stops and ends the session thread.
    fun close() {
        stop()
        worker.shutdown()
    }
}
