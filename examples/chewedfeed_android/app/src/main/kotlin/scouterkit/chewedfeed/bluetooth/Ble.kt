package scouterkit.chewedfeed.bluetooth

// The little of Android's BLE stack the arms need, so ArmsLink runs against fakes in the JVM tests. AndroidBle.kt
// is the real implementation. Every callback arrives on the Scheduler's thread.

interface Cancellable {
    fun cancel()
}

// One thread that runs the link's work in order (a HandlerThread on Android).
interface Scheduler {
    fun post(task: () -> Unit)
    fun postDelayed(ms: Long, task: () -> Unit): Cancellable
}

interface BleAdapter {
    val enabled: Boolean
    // Throws SecurityException without the Nearby devices permission.
    fun device(address: String): BleDevice
}

interface BleDevice {
    val bonded: Boolean
    // background = autoConnect: the stack waits for the device's advertising.
    fun connect(background: Boolean, callbacks: BleCallbacks): BleConnection
}

interface BleConnection {
    fun requestMtu(mtu: Int)
    fun preferTwoMegabit()
    fun discoverServices()
    // Subscribes to EVT; false when it could not even be asked.
    fun enableEvents(): Boolean
    // CMD (channel 0) or CTRL (1) without response; false when the stack is busy.
    fun write(channel: Int, data: ByteArray): Boolean
    fun close()
}

interface BleCallbacks {
    fun onConnectionState(connected: Boolean, status: Int)
    fun onMtu(mtu: Int, ok: Boolean)
    // scouter: the scouter service with CMD, CTRL and EVT is there.
    fun onServices(ok: Boolean, scouter: Boolean)
    fun onEventsEnabled(status: Int)
    fun onWriteDone()
    fun onData(data: ByteArray)
}

// Where the arms' link events go: Native.link* in the app (NativeLinkEvents.kt), a recorder in the tests.
interface NativeLinkEvents {
    fun linkUp(arm: Int, mtu: Int)
    fun linkDown(arm: Int)
    fun linkData(arm: Int, data: ByteArray)
    fun linkFound(arm: Int, address: String)
    fun linkProblem(code: Int, text: String)
}
