package scouterkit.chewedfeed

import android.Manifest
import android.app.Activity
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import android.os.PowerManager
import android.util.Log
import android.widget.RadioButton
import android.widget.RadioGroup
import android.widget.TextView
import scouterkit.chewedfeed.handover.PhoneAddress
import scouterkit.chewedfeed.service.GlassesService
import scouterkit.chewedfeed.service.Holder
import scouterkit.chewedfeed.service.Phase
import scouterkit.chewedfeed.service.Progress
import scouterkit.chewedfeed.service.ownershipView
import scouterkit.chewedfeed.settings.AppSettings
import scouterkit.chewedfeed.settings.OwnerMode
import scouterkit.chewedfeed.settings.Prefs
import java.util.concurrent.CountDownLatch
import java.util.concurrent.LinkedBlockingQueue

// Connection overview. The emulated flavor also runs a three-round native session self-check.
class MainActivity : Activity() {
    private var session: GlassesSession? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        check(Native.abiVersion() == Native.ABI_VERSION) { "libchewedfeed.so does not match the app" }
        val status = TextView(this).apply {
            text = getString(R.string.status, Native.backend(), Native.abiVersion())
        }
        actionBar?.hide()
        val ui = AppUi(this)
        val page = ui.screen(R.string.home_title, R.string.home_subtitle)
        if (BuildConfig.FLAVOR == "glasses") {
            val connection = ui.card(page, R.string.connection_title)
            connection.addView(android.widget.ImageView(this).apply {
                setImageResource(R.drawable.glasses)
                importantForAccessibility = android.view.View.IMPORTANT_FOR_ACCESSIBILITY_NO
                layoutParams = android.widget.LinearLayout.LayoutParams(-1, ui.dp(100))
                setPadding(0, ui.dp(8), 0, ui.dp(12))
            })
            ui.styleStatus(holder, prominent = true)
            ui.styleStatus(service)
            connection.addView(holder)
            connection.addView(service)
            connection.addView(connectionButton)
            val essentials = ui.card(page, R.string.essentials_title)
            essentials.addView(ui.button(R.string.settings) {
                startActivity(Intent(this, SettingsActivity::class.java))
            })
            essentials.addView(ui.button(R.string.setup) {
                startActivity(Intent(this, SetupActivity::class.java))
            })
            val handover = ui.card(page, R.string.handover_title, R.string.handover_subtitle)
            val app = AppSettings.load(Prefs(this, "app"))
            handover.addView(RadioGroup(this).apply {
                for ((mode, label) in listOf(OwnerMode.PREFER_LAPTOP to R.string.owner_prefer_laptop,
                    OwnerMode.KEEP_ON_PHONE to R.string.owner_keep_phone)) {
                    addView(RadioButton(this@MainActivity).apply {
                        id = mode.ordinal + 1
                        setText(label)
                        textSize = 14f
                        minHeight = ui.dp(56)
                        setPadding(ui.dp(4), ui.dp(8), 0, ui.dp(8))
                        isChecked = app.owner == mode
                    })
                }
                setOnCheckedChangeListener { _, id ->
                    val kv = Prefs(this@MainActivity, "app")
                    AppSettings.load(kv).copy(owner = OwnerMode.entries[id - 1]).save(kv)
                    GlassesService.reload(this@MainActivity)
                }
            })
            ui.styleStatus(laptop)
            handover.addView(laptop)
            val background = ui.disclosure(page, R.string.background_title, R.string.battery_help)
            ui.styleStatus(battery)
            background.addView(battery)
            background.addView(ui.button(R.string.battery_settings) {
                startActivity(Intent(android.provider.Settings.ACTION_IGNORE_BATTERY_OPTIMIZATION_SETTINGS))
            })
            val details = ui.disclosure(page, R.string.diagnostics_title, R.string.diagnostics_subtitle)
            ui.styleStatus(address)
            details.addView(address)
            ui.styleStatus(status)
            details.addView(status)
        } else {
            val preview = ui.card(page, R.string.connection_title)
            ui.styleStatus(status)
            preview.addView(status)
            preview.addView(ui.button(R.string.settings) {
                startActivity(Intent(this, SettingsActivity::class.java))
            })
        }
        setContentView(ui.scroll(page))
        if (BuildConfig.FLAVOR == "emulated") {
            val s = GlassesSession(callbacks = { it.run() }, onEvents = { _, _ -> })
            session = s
            Thread({
                val result = selfCheck(s, 3)
                Log.i(TAG, "self-check: $result")
                runOnUiThread { status.append("\n$result") }
            }, "self-check").start()
        }
    }

    private val connectionButton by lazy {
        AppUi(this).button(R.string.connect, primary = true) {
            if (requested || GlassesService.State.phase != Phase.STOPPED) {
                requested = false
                GlassesService.stop(this)
            } else if (permissions()) {
                requested = true
                GlassesService.start(this)
            }
            showService()
        }
    }

    private val service by lazy { TextView(this) }
    private val holder by lazy { TextView(this).apply { textSize = 18f } }
    private val laptop by lazy { TextView(this) }
    private val address by lazy { TextView(this).apply { setTextIsSelectable(true) } }
    private var requested = false
    private val battery by lazy { TextView(this) }

    override fun onResume() {
        super.onResume()
        GlassesService.State.listener = ::showService
        showService()
        val ignoring = getSystemService(PowerManager::class.java).isIgnoringBatteryOptimizations(packageName)
        battery.setText(if (ignoring) R.string.battery_unrestricted else R.string.battery_optimised)
    }

    override fun onPause() {
        GlassesService.State.listener = null
        super.onPause()
    }

    private fun showService() {
        val st = GlassesService.State
        connectionButton.setText(if (requested || st.phase != Phase.STOPPED) R.string.disconnect else R.string.connect)
        val view = ownershipView(st.phase, requested, st.owner)
        if (view.progress != Progress.REQUESTED) requested = false
        holder.setText(when (view.holder) {
            Holder.THIS_PHONE -> R.string.holder_phone
            Holder.OTHER_DEVICE -> R.string.holder_other
            Holder.UNKNOWN -> R.string.holder_unknown
            Holder.NOBODY -> R.string.holder_nobody
        })
        // What to give the laptop's chewedfeed (-peer-endpoint), on this network.
        val endpoints = PhoneAddress.endpoints(this)
        address.text = if (endpoints.isEmpty()) getString(R.string.phone_address_none)
            else getString(R.string.phone_address, endpoints.joinToString("  ") { "-peer-endpoint $it" })
        laptop.setText(when (st.laptop) {
            "connected" -> R.string.laptop_link_connected
            "unreachable" -> if (endpoints.isEmpty()) R.string.laptop_link_no_wifi else R.string.laptop_link_unreachable
            else -> R.string.laptop_handover_unset
        })
        if (view.progress == Progress.REQUESTED) return service.setText(R.string.progress_requested)
        val detail = if (st.detail.isEmpty()) "" else getString(R.string.glasses_detail, st.detail)
        val label = getString(when (view.progress) {
            Progress.READY -> R.string.progress_ready
            Progress.PENDING -> R.string.progress_pending
            Progress.FAILED -> R.string.progress_failed
            else -> R.string.progress_idle
        })
        service.text = getString(R.string.glasses_state, label, st.phase.text, detail, if (st.online) "" else getString(R.string.offline))
    }

    // Nearby devices (the connection itself) and notifications (its status notification; optional).
    private fun permissions(): Boolean {
        val notify = if (Build.VERSION.SDK_INT >= 33) listOf(Manifest.permission.POST_NOTIFICATIONS) else emptyList()
        val need = (listOf(Manifest.permission.BLUETOOTH_SCAN, Manifest.permission.BLUETOOTH_CONNECT) + notify)
            .filter { checkSelfPermission(it) != PackageManager.PERMISSION_GRANTED }
        if (need.isEmpty() || need == notify && askedNotifications) return true
        askedNotifications = true
        requestPermissions(need.toTypedArray(), 1)
        return false
    }

    private var askedNotifications = false

    override fun onRequestPermissionsResult(requestCode: Int, permissions: Array<out String>, grantResults: IntArray) {
        if (checkSelfPermission(Manifest.permission.BLUETOOTH_CONNECT) == PackageManager.PERMISSION_GRANTED &&
            checkSelfPermission(Manifest.permission.BLUETOOTH_SCAN) == PackageManager.PERMISSION_GRANTED) {
            requested = true
            GlassesService.start(this)
            showService()
        }
    }

    override fun onDestroy() {
        session?.close()
        super.onDestroy()
    }

    // Runs on its own thread: the session's calls block.
    private fun selfCheck(s: GlassesSession, rounds: Int): String {
        var last = 0L
        for (round in 1..rounds) {
            val started = LinkedBlockingQueue<StartOutcome>()
            s.start(StartConfig(), null) { started.put(it) }
            val outcome = started.take()
            if (outcome !is StartOutcome.Ready) return "FAIL start: $outcome"
            if (outcome.generation <= last || !outcome.status.ready) return "FAIL generation or arms: $outcome"
            val stale = last
            val drawn = s.draw { f ->
                val font = f.loadFont(2)
                val m = f.fontMetrics(font) ?: return@draw false
                f.canvas(400, 120) && f.clear() && f.text(font, "Chewedfeed ✓", 4, m[0] + 4) && f.present(100, 100) &&
                    lit() && !Frame(NativeBridge, stale).present(0, 0)
            }.get() == true
            if (!drawn) return "FAIL draw in round $round"
            val stopped = CountDownLatch(1)
            s.stop { stopped.countDown() }
            stopped.await()
            last = outcome.generation
        }
        return "ok: $rounds rounds, generation $last"
    }

    // Some pixel of the emulated left panel is on (the arms run the queue on their own threads).
    private fun lit(): Boolean {
        repeat(200) {
            if (Native.emulatedPanel(0)?.any { it.toInt() != 0 } == true) return true
            Thread.sleep(10)
        }
        return false
    }

    private companion object {
        const val TAG = "chewedfeed"
    }
}
