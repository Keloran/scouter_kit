package scouterkit.chewedfeed.bluetooth

import android.annotation.SuppressLint
import android.bluetooth.BluetoothAdapter
import android.bluetooth.BluetoothDevice
import android.bluetooth.BluetoothGatt
import android.bluetooth.BluetoothGattCallback
import android.bluetooth.BluetoothGattCharacteristic
import android.bluetooth.BluetoothGattDescriptor
import android.bluetooth.BluetoothProfile
import android.content.Context
import android.os.Handler
import android.os.HandlerThread
import scouterkit.chewedfeed.Native
import java.util.UUID

// Android's BLE stack behind the Ble.kt interfaces. Adapted from examples/ssh GlassesLink.kt (same subtree licence).

class HandlerScheduler(name: String = "glasses-link") : Scheduler {
    private val thread = HandlerThread(name).apply { start() }
    private val handler = Handler(thread.looper)
    val looperHandler: Handler get() = handler

    override fun post(task: () -> Unit) {
        handler.post(task)
    }

    override fun postDelayed(ms: Long, task: () -> Unit): Cancellable {
        val r = Runnable(task)
        handler.postDelayed(r, ms)
        return object : Cancellable {
            override fun cancel() = handler.removeCallbacks(r)
        }
    }

    fun quit() {
        thread.quitSafely()
    }
}

@SuppressLint("MissingPermission")
class AndroidBleAdapter(private val context: Context, private val adapter: BluetoothAdapter, private val handler: Handler) : BleAdapter {
    override val enabled get() = adapter.isEnabled

    override fun device(address: String): BleDevice {
        val d = adapter.getRemoteDevice(address)
        return object : BleDevice {
            override val bonded get() = d.bondState == BluetoothDevice.BOND_BONDED
            override fun connect(background: Boolean, callbacks: BleCallbacks): BleConnection = AndroidConnection(context, d, background, callbacks, handler)
        }
    }
}

@SuppressLint("MissingPermission")
@Suppress("DEPRECATION")
private class AndroidConnection(context: Context, device: BluetoothDevice, background: Boolean, private val cb: BleCallbacks, handler: Handler) : BleConnection {
    private var chars = arrayOfNulls<BluetoothGattCharacteristic>(2)
    private var evt: BluetoothGattCharacteristic? = null

    private val gatt: BluetoothGatt = device.connectGatt(context, background, object : BluetoothGattCallback() {
        override fun onConnectionStateChange(g: BluetoothGatt, status: Int, state: Int) =
            cb.onConnectionState(state == BluetoothProfile.STATE_CONNECTED, status)

        override fun onMtuChanged(g: BluetoothGatt, mtu: Int, status: Int) = cb.onMtu(mtu, status == BluetoothGatt.GATT_SUCCESS)

        override fun onServicesDiscovered(g: BluetoothGatt, status: Int) {
            val service = g.getService(SERVICE)
            val cmd = service?.getCharacteristic(CMD)
            val ctrl = service?.getCharacteristic(CTRL)
            evt = service?.getCharacteristic(EVT)
            chars = arrayOf(cmd, ctrl)
            cb.onServices(status == BluetoothGatt.GATT_SUCCESS, cmd != null && ctrl != null && evt != null)
        }

        override fun onDescriptorWrite(g: BluetoothGatt, d: BluetoothGattDescriptor, status: Int) = cb.onEventsEnabled(status)

        override fun onCharacteristicWrite(g: BluetoothGatt, c: BluetoothGattCharacteristic, status: Int) = cb.onWriteDone()

        override fun onCharacteristicChanged(g: BluetoothGatt, c: BluetoothGattCharacteristic) {
            if (c.uuid == EVT) cb.onData(c.value)
        }

        override fun onCharacteristicChanged(g: BluetoothGatt, c: BluetoothGattCharacteristic, value: ByteArray) {
            if (c.uuid == EVT) cb.onData(value)
        }
    }, BluetoothDevice.TRANSPORT_LE, BluetoothDevice.PHY_LE_1M_MASK, handler)

    override fun requestMtu(mtu: Int) {
        gatt.requestMtu(mtu)
    }

    override fun preferTwoMegabit() =
        gatt.setPreferredPhy(BluetoothDevice.PHY_LE_2M_MASK, BluetoothDevice.PHY_LE_2M_MASK, BluetoothDevice.PHY_OPTION_NO_PREFERRED)

    override fun discoverServices() {
        gatt.discoverServices()
    }

    override fun enableEvents(): Boolean {
        val c = evt ?: return false
        if (!gatt.setCharacteristicNotification(c, true)) return false
        val cccd = c.getDescriptor(CCCD) ?: return false
        cccd.value = BluetoothGattDescriptor.ENABLE_NOTIFICATION_VALUE
        return gatt.writeDescriptor(cccd)
    }

    override fun write(channel: Int, data: ByteArray): Boolean {
        val c = chars.getOrNull(channel) ?: return false
        c.writeType = BluetoothGattCharacteristic.WRITE_TYPE_NO_RESPONSE
        c.value = data
        return gatt.writeCharacteristic(c)
    }

    override fun close() = gatt.close()

    companion object {
        val SERVICE: UUID = UUID.fromString("f3da4d59-277a-4fc9-a1ed-41c9c22d4400")
        val CMD: UUID = UUID.fromString("f3da4d59-277a-4fc9-a1ed-41c9c22d4401")
        val CTRL: UUID = UUID.fromString("f3da4d59-277a-4fc9-a1ed-41c9c22d4402")
        val EVT: UUID = UUID.fromString("f3da4d59-277a-4fc9-a1ed-41c9c22d4403")
        val CCCD: UUID = UUID.fromString("00002902-0000-1000-8000-00805f9b34fb")
    }
}

// The link events into the native transport (native/link_transport.cpp).
object NativeEvents : NativeLinkEvents {
    override fun linkUp(arm: Int, mtu: Int) = Native.linkUp(arm, mtu)
    override fun linkDown(arm: Int) = Native.linkDown(arm)
    override fun linkData(arm: Int, data: ByteArray) = Native.linkData(arm, data)
    override fun linkFound(arm: Int, address: String) = Native.linkFound(arm, address)
    override fun linkProblem(code: Int, text: String) = Native.linkProblem(code, text)
}
