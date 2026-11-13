package scouterkit.chewedfeed.handover

import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Assume.assumeTrue
import org.junit.Before
import org.junit.Test
import java.io.IOException
import java.net.InetAddress
import java.nio.file.Files
import java.util.concurrent.LinkedBlockingQueue
import java.util.concurrent.TimeUnit

// This Kotlin code against the Go peer (go/examples/internal/handover/peertest) over loopback: enrollment, the
// pinned TLS handshake, hellos and messages both ways. Runs only with -Ppeertest=<built peertest>.
class HandoverInteropTest {
    private val processes = mutableListOf<Process>()
    private lateinit var bin: String

    @Before
    fun need() {
        bin = System.getProperty("handover.peertest").orEmpty()
        assumeTrue("set -Ppeertest to run against the Go peer", bin.isNotEmpty())
    }

    @After
    fun stop() = processes.forEach { it.destroy() }

    private fun go(vararg args: String): Pair<Process, java.io.BufferedReader> {
        val dir = Files.createTempDirectory("peertest").toFile().apply { deleteOnExit() }
        val p = ProcessBuilder(listOf(bin, "-dir", dir.path, "-for", "30s") + args).redirectError(ProcessBuilder.Redirect.INHERIT).start()
        processes += p
        return p to p.inputStream.bufferedReader()
    }

    private fun line(r: java.io.BufferedReader) = StrictJson.parse(r.readLine().toByteArray(), 8192) as Map<*, *>

    // Production direction: the phone hosts (invitation, listener); the Go laptop enrolls and dials it, as the
    // desktop's -enroll-invitation does.
    @Test
    fun phoneHostsTheGoLaptop() {
        val phone = memoryStore()
        val enroller = Enroller(phone)
        val enrolled = LinkedBlockingQueue<Pair<Peer, String>>()
        val got = LinkedBlockingQueue<String>()
        val server = PeerServer(phone, PeerOptions("phone"), enroller, { _ -> { m -> got += m.kind } },
            { p, code -> enrolled += p to code }, port = 0, bind = InetAddress.getLoopbackAddress())
        try {
            val inv = enroller.invite("phone", listOf("127.0.0.1:${server.port}"))
            val (_, out) = go("-invitation", inv.encode())
            val goCode = line(out)["code"]
            val (laptop, code) = enrolled.poll(15, TimeUnit.SECONDS)!!
            assertEquals("laptop", laptop.role)
            assertEquals(goCode, code) // both show the same six digits
            assertFalse(enroller.pending()) // used up
            assertEquals("availability", got.poll(15, TimeUnit.SECONDS)) // Go -> Kotlin
            assertEquals("heartbeat", line(out)["got"]) // Kotlin -> Go (the phone's heartbeat)
            assertTrue(phone.enrolled(laptop.id) != null)
        } finally {
            server.stop()
        }
    }

    // The other direction (a relay route, or the phone as a guest): the Go host enrolls and serves this phone.
    @Test
    fun goHostEnrollsThisPhone() {
        val (_, out) = go()
        val v = line(out)
        val invitation = Invitation.parse(v["invitation"] as String, System.currentTimeMillis())
        assertEquals(v["id"], invitation.hostId)
        val route = DirectRoute.parse(invitation.endpoints[0])!!
        val phone = memoryStore()
        val (host, code) = enroll(phone, invitation, "phone", route)
        assertEquals(invitation.hostId, host.id)
        assertTrue(code.matches(Regex("^[0-9]{6}$")))
        // Single use: a second guest with the same invitation is refused and records nothing.
        val other = memoryStore()
        assertThrows(IOException::class.java) { enroll(other, invitation, "phone", route) }
        assertTrue(other.peers().isEmpty())

        val c = PeerConn.dial(phone, route, host.id, PeerOptions("phone"))
        assertEquals("laptop", c.peerRole)
        assertTrue(c.negotiated(Wire.CAP_SNAPSHOT) && c.negotiated(Wire.CAP_TELEMETRY))
        val got = LinkedBlockingQueue<Message>()
        val closed = LinkedBlockingQueue<Throwable>()
        c.start({ got += it }) { closed += it }
        assertEquals("availability", got.poll(10, TimeUnit.SECONDS)?.kind)
        c.send("heartbeat", 0, "", linkedMapOf("phase" to "idle", "ready_epoch" to 0))
        assertEquals("heartbeat", got.poll(10, TimeUnit.SECONDS)?.kind)
        assertEquals(null, closed.poll(1, TimeUnit.SECONDS)) // both sides accepted every message
        c.close()
        // Pinned to a key the Go host does not have: no connection.
        val wrong = memoryStore(phone.identity(), listOf(Peer("0".repeat(64), "laptop", 0)))
        assertThrows(IOException::class.java) { PeerConn.dial(wrong, route, "0".repeat(64), PeerOptions("phone")) }
    }

    // The full handover against the Go laptop: the phone's real coordinator and listener, fake glasses. The laptop's
    // lid closes (the phone requests, gets release and released, acquires, says ready), then it wakes and asks back
    // (the phone releases its glasses first, then says released). Go's stream checks every phone message.
    @Test
    fun handoverWithTheGoLaptop() {
        val exec = java.util.concurrent.Executors.newSingleThreadScheduledExecutor()
        val scheduler = object : scouterkit.chewedfeed.bluetooth.Scheduler {
            override fun post(task: () -> Unit) { exec.execute(task) }
            override fun postDelayed(ms: Long, task: () -> Unit): scouterkit.chewedfeed.bluetooth.Cancellable {
                val f = exec.schedule(task, ms, TimeUnit.MILLISECONDS)
                return object : scouterkit.chewedfeed.bluetooth.Cancellable { override fun cancel() { f.cancel(false) } }
            }
        }
        val calls = java.util.Collections.synchronizedList(mutableListOf<String>())
        val glasses = object : OwnershipNative {
            override fun acquire(done: (Boolean) -> Unit) { calls += "acquire"; exec.schedule({ done(true) }, 200, TimeUnit.MILLISECONDS) }
            override fun release(done: () -> Unit) { calls += "release"; exec.schedule({ calls += "released"; done() }, 200, TimeUnit.MILLISECONDS) }
            override fun abort() { calls += "abort" }
        }
        val phone = memoryStore()
        val enroller = Enroller(phone)
        lateinit var coordinator: Coordinator
        exec.submit { coordinator = Coordinator(true, glasses, scheduler, { System.nanoTime() / 1_000_000 }, SnapshotBridge()) }.get()
        val server = PeerServer(phone, PeerOptions("phone", heartbeat = { coordinator.heartbeat() }), enroller,
            { c -> val link = ConnLink(c); exec.execute { coordinator.linkUp(link) }; { m -> exec.execute { coordinator.message(link, m) } } },
            port = 0, bind = InetAddress.getLoopbackAddress())
        try {
            exec.submit { coordinator.start(eligible = true, pinned = false) }.get()
            val inv = enroller.invite("phone", listOf("127.0.0.1:${server.port}"))
            val (p, out) = go("-invitation", inv.encode(), "-script", "handover")
            line(out) // the enrollment code
            val got = generateSequence { out.readLine() }.map { (StrictJson.parse(it.toByteArray(), 8192) as Map<*, *>)["got"] as String }.toList()
            assertTrue(p.waitFor(30, TimeUnit.SECONDS))
            assertEquals("the Go laptop finished its script; got $got", 0, p.exitValue())
            val k = got.filter { it != "status" && it != "heartbeat" }
            assertEquals(listOf("request", "acquiring", "ready", "release", "released"), k)
            assertEquals(listOf("acquire", "release", "released"), calls.toList()) // released only after the glasses were down
            exec.submit { assertEquals(Phase.IDLE, coordinator.state.phase) }.get()
        } finally {
            server.stop()
            exec.shutdownNow()
        }
    }
}
