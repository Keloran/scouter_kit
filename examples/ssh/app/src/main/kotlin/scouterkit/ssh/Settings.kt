package scouterkit.ssh

import android.content.Context

// Plain settings (R6): secrets live in the Vault, not here.
class Settings(context: Context) {
    private val prefs = context.getSharedPreferences("settings", Context.MODE_PRIVATE)

    var host: String by Text("host", "")
    var port: Int by Number("port", 22)
    var user: String by Text("user", "")
    var command: String by Text("command", "")
    var moshPorts: String by Text("mosh_ports", "") // mosh-server's UDP ports, e.g. 60000:60009; empty: its default
    var dx: Int by Number("dx", 100)
    var dy: Int by Number("dy", 9)
    var angle: Int by Number("angle", 20)
    var brightness: Int by Number("brightness", 21)
    var left: String by Text("left", "")
    var right: String by Text("right", "")
    var keyer: String by Text("keyer", "") // bonded address; empty: no keyer link
    // Debug builds: the terminal shows with the glasses folded (WORN 0), for tests on the table.
    var ignoreWorn: Boolean
        get() = prefs.getBoolean("ignore_worn", false)
        set(v) = prefs.edit().putBoolean("ignore_worn", v).apply()
    // The app connects to G1 arms only when this is on and both addresses are set (off by default).
    var glassesAllowed: Boolean
        get() = prefs.getBoolean("glasses_allowed", false)
        set(v) = prefs.edit().putBoolean("glasses_allowed", v).apply()

    // An address as the arm (or keyer) it is, for logs and the UI.
    fun side(address: String) = when {
        address.equals(left, ignoreCase = true) -> "left arm"
        address.equals(right, ignoreCase = true) -> "right arm"
        address.equals(keyer, ignoreCase = true) -> "keyer"
        else -> "another device"
    }

    val cols get() = grid(dx, dy).first
    val rows get() = grid(dx, dy).second

    companion object {
        // The terminal's size for dx and dy: the binocular area in 7 x 14 cells (R19).
        fun grid(dx: Int, dy: Int) = (640 - dx) / 7 to (480 - dy) / 14
    }

    private inner class Text(val key: String, val default: String) {
        operator fun getValue(owner: Any, p: Any): String = prefs.getString(key, default) ?: default
        operator fun setValue(owner: Any, p: Any, v: String) = prefs.edit().putString(key, v).apply()
    }

    private inner class Number(val key: String, val default: Int) {
        operator fun getValue(owner: Any, p: Any): Int = prefs.getInt(key, default)
        operator fun setValue(owner: Any, p: Any, v: Int) = prefs.edit().putInt(key, v).apply()
    }
}
