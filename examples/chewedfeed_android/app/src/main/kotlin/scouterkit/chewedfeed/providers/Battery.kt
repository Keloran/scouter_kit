package scouterkit.chewedfeed.providers

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.os.BatteryManager
import android.os.Handler
import scouterkit.chewedfeed.GlassesStatus
import scouterkit.chewedfeed.render.Batteries

// The case reports its battery only while an arm is in it (and briefly: the firmware clears it 3 s after the
// case's last message), and the dashboard is hidden then. So the last real reading is kept, from the arms' CASE
// events (VM.md §6.3: bit 2 CASE_CHARGING, bits 8-15 CASE_BATT_PCT, 0xFF unknown) and from status polls while in
// the case, and shown when the glasses are out. Process-wide: it outlives a glasses session.
object LastCase {
    @Volatile var percent: Int? = null
        private set
    @Volatile var charging = false
        private set

    // A CASE input's value.
    fun event(value: Int) {
        val p = (value ushr 8) and 0xFF
        if (p > 100) return
        percent = p
        charging = value and 4 != 0
    }

    // A status poll's reading (null: not in the case now: keep the last one).
    fun polled(p: Int?) {
        if (p != null && p in 0..100) percent = p
    }

    internal fun clear() {
        percent = null
        charging = false
    }
}

// The status panel's batteries: each arm's only while that arm is connected (a reading from before a disconnect
// is not current: unknown, not 0%); the case's as it last reported (LastCase); this phone's from Android. host is
// this phone; a laptop's battery, if ever shown here, needs its own label.
fun batteries(st: GlassesStatus, phone: Int?, phoneCharging: Boolean = false): Batteries {
    LastCase.polled(st.caseBattery)
    return Batteries(
        left = st.left.battery?.takeIf { st.left.connected && it in 0..100 },
        right = st.right.battery?.takeIf { st.right.connected && it in 0..100 },
        case = LastCase.percent,
        host = phone?.takeIf { it in 0..100 },
        caseCharging = LastCase.charging && LastCase.percent != null,
        hostCharging = phoneCharging && phone != null,
    )
}

// level / scale from ACTION_BATTERY_CHANGED, as a percentage; null when unknown.
fun batteryPercent(level: Int, scale: Int): Int? = if (level < 0 || scale <= 0) null else (level * 100 / scale).coerceIn(0, 100)

// This phone's battery from Android's battery broadcasts (no polling): the latest percentage, null until known.
class PhoneBattery(private val context: Context, private val handler: Handler) {
    @Volatile var percent: Int? = null
        private set
    @Volatile var charging = false // plugged in (charging, or full on the charger)
        private set
    private var registered = false
    private val receiver = object : BroadcastReceiver() {
        override fun onReceive(c: Context, i: Intent) {
            percent = batteryPercent(i.getIntExtra(BatteryManager.EXTRA_LEVEL, -1), i.getIntExtra(BatteryManager.EXTRA_SCALE, -1))
            charging = i.getIntExtra(BatteryManager.EXTRA_PLUGGED, 0) != 0
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
