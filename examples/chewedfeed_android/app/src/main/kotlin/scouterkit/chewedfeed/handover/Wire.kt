package scouterkit.chewedfeed.handover

import java.io.EOFException
import java.io.InputStream
import java.io.OutputStream

// The handover wire protocol, as go/examples/internal/handover wire.go and stream.go (tests/handover: policy.json
// "wire", protocol.schema.json, messages.json, sequences.json): a u32 big-endian length, then that many bytes of
// strict UTF-8 JSON, one message per frame; every message checked against the schema and the rules between fields;
// per connection, a Stream that only lets through the enrolled peer's messages in order.
object Wire {
    const val MAX_FRAME_BYTES = 65536
    const val MAX_SNAPSHOT_BYTES = 8192
    const val MAX_TELEMETRY_BYTES = 32768
    const val PROTOCOL_VERSION = 1

    const val CAP_TWO_ARM = "two_arm_v1"
    const val CAP_RELEASE_FIRST = "release_before_acquire_v1"
    const val CAP_SNAPSHOT = "snapshot_v1"
    const val CAP_TELEMETRY = "telemetry_v1"
    val REQUIRED = listOf(CAP_TWO_ARM, CAP_RELEASE_FIRST)
    val OPTIONAL = listOf(CAP_SNAPSHOT, CAP_TELEMETRY)

    // A copy of tests/handover/protocol.schema.json (a test keeps them equal).
    private val schema: Map<String, Any?> by lazy {
        val bytes = Wire::class.java.getResourceAsStream("/handover/protocol.schema.json")!!.use { it.readBytes() }
        @Suppress("UNCHECKED_CAST")
        (StrictJson.parse(bytes, 1 shl 20) as Map<String, Any?>).also { Schema.check(it, it) }
    }

    // A frame's payload: strict JSON, the schema, the rules between fields.
    fun decode(payload: ByteArray): Message {
        val v = StrictJson.parse(payload, MAX_FRAME_BYTES)
        Schema.validate(v, schema, schema)
        @Suppress("UNCHECKED_CAST")
        val m = Message.from(v as Map<String, Any?>)
        payloadChecks(m)
        return m
    }

    fun encode(m: Message): ByteArray = StrictJson.write(m.toJson()).toByteArray(Charsets.UTF_8)

    // Writes one message after checking it as the receiver would: a local bug fails here, not on the peer.
    fun writeFrame(out: OutputStream, m: Message) {
        val payload = encode(m)
        decode(payload)
        val frame = ByteArray(4 + payload.size)
        frame[0] = (payload.size ushr 24).toByte(); frame[1] = (payload.size ushr 16).toByte()
        frame[2] = (payload.size ushr 8).toByte(); frame[3] = payload.size.toByte()
        payload.copyInto(frame, 4)
        out.write(frame)
        out.flush()
    }

    // Reads one frame; the length is checked before the payload is read. EOFException for a cut stream.
    fun readFrame(input: InputStream): Message = decode(readPayload(input, MAX_FRAME_BYTES))

    internal fun readPayload(input: InputStream, max: Int): ByteArray {
        val header = readFully(input, 4)
        val n = ((header[0].toInt() and 0xFF) shl 24) or ((header[1].toInt() and 0xFF) shl 16) or
            ((header[2].toInt() and 0xFF) shl 8) or (header[3].toInt() and 0xFF)
        if (n == 0 || n < 0 || n > max) invalid("frame length")
        return readFully(input, n)
    }

    private fun readFully(input: InputStream, n: Int): ByteArray {
        val b = ByteArray(n)
        var got = 0
        while (got < n) {
            val r = input.read(b, got, n - got)
            if (r < 0) throw EOFException("unexpected end of stream")
            got += r
        }
        return b
    }

    private val PAGE_ID = Regex("^[A-Za-z0-9_:/-]+$")

    private fun Map<*, *>.obj(k: String) = this[k] as? Map<*, *>
    private fun Map<*, *>.bool(k: String) = this[k] as? Boolean ?: false
    private fun Map<*, *>.num(k: String) = (this[k] as? JsonNumber)?.double ?: 0.0
    private fun Map<*, *>.str(k: String) = this[k] as? String ?: ""
    private fun Map<*, *>.list(k: String) = (this[k] as? List<*>)?.map { it as String } ?: emptyList()
    private fun arms(b: Map<*, *>?) = (b?.get("arms") as? Map<*, *>)?.let { it.bool("left") to it.bool("right") } ?: (false to false)

    // Rules between fields (wire.go payloadChecks, contract.py payload_checks).
    fun payloadChecks(m: Message) {
        val b = m.body
        when (m.kind) {
            "hello" -> {
                val req = b.list("required_capabilities")
                val opt = b.list("optional_capabilities")
                for (c in REQUIRED) if (c !in req) invalid("missing required capability $c")
                val seen = HashSet<String>()
                for (c in req) {
                    if (c !in REQUIRED + OPTIONAL) invalid("unsupported required capability")
                    if (!seen.add(c)) invalid("duplicate required capability")
                }
                val o = HashSet<String>()
                for (c in opt) {
                    if (!o.add(c)) invalid("duplicate optional capability")
                    if (c in seen) invalid("ambiguous capabilities")
                }
            }
            "ready", "released" -> {
                val want = m.kind == "ready"
                val (l, r) = arms(b)
                if (l != want || r != want) invalid("wrong arm readiness")
            }
            "status" -> {
                val ready = b.num("ready_epoch")
                val (l, r) = arms(b)
                val phase = b.str("phase")
                if (ready > m.epoch.toDouble()) invalid("future ready epoch")
                if (phase == "active" && (!(l && r) || !b.bool("renderer_ready") || ready != m.epoch.toDouble() || b["transfer"] == null)) invalid("active before ready")
                if (phase in setOf("idle", "requesting", "recovering") && (l || r || b.bool("renderer_ready"))) invalid("inactive retains arms")
                if (b.str("role") == "laptop" && b.bool("pinned")) invalid("laptop pin unsupported")
            }
            "snapshot" -> {
                val s = b.obj("snapshot") ?: invalid("snapshot")
                val rotation = s.num("rotation_ms")
                when {
                    StrictJson.write(s).toByteArray(Charsets.UTF_8).size > MAX_SNAPSHOT_BYTES -> invalid("snapshot too large")
                    rotation != 0.0 && rotation < 1000 -> invalid("rotation too fast")
                    !PAGE_ID.containsMatchIn(s.str("page_id")) -> invalid("invalid page ID")
                    s.str("view") == "notifications" && s.str("page_id") != "notifications/empty" -> invalid("notification history is local")
                }
            }
            "telemetry" -> {
                val t = b.obj("telemetry") ?: invalid("telemetry")
                val values = t["values"]
                val status = t.str("status")
                when {
                    StrictJson.write(t).toByteArray(Charsets.UTF_8).size > MAX_TELEMETRY_BYTES -> invalid("telemetry too large")
                    status == "unavailable" && values != null -> invalid("unavailable must not invent values")
                    status != "unavailable" && (values == null || t["sample_unix_ms"] == null) -> invalid("sample missing")
                }
                if (values != null) {
                    if ((values is List<*>) != (t.str("source") == "ai")) invalid("source/value mismatch")
                    if (t.str("source") == "system" && values is Map<*, *> && values.num("memory_used_bytes") > values.num("memory_total_bytes")) invalid("memory exceeds total")
                }
            }
        }
    }

}

// A protocol envelope (wire.go Message). peerSession and transfer are null for JSON null; body is the parsed
// object (numbers as JsonNumber), checked by Wire.decode.
data class Message(
    val version: Int,
    val sender: String,
    val recipient: String,
    val session: String,
    val peerSession: String?,
    val seq: Long,
    val epoch: Long,
    val transfer: String?,
    val kind: String,
    val body: Map<String, Any?>,
) {
    fun toJson(): Map<String, Any?> = linkedMapOf(
        "version" to version, "sender" to sender, "recipient" to recipient, "session" to session,
        "peer_session" to peerSession, "seq" to seq, "epoch" to epoch, "transfer" to transfer, "kind" to kind, "body" to body,
    )

    companion object {
        // After the schema: every field is present with its type.
        @Suppress("UNCHECKED_CAST")
        fun from(v: Map<String, Any?>) = Message(
            (v["version"] as JsonNumber).long.toInt(), v["sender"] as String, v["recipient"] as String, v["session"] as String,
            v["peer_session"] as String?, (v["seq"] as JsonNumber).long, (v["epoch"] as JsonNumber).long, v["transfer"] as String?,
            v["kind"] as String, v["body"] as Map<String, Any?>,
        )
    }
}

// The schema subset the protocol uses (wire.go checkSchema/validate, contract.py check_schema/validate). Integers
// must be written without a fraction or exponent.
internal object Schema {
    private val KEYWORDS = setOf("\$schema", "\$defs", "\$ref", "title", "description", "type", "enum", "const", "oneOf", "properties",
        "required", "additionalProperties", "items", "maxItems", "minimum", "maximum", "minLength", "maxLength", "pattern")
    private val patterns = HashMap<String, Regex>()

    @Suppress("UNCHECKED_CAST")
    private fun ref(root: Map<String, Any?>, r: String): Map<String, Any?> {
        if (!r.startsWith("#/")) invalid("only local schema references")
        var node: Any? = root
        for (part in r.substring(2).split("/")) node = (node as? Map<*, *>)?.get(part.replace("~1", "/").replace("~0", "~"))
        return node as? Map<String, Any?> ?: invalid("bad reference")
    }

    @Suppress("UNCHECKED_CAST")
    fun check(s: Map<String, Any?>, root: Map<String, Any?>) {
        for (k in s.keys) require(k in KEYWORDS) { "unsupported schema keyword $k" }
        (s["\$ref"] as? String)?.let { ref(root, it) }
        (s["pattern"] as? String)?.let { patterns[it] = Regex(it) }
        for (kw in listOf("\$defs", "properties")) (s[kw] as? Map<*, *>)?.values?.forEach { check(it as Map<String, Any?>, root) }
        (s["oneOf"] as? List<*>)?.forEach { check(it as Map<String, Any?>, root) }
        (s["items"] as? Map<*, *>)?.let { check(it as Map<String, Any?>, root) }
    }

    private fun num(v: Any?): Double? = when (v) {
        is JsonNumber -> v.double
        is Number -> v.toDouble()
        else -> null
    }

    private fun equal(a: Any?, b: Any?): Boolean {
        val x = num(a)
        val y = num(b)
        if (x != null || y != null) return x != null && y != null && x == y
        return when (b) {
            null, is String, is Boolean -> a == b
            else -> false
        }
    }

    private fun hasType(v: Any?, t: String) = when (t) {
        "object" -> v is Map<*, *>
        "array" -> v is List<*>
        "string" -> v is String
        "boolean" -> v is Boolean
        "null" -> v == null
        "integer" -> v is JsonNumber && v.isInteger
        "number" -> v is JsonNumber
        else -> false
    }

    @Suppress("UNCHECKED_CAST")
    fun validate(v: Any?, s: Map<String, Any?>, root: Map<String, Any?>) {
        (s["\$ref"] as? String)?.let { validate(v, ref(root, it), root) }
        (s["oneOf"] as? List<*>)?.let { list ->
            var matches = 0
            for (c in list) if (runCatching { validate(v, c as Map<String, Any?>, root) }.isSuccess) matches++
            if (matches != 1) invalid(if (matches == 0) "no schema matches" else "oneOf must match exactly once")
        }
        if (s.containsKey("const") && !equal(v, s["const"])) invalid("constant mismatch")
        (s["enum"] as? List<*>)?.let { e -> if (e.none { equal(v, it) }) invalid("unknown enum value") }
        (s["type"] as? String)?.let { if (!hasType(v, it)) invalid("wrong JSON type") }
        when (v) {
            is Map<*, *> -> {
                val props = s["properties"] as? Map<*, *> ?: emptyMap<String, Any?>()
                (s["required"] as? List<*>)?.forEach { if (!v.containsKey(it)) invalid("missing field") }
                for ((name, item) in v) {
                    val p = props[name]
                    if (p == null) {
                        if (s["additionalProperties"] == false) invalid("unknown field")
                        continue
                    }
                    validate(item, p as Map<String, Any?>, root)
                }
            }
            is List<*> -> {
                num(s["maxItems"])?.let { if (v.size > it) invalid("too many items") }
                (s["items"] as? Map<*, *>)?.let { items -> v.forEach { validate(it, items as Map<String, Any?>, root) } }
            }
            is String -> {
                val n = v.codePointCount(0, v.length).toDouble()
                num(s["minLength"])?.let { if (n < it) invalid("short string") }
                num(s["maxLength"])?.let { if (n > it) invalid("long string") }
                (s["pattern"] as? String)?.let { p -> if (!(patterns[p] ?: Regex(p)).containsMatchIn(v)) invalid("pattern mismatch") }
            }
            is JsonNumber -> {
                num(s["minimum"])?.let { if (v.double < it) invalid("number too small") }
                num(s["maximum"])?.let { if (v.double > it) invalid("number too large") }
            }
        }
    }
}

// The checks for one authenticated peer connection (stream.go Stream, contract.py PeerStream), after TLS proved
// the peer's key and after Wire.decode. Watermarks move only when a message passes every check.
class Stream(
    private val sender: String,         // the enrolled peer's ID
    private val recipient: String,      // ours
    private val localSession: String,   // ours for this connection
    retired: Collection<String> = emptyList(),
    private var pending: String = "",   // a transfer the peer releases for, or that we requested
    private val local: List<String> = Wire.REQUIRED + Wire.OPTIONAL,
) {
    private val retired = retired.toHashSet()
    private var releaseEpoch = 0L
    private var hasRelease = false
    var session = ""
        private set
    var role = ""
        private set
    private var lastSeq = 0L
    private var lastEpoch = 0L
    private var active = false
    private var capabilities = emptyList<String>()

    fun negotiated(capability: String) = capability in capabilities

    // A transfer this host requested: the peer's release and released for it are accepted.
    fun expect(transfer: String) {
        pending = transfer
    }

    fun receive(m: Message) {
        Wire.payloadChecks(m)
        if (m.sender != sender || m.recipient != recipient) invalid("wrong enrolled peer")
        if (m.sender == m.recipient) invalid("self message")
        if (m.session in retired) invalid("retired session")
        if (m.seq <= lastSeq || m.epoch < lastEpoch) invalid("replay or stale epoch")
        val peerSession = m.peerSession ?: ""
        val transfer = m.transfer ?: ""
        val b = m.body
        if (session.isEmpty()) {
            if (m.kind != "hello") invalid("hello required first")
            if (peerSession.isNotEmpty() && peerSession != localSession) invalid("wrong recipient session")
            for (c in (b["required_capabilities"] as List<*>)) if (c !in local) invalid("locally unsupported required capability")
        } else {
            if (m.kind == "hello" || m.session != session) invalid("session changed without handshake")
            if (peerSession != localSession) invalid("wrong recipient session")
        }
        val target = b["target"] as? String ?: ""
        when (m.kind) {
            "status" -> if ((b["role"] as? String ?: "") != role) invalid("role changed")
            "request", "acquiring", "ready" -> {
                if (target != role) invalid("wrong incoming target")
                if (m.kind == "request" && b["pinned"] == true && (role != "phone" || b["reason"] != "manual")) invalid("invalid pinned request")
            }
            "release", "released" -> {
                if (target == role) invalid("wrong release target")
                if (pending.isNotEmpty() && pending != transfer) invalid("wrong transfer")
                if (m.kind == "release" && pending.isEmpty() && !active) invalid("unsolicited release without known owner")
                if (m.kind == "released" && (pending != transfer || !hasRelease || releaseEpoch != m.epoch)) invalid("released without matching owner release")
            }
            "snapshot" -> if (Wire.CAP_SNAPSHOT !in capabilities || pending != transfer) invalid("unnegotiated or stale snapshot")
            "telemetry" -> if (Wire.CAP_TELEMETRY !in capabilities) invalid("unnegotiated telemetry")
        }
        // Every check passed: commit.
        session = m.session; lastSeq = m.seq; lastEpoch = m.epoch
        when (m.kind) {
            "hello" -> {
                role = b["role"] as String
                capabilities = ((b["required_capabilities"] as List<*>) + (b["optional_capabilities"] as List<*>)).map { it as String }.filter { it in local }
            }
            "status" -> active = b["phase"] == "active"
            "ready" -> active = true
            "release" -> { pending = transfer; releaseEpoch = m.epoch; hasRelease = true }
            "released" -> { pending = ""; releaseEpoch = 0; hasRelease = false; active = false }
        }
    }
}

// The sending side of one connection (stream.go Outbox): seq strictly increasing from 1, epoch never decreasing.
class Outbox(private val sender: String, private val recipient: String, private val session: String) {
    @Volatile var peerSession = ""
    private var seq = 0L
    private var epoch = 0L

    @Synchronized fun next(kind: String, epoch: Long, transfer: String, body: Map<String, Any?>): Message {
        seq++
        this.epoch = maxOf(this.epoch, epoch)
        return Message(Wire.PROTOCOL_VERSION, sender, recipient, session, peerSession.ifEmpty { null }, seq, this.epoch,
            transfer.ifEmpty { null }, kind, body)
    }
}
