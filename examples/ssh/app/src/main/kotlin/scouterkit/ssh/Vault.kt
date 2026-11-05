package scouterkit.ssh

import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import java.io.File
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

// Secrets (R6): the SSH key and its passphrase, the mosh session, the host key's fingerprint. Each is a file
// in filesDir/vault, AES-256-GCM with a key in AndroidKeyStore that needs no user authentication (the
// service runs with the screen off): 12-byte IV, then the ciphertext.
class Vault(context: Context) {
    private val dir = File(context.filesDir, "vault").apply { mkdirs() }

    private fun key(): SecretKey {
        val store = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        (store.getKey(ALIAS, null) as SecretKey?)?.let { return it }
        val generator = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore")
        generator.init(
            KeyGenParameterSpec.Builder(ALIAS, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .setKeySize(256)
                .build()
        )
        return generator.generateKey()
    }

    fun put(name: String, value: ByteArray) {
        val cipher = Cipher.getInstance("AES/GCM/NoPadding").apply { init(Cipher.ENCRYPT_MODE, key()) }
        val file = File(dir, name)
        val temp = File(dir, "$name.new")
        temp.writeBytes(cipher.iv + cipher.doFinal(value))
        temp.renameTo(file)
    }

    fun get(name: String): ByteArray? {
        val data = File(dir, name).takeIf { it.exists() }?.readBytes() ?: return null
        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        cipher.init(Cipher.DECRYPT_MODE, key(), GCMParameterSpec(128, data, 0, 12))
        return cipher.doFinal(data, 12, data.size - 12)
    }

    fun putText(name: String, value: String) = put(name, value.toByteArray())
    fun text(name: String): String? = get(name)?.toString(Charsets.UTF_8)
    fun delete(name: String) = File(dir, name).delete()
    fun has(name: String) = File(dir, name).exists()

    companion object {
        const val ALIAS = "scouter-vault"
        const val SSH_KEY = "ssh_key"
        const val SSH_PASSPHRASE = "ssh_passphrase"
        const val HOST_KEY = "host_key" // "host:port fingerprint"
        const val MOSH = "mosh"         // "ip port key pid"
    }
}
