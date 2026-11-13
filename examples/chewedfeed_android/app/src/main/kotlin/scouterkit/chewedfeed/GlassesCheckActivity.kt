package scouterkit.chewedfeed

import android.Manifest
import android.annotation.SuppressLint
import android.app.Activity
import android.bluetooth.BluetoothDevice
import android.bluetooth.BluetoothManager
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.content.pm.PackageManager
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.util.Log
import android.widget.Button
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import scouterkit.chewedfeed.bluetooth.AndroidBleAdapter
import scouterkit.chewedfeed.bluetooth.ArmsLink
import scouterkit.chewedfeed.bluetooth.BluetoothWatch
import scouterkit.chewedfeed.bluetooth.HandlerScheduler
import scouterkit.chewedfeed.bluetooth.NativeEvents
import scouterkit.chewedfeed.bluetooth.PairFinder
import scouterkit.chewedfeed.bluetooth.PairScan

// Hardware check for the glasses link (until setup, CF-45): scan for pairs by their pair record, bond the chosen
// pair only on the user's tap (the glasses in the open case), connect both arms through the native session and
// draw a test frame. Nothing here pairs or connects by itself.
@SuppressLint("MissingPermission", "SetTextI18n")
class GlassesCheckActivity : Activity() {
    private val main = Handler(Looper.getMainLooper())
    private lateinit var state: TextView
    private lateinit var list: LinearLayout
    private val adapter by lazy { getSystemService(BluetoothManager::class.java).adapter }
    private var scan: PairScan? = null
    private var scheduler: HandlerScheduler? = null
    private var link: ArmsLink? = null
    private var watch: BluetoothWatch? = null
    private var session: GlassesSession? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        list = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL; setPadding(32, 32, 32, 32) }
        state = TextView(this)
        list.addView(TextView(this).apply {
            text = "Put the glasses in the charging case and open it to pair: the phone takes one of each arm's " +
                "3 bond slots (the oldest is replaced)."
        })
        list.addView(state)
        list.addView(Button(this).apply { text = "Scan for glasses (15 s)"; setOnClickListener { scanPairs() } })
        list.addView(Button(this).apply { text = "Connect saved glasses"; setOnClickListener { saved()?.let { start(it.first, it.second) } } })
        list.addView(Button(this).apply { text = "Stop"; setOnClickListener { stopSession() } })
        setContentView(ScrollView(this).apply { addView(list) })
        permissions()
    }

    override fun onDestroy() {
        scan?.stop()
        stopSession()
        super.onDestroy()
    }

    private fun permissions(): Boolean {
        val need = arrayOf(Manifest.permission.BLUETOOTH_SCAN, Manifest.permission.BLUETOOTH_CONNECT)
        if (need.all { checkSelfPermission(it) == PackageManager.PERMISSION_GRANTED }) return true
        requestPermissions(need, 1)
        show("allow Nearby devices, then try again")
        return false
    }

    private fun show(text: String) = main.post {
        state.text = text
        Log.i(TAG, "check: $text")
    }

    private fun saved(): Pair<String, String>? {
        val p = getSharedPreferences("glasses", Context.MODE_PRIVATE)
        val l = p.getString("left", null) ?: return null.also { show("no saved glasses: scan first") }
        return l to (p.getString("right", null) ?: return null)
    }

    private fun scanPairs() {
        if (!permissions()) return
        if (!adapter.isEnabled) return show("Bluetooth is off").let {}
        val buttons = mutableMapOf<String, Button>()
        val s = PairScan(adapter) { pairs ->
            main.post {
                for (p in pairs) {
                    val b = buttons.getOrPut(p.pairId) {
                        Button(this).also { list.addView(it) }
                    }
                    b.text = "Glasses ${p.pairId}: ${if (p.left != null) "left" else "no left"}, ${if (p.right != null) "right" else "no right"}"
                    b.isEnabled = p.complete
                    b.setOnClickListener { scan?.stop(); bondPair(p) }
                }
            }
        }
        scan = s
        s.start()
        show("scanning…")
        main.postDelayed({ s.stop(); if (buttons.isEmpty()) show("no glasses found: is the case open?") else show("tap the glasses to pair") }, 15000)
    }

    // Bonds both arms (only those not bonded yet), one after the other, then connects.
    private fun bondPair(p: PairFinder.Pair) {
        getSharedPreferences("glasses", Context.MODE_PRIVATE).edit().putString("left", p.left).putString("right", p.right).apply()
        val todo = listOf(p.left!!, p.right!!).map { adapter.getRemoteDevice(it) }.filter { it.bondState != BluetoothDevice.BOND_BONDED }
        bondNext(todo, p)
    }

    private fun bondNext(todo: List<BluetoothDevice>, p: PairFinder.Pair) {
        val d = todo.firstOrNull() ?: return start(p.left!!, p.right!!)
        val name = if (d.address == p.left) "left" else "right"
        val receiver = object : BroadcastReceiver() {
            override fun onReceive(c: Context, i: Intent) {
                @Suppress("DEPRECATION")
                val dev = i.getParcelableExtra<BluetoothDevice>(BluetoothDevice.EXTRA_DEVICE) ?: return
                if (dev.address != d.address) return
                when (i.getIntExtra(BluetoothDevice.EXTRA_BOND_STATE, -1)) {
                    BluetoothDevice.BOND_BONDED -> { unregisterReceiver(this); show("$name arm paired"); bondNext(todo.drop(1), p) }
                    BluetoothDevice.BOND_NONE -> { unregisterReceiver(this); show("pairing the $name arm failed: is the case open?") }
                }
            }
        }
        registerReceiver(receiver, IntentFilter(BluetoothDevice.ACTION_BOND_STATE_CHANGED))
        show("pairing the $name arm…")
        if (!d.createBond()) { unregisterReceiver(receiver); show("cannot pair the $name arm") }
    }

    private fun start(left: String, right: String) {
        if (!permissions() || session != null) return
        Native.setLog(true)
        val sched = HandlerScheduler()
        val l = ArmsLink(AndroidBleAdapter(this, adapter, sched.looperHandler), arrayOf(left, right), NativeEvents, sched) { s, why ->
            show("link: $s ${why.ifEmpty { "" }}")
        }
        val w = BluetoothWatch(this, l).also { it.start() }
        val s = GlassesSession(callbacks = { it.run() }, onEvents = { _, events ->
            for (e in events) if (e.type == GlassesEvent.INPUT) Log.i(TAG, "check: arm ${e.arm} input trigger ${e.trigger} value ${e.a}")
        })
        scheduler = sched; link = l; watch = w; session = s
        show("connecting both arms (up to 30 s)…")
        s.start(StartConfig(left, right), l) { outcome ->
            when (outcome) {
                is StartOutcome.Failed -> show("start failed (${outcome.code}): ${outcome.message}; link ${l.status} ${l.reason}")
                is StartOutcome.Ready -> {
                    val st = outcome.status
                    val drawn = s.draw { f ->
                        val font = f.loadFont(2)
                        val m = f.fontMetrics(font) ?: return@draw false
                        val w2 = minOf(600, st.width - 40)
                        f.canvas(w2, 160) && f.clear() &&
                            f.text(font, "Chewedfeed", w2 / 2, m[0] + 20, Frame.ALIGN_CENTER) &&
                            f.text(font, "phone link OK ✓", w2 / 2, m[0] + 20 + m[2], Frame.ALIGN_CENTER) &&
                            f.present((st.width - w2) / 2, (st.height - 160) / 2)
                    }.get()
                    show("connected: generation ${outcome.generation}, left ${st.left.battery}% right ${st.right.battery}%, " +
                        "in case ${st.left.inCase}/${st.right.inCase}, frame ${if (drawn == true) "drawn" else "FAILED"}")
                }
            }
        }
    }

    private fun stopSession() {
        val s = session ?: return
        session = null
        s.stop {
            watch?.stop()
            scheduler?.quit()
            watch = null; link = null; scheduler = null
            show("stopped: glasses released")
        }
    }

    private companion object {
        const val TAG = "chewedfeed"
    }
}
