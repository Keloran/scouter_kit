package scouterkit.chewedfeed.service

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

// The service's decisions with a fake service: no Android, no Bluetooth.
class LifecycleTest {
    private class Fake : LifecycleActions {
        val log = mutableListOf<String>()
        var attempt = 0L
        var retryAt: Pair<Long, Long>? = null // ms, attempt
        var connected = false
        var stopped = false
        var wanted: Boolean? = null
        override fun connect(attempt: Long) { this.attempt = attempt; connected = true; log += "connect" }
        override fun disconnect() { connected = false; log += "disconnect" }
        override fun schedule(ms: Long, attempt: Long) { retryAt = ms to attempt }
        override fun cancelRetry() { retryAt = null }
        override fun phase(p: Phase, detail: String) {}
        override fun stopService() { stopped = true }
        override fun saveWanted(wanted: Boolean) { this.wanted = wanted }
    }

    private val f = Fake()
    private val l = Lifecycle(f, maxAttempts = 3)

    private fun ready() {
        l.userStart()
        l.conditions(bluetooth = true, permission = true, paired = true)
        l.ownership(true)
    }

    @Test
    fun startConnectsOnlyWhenEverythingAllows() {
        l.userStart()
        assertEquals(Phase.NEEDS_PERMISSION, l.phase)
        l.conditions(bluetooth = false, permission = true, paired = true)
        assertEquals(Phase.WAITING_BLUETOOTH, l.phase)
        l.conditions(bluetooth = true, permission = true, paired = false)
        assertEquals(Phase.NEEDS_SETUP, l.phase)
        l.conditions(bluetooth = true, permission = true, paired = true)
        assertEquals(Phase.STANDBY, l.phase) // eligible, but not the owner
        assertFalse(f.connected)
        l.ownership(true)
        assertEquals(Phase.CONNECTING, l.phase)
        l.connected(f.attempt)
        assertEquals(Phase.ACTIVE, l.phase)
        assertEquals(true, f.wanted)
    }

    @Test
    fun releasingOwnershipClosesAndStaysEligible() {
        ready(); l.connected(f.attempt)
        l.ownership(false)
        assertEquals(Phase.STANDBY, l.phase)
        assertFalse(f.connected)
        assertEquals(null, f.retryAt)
        assertFalse(f.stopped) // the service stays, ready to take over again
        l.ownership(true)
        assertEquals(Phase.CONNECTING, l.phase)
    }

    @Test
    fun bluetoothOffReleasesAndOnReconnects() {
        ready(); l.connected(f.attempt)
        l.conditions(bluetooth = false, permission = true, paired = true)
        assertEquals(Phase.WAITING_BLUETOOTH, l.phase)
        assertFalse(f.connected)
        l.conditions(bluetooth = true, permission = true, paired = true)
        assertEquals(Phase.CONNECTING, l.phase)
        assertTrue(f.connected)
    }

    @Test
    fun permissionRevokedReleases() {
        ready(); l.connected(f.attempt)
        l.conditions(bluetooth = true, permission = false, paired = true)
        assertEquals(Phase.NEEDS_PERMISSION, l.phase)
        assertFalse(f.connected)
    }

    @Test
    fun userStopHoldsThroughStickyRestart() {
        ready(); l.connected(f.attempt)
        l.userStop()
        assertEquals(Phase.STOPPED, l.phase)
        assertTrue(f.stopped && !f.connected)
        assertEquals(false, f.wanted)
        // Process death and a sticky restart with what was saved: stays stopped.
        val f2 = Fake()
        val l2 = Lifecycle(f2)
        l2.restored(wanted = false)
        l2.conditions(bluetooth = true, permission = true, paired = true)
        assertTrue(f2.stopped && f2.log.isEmpty())
    }

    @Test
    fun processRecreationWaitsForOwnership() {
        val l2 = Lifecycle(f)
        l2.restored(wanted = true)
        l2.conditions(bluetooth = true, permission = true, paired = true)
        assertEquals(Phase.STANDBY, l2.phase) // reconcile with the peer first
        assertFalse(f.connected)
        l2.ownership(true)
        assertEquals(Phase.CONNECTING, l2.phase)
    }

    @Test
    fun cancelDuringAcquireIgnoresTheLateResult() {
        ready()
        val first = f.attempt
        l.ownership(false) // released while connecting
        assertEquals(listOf("connect", "disconnect"), f.log)
        l.connected(first) // the native start finished anyway
        assertEquals(Phase.STANDBY, l.phase)
        l.failed(first)
        assertEquals(null, f.retryAt)
    }

    @Test
    fun retriesAreBoundedThenWaitForATrigger() {
        ready()
        for (i in 1..2) {
            l.failed(f.attempt, why = "timeout")
            assertEquals(Phase.RETRY_WAIT, l.phase)
            assertEquals(Lifecycle.backoff(i), f.retryAt!!.first)
            l.retryDue(f.retryAt!!.second)
            assertEquals(Phase.CONNECTING, l.phase)
        }
        l.failed(f.attempt)
        assertEquals(Phase.FAILED, l.phase)
        assertEquals(null, f.retryAt)
        l.conditions(bluetooth = true, permission = true, paired = true) // nothing new: stays
        assertEquals(Phase.FAILED, l.phase)
        l.userStart() // the user tries again
        assertEquals(Phase.CONNECTING, l.phase)
        assertEquals(listOf(2_000L, 4_000L, 60_000L, 60_000L), listOf(1, 2, 6, 20).map(Lifecycle::backoff))
    }

    @Test
    fun lostSessionAndUnpairedArms() {
        ready(); l.connected(f.attempt)
        l.failed(f.attempt) // the session dropped
        assertEquals(Phase.RETRY_WAIT, l.phase)
        assertFalse(f.connected)
        l.retryDue(f.retryAt!!.second)
        l.failed(f.attempt, setup = true, why = "bond lost")
        assertEquals(Phase.NEEDS_SETUP, l.phase)
        l.conditions(bluetooth = true, permission = true, paired = true) // paired again
        assertEquals(Phase.CONNECTING, l.phase)
    }
}
