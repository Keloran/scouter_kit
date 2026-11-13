package scouterkit.chewedfeed.providers

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.os.BatteryManager
import android.os.Handler
import scouterkit.chewedfeed.GlassesStatus
import scouterkit.chewedfeed.render.Batteries

// The status panel's batteries: each arm's only while that arm is connected (a reading from before a disconnect
// is not current: unknown, not 0%); the case's only as the arms report it (in the case); this phone's from
// Android. host is this phone; a laptop's battery, if ever shown here, needs its own label.
fun batteries(st: GlassesStatus, phone: Int?) = Batteries(
    left = st.left.battery?.takeIf { st.left.connected && it in 0..100 },
    right = st.right.battery?.takeIf { st.right.connected && it in 0..100 },
    case = st.caseBattery?.takeIf { it in 0..100 },
    host = phone?.takeIf { it in 0..100 },
)

// level / scale from ACTION_BATTERY_CHANGED, as a percentage; null when unknown.
fun batteryPercent(level: Int, scale: Int): Int? = if (level < 0 || scale <= 0) null else (level * 100 / scale).coerceIn(0, 100)

// This phone's battery from Android's battery broadcasts (no polling): the latest percentage, null until known.
class PhoneBattery(private val context: Context, private val handler: Handler) {
    @Volatile var percent: Int? = null
        private set
    private var registered = false
    private val receiver = object : BroadcastReceiver() {
        override fun onReceive(c: Context, i: Intent) {
            percent = batteryPercent(i.getIntExtra(BatteryManager.EXTRA_LEVEL, -1), i.getIntExtra(BatteryManager.EXTRA_SCALE, -1))
        }
    }

    fun start() {
        if (registered) return
        registered = true
        // The sticky broadcast answers at once with the current level.
        context.registerReceiver(receiver, IntentFilter(Intent.ACTION_BATTERY_CHANGED), null, handler)?.let { receiver.onReceive(context, it) }
    }

    fun stop() {
        if (!registered) return
        registered = false
        runCatching { context.unregisterReceiver(receiver) }
        percent = null
    }
}
