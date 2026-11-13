package scouterkit.chewedfeed

import android.Manifest
import android.app.Activity
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import android.os.PowerManager
import android.util.Log
import android.widget.Button
import android.widget.LinearLayout
import android.widget.TextView
import scouterkit.chewedfeed.service.GlassesService
import java.util.concurrent.CountDownLatch
import java.util.concurrent.LinkedBlockingQueue

// Placeholder launcher: shows the native backend and opens the settings. The emulated flavor also runs a
// self-check of the glasses session on the emulated arms (start, draw, read back, stop, three times).
class MainActivity : Activity() {
    private var session: GlassesSession? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        check(Native.abiVersion() == Native.ABI_VERSION) { "libchewedfeed.so does not match the app" }
        val status = TextView(this).apply {
            text = getString(R.string.status, Native.backend(), Native.abiVersion())
        }
        val settings = Button(this).apply {
            setText(R.string.settings)
            setOnClickListener { startActivity(Intent(this@MainActivity, SettingsActivity::class.java)) }
        }
        setContentView(LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            addView(status)
            addView(settings)
            if (BuildConfig.FLAVOR == "glasses") {
                // The glasses connection in the background (GlassesService), its state and the battery setting.
                addView(service)
                addView(Button(this@MainActivity).apply {
                    setText(R.string.start_glasses)
                    setOnClickListener { if (permissions()) GlassesService.start(this@MainActivity) }
                })
                addView(Button(this@MainActivity).apply {
                    setText(R.string.stop_glasses)
                    setOnClickListener { GlassesService.stop(this@MainActivity) }
                })
                addView(battery)
                addView(Button(this@MainActivity).apply {
                    setText(R.string.battery_settings)
                    setOnClickListener { startActivity(Intent(android.provider.Settings.ACTION_IGNORE_BATTERY_OPTIMIZATION_SETTINGS)) }
                })
                addView(Button(this@MainActivity).apply {
                    setText(R.string.glasses_check)
                    setOnClickListener { startActivity(Intent(this@MainActivity, GlassesCheckActivity::class.java)) }
                })
            }
        })
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

    private val service by lazy { TextView(this) }
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
        val detail = if (st.detail.isEmpty()) "" else getString(R.string.glasses_detail, st.detail)
        service.text = getString(R.string.glasses_state, st.phase.text, detail, if (st.online) "" else getString(R.string.offline))
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
        if (checkSelfPermission(Manifest.permission.BLUETOOTH_CONNECT) == PackageManager.PERMISSION_GRANTED) GlassesService.start(this)
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
