package scouterkit.chewedfeed.api

import scouterkit.chewedfeed.handover.JsonNumber
import scouterkit.chewedfeed.handover.StrictJson
import scouterkit.chewedfeed.render.SourceResult
import java.io.File
import java.io.FileOutputStream
import java.security.MessageDigest

// The last good Sites data on disk, for a start without network (CF-63): the API client's allowlisted rows only,
// never the customer source (needs the session), notifications, media or anything of the handover. One file in the
// app's no-backup directory, bound to the repository key (API, project, details, session): any other key, a newer
// or older format, more than MAX_AGE_MS old, oversized or unreadable is deleted and ignored. Replaced atomically
// (temporary file, fsync, rename): a failed or interrupted write leaves the previous file.
class SitesCache(private val file: File, private val clock: () -> Long = System::currentTimeMillis) {
    fun load(key: String): List<SourceResult>? = synchronized(this) {
        val bytes = runCatching { if (file.length() > MAX_BYTES) null else file.readBytes() }.getOrNull() ?: return discard()
        val root = runCatching { StrictJson.parse(bytes, MAX_BYTES) }.getOrNull() as? Map<*, *> ?: return discard()
        val saved = (root["saved"] as? JsonNumber)?.text?.toLongOrNull()
        if ((root["version"] as? JsonNumber)?.text != VERSION.toString() || root["key"] != digest(key) ||
            saved == null || clock() - saved !in 0..MAX_AGE_MS) return discard()
        val sources = (root["sources"] as? List<*>)?.map { s ->
            val m = s as? Map<*, *> ?: return discard()
            val name = m["key"] as? String ?: return discard()
            val at = (m["at"] as? JsonNumber)?.text?.toLongOrNull() ?: return discard()
            val rows = (m["rows"] as? List<*>)?.map { (plain(it) as? Map<*, *>)?.let { r -> r.mapKeys { k -> k.key as String } } ?: return discard() }
                ?: return discard()
            SourceResult(name, rows, at)
        } ?: return discard()
        sources.filter { clock() - (it.atMs ?: 0) <= MAX_AGE_MS } // a source kept failing: its rows age out
    }

    // False when it could not be written (disk full, no space for the temporary file): the previous file stays.
    fun save(key: String, sources: List<SourceResult>): Boolean = synchronized(this) {
        val kept = sources.filter { it.atMs != null && it.key != "customers" }
        val text = StrictJson.write(linkedMapOf(
            "version" to VERSION, "key" to digest(key), "saved" to clock(),
            "sources" to kept.map { linkedMapOf("key" to it.key, "at" to it.atMs, "rows" to it.rows) },
        )).toByteArray(Charsets.UTF_8)
        if (text.size > MAX_BYTES) return false
        val tmp = File(file.parentFile, file.name + ".tmp")
        return try {
            FileOutputStream(tmp).use { it.write(text); it.fd.sync() }
            if (!tmp.renameTo(file)) throw java.io.IOException("rename")
            true
        } catch (_: Exception) {
            tmp.delete()
            false
        }
    }

    // Sign-out, a new session or different API settings.
    fun clear() = synchronized(this) { file.delete(); File(file.parentFile, file.name + ".tmp").delete(); Unit }

    private fun discard(): Nothing? { file.delete(); return null }

    private fun plain(v: Any?): Any? = when (v) {
        is JsonNumber -> v.double
        is List<*> -> v.map { plain(it) }
        is Map<*, *> -> v.entries.associate { it.key to plain(it.value) }
        else -> v
    }

    companion object {
        // The app's one cache: no-backup storage, never in Android backup or device transfer.
        fun forApp(context: android.content.Context) = SitesCache(File(context.noBackupFilesDir, "sites-cache.json"))

        const val VERSION = 1
        const val MAX_BYTES = 256 * 1024
        const val MAX_AGE_MS = 7L * 24 * 3600 * 1000
        private fun digest(key: String) = MessageDigest.getInstance("SHA-256").digest(key.toByteArray(Charsets.UTF_8)).joinToString("") { "%02x".format(it) }
    }
}
