package scouterkit.chewedfeed.handover

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertThrows
import org.junit.Test

// tests/handover/enrollment.json, as go/examples/internal/handover checks it.
class EnrollTest {
    private val f = handoverFixture("enrollment.json")
    private fun unhex(s: String) = ByteArray(s.length / 2) { s.substring(2 * it, 2 * it + 2).toInt(16).toByte() }

    @Test
    fun macsAndCode() {
        val secret = unhex(f["secret_hex"] as String)
        val host = f["host_id"] as String
        val guest = f["guest_id"] as String
        val role = f["guest_role"] as String
        assertArrayEquals(unhex(f["proof_hex"] as String), Invitation.proof(secret, host, guest, role))
        assertArrayEquals(unhex(f["confirm_hex"] as String), Invitation.confirm(secret, host, guest, role))
        assertEquals(f["code"], Invitation.code(secret, host, guest))
    }

    @Test
    fun invitationText() {
        val text = f["invitation_text"] as String
        val inv = Invitation.parse(text, 0)
        val want = f["invitation"] as Map<*, *>
        assertEquals(want["id"], inv.hostId)
        assertEquals(want["role"], inv.hostRole)
        assertEquals(want["secret"], inv.secret)
        assertEquals((want["exp"] as JsonNumber).long, inv.expiresUnixMs)
        assertEquals(want["ep"], inv.endpoints)
        assertEquals(text, inv.encode()) // byte for byte as Go writes it
        assertFalse(inv.toString().contains(inv.secret)) // never in logs
        assertThrows(IllegalArgumentException::class.java) { Invitation.parse(text, inv.expiresUnixMs) } // expired
        for (c in f["invalid_invitations"] as List<*>) {
            c as Map<*, *>
            assertThrows(c["name"].toString(), IllegalArgumentException::class.java) { Invitation.parse(c["text"] as String, 0) }
        }
    }
}
