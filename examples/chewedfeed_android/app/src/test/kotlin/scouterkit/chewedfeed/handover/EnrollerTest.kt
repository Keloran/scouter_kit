package scouterkit.chewedfeed.handover

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.IOException
import java.net.InetAddress
import java.util.concurrent.LinkedBlockingQueue
import java.util.concurrent.TimeUnit

// The phone as host, Kotlin only: invitations, single use, expiry, guessing, revoke and rotate.
class EnrollerTest {
    private var now = 1_000_000L
    private val phone = memoryStore()
    private val enroller = Enroller(phone) { now }
    private val enrolled = LinkedBlockingQueue<Pair<Peer, String>>()
    private val messages = LinkedBlockingQueue<String>()
    private fun server() = PeerServer(phone, PeerOptions("phone"), enroller, { _ -> { m -> messages += m.kind } },
        { p, c -> enrolled += p to c }, port = 0, bind = InetAddress.getLoopbackAddress())

    @Test
    fun enrollOnceThenTalk() {
        val s = server()
        try {
            val route = DirectRoute("127.0.0.1", s.port)
            val inv = enroller.invite("phone", listOf("127.0.0.1:${s.port}"))
            assertTrue(enroller.pending())
            val laptop = memoryStore()
            val (host, code) = enroll(laptop, Invitation.parse(inv.encode(), now), "laptop", route, now)
            val (guest, hostCode) = enrolled.poll(10, TimeUnit.SECONDS)!!
            assertEquals(phone.identity().id, host.id)
            assertEquals(laptop.identity().id, guest.id)
            assertEquals(code, hostCode)
            assertFalse(enroller.pending())
            // The invitation is used up: a second laptop is refused, and records nothing.
            val second = memoryStore()
            assertThrows(IOException::class.java) { enroll(second, Invitation.parse(inv.encode(), now), "laptop", route, now) }
            assertTrue(second.peers().isEmpty())
            // The enrolled laptop dials and talks.
            val c = PeerConn.dial(laptop, route, host.id, PeerOptions("laptop")).start({}) {}
            c.send("availability", 0, "", linkedMapOf("eligible" to true, "lid" to "open", "awake" to true, "reason" to "resume"))
            assertEquals("availability", messages.poll(10, TimeUnit.SECONDS))
            c.close()
            // Revoked: refused before any protocol message.
            phone.revoke(guest.id)
            assertThrows(IOException::class.java) { PeerConn.dial(laptop, route, host.id, PeerOptions("laptop")) }
        } finally {
            s.stop()
        }
    }

    @Test
    fun expiredInvitationsEnrollNobody() {
        val s = server()
        try {
            val inv = enroller.invite("phone", listOf("127.0.0.1:${s.port}"))
            now += Enroller.TTL_MS
            assertFalse(enroller.pending())
            assertThrows(IllegalArgumentException::class.java) { Invitation.parse(inv.encode(), now) } // the guest sees it too
            assertThrows(IOException::class.java) { enroll(memoryStore(), inv, "laptop", DirectRoute("127.0.0.1", s.port), now - 1) }
            assertTrue(phone.peers().isEmpty())
        } finally {
            s.stop()
        }
    }

    @Test
    fun guessingWithdrawsEveryInvitation() {
        enroller.invite("phone", emptyList())
        val bad = linkedMapOf("enroll" to JsonNumber("1"), "role" to "laptop", "proof" to "0".repeat(64))
        repeat(Enroller.MAX_FAILURES - 1) { assertThrows(IOException::class.java) { enroller.admit("a".repeat(64), bad) } }
        assertTrue(enroller.pending())
        assertThrows(IOException::class.java) { enroller.admit("a".repeat(64), bad) }
        assertFalse(enroller.pending()) // withdrawn
        assertThrows(InvalidMessage::class.java) { enroller.admit("a".repeat(64), bad + ("extra" to true)) }
        assertTrue(phone.peers().isEmpty())
    }

    @Test
    fun rotateForgetsEveryPeer() {
        phone.addPeer(Peer("b".repeat(64), "laptop", 0))
        val before = phone.identity().id
        phone.rotate()
        assertNotEquals(before, phone.identity().id)
        assertTrue(phone.peers().isEmpty())
        assertThrows(IllegalArgumentException::class.java) { phone.addPeer(Peer(phone.identity().id, "laptop", 0)) }
        val text = PeerStore.encodePeers(listOf(Peer("c".repeat(64), "laptop", 5)))
        assertEquals(listOf(Peer("c".repeat(64), "laptop", 5)), PeerStore.decodePeers(text))
        assertEquals(emptyList<Peer>(), PeerStore.decodePeers("""{"peers":[{"id":"short","role":"laptop"}]}"""))
    }

    @Test
    fun unknownKeyWithoutAnInvitationIsRefused() {
        val s = server()
        try {
            val stranger = memoryStore(peers = listOf(Peer(phone.identity().id, "phone", 0)))
            assertThrows(IOException::class.java) { PeerConn.dial(stranger, DirectRoute("127.0.0.1", s.port), phone.identity().id, PeerOptions("laptop")) }
            assertEquals(null, messages.poll(500, TimeUnit.MILLISECONDS))
        } finally {
            s.stop()
        }
    }
}
