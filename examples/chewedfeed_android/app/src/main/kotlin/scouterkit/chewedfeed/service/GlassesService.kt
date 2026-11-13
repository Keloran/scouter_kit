package scouterkit.chewedfeed.service

import android.Manifest
import android.annotation.SuppressLint
import android.app.ForegroundServiceStartNotAllowedException
import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.bluetooth.BluetoothAdapter
import android.bluetooth.BluetoothDevice
import android.bluetooth.BluetoothManager
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.content.pm.PackageManager
import android.content.pm.ServiceInfo
import android.net.ConnectivityManager
import android.net.Network
import android.os.BatteryManager
import android.os.Handler
import android.os.IBinder
import android.os.Looper
import scouterkit.chewedfeed.render.TEXT_INSET
import scouterkit.chewedfeed.dashboard.Screen
import scouterkit.chewedfeed.dashboard.Navigation
import scouterkit.chewedfeed.dashboard.Dashboard
import scouterkit.chewedfeed.GlassesStatus
import scouterkit.chewedfeed.GlassesEvent
import java.time.format.DateTimeFormatter
import java.time.LocalTime
import android.os.SystemClock
import scouterkit.chewedfeed.DisplaySettings
import scouterkit.chewedfeed.GlassesSession
import scouterkit.chewedfeed.MainActivity
import scouterkit.chewedfeed.Native
import scouterkit.chewedfeed.R
import scouterkit.chewedfeed.StartConfig
import scouterkit.chewedfeed.StartOutcome
import scouterkit.chewedfeed.bluetooth.AndroidBleAdapter
import scouterkit.chewedfeed.bluetooth.ArmsLink
import scouterkit.chewedfeed.bluetooth.BluetoothWatch
import scouterkit.chewedfeed.bluetooth.HandlerScheduler
import scouterkit.chewedfeed.bluetooth.LinkStatus
import scouterkit.chewedfeed.bluetooth.NativeEvents
import scouterkit.chewedfeed.dashboard.Page
import scouterkit.chewedfeed.notifications.FeedEvent
import scouterkit.chewedfeed.notifications.Hub
import scouterkit.chewedfeed.render.Batteries
import scouterkit.chewedfeed.render.GlassesScreen
import scouterkit.chewedfeed.render.MusicStatus

// Keeps the glasses session going with the screen off: a connectedDevice foreground service with a status
// notification and a Stop action. Started explicitly from the app; a sticky restart after process death comes
// back only if the user had not stopped it, and then waits for ownership to be confirmed before connecting.
// Android may still stop it (Doze, vendor battery limits, force-stop): the app shows the battery setting.
// No wake lock: the BLE callbacks wake the process and nothing here polls on its own.
// All Lifecycle calls run on the main thread.
@SuppressLint("MissingPermission")
class GlassesService : Service(), LifecycleActions {
    private val main = Handler(Looper.getMainLooper())
    private val lifecycle = Lifecycle(this)
    private val adapter by lazy { getSystemService(BluetoothManager::class.java).adapter }
    private var retry: Runnable? = null
    private var conn: Connection? = null
    private var foreground = false

    private val bluetoothReceiver = object : BroadcastReceiver() {
        override fun onReceive(c: Context, i: Intent) = refresh()
    }
    private val network = object : ConnectivityManager.NetworkCallback() {
        override fun onAvailable(n: Network) { State.online = true; State.changed() }
        override fun onLost(n: Network) { State.online = false; State.changed() }
    }

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        getSystemService(NotificationManager::class.java).createNotificationChannel(
            NotificationChannel(CHANNEL, getString(R.string.service_channel), NotificationManager.IMPORTANCE_LOW))
        registerReceiver(bluetoothReceiver, IntentFilter().apply {
            addAction(BluetoothAdapter.ACTION_STATE_CHANGED)
            addAction(BluetoothDevice.ACTION_BOND_STATE_CHANGED)
        })
        getSystemService(ConnectivityManager::class.java).registerDefaultNetworkCallback(network)
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_STOP -> lifecycle.userStop()
            ACTION_START -> if (enterForeground()) {
                lifecycle.userStart()
                refresh()
                reconcile()
            }
            else -> { // a sticky restart: only what was saved
                val wanted = prefs().getBoolean("wanted", false)
                if (!wanted || !enterForeground()) {
                    stopSelf()
                    return START_NOT_STICKY
                }
                lifecycle.restored(true)
                refresh()
                reconcile()
            }
        }
        return START_STICKY
    }

    override fun onDestroy() {
        disconnect()
        cancelRetry()
        runCatching { unregisterReceiver(bluetoothReceiver) }
        runCatching { getSystemService(ConnectivityManager::class.java).unregisterNetworkCallback(network) }
        State.phase = Phase.STOPPED; State.detail = ""; State.changed()
        super.onDestroy()
    }

    // The Nearby devices permission is what lets a connectedDevice service start (Android 14); without it, or
    // when Android refuses a start from the background, the service does not run and says why.
    private fun enterForeground(): Boolean {
        if (foreground) return true
        if (!permitted()) {
            State.phase = Phase.NEEDS_PERMISSION; State.changed()
            return false
        }
        return try {
            startForeground(NOTIFICATION, notification(Phase.STOPPED.text), ServiceInfo.FOREGROUND_SERVICE_TYPE_CONNECTED_DEVICE)
            foreground = true
            true
        } catch (e: ForegroundServiceStartNotAllowedException) {
            State.phase = Phase.STOPPED; State.detail = "Android did not allow a start in the background: open the app"; State.changed()
            false
        }
    }

    // ponytail: no coordinator yet (CF-48): this device takes ownership once its conditions allow. The
    // coordinator replaces this with the peer's answer.
    private fun reconcile() = lifecycle.ownership(true)

    private fun permitted() = checkSelfPermission(Manifest.permission.BLUETOOTH_CONNECT) == PackageManager.PERMISSION_GRANTED

    private fun saved(): Pair<String, String>? {
        val p = getSharedPreferences("glasses", Context.MODE_PRIVATE)
        val left = p.getString("left", null) ?: return null
        val right = p.getString("right", null) ?: return null
        if (!permitted()) return null
        val bonded = adapter.bondedDevices.map { it.address }.toSet()
        return if (left in bonded && right in bonded) left to right else null
    }

    private fun refresh() {
        main.post { lifecycle.conditions(adapter?.isEnabled == true, permitted(), saved() != null) }
    }

    private fun phoneBattery() = getSystemService(BatteryManager::class.java).getIntProperty(BatteryManager.BATTERY_PROPERTY_CAPACITY).takeIf { it in 0..100 }

    private fun prefs() = getSharedPreferences("service", Context.MODE_PRIVATE)

    // LifecycleActions, on the main thread.

    override fun connect(attempt: Long) {
        val (left, right) = saved() ?: return lifecycle.failed(attempt, setup = true)
        conn = Connection(attempt, left, right).also { it.start() }
    }

    override fun disconnect() {
        conn?.stop()
        conn = null
    }

    override fun schedule(ms: Long, attempt: Long) {
        cancelRetry()
        retry = Runnable { lifecycle.retryDue(attempt) }.also { main.postDelayed(it, ms) }
    }

    override fun cancelRetry() {
        retry?.let(main::removeCallbacks)
        retry = null
    }

    override fun phase(p: Phase, detail: String) {
        State.phase = p; State.detail = detail; State.changed()
        if (foreground) getSystemService(NotificationManager::class.java).notify(NOTIFICATION, notification(p.text))
    }

    override fun stopService() {
        foreground = false
        stopForeground(STOP_FOREGROUND_REMOVE)
        stopSelf()
    }

    override fun saveWanted(wanted: Boolean) {
        prefs().edit().putBoolean("wanted", wanted).apply()
    }

    private fun notification(text: String): Notification {
        val open = PendingIntent.getActivity(this, 0, Intent(this, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE)
        val stop = PendingIntent.getService(this, 1, Intent(this, GlassesService::class.java).setAction(ACTION_STOP), PendingIntent.FLAG_IMMUTABLE)
        return Notification.Builder(this, CHANNEL)
            .setSmallIcon(R.drawable.icon)
            .setContentTitle(getString(R.string.app_name))
            .setContentText(text)
            .setContentIntent(open)
            .setOngoing(true)
            .addAction(Notification.Action.Builder(null, getString(R.string.stop), stop).build())
            .build()
    }

    // One attempt's session: the two-arm link, the native session, and the dashboard controller deciding what
    // shows: head tilt (Settings: shown while looking up past showAbove or down past showBelow, as the desktop),
    // the case, touches and notification popups. Until the dashboard runtime (CF-48) the page is a status page.
    private inner class Connection(val attempt: Long, private val left: String, private val right: String) {
        private val sched = HandlerScheduler()
        private val link = ArmsLink(AndroidBleAdapter(this@GlassesService, adapter, sched.looperHandler), arrayOf(left, right), NativeEvents, sched) { s, why ->
            when (s) {
                LinkStatus.SETUP_NEEDED, LinkStatus.ENCRYPTION_FAILED, LinkStatus.WRONG_FIRMWARE -> main.post { lifecycle.failed(attempt, setup = true, why = why) }
                LinkStatus.FAILED, LinkStatus.PERMISSION_NEEDED -> main.post { lifecycle.failed(attempt, why = why) }
                else -> {}
            }
        }
        private val watch = BluetoothWatch(this@GlassesService, link)
        private val session = GlassesSession(callbacks = { main.post(it) }, onEvents = { gen, events -> events(gen, events) })
        private val settings = DisplaySettings.load(this@GlassesService)
        private val clock = MusicStatus()
        private var screen: GlassesScreen? = null
        private var dashboard: Dashboard? = null
        private var generation = 0L
        private var batteries = Batteries()
        private var minute = -1L
        private val tick = object : Runnable {
            override fun run() {
                if (conn !== this@Connection) return
                sample()
                main.postDelayed(this, TICK_MS)
            }
        }

        fun start() {
            watch.start()
            session.start(StartConfig(left, right, settings.dx, settings.dy), link) { outcome ->
                if (conn !== this) return@start
                when (outcome) {
                    is StartOutcome.Failed -> lifecycle.failed(attempt, why = outcome.message)
                    is StartOutcome.Ready -> ready(outcome.generation, outcome.status)
                }
            }
        }

        private fun ready(gen: Long, st: GlassesStatus) {
            session.draw { f ->
                val g = GlassesScreen.open(f, st.width, st.height, settings.dx, settings.scale, Native::departureMono)
                main.post { if (conn === this) opened(gen, g) }
            }
        }

        private fun opened(gen: Long, g: GlassesScreen?) {
            if (g == null) return lifecycle.failed(attempt, why = "no font or canvas")
            screen = g
            val rows = g.renderer.layout.rows
            val width = g.renderer.layout.contentW - TEXT_INSET
            val page = Page("service", "Chewedfeed", listOf("Phone connected", "Dashboard coming soon"), "Unavailable")
            // History pages measure with the session font from the main thread: native calls take the session lock.
            dashboard = Dashboard(Navigation(listOf(page), emptyList(), emptyList()), settings.displayOptions(), SystemClock::elapsedRealtime) { h ->
                h.pages(width, rows) { t -> g.measure(t) }
            }.also { it.acquired(gen) }
            generation = gen
            lifecycle.connected(attempt)
            Hub.attach(mainExecutor) { e ->
                val d = dashboard ?: return@attach
                if (conn !== this) return@attach
                when (e) {
                    is FeedEvent.Show -> d.notify(e.note, LocalTime.now().format(RECEIVED), e.key)
                    is FeedEvent.Dismiss -> d.dismissed(e.key)
                }
                render()
            }
            sample()
            main.postDelayed(tick, TICK_MS)
        }

        // Head angle, case and batteries, read on the session thread (both arms report the same angles); then
        // what to show.
        private fun sample() {
            session.draw { f ->
                val st = f.status() ?: return@draw
                main.post {
                    val d = dashboard ?: return@post
                    if (conn !== this) return@post
                    batteries = Batteries(st.left.battery, st.right.battery, st.caseBattery, phoneBattery())
                    d.pitch(generation, st.pitch)
                    st.left.inCase?.let { d.caseChanged(generation, 0, it) }
                    st.right.inCase?.let { d.caseChanged(generation, 1, it) }
                    val m = System.currentTimeMillis() / 60_000
                    if (m != minute) { minute = m; d.invalidate() } // the footer clock
                    render()
                }
            }
        }

        private fun events(gen: Long, events: List<GlassesEvent>) {
            val d = dashboard ?: return
            if (conn !== this) return
            for (e in events) if (e.type == GlassesEvent.INPUT && e.trigger == GlassesEvent.TRIGGER_TOUCH) d.touch(gen, e.arm, e.a)
            render()
        }

        // Only when the screen changed: the page (or a popup), or black and off.
        private fun render() {
            val d = dashboard ?: return
            val g = screen ?: return
            when (val s = d.poll() ?: return) {
                Screen.Hidden -> session.draw { g.hide() }
                is Screen.Shown -> session.draw {
                    g.show(s.page, s.index, s.total, s.paused, s.popup, batteries) { w -> clock.line(System.currentTimeMillis(), w, g::measure) }
                }
            }
        }

        // Blank, deinitialize (closes GATT, ends reconnects), then free the link's thread.
        fun stop() {
            Hub.detach()
            main.removeCallbacks(tick)
            dashboard?.released()
            dashboard = null
            session.stop {
                watch.stop()
                sched.quit()
                session.close()
            }
        }
    }

    // What the app shows; changed on the main thread.
    object State {
        @Volatile var phase = Phase.STOPPED
        @Volatile var detail = ""
        @Volatile var online = false
        var listener: (() -> Unit)? = null
        fun changed() { listener?.let { Handler(Looper.getMainLooper()).post(it) } }
    }

    companion object {
        const val ACTION_START = "scouterkit.chewedfeed.START"
        const val ACTION_STOP = "scouterkit.chewedfeed.STOP"
        private const val CHANNEL = "glasses"
        private const val NOTIFICATION = 1
        private const val TICK_MS = 500L // the desktop's head-angle interval
        private val RECEIVED = DateTimeFormatter.ofPattern("HH:mm")

        fun start(c: Context) = c.startForegroundService(Intent(c, GlassesService::class.java).setAction(ACTION_START))
        fun stop(c: Context) = c.startService(Intent(c, GlassesService::class.java).setAction(ACTION_STOP))
    }
}
