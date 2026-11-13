package scouterkit.chewedfeed.bluetooth

import android.annotation.SuppressLint
import android.bluetooth.BluetoothAdapter
import android.bluetooth.BluetoothDevice
import android.bluetooth.le.ScanCallback
import android.bluetooth.le.ScanFilter
import android.bluetooth.le.ScanResult
import android.bluetooth.le.ScanSettings
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.os.ParcelUuid
import java.util.UUID

// Bluetooth switched off or on, and lost bonds, into the link. Android drops the links without GATT callbacks
// when Bluetooth goes off.
class BluetoothWatch(private val context: Context, private val link: ArmsLink) {
    @Suppress("DEPRECATION")
    private val receiver = object : BroadcastReceiver() {
        override fun onReceive(c: Context, intent: Intent) {
            when (intent.action) {
                BluetoothAdapter.ACTION_STATE_CHANGED -> when (intent.getIntExtra(BluetoothAdapter.EXTRA_STATE, -1)) {
                    BluetoothAdapter.STATE_TURNING_OFF, BluetoothAdapter.STATE_OFF -> link.bluetoothOff()
                    BluetoothAdapter.STATE_ON -> link.bluetoothOn()
                }
                BluetoothDevice.ACTION_BOND_STATE_CHANGED -> {
                    val device = intent.getParcelableExtra<BluetoothDevice>(BluetoothDevice.EXTRA_DEVICE) ?: return
                    if (intent.getIntExtra(BluetoothDevice.EXTRA_BOND_STATE, -1) == BluetoothDevice.BOND_NONE) link.bondLost(device.address)
                }
            }
        }
    }

    fun start() {
        context.registerReceiver(receiver, IntentFilter().apply {
            addAction(BluetoothAdapter.ACTION_STATE_CHANGED)
            addAction(BluetoothDevice.ACTION_BOND_STATE_CHANGED)
        })
    }

    fun stop() {
        runCatching { context.unregisterReceiver(receiver) }
    }
}

// Scans for arms advertising the scouter service and groups them by pair record. Runs only when setup asks
// (the user's explicit action); the caller stops it. Bonding happens later, at the first connection.
@SuppressLint("MissingPermission")
class PairScan(private val adapter: BluetoothAdapter, private val onPairs: (List<PairFinder.Pair>) -> Unit) {
    private val finder = PairFinder()
    private val callback = object : ScanCallback() {
        override fun onScanResult(type: Int, result: ScanResult) {
            finder.add(result.device.address, PairRecord.parse(result.scanRecord?.getManufacturerSpecificData(PairRecord.MANUFACTURER_ID)))
            onPairs(finder.pairs)
        }
    }

    // SecurityException without the Nearby devices permission.
    fun start() {
        val filter = ScanFilter.Builder().setServiceUuid(ParcelUuid(SERVICE)).build()
        adapter.bluetoothLeScanner?.startScan(listOf(filter), ScanSettings.Builder().setScanMode(ScanSettings.SCAN_MODE_LOW_LATENCY).build(), callback)
    }

    fun stop() {
        runCatching { adapter.bluetoothLeScanner?.stopScan(callback) }
    }

    private companion object {
        val SERVICE: UUID = UUID.fromString("f3da4d59-277a-4fc9-a1ed-41c9c22d4400")
    }
}
