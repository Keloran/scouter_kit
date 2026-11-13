package scouterkit.chewedfeed.handover

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.File
import java.io.IOException
import java.net.InetAddress
import java.net.ServerSocket
import java.security.KeyStore
import java.security.PrivateKey
import java.security.cert.X509Certificate
import java.util.concurrent.CountDownLatch
import java.util.concurrent.LinkedBlockingQueue
import java.util.concurrent.TimeUnit
import javax.net.ssl.SSLSocket

// A throwaway P-256 identity from the JDK's keytool (plain Java cannot make certificates).
internal fun testIdentity(): Identity {
    val f = File.createTempFile("peer", ".p12").apply { delete(); deleteOnExit() }
    val keytool = File(System.getProperty("java.home"), "bin/keytool").path
    val p = ProcessBuilder(keytool, "-genkeypair", "-keyalg", "EC", "-groupname", "secp256r1", "-sigalg", "SHA256withECDSA",
        "-dname", "CN=chewedfeed peer", "-validity", "2", "-storetype", "PKCS12", "-keystore", f.path,
        "-storepass", "test-only", "-keypass", "test-only", "-alias", "peer").redirectErrorStream(true).start()
    check(p.waitFor(60, TimeUnit.SECONDS) && p.exitValue() == 0) { p.inputStream.readBytes().decodeToString() }
    val ks = KeyStore.getInstance("PKCS12").apply { f.inputStream().use { load(it, "test-only".toCharArray()) } }
    return Identity(ks.getKey("peer", "test-only".toCharArray()) as PrivateKey, ks.getCertificate("peer") as X509Certificate)
}

internal fun memoryStore(id: Identity = testIdentity(), peers: List<Peer> = emptyList()) = PeerStore(id, peers, {}, ::testIdentity)

// Kotlin to Kotlin over loopback TLS: the laptop side here is PeerConn.accept.
class TransportTest {
    private val phoneId = testIdentity()
    private val laptopId = testIdentity()
    private val phone = memoryStore(phoneId, listOf(Peer(laptopId.id, "laptop", 0)))
    private val laptop = memoryStore(laptopId, listOf(Peer(phoneId.id, "phone", 0)))
    private val server = ServerSocket(0, 4, InetAddress.getLoopbackAddress())
    private val route = DirectRoute(server.inetAddress.hostAddress!!, server.localPort)

    private fun acceptOne(store: PeerStore = laptop, opts: PeerOptions = PeerOptions("laptop")): LinkedBlockingQueue<Any> {
        val result = LinkedBlockingQueue<Any>()
        Thread { result += runCatching { PeerConn.accept(server.accept(), store, opts) }.getOrElse { it } }.start()
        return result
    }

    @Test
    fun handshakeAndMessages() {
        val accepted = acceptOne()
        val c = PeerConn.dial(phone, route, laptopId.id, PeerOptions("phone"))
        val l = accepted.poll(10, TimeUnit.SECONDS) as PeerConn
        assertEquals("laptop", c.peerRole)
        assertEquals("phone", l.peerRole)
        assertTrue(c.negotiated(Wire.CAP_SNAPSHOT))
        val atLaptop = LinkedBlockingQueue<Message>()
        l.start({ atLaptop += it }) {}
        c.start({}) {}
        c.send("availability", 0, "", linkedMapOf("eligible" to true, "lid" to "absent", "awake" to true, "reason" to "startup"))
        val m = atLaptop.poll(10, TimeUnit.SECONDS)!!
        assertEquals("availability", m.kind)
        assertEquals(c.session, m.session)
        c.close()
        l.close()
    }

    @Test
    fun wrongIdentityAndUnknownKeysFailClosed() {
        // The phone pins a different laptop key: the handshake fails before any message.
        val other = memoryStore(phoneId, listOf(Peer("0".repeat(64), "laptop", 0)))
        acceptOne()
        assertThrows(IOException::class.java) { PeerConn.dial(other, route, "0".repeat(64), PeerOptions("phone")) }
        // A key the laptop never enrolled is refused.
        val stranger = memoryStore(testIdentity(), listOf(Peer(laptopId.id, "laptop", 0)))
        val refused = acceptOne()
        runCatching { PeerConn.dial(stranger, route, laptopId.id, PeerOptions("phone")) }
        assertTrue(refused.poll(10, TimeUnit.SECONDS) is NotEnrolled)
        // Not enrolled here: no dial at all.
        assertThrows(NotEnrolled::class.java) { PeerConn.dial(memoryStore(), route, laptopId.id, PeerOptions("phone")) }
    }

    @Test
    fun revokedWhileConnectedEnds() {
        val accepted = acceptOne()
        val c = PeerConn.dial(phone, route, laptopId.id, PeerOptions("phone"))
        val l = accepted.poll(10, TimeUnit.SECONDS) as PeerConn
        val closed = LinkedBlockingQueue<Throwable>()
        c.start({}) { closed += it }
        l.start({}) {}
        phone.revoke(laptopId.id)
        l.send("availability", 0, "", linkedMapOf("eligible" to true, "lid" to "open", "awake" to true, "reason" to "resume"))
        assertTrue(closed.poll(10, TimeUnit.SECONDS) is NotEnrolled)
        l.close()
    }

    @Test
    fun backpressureNeverBlocks() {
        val accepted = acceptOne()
        val c = PeerConn.dial(phone, route, laptopId.id, PeerOptions("phone"))
        accepted.poll(10, TimeUnit.SECONDS) as PeerConn // never started: it reads nothing
        // Not started either: nothing drains the queue.
        repeat(PeerConn.SEND_QUEUE) { c.send("heartbeat", 0, "", linkedMapOf("phase" to "idle", "ready_epoch" to 0)) }
        assertThrows(Backpressure::class.java) { c.send("heartbeat", 0, "", linkedMapOf("phase" to "idle", "ready_epoch" to 0)) }
        c.close()
        assertThrows(PeerClosed::class.java) { c.send("heartbeat", 0, "", linkedMapOf("phase" to "idle", "ready_epoch" to 0)) }
    }

    // The laptop's side written by hand, to send what a well-behaved peer never would.
    private fun rawLaptop(after: (SSLSocket, Message) -> Unit): CountDownLatch {
        val done = CountDownLatch(1)
        Thread {
            runCatching {
                val s = tlsContext(laptopId) {}.socketFactory.createSocket(server.accept(), null, 0, true) as SSLSocket
                s.useClientMode = false
                s.needClientAuth = true
                s.startHandshake()
                val hello = Wire.readFrame(s.inputStream)
                val out = Outbox(laptopId.id, phoneId.id, "e".repeat(32)).apply { peerSession = hello.session }
                Wire.writeFrame(s.outputStream, out.next("hello", 0, "", linkedMapOf("role" to "laptop",
                    "required_capabilities" to Wire.REQUIRED, "optional_capabilities" to Wire.OPTIONAL)))
                val next = out.next("availability", 0, "", linkedMapOf("eligible" to true, "lid" to "open", "awake" to true, "reason" to "resume"))
                after(s, next)
            }
            done.countDown()
        }.start()
        return done
    }

    private fun frame(m: Message) = Wire.encode(m).let { byteArrayOf(0, 0, (it.size ushr 8).toByte(), it.size.toByte()) + it }

    @Test
    fun tamperedAndReplayedFramesFailClosed() {
        for ((name, send) in listOf<Pair<String, (SSLSocket, Message) -> Unit>>(
            "tampered" to { s, m -> s.outputStream.write(frame(m).also { it[it.size - 3] = '!'.code.toByte() }); s.outputStream.flush() },
            "replay" to { s, m -> repeat(2) { s.outputStream.write(frame(m)) }; s.outputStream.flush() },
            "other sender" to { s, m -> s.outputStream.write(frame(m.copy(sender = "f".repeat(64)))); s.outputStream.flush() },
            "oversized" to { s, _ -> s.outputStream.write(byteArrayOf(0, 1, 0, 1)); s.outputStream.flush() },
        )) {
            rawLaptop { s, m -> send(s, m); Thread.sleep(2000); s.close() }
            val c = PeerConn.dial(phone, route, laptopId.id, PeerOptions("phone"))
            val got = LinkedBlockingQueue<Message>()
            val closed = LinkedBlockingQueue<Throwable>()
            c.start({ got += it }) { closed += it }
            val why = closed.poll(10, TimeUnit.SECONDS)
            assertNotNull(name, why)
            if (name == "replay") assertEquals(name, 1, got.size) else assertEquals(name, 0, got.size)
            assertTrue("$name: $why", why is InvalidMessage || why?.cause is InvalidMessage || why is IOException)
        }
    }

    @Test
    fun disconnectMidTransferEndsOnce() {
        val accepted = acceptOne()
        val c = PeerConn.dial(phone, route, laptopId.id, PeerOptions("phone"))
        val l = accepted.poll(10, TimeUnit.SECONDS) as PeerConn
        val ends = LinkedBlockingQueue<Throwable>()
        c.start({}) { ends += it }
        l.start({}) {}
        c.send("request", 0, "a".repeat(32), linkedMapOf("target" to "phone", "reason" to "lid_closed", "pinned" to false))
        l.close() // the laptop goes away mid-transfer
        assertNotNull(ends.poll(20, TimeUnit.SECONDS))
        assertEquals(null, ends.poll(500, TimeUnit.MILLISECONDS)) // exactly once
        assertThrows(PeerClosed::class.java) { c.send("heartbeat", 0, "", linkedMapOf("phase" to "idle", "ready_epoch" to 0)) }
    }

    @Test
    fun linkReconnectsWithFreshSessions() {
        val sessions = LinkedBlockingQueue<String>()
        val retired = LinkedBlockingQueue<List<String>>()
        Thread {
            repeat(2) {
                runCatching {
                    val l = PeerConn.accept(server.accept(), laptop, PeerOptions("laptop"))
                    sessions += l.peerSession
                    l.start({}) {}
                    Thread.sleep(300)
                    l.close() // drop it: the phone dials again
                }
            }
        }.start()
        val link = PeerLink(phone, route, laptopId.id, PeerOptions("phone"), { c -> retired += c.peerSession.let { listOf(it) }; { } }, random = { 0.0 })
        link.start()
        val first = sessions.poll(10, TimeUnit.SECONDS)!!
        link.kick() // the network changed: no 5 s wait
        val second = sessions.poll(15, TimeUnit.SECONDS)!!
        assertFalse(first == second) // a new session per connection
        link.stop()
    }
}
