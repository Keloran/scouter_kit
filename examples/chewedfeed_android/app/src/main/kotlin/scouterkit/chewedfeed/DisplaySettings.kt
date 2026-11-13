package scouterkit.chewedfeed

import android.content.Context

// This phone's display settings for the glasses: eye alignment (dx, dy, pixels) and text scale. Per device:
// they never transfer to or from the laptop (policy.json snapshot "geometry": device-local settings win).
data class DisplaySettings(val dx: Int = 0, val dy: Int = 0, val scale: Int = 2) {
    // null when every value is in range, else what to fix (the desktop's ranges; scale whole numbers for now).
    fun problem(): String? = when {
        dx !in -240..240 -> "dx must be between -240 and 240"
        dy !in -120..120 -> "dy must be between -120 and 120"
        scale !in 1..4 -> "text scale must be 1 to 4"
        else -> null
    }

    fun save(context: Context) {
        context.getSharedPreferences(PREFS, Context.MODE_PRIVATE).edit()
            .putInt("dx", dx).putInt("dy", dy).putInt("scale", scale).apply()
    }

    companion object {
        private const val PREFS = "glasses"

        fun load(context: Context): DisplaySettings {
            val p = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
            val s = DisplaySettings(p.getInt("dx", 0), p.getInt("dy", 0), p.getInt("scale", 2))
            return if (s.problem() == null) s else DisplaySettings()
        }
    }
}
