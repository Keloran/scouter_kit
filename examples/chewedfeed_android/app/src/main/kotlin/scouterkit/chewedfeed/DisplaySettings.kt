package scouterkit.chewedfeed

import android.content.Context
import scouterkit.chewedfeed.dashboard.DisplayOptions
import scouterkit.chewedfeed.settings.KeyValue
import scouterkit.chewedfeed.settings.Prefs

// This phone's display settings for the glasses: eye alignment (dx, dy, pixels), text scale, and the head tilt
// that shows the dashboard: while the pitch is above showAbove or below showBelow (degrees, + looking up), as the
// desktop's -show-above / -show-below; alwaysNotes shows popups at any angle. Per device: they never transfer to
// or from the laptop (policy.json snapshot "geometry": device-local settings win).
data class DisplaySettings(
    val dx: Int = 0, val dy: Int = 0, val scale: Double = 2.0,
    val showAbove: Int = DisplayOptions().showAbove, val showBelow: Int = DisplayOptions().showBelow,
    val alwaysNotes: Boolean = DisplayOptions().alwaysShowNotifications,
) {
    // null when every value is in range, else what to fix (the desktop's ranges).
    fun problem(): String? = when {
        dx !in -240..240 -> "dx must be between -240 and 240"
        dy !in -120..120 -> "dy must be between -120 and 120"
        scale.isNaN() || scale !in 0.1..3.0 -> "text scale must be between 0.1 and 3"
        showAbove !in -90..90 || showBelow !in -90..90 -> "tilt angles must be between -90 and 90"
        else -> null
    }

    fun save(context: Context) = save(Prefs(context, PREFS))

    // Only valid settings are saved: nothing invalid can reach the native code.
    fun save(kv: KeyValue) {
        require(problem() == null) { problem()!! }
        kv.edit {
            int("dx", dx); int("dy", dy); float("text_scale", scale.toFloat()); remove("scale")
            int("show_above", showAbove); int("show_below", showBelow); bool("always_notes", alwaysNotes)
        }
    }

    // The dashboard's visibility with these tilt angles.
    fun displayOptions(base: DisplayOptions = DisplayOptions()) =
        base.copy(showAbove = showAbove, showBelow = showBelow, alwaysShowNotifications = alwaysNotes)

    companion object {
        private const val PREFS = "glasses"

        fun load(context: Context) = load(Prefs(context, PREFS))

        fun load(kv: KeyValue): DisplaySettings {
            val d = DisplaySettings()
            // "scale" was a whole number before fractional scales (0.1 steps; the float keeps 1.5 exact).
            val scale = if (kv.has("text_scale")) Math.round(kv.float("text_scale", 2f) * 100) / 100.0 else kv.int("scale", 2).toDouble()
            val s = DisplaySettings(kv.int("dx", d.dx), kv.int("dy", d.dy), scale,
                kv.int("show_above", d.showAbove), kv.int("show_below", d.showBelow), kv.bool("always_notes", d.alwaysNotes))
            return if (s.problem() == null) s else d
        }
    }
}
