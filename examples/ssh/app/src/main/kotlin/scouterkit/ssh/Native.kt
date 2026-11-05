package scouterkit.ssh

// libscouter_ssh.so: ScouterKit, mosh and the glasses renderer (native/jni.cpp).
object Native {
    // ScouterKit's InitError values for linkProblem.
    const val ERROR_BLUETOOTH = 1
    const val ERROR_BONDING = 3
    const val ERROR_FIRMWARE = 4

    @Volatile var onChanged: (() -> Unit)? = null

    init {
        System.loadLibrary("scouter_ssh")
    }

    @JvmStatic external fun version(): String

    // Blocks until both arms are ready (at most 30 s), then starts the terminal on them; null link: the
    // emulated arms. `font`: departure_mono_nerd.skf. Returns an error or null.
    // ignoreWorn (debug): the terminal also shows with the glasses folded.
    @JvmStatic external fun glassesStart(link: GlassesLink?, left: String, right: String, dx: Int, dy: Int,
                                         brightness: Int, angle: Int, font: ByteArray, ignoreWorn: Boolean): String?
    // keep: the terminal's program and its last screen stay on the arms, which show them when the head looks
    // up (keyer off); otherwise the programs stop.
    @JvmStatic external fun glassesStop(keep: Boolean)
    // Read-back of what the arms show against what was sent (T3); blocks for seconds.
    @JvmStatic external fun check(): String
    @JvmStatic external fun emulatedPanel(arm: Int): ByteArray?
    @JvmStatic external fun emulatedProperty(arm: Int, id: Int, value: Int)
    const val PROP_PITCH = 0x1B

    @JvmStatic external fun linkUp(arm: Int, mtu: Int)
    @JvmStatic external fun linkDown(arm: Int)
    @JvmStatic external fun linkData(arm: Int, data: ByteArray)
    @JvmStatic external fun linkFound(arm: Int, address: String)
    @JvmStatic external fun linkProblem(code: Int, text: String)

    @JvmStatic external fun moshStart(ip: String, port: Int, key: String, cols: Int, rows: Int): String?
    // No traffic while paused (keyer off); the session resumes where it was.
    @JvmStatic external fun moshPause(on: Boolean)
    @JvmStatic external fun moshStop()
    @JvmStatic external fun resize(cols: Int, rows: Int)
    @JvmStatic external fun keyEvent(kind: Int, mods: Int, value: Int)
    // keyer held: the terminal shows on the glasses whatever the head's angle (R25).
    @JvmStatic external fun force(on: Boolean)
    // heard, ended, silent ms, left up, right up, terminal shown on an eye.
    @JvmStatic external fun status(): IntArray
    // The mosh session's last error and the terminal's title.
    @JvmStatic external fun texts(): Array<String>
    @JvmStatic external fun snapshot(): ByteArray?

    // From the native threads: a new screen or status.
    @JvmStatic fun changed() {
        onChanged?.invoke()
    }
}
