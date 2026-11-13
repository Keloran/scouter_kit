package scouterkit.chewedfeed

// The Bluetooth side of the glasses link, implemented by the BLE layer (CF-40) and called by
// native/link_transport.cpp from the library's Bluetooth thread: connect and disconnect an arm, drop it and
// reconnect after `ms` (hold), write CMD (channel 0) or CTRL (1) without response, stop everything. Its answers
// go back through Native.linkUp/linkDown/linkData/linkFound/linkProblem, from any thread. Exceptions thrown
// here are cleared natively (a write counts as failed), so they never leave the library stuck.
interface ArmLink {
    fun write(arm: Int, channel: Int, data: ByteArray): Boolean
    fun connect(arm: Int, unused: Int)
    fun disconnect(arm: Int, unused: Int)
    fun hold(arm: Int, ms: Int)
    fun stop(unused: Int, unused2: Int)
}
