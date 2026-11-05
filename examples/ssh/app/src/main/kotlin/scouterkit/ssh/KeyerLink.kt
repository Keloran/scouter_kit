package scouterkit.ssh

import android.annotation.SuppressLint
import android.bluetooth.BluetoothDevice
import android.bluetooth.BluetoothGatt
import android.bluetooth.BluetoothGattCallback
import android.bluetooth.BluetoothGattCharacteristic
import android.bluetooth.BluetoothGattDescriptor
import android.bluetooth.BluetoothManager
import android.bluetooth.BluetoothProfile
import android.content.Context
import android.os.Handler
import android.os.HandlerThread
import android.util.Log
import java.util.UUID

// keyer's key stream (docs/keyer.md): subscribing to KEYS in a connection takes keyer's keys from its HID
// keyboard, unsubscribing before close gives them back. Events (6 bytes: kind, mods, u32 value) go to mosh
// through Native.keyEvent. Connects only to the configured, bonded address.
@Suppress("DEPRECATION")
@SuppressLint("MissingPermission")
class KeyerLink(
    private val context: Context,
    private val address: String,
    private val onState: (String) -> Unit,
) {
    private val thread = HandlerThread("keyer").apply { start() }
    private val handler = Handler(thread.looper)
    private val adapter = context.getSystemService(BluetoothManager::class.java).adapter
    private var gatt: BluetoothGatt? = null
    private var keys: BluetoothGattCharacteristic? = null
    private var mode: BluetoothGattCharacteristic? = null
    private var subscribed = false
    private var wanted = false
    private var refreshed = false
    private var closing: Runnable? = null
    // keyer's GATT link is up (whether or not the keys come): keyer is on, the session runs (ScouterService).
    @Volatile var up = false
        private set

    fun start() = handler.post {
        wanted = true
        open(false)
    }

    fun stop() = handler.post {
        wanted = false
        release()
    }

    fun quit() = handler.post {
        wanted = false
        release()
        handler.postDelayed({ thread.quitSafely() }, 1500)
    }

    // Android reports keyer near (advertising now): a direct connect (about a second) replaces a waiting
    // background one (autoConnect: tens of seconds while the PC holds keyer), as the session waits for the link.
    fun nudge() = handler.post {
        if (!wanted || up || (gatt != null && !backgroundConnect)) return@post
        runCatching { gatt?.close() }
        gatt = null
        open(false)
    }

    private var backgroundConnect = false // the pending connect is autoConnect

    private fun open(background: Boolean) {
        if (gatt != null || !wanted) return
        if (!adapter.isEnabled) return onState("Bluetooth is off")
        val device = adapter.getRemoteDevice(address)
        if (device.bondState != BluetoothDevice.BOND_BONDED) return onState("not bonded (pair keyer in Bluetooth settings)")
        onState("connecting")
        backgroundConnect = background
        gatt = device.connectGatt(context, background, callback, BluetoothDevice.TRANSPORT_LE, BluetoothDevice.PHY_LE_1M_MASK, handler)
    }

    // Gives the keys back (CCCD 0) before closing, else keyer keeps sending to the app while another client
    // (the system's HID host) holds the link. With Bluetooth off the write fails (the link is gone): closed at once.
    private fun release() {
        val g = gatt ?: return
        val k = keys
        if (subscribed && k != null && up) {
            val written = runCatching {
                g.setCharacteristicNotification(k, false)
                val cccd = k.getDescriptor(CCCD)
                cccd.value = BluetoothGattDescriptor.DISABLE_NOTIFICATION_VALUE
                g.writeDescriptor(cccd)
            }.getOrDefault(false)
            if (written) {
                closing = Runnable { close() }.also { handler.postDelayed(it, 1000) }
                return
            }
        }
        close()
    }

    private fun close() {
        closing?.let { handler.removeCallbacks(it) }
        closing = null
        up = false
        runCatching { gatt?.close() }
        gatt = null
        keys = null
        mode = null
        subscribed = false
        onState("off")
    }

    private var discovering = false

    private fun discover(g: BluetoothGatt) {
        if (discovering || g != gatt) return
        discovering = true
        g.discoverServices()
    }

    private fun subscribe(g: BluetoothGatt, c: BluetoothGattCharacteristic) {
        g.setCharacteristicNotification(c, true)
        val cccd = c.getDescriptor(CCCD)
        cccd.value = BluetoothGattDescriptor.ENABLE_NOTIFICATION_VALUE
        g.writeDescriptor(cccd)
    }

    private val callback = object : BluetoothGattCallback() {
        override fun onConnectionStateChange(g: BluetoothGatt, status: Int, state: Int) {
            Log.i(TAG, "keyer: state $state status $status")
            if (g != gatt) return
            if (state == BluetoothProfile.STATE_CONNECTED) {
                up = true
                onState("connected")
                // No MTU request: on a link the system's HID host already holds, Android 12 answers it twice
                // and drops a CCCD write issued in between without a callback. The link's MTU stays.
                discovering = false
                discover(g)
                return
            }
            up = false
            g.close()
            gatt = null
            keys = null
            subscribed = false
            onState(if (wanted) "connecting" else "off")
            if (wanted) open(true) // autoConnect: slow while the PC holds keyer, but costs little
        }

        override fun onServicesDiscovered(g: BluetoothGatt, status: Int) {
            val service = g.getService(SERVICE)
            if (service == null) {
                // Android may still hold keyer's table from before the key stream: refresh it once.
                if (!refreshed) {
                    refreshed = true
                    runCatching { g.javaClass.getMethod("refresh").invoke(g) }
                    handler.postDelayed({ if (gatt == g) g.discoverServices() }, 500)
                    return
                }
                Log.w(TAG, "keyer: no key-stream service")
                return onState("no key stream (old firmware?)")
            }
            keys = service.getCharacteristic(KEYS)
            mode = service.getCharacteristic(MODE)
            mode?.let { subscribe(g, it) } ?: keys?.let { subscribe(g, it) }
        }

        override fun onDescriptorWrite(g: BluetoothGatt, d: BluetoothGattDescriptor, status: Int) {
            val c = d.characteristic
            if (!wanted && c.uuid == KEYS) return close()
            if (status != BluetoothGatt.GATT_SUCCESS) {
                Log.w(TAG, "keyer: subscription to ${c.uuid} failed, status $status")
                return onState("subscription failed ($status)")
            }
            when (c.uuid) {
                MODE -> keys?.let { subscribe(g, it) }
                KEYS -> {
                    subscribed = true
                    mode?.let { g.readCharacteristic(it) } ?: onState("keys")
                }
            }
        }

        override fun onCharacteristicRead(g: BluetoothGatt, c: BluetoothGattCharacteristic, status: Int) {
            if (c.uuid == MODE) showMode(c.value)
        }

        override fun onCharacteristicChanged(g: BluetoothGatt, c: BluetoothGattCharacteristic) = changed(c.uuid, c.value)

        override fun onCharacteristicChanged(g: BluetoothGatt, c: BluetoothGattCharacteristic, value: ByteArray) = changed(c.uuid, value)
    }

    private fun showMode(value: ByteArray?) = onState(if (value?.firstOrNull()?.toInt() == 1) "keys" else "connected (keys to HID)")

    private fun changed(uuid: UUID, value: ByteArray?) {
        if (value == null) return
        if (uuid == MODE) return showMode(value)
        if (uuid != KEYS) return
        var events = 0
        for (i in 0..value.size - 6 step 6) {
            val kind = value[i].toInt()
            if (kind != 1 && kind != 2) continue
            val v = (value[i + 2].toInt() and 0xff) or (value[i + 3].toInt() and 0xff shl 8) or
                (value[i + 4].toInt() and 0xff shl 16) or (value[i + 5].toInt() and 0xff shl 24)
            Native.keyEvent(kind, value[i + 1].toInt() and 0xff, v)
            events++
        }
        keyEvents += events
        if (events == 0) return
        if (BuildConfig.DEBUG) Log.i(TAG, "keyer: $events key events, $keyEvents in all")
    }

    companion object {
        const val TAG = "scouter"
        val SERVICE: UUID = UUID.fromString("08082eb2-be4b-4be8-a5ff-835a1305ea00")
        val KEYS: UUID = UUID.fromString("08082eb2-be4b-4be8-a5ff-835a1305ea01")
        val MODE: UUID = UUID.fromString("08082eb2-be4b-4be8-a5ff-835a1305ea02")
        val CCCD: UUID = UUID.fromString("00002902-0000-1000-8000-00805f9b34fb")
        @Volatile var keyEvents = 0L // since the app started, for tests; never the keys themselves
    }
}
