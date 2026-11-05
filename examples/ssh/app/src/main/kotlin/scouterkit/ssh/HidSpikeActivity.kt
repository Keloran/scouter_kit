package scouterkit.ssh

import android.Manifest
import android.app.Activity
import android.bluetooth.BluetoothDevice
import android.bluetooth.BluetoothGatt
import android.bluetooth.BluetoothGattCallback
import android.bluetooth.BluetoothGattCharacteristic
import android.bluetooth.BluetoothGattDescriptor
import android.bluetooth.BluetoothManager
import android.bluetooth.BluetoothProfile
import android.bluetooth.le.ScanCallback
import android.bluetooth.le.ScanFilter
import android.bluetooth.le.ScanResult
import android.bluetooth.le.ScanSettings
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.os.ParcelUuid
import android.util.Log
import android.widget.ScrollView
import android.widget.TextView
import java.util.UUID

// Can an app read a BLE keyboard's HID-over-GATT reports itself? Connects to keyer (a HID device named
// 𝖒𝖆𝖋.🎹 or maf.*, or the address in the `address` extra) and tries, each on a fresh connection: a
// readable control characteristic, the report map, and notifications of the input reports.
@Suppress("DEPRECATION", "MissingPermission")
class HidSpikeActivity : Activity() {
    private val handler = Handler(Looper.getMainLooper())
    private lateinit var text: TextView
    private var device: BluetoothDevice? = null
    private var gatt: BluetoothGatt? = null
    private var step = 0
    private var token = 0 // advances when a step ends or waits longer: stale timeouts do nothing
    private var reports = 0
    private val results = mutableListOf<String>()

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        text = TextView(this).apply { setPadding(24, 24, 24, 24); textSize = 12f }
        setContentView(ScrollView(this).apply { addView(text) })
        requestPermissions(arrayOf(Manifest.permission.BLUETOOTH_SCAN, Manifest.permission.BLUETOOTH_CONNECT), 1)
    }

    override fun onRequestPermissionsResult(code: Int, permissions: Array<String>, grants: IntArray) {
        if (grants.any { it != 0 }) return say("permissions refused")
        val adapter = getSystemService(BluetoothManager::class.java).adapter
        adapter.getProfileProxy(this, object : BluetoothProfile.ServiceListener {
            override fun onServiceConnected(profile: Int, proxy: BluetoothProfile) {
                say("HID host profile: ${proxy.connectedDevices.size} connected input device(s)")
            }
            override fun onServiceDisconnected(profile: Int) {}
        }, HID_HOST)
        say("Android ${android.os.Build.VERSION.RELEASE} (SDK ${android.os.Build.VERSION.SDK_INT}), ${android.os.Build.DISPLAY}, patch ${android.os.Build.VERSION.SECURITY_PATCH}")
        val address = intent.getStringExtra("address")
        if (address != null) return start(adapter.getRemoteDevice(address))
        say("scanning for keyer (HID service, name maf) for 15 s")
        val scanner = adapter.bluetoothLeScanner
        val callback = object : ScanCallback() {
            override fun onScanResult(type: Int, result: ScanResult) {
                val name = result.scanRecord?.deviceName ?: ""
                if (device != null || !(name.contains("𝖒𝖆𝖋") || name.contains("maf", ignoreCase = true))) return
                scanner.stopScan(this)
                say("found ${result.device.address} '$name' rssi ${result.rssi}")
                if (intent.getBooleanExtra("scan_only", false)) device = result.device else start(result.device)
            }
        }
        val filter = ScanFilter.Builder().setServiceUuid(ParcelUuid(uuid16(0x1812))).build()
        scanner.startScan(listOf(filter), ScanSettings.Builder().setScanMode(ScanSettings.SCAN_MODE_LOW_LATENCY).build(), callback)
        handler.postDelayed({ if (device == null) { scanner.stopScan(callback); say("nothing found") } }, 15000)
    }

    private fun start(d: BluetoothDevice) {
        device = d
        say("device ${d.address} bond state ${d.bondState} (10 none, 12 bonded)")
        connect()
    }

    private fun connect() {
        gatt = device!!.connectGatt(this, false, callback, BluetoothDevice.TRANSPORT_LE)
        timeout(20000) { "no connection in 20 s (is it connected to another host?)" }
    }

    private fun timeout(ms: Long, result: () -> String) {
        val t = token
        handler.postDelayed({ if (t == token) next(result()) }, ms)
    }

    private fun next(result: String) {
        token++
        results += "step $step: $result"
        say("RESULT step $step: $result")
        gatt?.close()
        gatt = null
        step++
        if (step < STEPS) handler.postDelayed({ connect() }, 1500)
        else say("SPIKE DONE\n" + results.joinToString("\n"))
    }

    private fun run(g: BluetoothGatt) {
        val hid = g.getService(uuid16(0x1812))
        val battery = g.getService(uuid16(0x180F))?.getCharacteristic(uuid16(0x2A19))
        val reportMap = hid?.getCharacteristic(uuid16(0x2A4B))
        val input = hid?.characteristics?.firstOrNull {
            it.uuid == uuid16(0x2A4D) && it.properties and BluetoothGattCharacteristic.PROPERTY_NOTIFY != 0
        }
        timeout(5000) { "no callback in 5 s (blocked)" }
        when (step) {
            0 -> if (battery == null) next("no battery level") else say("read battery: ${g.readCharacteristic(battery)}")
            1 -> if (reportMap == null) next("no report map") else say("read report map: ${g.readCharacteristic(reportMap)}")
            2 -> {
                if (input == null) return next("no input report")
                say("local notify on ${input.instanceId}: ${g.setCharacteristicNotification(input, true)}")
                val cccd = input.getDescriptor(uuid16(0x2902))
                cccd.value = BluetoothGattDescriptor.ENABLE_NOTIFICATION_VALUE
                say("write CCCD: ${g.writeDescriptor(cccd)}; press keys on the keyboard now")
            }
        }
    }

    private val callback = object : BluetoothGattCallback() {
        override fun onConnectionStateChange(g: BluetoothGatt, status: Int, state: Int) {
            say("connection state $state status $status")
            val t = token
            if (state == BluetoothProfile.STATE_CONNECTED) g.discoverServices()
            else handler.post { if (t == token) next("disconnected (status $status)") }
        }

        override fun onServicesDiscovered(g: BluetoothGatt, status: Int) {
            if (step == 0) for (service in g.services) {
                say("service ${service.uuid}")
                for (c in service.characteristics)
                    say("  char ${c.uuid} props 0x${c.properties.toString(16)} descriptors ${c.descriptors.size}")
            }
            handler.post {
                token++
                run(g)
            }
        }

        override fun onCharacteristicRead(g: BluetoothGatt, c: BluetoothGattCharacteristic, status: Int) {
            val result = "read ${c.uuid} status $status value ${hex(c.value)}"
            handler.post { next(result) }
        }

        override fun onDescriptorWrite(g: BluetoothGatt, d: BluetoothGattDescriptor, status: Int) {
            handler.post {
                say("CCCD written, status $status; waiting 20 s for reports")
                token++
                timeout(20000) { "CCCD status $status, reports: $reports" }
            }
        }

        override fun onCharacteristicChanged(g: BluetoothGatt, c: BluetoothGattCharacteristic) {
            val value = hex(c.value)
            handler.post { reports++ }
            say("REPORT ${c.uuid}: $value")
        }
    }

    private fun say(line: String) {
        Log.i(TAG, "spike: $line")
        handler.post { text.append(line + "\n") }
    }

    override fun onDestroy() {
        gatt?.close()
        super.onDestroy()
    }

    companion object {
        const val TAG = "scouter"
        const val HID_HOST = 4 // BluetoothProfile.HID_HOST, a system constant
        const val STEPS = 3
        fun uuid16(n: Int): UUID = UUID.fromString(String.format("%08x-0000-1000-8000-00805f9b34fb", n))
        fun hex(b: ByteArray?) = b?.joinToString("") { "%02x".format(it) } ?: "null"
    }
}
