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
import android.widget.Button
import android.widget.LinearLayout
import android.widget.TextView
import scouterkit.chewedfeed.bluetooth.PairFinder
import scouterkit.chewedfeed.bluetooth.PairScan
import scouterkit.chewedfeed.handover.AndroidPeers
import scouterkit.chewedfeed.handover.Enroller
import scouterkit.chewedfeed.handover.PhoneAddress
import scouterkit.chewedfeed.handover.Peer
import scouterkit.chewedfeed.handover.PeerServer
import scouterkit.chewedfeed.service.GlassesCompanion
import scouterkit.chewedfeed.service.GlassesService
import scouterkit.chewedfeed.service.Phase

// Glasses setup: permission and Bluetooth state, scanning for pairs by their pair record and pairing the chosen
// one on the user's tap (the glasses in the open case), and the alignment calibration. Works, and says what is
// missing, without the permission, with Bluetooth off or without glasses nearby.
@SuppressLint("MissingPermission")
class SetupActivity : Activity() {
    private val main = Handler(Looper.getMainLooper())
    private val adapter by lazy { getSystemService(BluetoothManager::class.java)?.adapter }
    private lateinit var list: LinearLayout
    private lateinit var state: TextView
    private lateinit var paired: TextView
    private val found = mutableMapOf<String, Button>()
    private var scan: PairScan? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setTitle(R.string.setup)
        actionBar?.setDisplayHomeAsUpEnabled(true)
        val ui = AppUi(this)
        val page = ui.screen(R.string.setup, R.string.setup_subtitle)
        val glasses = ui.card(page, R.string.connection_title, R.string.scan_help)
        paired = TextView(this).also { ui.styleStatus(it) }
        state = TextView(this).also {
            ui.styleStatus(it)
            it.accessibilityLiveRegion = android.view.View.ACCESSIBILITY_LIVE_REGION_POLITE
        }
        glasses.addView(paired)
        glasses.addView(ui.button(R.string.scan_glasses, primary = true) { scanPairs() })
        glasses.addView(state)
        list = ui.column()
        glasses.addView(list)
        val fit = ui.card(page, R.string.fit_title, R.string.fit_subtitle)
        fit.addView(ui.button(R.string.calibrate) {
            startActivity(Intent(this, CalibrationActivity::class.java))
        })
        val background = ui.card(page, R.string.background_title)
        ui.styleStatus(companion)
        background.addView(companion)
        background.addView(ui.button(R.string.companion_set_up) { associate() })
        val laptop = ui.card(page, R.string.handover_title, R.string.laptop_section)
        ui.styleStatus(laptopState)
        laptop.addView(laptopState)
        laptop.addView(ui.button(R.string.laptop_pair) { pairLaptop() })
        laptop.addView(invitationText)
        laptop.addView(copyButton)
        val manage = ui.disclosure(page, R.string.manage_title, R.string.manage_subtitle)
        manage.addView(ui.button(R.string.forget_glasses) { forget() })
        manage.addView(ui.button(R.string.laptop_forget) { forgetLaptop() })
        manage.addView(ui.button(R.string.laptop_new_key) { newPhoneKey() })
        setContentView(ui.scroll(page))
    }

    private val hub by lazy { AndroidPeers.hub(this) }
    private val peers get() = hub.store
    private val laptopState by lazy { TextView(this) }
    private val invitationText by lazy { TextView(this).apply { setTextIsSelectable(true); visibility = android.view.View.GONE } }
    private val copyButton by lazy {
        AppUi(this).button(R.string.laptop_copy) {
            val clip = android.content.ClipData.newPlainText("invitation", invitationText.text)
            // Keep the invitation secret out of clipboard previews (Android 13+).
            clip.description.extras = android.os.PersistableBundle().apply {
                putBoolean("android.content.extra.IS_SENSITIVE", true)
            }
            getSystemService(android.content.ClipboardManager::class.java).setPrimaryClip(clip)
        }.apply { visibility = android.view.View.GONE }
    }

    private var pairing = false

    private fun showLaptop() {
        val laptop = peers.peers().firstOrNull { it.role == "laptop" }
        laptopState.text = if (laptop == null) getString(R.string.laptop_none) else getString(R.string.laptop_paired, laptop.id.take(8))
    }

    // Listens for the laptop and shows a single-use invitation for this phone's Wi-Fi address(es), valid 5 minutes.
    private fun pairLaptop() {
        stopPairing()
        val endpoints = PhoneAddress.endpoints(this)
        if (endpoints.isEmpty()) return show(getString(R.string.laptop_no_network))
        hub.onEnrolled = { peer, code -> main.post { paired(peer, code) } }
        try {
            hub.open("pairing") // shared with the glasses service's listener
        } catch (x: java.io.IOException) {
            hub.onEnrolled = null
            return show(getString(R.string.laptop_listen_failed, x.message))
        }
        pairing = true
        val inv = hub.enroller.invite("phone", endpoints)
        window.addFlags(android.view.WindowManager.LayoutParams.FLAG_SECURE) // no screenshots while the secret shows
        invitationText.text = inv.encode()
        invitationText.visibility = android.view.View.VISIBLE
        copyButton.visibility = android.view.View.VISIBLE
        show(getString(R.string.laptop_invite_help))
        main.postDelayed(::stopPairing, Enroller.TTL_MS)
    }

    private fun paired(peer: Peer, code: String) {
        stopPairing()
        GlassesService.reload(this) // listens for the laptop and coordinates with it from now on
        showLaptop()
        show(getString(R.string.laptop_enrolled, code))
    }

    private fun stopPairing() {
        if (pairing) {
            hub.enroller.withdraw()
            hub.onEnrolled = null
            hub.close("pairing")
            pairing = false
        }
        invitationText.text = ""
        invitationText.visibility = android.view.View.GONE
        copyButton.visibility = android.view.View.GONE
        window.clearFlags(android.view.WindowManager.LayoutParams.FLAG_SECURE)
    }

    private fun forgetLaptop() {
        peers.peers().filter { it.role == "laptop" }.forEach { peers.revoke(it.id) }
        GlassesService.reload(this) // an open connection ends at its next message; the listener closes
        showLaptop()
        show(getString(R.string.laptop_forgotten))
    }

    private fun newPhoneKey() {
        stopPairing()
        peers.rotate()
        GlassesService.reload(this)
        showLaptop()
        show(getString(R.string.laptop_rotated))
    }


    override fun onResume() {
        super.onResume()
        showPaired()
        showLaptop()
    }

    private val companion by lazy { TextView(this) }
    private var resumeService = false

    // Companion association (optional): the left arm must be advertising, so the connection pauses meanwhile.
    private fun associate() {
        val saved = Pairing.saved(this) ?: return show(getString(R.string.setup_none))
        if (GlassesService.State.phase != Phase.STOPPED) {
            resumeService = true
            GlassesService.stop(this)
        }
        show(getString(R.string.companion_waiting))
        GlassesCompanion.associate(this, saved.first) { error -> if (error != null) finishAssociation(error) }
    }

    @Deprecated("Activity result of the companion dialog")
    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        if (requestCode != GlassesCompanion.REQUEST) return
        val saved = Pairing.saved(this)
        finishAssociation(when {
            resultCode != RESULT_OK || saved == null -> getString(R.string.companion_cancelled)
            else -> GlassesCompanion.observe(this, saved.first)
        })
    }

    private fun finishAssociation(error: String?) {
        show(if (error == null) getString(R.string.companion_done) else getString(R.string.companion_failed, error))
        if (resumeService) GlassesService.start(this)
        resumeService = false
        showPaired()
    }

    override fun onDestroy() {
        stopPairing()
        scan?.stop()
        main.removeCallbacksAndMessages(null)
        super.onDestroy()
    }

    override fun onNavigateUp(): Boolean {
        finish()
        return true
    }

    private fun granted() = checkSelfPermission(Manifest.permission.BLUETOOTH_CONNECT) == PackageManager.PERMISSION_GRANTED &&
        checkSelfPermission(Manifest.permission.BLUETOOTH_SCAN) == PackageManager.PERMISSION_GRANTED

    private fun show(text: String) {
        main.post { state.text = text }
    }

    // What is saved and whether this phone is still paired with both arms.
    private fun showPaired() {
        val saved = Pairing.saved(this)
        paired.text = when {
            !granted() -> getString(R.string.setup_permission)
            adapter?.isEnabled != true -> getString(R.string.setup_bluetooth_off)
            saved == null -> getString(R.string.setup_none)
            else -> {
                val bonded = adapter!!.bondedDevices.map { it.address }.toSet()
                if (saved.first in bonded && saved.second in bonded) getString(R.string.setup_paired) else getString(R.string.setup_unpaired)
            }
        }
        companion.setText(if (saved != null && GlassesCompanion.associated(this, saved.first)) R.string.companion_on else R.string.companion_off)
    }

    private fun scanPairs() {
        if (!granted()) {
            requestPermissions(arrayOf(Manifest.permission.BLUETOOTH_SCAN, Manifest.permission.BLUETOOTH_CONNECT), 1)
            return show(getString(R.string.setup_permission))
        }
        val a = adapter
        if (a == null || !a.isEnabled) return show(getString(R.string.setup_bluetooth_off))
        scan?.stop()
        val s = PairScan(a) { pairs ->
            main.post {
                for (p in pairs) {
                    val b = found.getOrPut(p.pairId) { AppUi(this).button(R.string.scan_glasses) {}.also { list.addView(it) } }
                    b.text = getString(R.string.setup_found, p.pairId,
                        getString(if (p.left != null) R.string.arm_found else R.string.arm_missing, getString(R.string.left)),
                        getString(if (p.right != null) R.string.arm_found else R.string.arm_missing, getString(R.string.right)))
                    b.isEnabled = p.complete
                    b.setOnClickListener { scan?.stop(); bondPair(p) }
                }
            }
        }
        scan = s
        s.start()
        show(getString(R.string.setup_scanning))
        main.postDelayed({
            s.stop()
            show(getString(if (found.isEmpty()) R.string.setup_nothing_found else R.string.setup_tap))
        }, 15_000)
    }

    override fun onRequestPermissionsResult(requestCode: Int, permissions: Array<out String>, grantResults: IntArray) = showPaired()

    // Bonds both arms (only those not bonded yet), one after the other.
    private fun bondPair(p: PairFinder.Pair) {
        val a = adapter ?: return
        Pairing.save(this, p.left!!, p.right!!)
        bondNext(listOf(p.left, p.right).map { a.getRemoteDevice(it) }.filter { it.bondState != BluetoothDevice.BOND_BONDED }, p)
    }

    private fun bondNext(todo: List<BluetoothDevice>, p: PairFinder.Pair) {
        val d = todo.firstOrNull() ?: return done(p)
        val name = getString(if (d.address == p.left) R.string.left else R.string.right)
        val receiver = object : BroadcastReceiver() {
            override fun onReceive(c: Context, i: Intent) {
                @Suppress("DEPRECATION")
                val dev = i.getParcelableExtra<BluetoothDevice>(BluetoothDevice.EXTRA_DEVICE) ?: return
                if (dev.address != d.address) return
                when (i.getIntExtra(BluetoothDevice.EXTRA_BOND_STATE, -1)) {
                    BluetoothDevice.BOND_BONDED -> { unregisterReceiver(this); bondNext(todo.drop(1), p) }
                    BluetoothDevice.BOND_NONE -> { unregisterReceiver(this); show(getString(R.string.setup_pair_failed, name)) }
                }
            }
        }
        registerReceiver(receiver, IntentFilter(BluetoothDevice.ACTION_BOND_STATE_CHANGED))
        show(getString(R.string.setup_pairing, name))
        if (!d.createBond()) {
            unregisterReceiver(receiver)
            show(getString(R.string.setup_pair_failed, name))
        }
    }

    private fun done(p: PairFinder.Pair) {
        show(getString(R.string.setup_done))
        showPaired()
    }

    // Forgets the saved pair here; unpairing itself is in Android's Bluetooth settings.
    private fun forget() {
        Pairing.saved(this)?.let { GlassesCompanion.forget(this, it.first) }
        Pairing.forget(this)
        showPaired()
        show(getString(R.string.setup_forgotten))
    }
}

// The chosen pair's arm addresses (left, right), on this phone only.
object Pairing {
    private const val PREFS = "glasses"

    fun saved(c: Context): Pair<String, String>? {
        val p = c.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
        val l = p.getString("left", null) ?: return null
        return l to (p.getString("right", null) ?: return null)
    }

    fun save(c: Context, left: String, right: String) {
        c.getSharedPreferences(PREFS, Context.MODE_PRIVATE).edit().putString("left", left).putString("right", right).apply()
    }

    fun forget(c: Context) {
        c.getSharedPreferences(PREFS, Context.MODE_PRIVATE).edit().remove("left").remove("right").apply()
    }
}
