package scouterkit.chewedfeed.handover

import scouterkit.chewedfeed.api.ApiJson
import scouterkit.chewedfeed.dashboard.Navigation
import scouterkit.chewedfeed.dashboard.View

// The navigation handed to the next owner of the glasses (go/examples/chewedfeed snapshot.go; tests/handover
// protocol.schema.json "snapshot", policy.json "snapshot"): the view, page and project, and rotation. Geometry,
// font scale, credentials, addresses, paths, notification contents and API data stay on each device: they are not
// fields here, and the encoder writes these seven fields by hand, so no serializer can add one.
data class UiSnapshot(
    val revision: Long,
    val view: String,
    val pageId: String,
    val project: String?,
    val rotationPaused: Boolean,
    val rotationMs: Long,
) {
    // null when valid, else why not (a fixed, bounded reason).
    fun problem(): String? = when {
        view !in VIEWS.values -> "unknown snapshot view"
        !validPageId(pageId) -> "invalid snapshot page"
        view == "notifications" && pageId != NOTIFICATION_PAGE -> "notification history is local"
        project != null && !PROJECT.matches(project) -> "invalid snapshot project"
        rotationMs < 0 || rotationMs > MAX_ROTATION_MS || rotationMs in 1..999 -> "invalid snapshot rotation"
        revision < 0 || revision > MAX_REVISION -> "invalid snapshot revision"
        else -> null
    }

    // Go's json.Marshal of uiSnapshot, byte for byte: the same fields in the same order. Only valid snapshots.
    fun encode(): String {
        problem()?.let { throw IllegalArgumentException(it) }
        fun str(s: String) = "\"" + s + "\"" // page ids and projects are [A-Za-z0-9_:/-]: nothing to escape
        return "{\"schema_version\":$VERSION,\"revision\":$revision,\"view\":${str(view)},\"page_id\":${str(pageId)}," +
            "\"project\":${project?.let(::str) ?: "null"},\"rotation_paused\":$rotationPaused,\"rotation_ms\":$rotationMs}"
    }

    // Selects the snapshot's view and page on nav: a page that no longer exists falls back to the view's first
    // page; a view this phone cannot show (laptop-only AI and Stats, or nothing in it) to the first Sites page.
    // Returns the rotation (ms) and project to use: settings given explicitly on this phone win; a zero rotation
    // keeps rotation paused.
    fun apply(nav: Navigation, local: Overrides, rotateMs: Long, project: String): Pair<Long, String> {
        val wanted = VIEWS.entries.firstOrNull { it.value == view }?.key ?: View.SITES
        val target = if (usable(nav, wanted)) wanted else View.SITES
        nav.select(target, if (target == wanted) pageId else "")
        nav.paused = rotationPaused
        val rotate = if (local.rotate) rotateMs else rotationMs
        if (rotate == 0L) nav.paused = true
        return rotate to if (local.project) project else (this.project ?: "")
    }

    // Settings given explicitly on this phone that a snapshot does not replace.
    data class Overrides(val rotate: Boolean = false, val project: Boolean = false)

    companion object {
        const val VERSION = 1
        const val MAX_BYTES = 8192
        const val MAX_PAGE_ID = 128
        const val MAX_ROTATION_MS = 3_600_000L
        const val MAX_REVISION = (1L shl 53) - 1
        const val NOTIFICATION_PAGE = "notifications/empty" // notification history never leaves the device
        val VIEWS = mapOf(View.SITES to "sites", View.AI to "ai", View.NOTIFICATIONS to "notifications", View.STATS to "stats")
        private val PAGE = Regex("^[A-Za-z0-9_:/-]+$")
        private val PROJECT = Regex("^[a-z0-9][a-z0-9_-]{0,63}$")
        private val FIELDS = listOf("schema_version", "revision", "view", "page_id", "project", "rotation_paused", "rotation_ms")

        fun validPageId(id: String) = id.codePointCount(0, id.length) <= MAX_PAGE_ID && PAGE.matches(id)

        // Placeholders this phone shows instead of laptop-only data, or for a source switched off.
        private fun usable(nav: Navigation, view: View) =
            view == View.NOTIFICATIONS || nav.pages(view).any { p -> listOf("/laptop", "/off", "/unavailable").none { p.id.endsWith(it) } }

        // What nav shows, for the next owner; revision counts this phone's navigation changes (SnapshotBridge).
        fun export(nav: Navigation, project: String, rotateMs: Long, revision: Long): UiSnapshot {
            val view = nav.selected
            val pages = nav.pages(view)
            val pageId = when {
                view == View.NOTIFICATIONS -> NOTIFICATION_PAGE
                validPageId(nav.page.id) -> nav.page.id
                pages.isNotEmpty() && validPageId(pages[0].id) -> pages[0].id
                else -> "summary"
            }
            var rotation = minOf(rotateMs, MAX_ROTATION_MS)
            if (rotation in 1..999) rotation = 1000
            return UiSnapshot(revision, VIEWS.getValue(view), pageId, project.takeIf { PROJECT.matches(it) }, nav.paused, rotation)
        }

        // Accepts only a complete, valid snapshot with nothing else in it. Throws IllegalArgumentException with a
        // fixed reason otherwise.
        fun decode(text: String): UiSnapshot {
            require(text.length <= MAX_BYTES && text.toByteArray(Charsets.UTF_8).size <= MAX_BYTES) { "snapshot too large" }
            val fields = runCatching { ApiJson(text).parse() }.getOrNull() as? Map<*, *> ?: throw IllegalArgumentException("corrupted snapshot")
            for (name in FIELDS) require(fields.containsKey(name)) { "snapshot without $name" }
            require(fields.keys.all { it in FIELDS }) { "corrupted snapshot" } // geometry, credentials, ... are not fields
            fun long(name: String): Long {
                val d = fields[name] as? Double ?: throw IllegalArgumentException("corrupted snapshot")
                require(d == Math.floor(d) && !d.isInfinite() && kotlin.math.abs(d) <= MAX_REVISION) { "corrupted snapshot" }
                return d.toLong()
            }
            fun string(name: String) = fields[name] as? String ?: throw IllegalArgumentException("corrupted snapshot")
            require(long("schema_version") == VERSION.toLong()) { "snapshot version" }
            val project = fields["project"]
            require(project == null || project is String) { "corrupted snapshot" }
            val s = UiSnapshot(long("revision"), string("view"), string("page_id"), project as String?,
                fields["rotation_paused"] as? Boolean ?: throw IllegalArgumentException("corrupted snapshot"), long("rotation_ms"))
            s.problem()?.let { throw IllegalArgumentException(it) }
            return s
        }
    }
}

// Decides whether an incoming snapshot may change navigation (snapshot.go snapshotGate): only one for the
// transfer this phone requested, newer than any already taken from that sender session, and only if the user
// changed nothing here since the request. Revisions of different sessions are never compared.
class SnapshotGate {
    private var transfer = ""
    var edits = 0L // local navigation edits when the request went out
        private set
    private var session = ""
    private var revision = 0L
    private var taken = false

    fun requested(transfer: String, edits: Long) {
        this.transfer = transfer; this.edits = edits; session = ""; revision = 0; taken = false
    }

    // null when accepted, else why not.
    fun accept(session: String, transfer: String, s: UiSnapshot, edits: Long): String? {
        when {
            this.transfer.isEmpty() || transfer != this.transfer -> return "snapshot for another transfer"
            edits != this.edits -> return "navigation changed here since the request"
            taken && session == this.session && s.revision <= revision -> return "older or repeated snapshot"
        }
        this.session = session; revision = s.revision; taken = true
        return null
    }
}

// Between the coordinator and the UI (snapshot_bridge.go): the UI publishes its navigation (a new revision only
// when it changed) and counts the user's edits; the coordinator stages an incoming snapshot for a requested
// transfer, and only the matching release proof commits it. take() hands it to the UI once, and never after a
// local edit. Thread-safe; notify runs when a committed snapshot is waiting.
class SnapshotBridge(private val notify: () -> Unit = {}) {
    private var current: UiSnapshot? = null
    private var edits = 0L
    private val gate = SnapshotGate()
    private var session = ""
    private var receiving = false
    private var pending: UiSnapshot? = null
    private var committed = false

    @Synchronized fun publish(s: UiSnapshot) {
        val now = current
        val next = if (now != null && s.copy(revision = now.revision) == now) now else s.copy(revision = (now?.revision ?: 0) + 1)
        if (next.problem() == null) current = next
    }

    @Synchronized fun export(): UiSnapshot? = current

    @Synchronized fun edited() {
        edits++
        pending = null
    }

    @Synchronized fun requested(session: String, transfer: String) {
        gate.requested(transfer, edits)
        this.session = session
        receiving = session.isNotEmpty() && transfer.isNotEmpty()
        pending = null
        committed = false
    }

    // body: the message body {"snapshot": {...}} and nothing else. False when it was not taken.
    fun receive(session: String, transfer: String, body: String): Boolean {
        if (body.length > UiSnapshot.MAX_BYTES + 32) return false
        val fields = runCatching { ApiJson(body).parse() }.getOrNull() as? Map<*, *> ?: return false
        if (fields.size != 1 || !fields.containsKey("snapshot")) return false
        val inner = body.substring(body.indexOf(':') + 1, body.lastIndexOf('}')).trim() // the object's one value
        val s = runCatching { UiSnapshot.decode(inner) }.getOrNull() ?: return false
        synchronized(this) {
            if (!receiving || session != this.session || gate.accept(session, transfer, s, edits) != null) return false
            pending = s
            return true
        }
    }

    // The release proof matched (commit) or the transfer ended without it.
    fun finish(commit: Boolean) {
        val wake = synchronized(this) {
            receiving = false
            committed = commit
            if (!commit) {
                pending = null
                session = ""
            }
            commit && pending != null
        }
        if (wake) notify()
    }

    @Synchronized fun take(): UiSnapshot? {
        if (!committed || pending == null || gate.edits != edits) return null
        return pending.also { pending = null }
    }
}
