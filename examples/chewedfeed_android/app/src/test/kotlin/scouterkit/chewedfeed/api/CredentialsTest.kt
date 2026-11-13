package scouterkit.chewedfeed.api

import org.junit.Assert.*
import org.junit.Test
import java.io.IOException
import java.io.Reader
import java.io.StringReader
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey

private class MemoryBlob : CredentialBlob {
    var bytes: ByteArray? = null
    var failWrite = false
    override fun read(limit: Int): ByteArray? = bytes?.also { require(it.size <= limit) }?.copyOf()
    override fun write(value: ByteArray) { if (failWrite) throw IOException("private storage detail"); bytes = value.copyOf() }
    override fun delete() { bytes = null }
}

private class MemoryKeys : CredentialKeys {
    var key: SecretKey? = null
    override fun encryptionKey(): SecretKey = key ?: KeyGenerator.getInstance("AES").apply { init(256) }.generateKey().also { key = it }
    override fun decryptionKey(): SecretKey? = key
    override fun erase() { key = null }
}

class CredentialsTest {
    @Test fun encryptedRoundTripRandomIvAndClear() {
        val blob = MemoryBlob()
        val keys = MemoryKeys()
        val vault = EncryptedSessionCredentials(blob, keys)
        assertNull(vault.load())
        assertNull(keys.key)
        val token = SessionToken.parse("test-session")
        vault.save(token)
        val first = blob.bytes!!.copyOf()
        assertFalse(String(first, Charsets.ISO_8859_1).contains("test-session"))
        assertEquals(token.cookie, vault.load()!!.cookie)
        vault.save(token)
        assertFalse(first.contentEquals(blob.bytes!!))
        assertEquals(token.cookie, vault.load()!!.cookie)
        vault.save(null)
        assertNull(vault.load())
        assertNull(blob.bytes)
        assertNull(keys.key)
    }

    @Test fun tamperingWrongKeysAndOversizeFailClosed() {
        val blob = MemoryBlob()
        val keys = MemoryKeys()
        val vault = EncryptedSessionCredentials(blob, keys)
        vault.save(SessionToken.parse("test-session"))
        val good = blob.bytes!!.copyOf()
        for (index in listOf(0, 1, 13, good.lastIndex)) {
            blob.bytes = good.copyOf().also { it[index] = (it[index].toInt() xor 1).toByte() }
            unavailable { vault.load() }
        }
        blob.bytes = good
        keys.key = KeyGenerator.getInstance("AES").apply { init(256) }.generateKey()
        unavailable { vault.load() }
        keys.key = null
        unavailable { vault.load() }
        blob.bytes = ByteArray(MAX_SESSION_BYTES + 30)
        unavailable { vault.load() }
    }

    @Test fun failedReplacementPreservesPreviousCredential() {
        val blob = MemoryBlob()
        val vault = EncryptedSessionCredentials(blob, MemoryKeys())
        vault.save(SessionToken.parse("test-session"))
        blob.failWrite = true
        unavailable { vault.save(SessionToken.parse("replacement-session")) }
        assertEquals("test-session", vault.load()!!.cookie)
    }

    @Test fun importsAreBoundedValidatedAndRedacted() {
        val vault = EncryptedSessionCredentials(MemoryBlob(), MemoryKeys())
        vault.import(StringReader("test-session\n"))
        assertEquals("test-session", vault.load()!!.cookie)
        assertFalse(vault.load().toString().contains("test-session"))
        for (bad in listOf("", "session=test-session", "bad;cookie", "bad\ncookie", "bad cookie", "bad,cookie", "bad\\cookie", "bad\"cookie", "bad\u0000cookie", "x".repeat(MAX_SESSION_BYTES + 1))) {
            try { SessionToken.parse(bad); fail("Accepted invalid cookie") } catch (_: IllegalArgumentException) { }
        }
        try { vault.import(StringReader("x".repeat(MAX_SESSION_BYTES + 3))); fail("Unbounded import") } catch (_: IllegalArgumentException) { }
        val failing = object : Reader() {
            override fun read(buffer: CharArray, offset: Int, length: Int): Int = throw IOException("private reader detail")
            override fun close() { }
        }
        try { vault.import(failing); fail("Failed import succeeded") } catch (e: IllegalArgumentException) {
            assertFalse(e.toString().contains("private reader detail"))
        }
        assertEquals("test-session", vault.load()!!.cookie)
    }

    private fun unavailable(call: () -> Unit) {
        try { call(); fail("Credential failure was ignored") } catch (e: CredentialsUnavailable) {
            assertTrue(e.message!!.contains("re-enter"))
            assertFalse(e.toString().contains("test-session"))
            assertFalse(e.toString().contains("private storage detail"))
        }
    }
}
