package scouterkit.chewedfeed

import android.content.Context

// This phone's display settings for the glasses: eye alignment (dx, dy, pixels) and text scale. Per device:
// they never transfer to or from the laptop (policy.json snapshot "geometry": device-local settings win).
data class DisplaySettings(val dx: Int = 0, val dy: Int = 0, val scale: Double = 2.0) {
    // null when every value is in range, else what to fix (the desktop's ranges).
    fun problem(): String? = when {
        dx !in -240..240 -> "dx must be between -240 and 240"
        dy !in -120..120 -> "dy must be between -120 and 120"
        scale.isNaN() || scale !in 0.1..3.0 -> "text scale must be between 0.1 and 3"
        else -> null
    }

    fun save(context: Context) {
        context.getSharedPreferences(PREFS, Context.MODE_PRIVATE).edit()
            .putInt("dx", dx).putInt("dy", dy).putFloat("text_scale", scale.toFloat()).remove("scale").apply()
    }

    companion object {
        private const val PREFS = "glasses"

        fun load(context: Context): DisplaySettings {
            val p = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
            // "scale" was a whole number before fractional scales (0.1 steps; the float keeps 1.5 exact).
            val scale = if (p.contains("text_scale")) Math.round(p.getFloat("text_scale", 2f) * 100) / 100.0 else p.getInt("scale", 2).toDouble()
            val s = DisplaySettings(p.getInt("dx", 0), p.getInt("dy", 0), scale)
            return if (s.problem() == null) s else DisplaySettings()
        }
    }
}
