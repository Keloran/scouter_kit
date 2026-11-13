package scouterkit.chewedfeed.api

import java.io.Reader
import java.nio.ByteBuffer
import java.nio.charset.CodingErrorAction
import javax.crypto.Cipher
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

internal const val MAX_SESSION_BYTES = 16 * 1024

// Deliberately not a data class: diagnostics and settings state cannot print it.
class SessionToken private constructor(internal val cookie: String) {
    override fun toString(): String = "<session credential>"

    companion object {
        fun parse(input: String): SessionToken {
            val value = input.trim()
            require(value.length in 1..MAX_SESSION_BYTES && !value.startsWith("session=") &&
                value.all { it.code in 0x21..0x7e && it !in "\";,\\" }) {
                "Enter only the session cookie value, without its name or whitespace"
            }
            return SessionToken(value)
        }
    }
}

class CredentialsUnavailable : IllegalStateException("Saved session unavailable; re-enter or clear the session cookie")

// Blocking interface independent of settings/activity/service lifetimes. No OAuth
// endpoint is assumed. Null means public mode; importing never logs the input.
interface SessionCredentialStore {
    fun load(): SessionToken?
    fun save(token: SessionToken?)

    fun import(reader: Reader) {
        try {
            val out = StringBuilder()
            val buffer = CharArray(1024)
            while (true) {
                val n = reader.read(buffer)
                if (n < 0) break
                require(out.length + n <= MAX_SESSION_BYTES + 2)
                out.append(buffer, 0, n)
            }
            save(SessionToken.parse(out.toString()))
        } catch (e: CredentialsUnavailable) { throw e
        } catch (_: Exception) { throw IllegalArgumentException("Session cookie import failed or exceeded limit; enter its value directly") }
    }
}

interface CredentialBlob {
    // Reads at most limit+1 bytes to detect oversize. Writes replace atomically.
    fun read(limit: Int): ByteArray?
    fun write(value: ByteArray)
    fun delete()
}

interface CredentialKeys {
    fun encryptionKey(): SecretKey
    fun decryptionKey(): SecretKey?
    fun erase()
}

class EncryptedSessionCredentials(private val blob: CredentialBlob, private val keys: CredentialKeys) : SessionCredentialStore {
    @Synchronized override fun load(): SessionToken? {
        try {
            val encoded = blob.read(MAX_SESSION_BYTES + 29) ?: return null
            require(encoded.size in 30..MAX_SESSION_BYTES + 29 && encoded[0] == 1.toByte())
            val cipher = Cipher.getInstance("AES/GCM/NoPadding")
            val key = keys.decryptionKey() ?: throw CredentialsUnavailable()
            cipher.init(Cipher.DECRYPT_MODE, key, GCMParameterSpec(128, encoded.copyOfRange(1, 13)))
            val plain = cipher.doFinal(encoded, 13, encoded.size - 13)
            try {
                val text = Charsets.UTF_8.newDecoder().onMalformedInput(CodingErrorAction.REPORT)
                    .onUnmappableCharacter(CodingErrorAction.REPORT).decode(ByteBuffer.wrap(plain)).toString()
                return SessionToken.parse(text)
            } finally { plain.fill(0) }
        } catch (_: Exception) { throw CredentialsUnavailable() }
    }

    @Synchronized override fun save(token: SessionToken?) {
        try {
            if (token == null) { blob.delete(); keys.erase(); return }
            val cipher = Cipher.getInstance("AES/GCM/NoPadding")
            cipher.init(Cipher.ENCRYPT_MODE, keys.encryptionKey())
            require(cipher.iv.size == 12)
            val plain = token.cookie.toByteArray(Charsets.UTF_8)
            try { blob.write(byteArrayOf(1) + cipher.iv + cipher.doFinal(plain)) }
            finally { plain.fill(0) }
        } catch (_: Exception) { throw CredentialsUnavailable() }
    }
}
