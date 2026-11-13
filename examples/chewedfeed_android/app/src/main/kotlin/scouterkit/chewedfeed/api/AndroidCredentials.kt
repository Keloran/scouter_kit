package scouterkit.chewedfeed.api

import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.AtomicFile
import java.io.ByteArrayOutputStream
import java.io.File
import java.security.KeyStore
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey

// Only ciphertext under noBackupFilesDir. The non-exportable AES key lives in
// Android Keystore; backup/transfer is also disabled for the whole application.
private val androidCredentialLock = Any()

fun androidSessionCredentials(context: Context): SessionCredentialStore {
    val vault = EncryptedSessionCredentials(
        PrivateCredentialBlob(File(context.applicationContext.noBackupFilesDir, "api_session.bin")), AndroidCredentialKeys(),
    )
    return object : SessionCredentialStore {
        override fun load(): SessionToken? = synchronized(androidCredentialLock) { vault.load() }
        override fun save(token: SessionToken?) = synchronized(androidCredentialLock) { vault.save(token) }
    }
}

private class PrivateCredentialBlob(file: File) : CredentialBlob {
    private val atomic = AtomicFile(file)

    override fun read(limit: Int): ByteArray? {
        if (!atomic.baseFile.exists() && !File(atomic.baseFile.path + ".bak").exists()) return null
        return atomic.openRead().use { input ->
            val out = ByteArrayOutputStream()
            val buffer = ByteArray(1024)
            while (true) {
                val n = input.read(buffer, 0, minOf(buffer.size, limit + 1 - out.size()))
                if (n < 0) break
                require(out.size() + n <= limit)
                out.write(buffer, 0, n)
            }
            out.toByteArray()
        }
    }

    override fun write(value: ByteArray) {
        val stream = atomic.startWrite()
        try { stream.write(value); atomic.finishWrite(stream) }
        catch (e: Exception) { atomic.failWrite(stream); throw e }
    }

    override fun delete() {
        atomic.delete()
        check(!atomic.baseFile.exists())
    }
}

private class AndroidCredentialKeys : CredentialKeys {
    private fun store() = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
    override fun decryptionKey(): SecretKey? = store().getKey(ALIAS, null) as? SecretKey

    override fun encryptionKey(): SecretKey {
        decryptionKey()?.let { return it }
        return KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore").run {
            init(KeyGenParameterSpec.Builder(ALIAS, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM).setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .setKeySize(256).setRandomizedEncryptionRequired(true).build())
            generateKey()
        }
    }

    override fun erase() { store().deleteEntry(ALIAS) }
    companion object { private const val ALIAS = "chewedfeed_api_session_v1" }
}
