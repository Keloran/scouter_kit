package scouterkit.chewedfeed

import android.app.Activity
import android.content.Intent
import android.os.Bundle
import android.util.Log
import android.widget.Button
import android.widget.LinearLayout
import android.widget.TextView
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
