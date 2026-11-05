package scouterkit.ssh

import android.annotation.SuppressLint
import android.app.Activity
import android.bluetooth.BluetoothAdapter
import android.bluetooth.BluetoothDevice
import android.bluetooth.BluetoothManager
import android.bluetooth.le.ScanResult
import android.companion.AssociationInfo
import android.companion.AssociationRequest
import android.companion.BluetoothDeviceFilter
import android.companion.CompanionDeviceManager
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentSender
import android.os.Build
import android.os.Parcelable
import android.util.Log

// The app as keyer's companion: Android reports when keyer comes or goes (ScouterCompanionService), which starts
// and ends the session (the user's battery rule), exempts the app from battery optimisation and lets it start its
// foreground service from the background. One association, confirmed in the system's dialog. The filter is
// keyer's bonded address, so Android offers it at once, also while it is connected (and not advertising). The
// arms are not associated: their presence would start the session without keyer, and while keyer is on
// GlassesLink waits for them anyway.
@Suppress("DEPRECATION")
object CompanionApp {
    const val REQUEST = 7

    private fun manager(context: Context) = context.getSystemService(CompanionDeviceManager::class.java)

    private fun devices(context: Context) = listOf(Settings(context).keyer).filter { it.isNotEmpty() }.map { it.uppercase() }

    // keyer's presence is observed only while Bluetooth is on. Switched off, Android often stays in BLE_ON (Low
    // Energy kept on for other apps), where its presence scan goes on and reported keyer every 10-40 s, each report
    // waking the app; unobserved, keyer is not scanned for. On again, Android reports keyer at once if it is near
    // (or still counted near: then keyer's link is tried and the session ends when it does not come).
    fun observe(context: Context, on: Boolean) {
        if (!ScouterService.bluetooth(context)) return
        val m = manager(context)
        val devices = devices(context)
        val associated = m.associations.map { it.uppercase() }.filter { it in devices }
        for (a in associated) runCatching {
            if (on) m.startObservingDevicePresence(a) else m.stopObservingDevicePresence(a)
        }.onFailure { Log.w("scouter", "companion: presence ${if (on) "on" else "off"}: ${it.message}") }
        if (associated.isNotEmpty())
            Log.i("scouter", "companion: keyer's presence ${if (on) "observed" else "not observed (Bluetooth off)"}")
    }

    // The bonded devices (keyer) the app is not the companion of yet. Associated ones are observed (again:
    // cheap), and associations with others (an earlier keyer, the arms) are dropped. Nothing with Bluetooth off.
    @SuppressLint("MissingPermission")
    fun missing(context: Context): List<String> {
        if (!ScouterService.bluetoothOn(context)) return emptyList()
        val m = manager(context)
        val devices = devices(context)
        val associated = m.associations.map { it.uppercase() }
        for (a in associated) if (a in devices) runCatching { m.startObservingDevicePresence(a) } else m.disassociate(a)
        val adapter = context.getSystemService(BluetoothManager::class.java).adapter
        return devices.filter { it !in associated && adapter.getRemoteDevice(it).bondState == BluetoothDevice.BOND_BONDED }
    }

    fun associate(activity: Activity, address: String, onError: (String) -> Unit) {
        val filter = BluetoothDeviceFilter.Builder().setAddress(address).build()
        val request = AssociationRequest.Builder().addDeviceFilter(filter).setSingleDevice(true).build()
        manager(activity).associate(request, object : CompanionDeviceManager.Callback() {
            override fun onDeviceFound(sender: IntentSender) {
                activity.startIntentSenderForResult(sender, REQUEST, null, 0, 0, 0)
            }

            override fun onFailure(error: CharSequence?) = onError("${error ?: "keyer was not found"}")
        }, null)
    }

    // The dialog's answer (Android 13+ also as the association): from now on Android reports keyer's presence.
    fun result(context: Context, data: Intent?): String {
        val address = when (val d = data?.getParcelableExtra<Parcelable>(CompanionDeviceManager.EXTRA_DEVICE)) {
            is BluetoothDevice -> d.address
            is ScanResult -> d.device.address
            else -> if (Build.VERSION.SDK_INT >= 33)
                data?.getParcelableExtra(CompanionDeviceManager.EXTRA_ASSOCIATION, AssociationInfo::class.java)?.deviceMacAddress?.toString()
            else null
        }?.uppercase() ?: return "not allowed"
        val m = manager(context)
        if (address !in devices(context)) {
            m.disassociate(address)
            return "another device answered"
        }
        m.startObservingDevicePresence(address)
        Log.i("scouter", "companion: associated ${Settings(context).side(address)}")
        return "allowed"
    }
}

// The user's Bluetooth switch, also while the app is not running (Android sends the broadcast to background
// receivers): off (BLE_ON as well) stops the presence reports of keyer, on starts them. The running service ends
// itself on off. After an update the reports follow the switch as it is.
class BluetoothSwitch : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        val on = when (intent.action) {
            BluetoothAdapter.ACTION_STATE_CHANGED -> when (intent.getIntExtra(BluetoothAdapter.EXTRA_STATE, -1)) {
                BluetoothAdapter.STATE_ON -> true
                BluetoothAdapter.STATE_TURNING_OFF, BluetoothAdapter.STATE_OFF -> false
                else -> return
            }
            Intent.ACTION_MY_PACKAGE_REPLACED -> ScouterService.bluetoothOn(context)
            else -> return
        }
        if (!on) Session.near = null
        CompanionApp.observe(context, on)
        Session.changed()
    }
}
