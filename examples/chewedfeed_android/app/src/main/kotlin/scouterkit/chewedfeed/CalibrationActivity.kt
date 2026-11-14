package scouterkit.chewedfeed

import android.annotation.SuppressLint
import android.app.Activity
import android.bluetooth.BluetoothManager
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.widget.LinearLayout
import android.widget.TextView
import scouterkit.chewedfeed.bluetooth.AndroidBleAdapter
import scouterkit.chewedfeed.bluetooth.ArmsLink
import scouterkit.chewedfeed.bluetooth.HandlerScheduler
import scouterkit.chewedfeed.bluetooth.NativeEvents
import scouterkit.chewedfeed.render.FontScale
import scouterkit.chewedfeed.service.GlassesService
import scouterkit.chewedfeed.service.Phase

// The arrows move the right eye's crosshair, as the desktop's calibration keys do (calibration_input.go): up and
// down change dy by -/+, right and left change dx by -/+; clamped to the desktop's ranges.
enum class Nudge(val ddx: Int, val ddy: Int) { UP(0, -1), DOWN(0, 1), RIGHT(-1, 0), LEFT(1, 0) }

fun nudge(s: DisplaySettings, n: Nudge, step: Int) =
    s.copy(dx = (s.dx + n.ddx * step).coerceIn(-240, 240), dy = (s.dy + n.ddy * step).coerceIn(-120, 120))

// Alignment without a laptop (desktop -calibrate): the left eye shows a pixel ruler, the right a crosshair; the
// arrows move the crosshair until it sits in the ruler's centre square, then Save keeps dx and dy for this phone.
// It needs the glasses to itself: the background connection pauses while it runs and resumes after.
@SuppressLint("MissingPermission", "SetTextI18n")
class CalibrationActivity : Activity() {
    private val main = Handler(Looper.getMainLooper())
    private lateinit var state: TextView
    private lateinit var values: TextView
    private var settings = DisplaySettings()
    private var session: GlassesSession? = null
    private var sched: HandlerScheduler? = null
    private var shown = false
    private var resumeService = false

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setTitle(R.string.calibrate)
        actionBar?.setDisplayHomeAsUpEnabled(true)
        settings = DisplaySettings.load(this)
        val ui = AppUi(this)
        val page = ui.screen(R.string.calibrate, R.string.calibrate_subtitle)
        ui.card(page, R.string.calibrate_pattern, R.string.calibrate_help)
        state = TextView(this).also {
            ui.styleStatus(it)
            it.accessibilityLiveRegion = android.view.View.ACCESSIBILITY_LIVE_REGION_POLITE
        }
        val adjustment = ui.card(page, R.string.calibrate_adjust)
        values = TextView(this).also {
            ui.styleStatus(it)
            it.accessibilityLiveRegion = android.view.View.ACCESSIBILITY_LIVE_REGION_POLITE
        }
        adjustment.addView(values)
        for ((step, label) in listOf(1 to R.string.calibrate_fine, 10 to R.string.calibrate_coarse)) {
            adjustment.addView(ui.text(label).apply { setPadding(0, ui.dp(16), 0, 0) })
            adjustment.addView(LinearLayout(this).apply {
                for ((index, n) in listOf(Nudge.LEFT, Nudge.UP, Nudge.DOWN, Nudge.RIGHT).withIndex()) {
                    val description = when (n) {
                        Nudge.LEFT -> R.plurals.calibrate_left
                        Nudge.UP -> R.plurals.calibrate_up
                        Nudge.DOWN -> R.plurals.calibrate_down
                        Nudge.RIGHT -> R.plurals.calibrate_right
                    }
                    addView(ui.button(R.string.calibrate_adjust) { move(n, step) }.apply {
                        text = when (n) {
                            Nudge.LEFT -> "←"
                            Nudge.UP -> "↑"
                            Nudge.DOWN -> "↓"
                            Nudge.RIGHT -> "→"
                        }
                        textSize = 24f
                        contentDescription = resources.getQuantityString(description, step, step)
                        minWidth = 0
                        setPadding(0, ui.dp(8), 0, ui.dp(8))
                        layoutParams = LinearLayout.LayoutParams(0, ui.dp(56), 1f).apply {
                            topMargin = ui.dp(8)
                            if (index > 0) marginStart = ui.dp(6)
                        }
                    })
                }
            })
        }
        val footer = ui.column().apply {
            setPadding(ui.dp(20), ui.dp(8), ui.dp(20), ui.dp(12))
            setBackgroundColor(ui.color(R.color.surface))
            addView(state)
            addView(ui.button(R.string.calibrate_save, primary = true) {
                DisplaySettings.load(this@CalibrationActivity).copy(dx = settings.dx, dy = settings.dy)
                    .save(this@CalibrationActivity)
                state.setText(R.string.calibrate_saved)
            })
        }
        setContentView(ui.column().apply {
            addView(ui.scroll(page), LinearLayout.LayoutParams(-1, 0, 1f))
            addView(footer)
        })
        showValues()
        connect()
    }

    override fun onDestroy() {
        session?.let { s -> s.stop { sched?.quit(); s.close() } }
        session = null
        if (resumeService) GlassesService.start(this)
        super.onDestroy()
    }

    override fun onNavigateUp(): Boolean {
        finish()
        return true
    }

    private fun showValues() {
        values.text = getString(R.string.calibrate_values, settings.dx, settings.dy)
    }

    private fun move(n: Nudge, step: Int) {
        settings = nudge(settings, n, step)
        showValues()
        if (shown) session?.draw { f -> f.calibrationMove(settings.dx, settings.dy) }
    }

    private fun connect() {
        val arms = Pairing.saved(this) ?: return state.setText(R.string.setup_none)
        val adapter = getSystemService(BluetoothManager::class.java)?.adapter
        if (adapter?.isEnabled != true) return state.setText(R.string.setup_bluetooth_off)
        if (GlassesService.State.phase != Phase.STOPPED) {
            resumeService = true
            GlassesService.stop(this) // one session at a time: retried below while it releases the glasses
        }
        val sc = HandlerScheduler().also { sched = it }
        val link = ArmsLink(AndroidBleAdapter(this, adapter, sc.looperHandler), arrayOf(arms.first, arms.second), NativeEvents, sc)
        val s = GlassesSession(callbacks = { main.post(it) }, onEvents = { _, _ -> })
        session = s
        state.setText(R.string.calibrate_connecting)
        start(s, link, arms, 0)
    }

    private fun start(s: GlassesSession, link: ArmsLink, arms: Pair<String, String>, tries: Int) {
        s.start(StartConfig(arms.first, arms.second, settings.dx, settings.dy), link) { outcome ->
            if (session !== s) return@start
            when (outcome) {
                is StartOutcome.Failed ->
                    if (outcome.code == BUSY && tries < 20) main.postDelayed({ start(s, link, arms, tries + 1) }, 500)
                    else state.text = getString(R.string.calibrate_failed, outcome.message)
                is StartOutcome.Ready -> s.draw { f ->
                    // The ruler's numbers at the dashboard's text size (desktop: 80 px marks above scale 1).
                    val whole = settings.scale == Math.rint(settings.scale)
                    val blob = if (whole) null else FontScale.scale(Native.departureMono(), settings.scale)
                    val ok = f.calibrationShow(blob, if (whole) settings.scale.toInt() else 1, if (settings.scale > 1) 80 else 40)
                    main.post {
                        shown = ok
                        state.setText(if (ok) R.string.calibrate_ready else R.string.calibrate_no_pattern)
                    }
                }
            }
        }
    }

    private companion object {
        const val BUSY = 101 // native kBusy: the background connection still holds the session
    }
}
