package scouterkit.ssh

import android.companion.CompanionDeviceService
import android.util.Log

// Android binds this while keyer, the associated device, is near and says when it comes or goes (Android 12:
// connected or seen advertising; gone 10 s after neither). keyer near starts the session (its link is tried),
// keyer gone ends it (ScouterService, the user's battery rule). With Bluetooth off (BLE_ON too) a report means
// nothing: keyer is no longer observed (BluetoothSwitch observes it again when Bluetooth is on).
@Suppress("DEPRECATION", "OVERRIDE_DEPRECATION")
class ScouterCompanionService : CompanionDeviceService() {
    override fun onDeviceAppeared(address: String) {
        if (bluetoothOff()) return
        Log.i("scouter", "companion: ${Settings(this).side(address)} appeared")
        Session.near = (Session.near ?: emptySet()) + address.uppercase()
        runCatching { ScouterService.send(this, ScouterService.ACTION_NEAR) }
            .onFailure { Log.w("scouter", "companion: the service did not start: ${it.message}") }
    }

    override fun onDeviceDisappeared(address: String) {
        if (bluetoothOff()) return
        Log.i("scouter", "companion: ${Settings(this).side(address)} disappeared")
        Session.near = (Session.near ?: emptySet()) - address.uppercase()
        if (ScouterService.running) ScouterService.send(this, ScouterService.ACTION_NEAR)
    }

    // A report with Bluetooth off (the switch's broadcast missed, e.g. before an update) or not allowed: ignored,
    // and keyer is unobserved (with Bluetooth allowed).
    private fun bluetoothOff(): Boolean {
        if (ScouterService.bluetoothOn(this)) return false
        Log.i("scouter", "companion: a report with Bluetooth off or not allowed, ignored")
        CompanionApp.observe(this, false)
        return true
    }
}
