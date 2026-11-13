package scouterkit.chewedfeed.handover

import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import java.io.File
import java.security.KeyPairGenerator
import java.security.KeyStore
import java.security.PrivateKey
import java.security.cert.X509Certificate
import java.security.spec.ECGenParameterSpec
import java.util.Date
import javax.security.auth.x500.X500Principal

// This phone's peer identity: a P-256 key that never leaves the Android Keystore (Android makes its self-signed
// certificate), and the enrolled peers in a private file outside backups (noBackupFilesDir; the app's backup rules
// exclude everything anyway). Rotating deletes the key, makes a new one and forgets every peer.
object AndroidPeers {
    private const val ALIAS = "chewedfeed-peer"

    fun open(context: Context): PeerStore {
        val file = File(context.noBackupFilesDir, "peers.json")
        val peers = runCatching { PeerStore.decodePeers(file.readText()) }.getOrDefault(emptyList())
        return PeerStore(load() ?: create(), peers, { save(file, it) }, { delete(); create() })
    }

    private fun keystore() = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }

    private fun load(): Identity? {
        val ks = keystore()
        val key = ks.getKey(ALIAS, null) as? PrivateKey ?: return null
        val cert = ks.getCertificate(ALIAS) as? X509Certificate ?: return null
        return Identity(key, cert)
    }

    private fun create(): Identity {
        val now = System.currentTimeMillis()
        KeyPairGenerator.getInstance(KeyProperties.KEY_ALGORITHM_EC, "AndroidKeyStore").apply {
            initialize(KeyGenParameterSpec.Builder(ALIAS, KeyProperties.PURPOSE_SIGN)
                .setAlgorithmParameterSpec(ECGenParameterSpec("secp256r1"))
                // TLS signs the handshake itself (NONE) or with these digests.
                .setDigests(KeyProperties.DIGEST_NONE, KeyProperties.DIGEST_SHA256, KeyProperties.DIGEST_SHA384, KeyProperties.DIGEST_SHA512)
                .setCertificateSubject(X500Principal("CN=chewedfeed peer"))
                .setCertificateNotBefore(Date(now - 3_600_000))
                .setCertificateNotAfter(Date(now + 30L * 365 * 24 * 3_600_000))
                .build())
        }.generateKeyPair()
        return load() ?: error("the Keystore did not keep the new key")
    }

    private fun delete() = keystore().deleteEntry(ALIAS)

    private fun save(file: File, peers: List<Peer>) {
        val tmp = File(file.parentFile, ".peers.tmp")
        tmp.writeText(PeerStore.encodePeers(peers))
        if (!tmp.renameTo(file)) error("cannot save the enrolled peers")
    }
}
