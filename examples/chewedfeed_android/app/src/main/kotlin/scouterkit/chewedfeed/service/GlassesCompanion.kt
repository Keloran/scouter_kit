package scouterkit.chewedfeed.service

import android.Manifest
import android.annotation.SuppressLint
import android.app.Activity
import android.bluetooth.le.ScanFilter
import android.companion.AssociationInfo
import android.companion.AssociationRequest
import android.companion.BluetoothLeDeviceFilter
import android.companion.CompanionDeviceManager
import android.companion.CompanionDeviceService
import android.content.Context
import android.content.IntentSender
import android.content.pm.PackageManager
import android.os.Build
import android.os.Handler
import android.os.Looper

// The glasses as this app's companion device (Companion Device Manager), optional: once the user confirms
// Android's dialog, Android may start the glasses service from the background (a sticky restart, or the glasses
// coming into range) and tells GlassesPresence when they appear. Manual start works without it; nothing here
// survives a force-stop.
// ponytail: one association, the left arm; both arms come and go together (case, head). Add the right if an
// arm-only appearance ever matters.
@SuppressLint("MissingPermission")
object GlassesCompanion {
    const val REQUEST = 76

    private fun cdm(c: Context) = c.getSystemService(CompanionDeviceManager::class.java)

    @Suppress("DEPRECATION")
    fun associated(c: Context, address: String): Boolean {
        val m = cdm(c) ?: return false
        return if (Build.VERSION.SDK_INT >= 33) m.myAssociations.any { it.deviceMacAddress?.toString().equals(address, true) }
        else m.associations.any { it.equals(address, true) }
    }

    // Shows Android's confirmation (the arm must be advertising: not connected to anything). done(error or null).
    fun associate(a: Activity, address: String, done: (String?) -> Unit) {
        val m = cdm(a) ?: return done("Companion devices are not supported on this phone")
        val filter = BluetoothLeDeviceFilter.Builder().setScanFilter(ScanFilter.Builder().setDeviceAddress(address).build()).build()
        val request = AssociationRequest.Builder().addDeviceFilter(filter).setSingleDevice(true).build()
        val callback = object : CompanionDeviceManager.Callback() {
            @Deprecated("API 33 adds onAssociationPending")
            override fun onDeviceFound(sender: IntentSender) = launch(sender)
            override fun onAssociationPending(sender: IntentSender) = launch(sender)
            override fun onAssociationCreated(info: AssociationInfo) {} // the activity result follows
            override fun onFailure(error: CharSequence?) = done(error?.toString() ?: "association failed")
            private fun launch(sender: IntentSender) {
                runCatching { a.startIntentSenderForResult(sender, REQUEST, null, 0, 0, 0) }.onFailure { done(it.message) }
            }
        }
        @Suppress("DEPRECATION")
        m.associate(request, callback, Handler(Looper.getMainLooper()))
    }

    // After the user confirmed: report presence from now on.
    @Suppress("DEPRECATION")
    fun observe(c: Context, address: String): String? =
        runCatching { cdm(c)?.startObservingDevicePresence(address) }.exceptionOrNull()?.message

    @Suppress("DEPRECATION")
    fun forget(c: Context, address: String) {
        val m = cdm(c) ?: return
        runCatching { m.stopObservingDevicePresence(address) }
        if (Build.VERSION.SDK_INT >= 33) m.myAssociations.filter { it.deviceMacAddress?.toString().equals(address, true) }.forEach { m.disassociate(it.id) }
        else runCatching { m.disassociate(address) }
    }
}

// Android's notice that the associated glasses came into range: the service starts if the user had started it
// (and was not stopped), or a waiting retry runs now.
class GlassesPresence : CompanionDeviceService() {
    @Deprecated("API 33 adds the AssociationInfo version")
    override fun onDeviceAppeared(address: String) = appeared()
    override fun onDeviceAppeared(info: AssociationInfo) = appeared()

    private fun appeared() {
        val wanted = getSharedPreferences("service", Context.MODE_PRIVATE).getBoolean("wanted", false)
        val permitted = checkSelfPermission(Manifest.permission.BLUETOOTH_CONNECT) == PackageManager.PERMISSION_GRANTED
        if (wanted && permitted) runCatching { GlassesService.present(this) } // refused: the app's Start still works
    }
}
