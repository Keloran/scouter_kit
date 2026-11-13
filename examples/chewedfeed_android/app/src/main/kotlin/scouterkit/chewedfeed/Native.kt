package scouterkit.chewedfeed

// libchewedfeed.so (native/jni.cpp). Use it through GlassesSession, which keeps every call off the UI thread.
object Native {
    // native/boundary.hpp kAbiVersion: checked at start so a stale library fails loudly.
    const val ABI_VERSION = 2

    init {
        System.loadLibrary("chewedfeed")
    }

    @JvmStatic external fun abiVersion(): Int

    // "glasses" or "emulated": the native backend, which must match BuildConfig.FLAVOR.
    @JvmStatic external fun backend(): String

    // Blocks up to 30 s: a generation (> 0), or minus an error (InitError, 100 cancelled, 101 busy).
    // link null: the emulated arms (emulated flavor).
    @JvmStatic external fun start(link: ArmLink?, left: String, right: String, dx: Int, dy: Int, brightness: Int): Long
    @JvmStatic external fun lastError(): String
    @JvmStatic external fun cancel()
    // Blank, panels off, then release: threads joined and reconnects stopped before it returns.
    @JvmStatic external fun stop()

    @JvmStatic external fun loadFont(generation: Long, scale: Int): Int
    @JvmStatic external fun fontMetrics(generation: Long, font: Int): IntArray?
    @JvmStatic external fun measure(generation: Long, font: Int, utf8: ByteArray): Int
    @JvmStatic external fun canvas(generation: Long, width: Int, height: Int): Boolean
    @JvmStatic external fun clear(generation: Long, gray: Int): Boolean
    @JvmStatic external fun fill(generation: Long, x: Int, y: Int, w: Int, h: Int, gray: Int): Boolean
    @JvmStatic external fun text(generation: Long, font: Int, utf8: ByteArray, x: Int, baseline: Int, align: Int, gray: Int): Boolean
    @JvmStatic external fun present(generation: Long, x: Int, y: Int): Boolean
    @JvmStatic external fun blank(generation: Long): Boolean
    @JvmStatic external fun status(generation: Long): IntArray?
    @JvmStatic external fun events(generation: Long): IntArray

    @JvmStatic external fun linkUp(arm: Int, mtu: Int)
    @JvmStatic external fun linkDown(arm: Int)
    @JvmStatic external fun linkData(arm: Int, data: ByteArray)
    @JvmStatic external fun linkFound(arm: Int, address: String)
    @JvmStatic external fun linkProblem(code: Int, text: String)

    @JvmStatic external fun emulatedPanel(arm: Int): ByteArray?
    @JvmStatic external fun emulatedProperty(arm: Int, id: Int, value: Int)
}
