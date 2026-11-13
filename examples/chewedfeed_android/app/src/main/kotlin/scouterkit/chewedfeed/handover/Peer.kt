package scouterkit.chewedfeed.handover

import java.io.IOException
import java.net.InetSocketAddress
import java.net.Socket
import java.security.MessageDigest
import java.security.PrivateKey
import java.security.SecureRandom
import java.security.cert.CertificateException
import java.security.cert.X509Certificate
import java.util.Base64
import java.util.concurrent.ArrayBlockingQueue
import java.util.concurrent.Executors
import java.util.concurrent.ScheduledExecutorService
import java.util.concurrent.TimeUnit
import javax.crypto.Mac
import javax.crypto.spec.SecretKeySpec
import javax.net.ssl.SSLContext
import javax.net.ssl.SSLEngine
import javax.net.ssl.SSLSocket
import javax.net.ssl.X509ExtendedKeyManager
import javax.net.ssl.X509ExtendedTrustManager

// The authenticated peer link between this phone and an enrolled laptop (go/examples/internal/handover identity.go,
// enroll.go, transport.go; tests/handover/policy.json "transport", "enrollment"): TLS 1.3, a certificate on each
// side, no CA. A peer is its key: the ID is the lower-hex SHA-256 of the certificate's SubjectPublicKeyInfo, and
// each side pins the other's. The phone is the host: it shows an invitation, listens (PeerServer) and enrolls the
// laptop, which then dials it (desktop -enroll-invitation, -peer-endpoint). The guest side (enroll, dial,
// PeerLink) is here too, for a relay route and for tests against the Go host.

private fun hex(b: ByteArray) = b.joinToString("") { "%02x".format(it) }
private fun unhex(s: String) = ByteArray(s.length / 2) { s.substring(2 * it, 2 * it + 2).toInt(16).toByte() }
private val HEX_ID = Regex("^[0-9a-f]{64}$")

fun peerId(cert: X509Certificate): String = hex(MessageDigest.getInstance("SHA-256").digest(cert.publicKey.encoded))

// This device's key and its self-signed certificate (names and dates are not checked: the key is what peers pin).
class Identity(val key: PrivateKey, val cert: X509Certificate) {
    val id = peerId(cert)
}

// An enrolled peer: its pinned ID and role ("laptop", "phone"), nothing else.
data class Peer(val id: String, val role: String, val enrolledUnixMs: Long)

// This device's identity and enrolled peers. Revoking a peer refuses its key from the next message on; rotating
// replaces this device's key, and every peer must enroll again. save persists the peers (privately, not in
// backups); newIdentity makes and stores a fresh key.
class PeerStore(
    private var current: Identity,
    peers: List<Peer>,
    private val save: (List<Peer>) -> Unit,
    private val newIdentity: () -> Identity,
) {
    private var list = peers.toList()

    @Synchronized fun identity() = current
    @Synchronized fun peers() = list
    @Synchronized fun enrolled(id: String): Peer? = list.firstOrNull { it.id == id }

    @Synchronized fun addPeer(p: Peer) {
        require(p.id != current.id) { "cannot enroll this device's own key" }
        list = list.filter { it.id != p.id } + p
        save(list)
    }

    @Synchronized fun revoke(id: String) {
        require(list.any { it.id == id }) { "no such enrolled peer" }
        list = list.filter { it.id != id }
        save(list)
    }

    @Synchronized fun rotate() {
        current = newIdentity()
        list = emptyList()
        save(list)
    }

    // The peers file's text ({"peers":[{"id","role","enrolled_unix_ms"}]}), and back.
    companion object {
        fun encodePeers(peers: List<Peer>) = StrictJson.write(mapOf("peers" to peers.map {
            linkedMapOf("id" to it.id, "role" to it.role, "enrolled_unix_ms" to it.enrolledUnixMs)
        }))

        fun decodePeers(text: String): List<Peer> {
            val v = StrictJson.parse(text.toByteArray(Charsets.UTF_8), 1 shl 20) as? Map<*, *> ?: return emptyList()
            return (v["peers"] as? List<*>).orEmpty().mapNotNull { p ->
                p as? Map<*, *> ?: return@mapNotNull null
                val id = p["id"] as? String ?: return@mapNotNull null
                val role = p["role"] as? String ?: return@mapNotNull null
                if (!HEX_ID.matches(id) || role !in setOf("laptop", "phone")) null
                else Peer(id, role, (p["enrolled_unix_ms"] as? JsonNumber)?.long ?: 0)
            }
        }
    }
}

// TLS 1.3 with this identity, accepting exactly one peer certificate that verify approves. A custom trust manager
// on purpose: peers have no CA; trust is the pinned key (verify), as on the desktop (identity.go VerifyConnection).
// The listening side's verify accepts any key: accept() then admits only enrolled keys, or enrolls one.
@android.annotation.SuppressLint("CustomX509TrustManager")
internal fun tlsContext(id: Identity, verify: (X509Certificate) -> Unit): SSLContext {
    val alias = "peer"
    val keys = object : X509ExtendedKeyManager() {
        override fun getClientAliases(keyType: String?, issuers: Array<out java.security.Principal>?) = arrayOf(alias)
        override fun chooseClientAlias(keyType: Array<out String>?, issuers: Array<out java.security.Principal>?, socket: Socket?) = alias
        override fun getServerAliases(keyType: String?, issuers: Array<out java.security.Principal>?) = arrayOf(alias)
        override fun chooseServerAlias(keyType: String?, issuers: Array<out java.security.Principal>?, socket: Socket?) = alias
        override fun chooseEngineClientAlias(keyType: Array<out String>?, issuers: Array<out java.security.Principal>?, engine: SSLEngine?) = alias
        override fun chooseEngineServerAlias(keyType: String?, issuers: Array<out java.security.Principal>?, engine: SSLEngine?) = alias
        override fun getCertificateChain(a: String?) = arrayOf(id.cert)
        override fun getPrivateKey(a: String?) = id.key
    }
    val trust = object : X509ExtendedTrustManager() {
        private fun check(chain: Array<out X509Certificate>?) {
            if (chain == null || chain.size != 1) throw CertificateException("exactly one peer certificate expected")
            verify(chain[0])
        }
        override fun checkClientTrusted(chain: Array<out X509Certificate>?, authType: String?) = check(chain)
        override fun checkServerTrusted(chain: Array<out X509Certificate>?, authType: String?) = check(chain)
        override fun checkClientTrusted(chain: Array<out X509Certificate>?, authType: String?, socket: Socket?) = check(chain)
        override fun checkServerTrusted(chain: Array<out X509Certificate>?, authType: String?, socket: Socket?) = check(chain)
        override fun checkClientTrusted(chain: Array<out X509Certificate>?, authType: String?, engine: SSLEngine?) = check(chain)
        override fun checkServerTrusted(chain: Array<out X509Certificate>?, authType: String?, engine: SSLEngine?) = check(chain)
        override fun getAcceptedIssuers() = arrayOf<X509Certificate>()
    }
    return SSLContext.getInstance("TLSv1.3").apply { init(arrayOf(keys), arrayOf(trust), SecureRandom()) }
}

// Only the peer whose key has this ID.
private fun pinned(peerId: String): (X509Certificate) -> Unit = { cert ->
    if (peerId(cert) != peerId) throw CertificateException("peer key does not match the enrolled ID")
}

// A TLS client over raw, pinned to peerId, handshaken (10 s).
internal fun clientTls(raw: Socket, id: Identity, peerId: String): SSLSocket {
    val s = tlsContext(id, pinned(peerId)).socketFactory.createSocket(raw, raw.inetAddress?.hostAddress, raw.port, true) as SSLSocket
    s.enabledProtocols = arrayOf("TLSv1.3")
    s.useClientMode = true
    handshake(s)
    return s
}

private fun handshake(s: SSLSocket) {
    s.soTimeout = 10_000
    try {
        s.startHandshake()
    } catch (e: IOException) {
        runCatching { s.close() }
        throw IOException("TLS: ${e.message}", e)
    }
}

// How to reach a peer: a direct endpoint, or a relay (CF-54). What it returns is untrusted; pinned TLS runs over it.
fun interface Route {
    fun dial(): Socket
}

// host:port over TCP (10 s to connect).
class DirectRoute(private val host: String, private val port: Int) : Route {
    override fun dial(): Socket {
        val s = Socket() // not apply {}: inside it, `port` would be the unconnected socket's own (0)
        s.connect(InetSocketAddress(host, port), 10_000)
        return s
    }

    companion object {
        // "host:port" or "[v6]:port", as invitation endpoints are written; null when it is not one.
        fun parse(endpoint: String): DirectRoute? {
            val i = endpoint.lastIndexOf(':')
            if (i <= 0) return null
            val port = endpoint.substring(i + 1).toIntOrNull()?.takeIf { it in 1..65535 } ?: return null
            return DirectRoute(endpoint.substring(0, i).removePrefix("[").removeSuffix("]"), port)
        }
    }
}

// The out-of-band enrollment payload (enroll.go Invitation): never log it; toString hides the secret.
class Invitation(val version: Int, val hostId: String, val hostRole: String, val secret: String, val expiresUnixMs: Long, val endpoints: List<String>) {
    override fun toString() = "invitation from $hostRole ${hostId.take(8)}…"

    // The text for a QR code or for pasting: Go's json.Marshal field order, unpadded base64url.
    fun encode(): String {
        val m = linkedMapOf<String, Any?>("v" to version, "id" to hostId, "role" to hostRole, "secret" to secret, "exp" to expiresUnixMs)
        if (endpoints.isNotEmpty()) m["ep"] = endpoints
        return PREFIX + Base64.getUrlEncoder().withoutPadding().encodeToString(StrictJson.write(m).toByteArray(Charsets.UTF_8))
    }

    companion object {
        const val PREFIX = "CFE1."
        private val FIELDS = setOf("v", "id", "role", "secret", "exp", "ep")

        // Checks the invitation, including its expiry. IllegalArgumentException with a fixed reason otherwise.
        fun parse(text: String, nowMs: Long): Invitation {
            val raw = text.trim()
            require(raw.startsWith(PREFIX) && raw.length <= PREFIX.length + 2048) { "not a Chewedfeed invitation" }
            val b = try {
                Base64.getUrlDecoder().decode(raw.substring(PREFIX.length).also { require('=' !in it) { "damaged invitation" } })
            } catch (e: IllegalArgumentException) {
                throw IllegalArgumentException("damaged invitation")
            }
            val v = runCatching { StrictJson.parse(b, 4096) }.getOrNull() as? Map<*, *> ?: throw IllegalArgumentException("damaged invitation")
            require(v.keys.all { it in FIELDS }) { "damaged invitation" }
            val version = (v["v"] as? JsonNumber)?.takeIf { it.isInteger }?.long ?: throw IllegalArgumentException("damaged invitation")
            require(version == 1L) { "unsupported invitation version" }
            val id = v["id"] as? String ?: ""
            val secret = v["secret"] as? String ?: ""
            val role = v["role"] as? String ?: ""
            val exp = (v["exp"] as? JsonNumber)?.takeIf { it.isInteger }?.long ?: throw IllegalArgumentException("damaged invitation")
            val ep = (v["ep"] as? List<*>)?.map { it as? String ?: throw IllegalArgumentException("damaged invitation") } ?: emptyList()
            require(v["ep"] == null || v["ep"] is List<*>) { "damaged invitation" }
            require(HEX_ID.matches(id) && HEX_ID.matches(secret) && role in setOf("laptop", "phone") && ep.size <= 8) { "damaged invitation" }
            require(nowMs < exp) { "invitation expired" }
            return Invitation(1, id, role, secret, exp, ep)
        }

        private fun mac(secret: ByteArray, label: String, vararg parts: String): ByteArray {
            val m = Mac.getInstance("HmacSHA256").apply { init(SecretKeySpec(secret, "HmacSHA256")) }
            m.update("chewedfeed-enroll-v1/$label".toByteArray())
            for (p in parts) m.update("\n$p".toByteArray())
            return m.doFinal()
        }

        fun proof(secret: ByteArray, hostId: String, guestId: String, guestRole: String) = mac(secret, "request", hostId, guestId, guestRole)
        fun confirm(secret: ByteArray, hostId: String, guestId: String, guestRole: String) = mac(secret, "confirm", hostId, guestId, guestRole)

        // Six digits both sides can show after enrolling.
        fun code(secret: ByteArray, hostId: String, guestId: String): String {
            val m = mac(secret, "code", hostId, guestId)
            val n = ((m[0].toLong() and 0xFF) shl 24) or ((m[1].toLong() and 0xFF) shl 16) or ((m[2].toLong() and 0xFF) shl 8) or (m[3].toLong() and 0xFF)
            return "%06d".format(n % 1_000_000)
        }
    }
}

// The guest's side of enrollment (enroll.go Enroll): connect to the invitation's host through route, TLS pinned to
// its key; prove the invitation with an HMAC bound to both keys; check the host's answer; record the host. Returns
// the host and the confirmation code to show. Throws on any mismatch; nothing is recorded then.
fun enroll(store: PeerStore, inv: Invitation, guestRole: String, route: Route, nowMs: Long = System.currentTimeMillis()): Pair<Peer, String> {
    Invitation.parse(inv.encode(), nowMs)
    val secret = unhex(inv.secret)
    val id = store.identity()
    clientTls(route.dial(), id, inv.hostId).use { tls ->
        tls.soTimeout = 15_000
        val proof = Invitation.proof(secret, inv.hostId, id.id, guestRole)
        writeSmall(tls, linkedMapOf("enroll" to 1, "role" to guestRole, "proof" to hex(proof)))
        val resp = runCatching { readSmall(tls) }.getOrElse { throw IOException("the host refused the invitation (used, expired or withdrawn)") }
        val got = (resp["confirm"] as? String)?.takeIf { HEX_ID.matches(it) }?.let(::unhex)
        if (resp.keys != setOf("enrolled", "confirm") || (resp["enrolled"] as? JsonNumber)?.text != "1" || got == null ||
            !MessageDigest.isEqual(got, Invitation.confirm(secret, inv.hostId, id.id, guestRole))) {
            throw IOException("the host's confirmation does not match the invitation")
        }
    }
    val host = Peer(inv.hostId, inv.hostRole, nowMs)
    store.addPeer(host)
    return host to Invitation.code(secret, inv.hostId, id.id)
}

private const val MAX_ENROLL_BYTES = 1024

private fun writeSmall(s: SSLSocket, v: Map<String, Any?>) {
    val b = StrictJson.write(v).toByteArray(Charsets.UTF_8)
    s.outputStream.write(byteArrayOf((b.size ushr 24).toByte(), (b.size ushr 16).toByte(), (b.size ushr 8).toByte(), b.size.toByte()) + b)
    s.outputStream.flush()
}

private fun readSmall(s: SSLSocket): Map<*, *> =
    StrictJson.parse(Wire.readPayload(s.inputStream, MAX_ENROLL_BYTES), MAX_ENROLL_BYTES) as? Map<*, *> ?: invalid("enrollment message")

// The host's side of enrollment (enroll.go Enroller): invitations that work once and for 300 s; a guest that
// proves one is enrolled. Ten wrong proofs withdraw every open invitation.
class Enroller(private val store: PeerStore, private val now: () -> Long = System::currentTimeMillis) {
    private class Open(val secret: ByteArray, val expiresMs: Long)
    private val open = mutableListOf<Open>()
    private var failures = 0

    // A new invitation from this phone ("phone"), with the endpoints (host:port) the guest should dial.
    @Synchronized fun invite(role: String, endpoints: List<String>): Invitation {
        val secret = ByteArray(32).also { SecureRandom().nextBytes(it) }
        val exp = now() + TTL_MS
        open += Open(secret, exp)
        return Invitation(1, store.identity().id, role, hex(secret), exp, endpoints)
    }

    @Synchronized fun pending(): Boolean {
        expire()
        return open.isNotEmpty()
    }

    // Withdraws every open invitation (the user cancelled pairing).
    @Synchronized fun withdraw() = open.clear()

    private fun expire() {
        val t = now()
        open.removeAll { t >= it.expiresMs }
    }

    // Checks a guest's proof against the open invitations; the match is used up, the guest enrolled, and the
    // host's confirmation and the six-digit code returned. Throws when nothing matches.
    @Synchronized internal fun admit(guestId: String, req: Map<*, *>): Pair<ByteArray, String> {
        val role = req["role"] as? String
        val proof = (req["proof"] as? String)?.takeIf { HEX_ID.matches(it) }?.let(::unhex)
        if (req.keys != setOf("enroll", "role", "proof") || (req["enroll"] as? JsonNumber)?.text != "1" ||
            role !in setOf("laptop", "phone") || proof == null) invalid("enrollment request")
        val hostId = store.identity().id
        expire()
        val i = open.indexOfFirst { MessageDigest.isEqual(proof, Invitation.proof(it.secret, hostId, guestId, role!!)) }
        if (i < 0) {
            if (++failures >= MAX_FAILURES) { open.clear(); failures = 0 } // someone is guessing
            throw IOException("no valid invitation for this proof")
        }
        val inv = open.removeAt(i) // single use
        store.addPeer(Peer(guestId, role!!, now()))
        return Invitation.confirm(inv.secret, hostId, guestId, role) to Invitation.code(inv.secret, hostId, guestId)
    }

    // One request, one answer, on a TLS connection from an unknown key. Returns the guest and the code.
    internal fun serve(tls: SSLSocket): Pair<Peer, String> {
        tls.soTimeout = 10_000
        val guestId = peerId(tls.session.peerCertificates[0] as X509Certificate)
        val (confirm, code) = admit(guestId, readSmall(tls))
        writeSmall(tls, linkedMapOf("enrolled" to 1, "confirm" to hex(confirm)))
        return store.enrolled(guestId)!! to code
    }

    companion object {
        const val TTL_MS = 300_000L
        const val MAX_FAILURES = 10
    }
}

class Backpressure : IOException("peer send queue full")
class PeerClosed : IOException("peer connection closed")
class NotEnrolled : IOException("peer key is not enrolled")
// accept: the connection was an enrollment (the guest is now enrolled); no protocol connection.
class Enrolled(val peer: Peer, val code: String) : IOException("connection used for enrollment")

// One side of a connection (transport.go Options): role "phone" or "laptop"; the optional capabilities offered;
// what heartbeats carry ({"phase","ready_epoch"}); the peer's sessions from earlier connections, refused if reused.
data class PeerOptions(
    val role: String,
    val optional: List<String> = Wire.OPTIONAL,
    val heartbeat: () -> Map<String, Any?> = { linkedMapOf("phase" to "idle", "ready_epoch" to 0) },
    val retired: List<String> = emptyList(),
)

// One authenticated, handshaken peer connection (transport.go Conn): both hellos first, a fresh session nonce,
// then checked messages to onMessage (on the reader thread) until it ends (onClosed, once). send never blocks: a
// full queue is Backpressure. Revoking the peer ends the connection at its next message.
class PeerConn private constructor(
    private val tls: SSLSocket,
    private val store: PeerStore,
    val peerId: String,
    private val opts: PeerOptions,
) {
    val session = newNonce()
    var peerSession = ""
        private set
    var peerRole = ""
        private set
    private val ourId = store.identity().id
    private val stream = Stream(peerId, ourId, session, opts.retired, "", Wire.REQUIRED + opts.optional)
    private val out = Outbox(ourId, peerId, session)
    private val queue = ArrayBlockingQueue<Message>(SEND_QUEUE)
    private val lock = Any()
    @Volatile var closed: Throwable? = null
        private set
    private var onMessage: (Message) -> Unit = {}
    private var onClosed: (Throwable) -> Unit = {}
    private val workers = mutableListOf<Thread>()
    private var beats: ScheduledExecutorService? = null

    private fun hello() {
        tls.soTimeout = 10_000
        val hello = out.next("hello", 0, "", linkedMapOf("role" to opts.role, "required_capabilities" to Wire.REQUIRED, "optional_capabilities" to opts.optional))
        Wire.writeFrame(tls.outputStream, hello)
        val m = Wire.readFrame(tls.inputStream)
        stream.receive(m)
        if (stream.role == opts.role) invalid("peer claims this host's role")
        peerRole = stream.role
        peerSession = m.session
        out.peerSession = m.session
    }

    // Starts the reader, writer and heartbeat; messages go to onMessage, the end to onClosed.
    fun start(onMessage: (Message) -> Unit, onClosed: (Throwable) -> Unit): PeerConn {
        this.onMessage = onMessage
        this.onClosed = onClosed
        tls.soTimeout = Policy.PEER_SUSPECT_MS.toInt() // no frame for this long: the connection is dead
        workers += Thread(::reader, "peer-read").apply { isDaemon = true; start() }
        workers += Thread(::writer, "peer-write").apply { isDaemon = true; start() }
        beats = Executors.newSingleThreadScheduledExecutor { r -> Thread(r, "peer-beat").apply { isDaemon = true } }.also {
            it.scheduleWithFixedDelay({
                val b = opts.heartbeat()
                runCatching { send("heartbeat", (b["ready_epoch"] as? Number)?.toLong() ?: 0, "", b) }
            }, HEARTBEAT_MS, HEARTBEAT_MS, TimeUnit.MILLISECONDS)
        }
        return this
    }

    fun negotiated(capability: String) = stream.negotiated(capability)

    // A transfer this host requested: the peer's release and released for it are accepted.
    fun expect(transfer: String) = synchronized(lock) { stream.expect(transfer) }

    // Queues one message, checked as the peer would check it.
    fun send(kind: String, epoch: Long, transfer: String, body: Map<String, Any?>) {
        synchronized(lock) {
            if (closed != null) throw PeerClosed()
            if (queue.remainingCapacity() == 0) throw Backpressure()
            val m = out.next(kind, epoch, transfer, body)
            Wire.decode(Wire.encode(m))
            queue.add(m)
        }
    }

    fun close() = fail(PeerClosed())

    private fun fail(e: Throwable) {
        synchronized(lock) {
            if (closed != null) return
            closed = e
        }
        runCatching { tls.close() }
        beats?.shutdownNow()
        workers.forEach { if (it !== Thread.currentThread()) it.interrupt() }
        onClosed(e)
    }

    private fun reader() {
        try {
            while (closed == null) {
                val m = Wire.readFrame(tls.inputStream)
                if (store.enrolled(peerId) == null) return fail(NotEnrolled()) // revoked while connected
                synchronized(lock) { stream.receive(m) } // fail closed: no partial trust
                onMessage(m)
            }
        } catch (e: Exception) {
            fail(e)
        }
    }

    private fun writer() {
        try {
            while (closed == null) Wire.writeFrame(tls.outputStream, queue.take())
        } catch (e: InterruptedException) {
            fail(PeerClosed())
        } catch (e: Exception) {
            fail(e)
        }
    }

    companion object {
        const val SEND_QUEUE = 32
        const val HEARTBEAT_MS = 3000L

        fun newNonce(): String = hex(ByteArray(16).also { SecureRandom().nextBytes(it) })

        // Connects to an enrolled peer through route.
        fun dial(store: PeerStore, route: Route, peerId: String, opts: PeerOptions): PeerConn {
            if (store.enrolled(peerId) == null) throw NotEnrolled()
            return handshaken(clientTls(route.dial(), store.identity(), peerId), store, peerId, opts)
        }

        // The listening side: an enrolled key gets a protocol connection. An unknown key may only enroll, while
        // enroller has an open invitation (then Enrolled is thrown); otherwise NotEnrolled. Either way an unknown
        // key never sees a protocol message.
        fun accept(raw: Socket, store: PeerStore, opts: PeerOptions, enroller: Enroller? = null): PeerConn {
            val s = tlsContext(store.identity()) { }.socketFactory.createSocket(raw, null, raw.port, true) as SSLSocket
            s.enabledProtocols = arrayOf("TLSv1.3")
            s.useClientMode = false
            s.needClientAuth = true
            handshake(s)
            val peer = peerId(s.session.peerCertificates[0] as X509Certificate)
            if (store.enrolled(peer) == null) {
                s.use {
                    if (enroller == null || !enroller.pending()) throw NotEnrolled()
                    val (guest, code) = try {
                        enroller.serve(s)
                    } catch (e: Exception) {
                        throw IOException("enrollment refused: ${e.message}", e)
                    }
                    throw Enrolled(guest, code)
                }
            }
            return handshaken(s, store, peer, opts)
        }

        private fun handshaken(s: SSLSocket, store: PeerStore, peerId: String, opts: PeerOptions): PeerConn {
            val c = PeerConn(s, store, peerId, opts)
            try {
                c.hello()
            } catch (e: Exception) {
                runCatching { s.close() }
                throw IOException("hello: ${e.message}", e)
            }
            return c
        }
    }
}

// Keeps an enrolled peer connected through a route (transport.go Link): dial, hand each connection to onConn
// (which returns that connection's message handler),
// wait for it to end, dial again after Policy.retryDelayMs (reset after a connection that lasted). Each
// connection is a new session; the peer's earlier ones are refused. kick() (the network changed) retries now with
// the backoff reset; stop() ends it and the current connection. A revoked peer stops it (onStopped).
class PeerLink(
    private val store: PeerStore,
    private val route: Route,
    private val peerId: String,
    private val opts: PeerOptions,
    private val onConn: (PeerConn) -> ((Message) -> Unit),
    private val onStopped: (Throwable) -> Unit = {},
    private val random: () -> Double = Math::random,
) {
    private val lock = Object()
    @Volatile private var running = false
    private var current: PeerConn? = null
    private var kicked = false
    private var thread: Thread? = null

    fun start() {
        running = true
        thread = Thread(::loop, "peer-link").apply { isDaemon = true; start() }
    }

    fun stop() {
        running = false
        synchronized(lock) { lock.notifyAll() }
        current?.close()
        thread?.interrupt()
    }

    fun kick() = synchronized(lock) {
        kicked = true
        lock.notifyAll()
    }

    private fun loop() {
        var failures = 0
        val retired = opts.retired.toMutableList()
        while (running) {
            try {
                val began = System.nanoTime()
                val c = PeerConn.dial(store, route, peerId, opts.copy(retired = retired.toList()))
                val ended = Object()
                synchronized(lock) { current = c }
                c.start(onConn(c)) { synchronized(ended) { ended.notifyAll() } }
                synchronized(ended) { while (c.closed == null && running) ended.wait(1000) }
                c.close()
                retired += c.peerSession
                if (System.nanoTime() - began > Policy.PEER_SUSPECT_MS * 1_000_000) failures = 0
            } catch (e: NotEnrolled) {
                running = false
                return onStopped(e)
            } catch (e: InterruptedException) {
                return
            } catch (e: Exception) {
                // Unreachable or refused: try again later.
            }
            if (!running) return
            val delay = Policy.retryDelayMs(failures, random())
            failures++
            synchronized(lock) {
                val until = System.currentTimeMillis() + delay
                while (running && !kicked && System.currentTimeMillis() < until) lock.wait(maxOf(1, until - System.currentTimeMillis()))
                if (kicked) failures = 0
                kicked = false
            }
        }
    }
}

// The phone's listener (the host side): every connection is accepted on its own thread; enrolled laptops get a
// protocol connection (onConn, which returns its message handler), guests proving an open invitation are enrolled
// (onEnrolled), anything else is refused before a protocol message. stop() closes the socket and every
// connection it accepted.
class PeerServer(
    private val store: PeerStore,
    private val opts: PeerOptions,
    private val enroller: Enroller?,
    private val onConn: (PeerConn) -> ((Message) -> Unit),
    private val onEnrolled: (Peer, String) -> Unit = { _, _ -> },
    port: Int = DEFAULT_PORT,
    bind: java.net.InetAddress? = null, // null: every interface
) {
    private val server = java.net.ServerSocket().apply {
        reuseAddress = true
        bind(InetSocketAddress(bind, port), 8)
    }
    val port: Int get() = server.localPort
    private val conns = java.util.Collections.synchronizedSet(HashSet<PeerConn>())
    @Volatile private var running = true

    init {
        Thread({
            while (running) {
                val raw = runCatching { server.accept() }.getOrNull() ?: break
                Thread({ handle(raw) }, "peer-accept").apply { isDaemon = true; start() }
            }
        }, "peer-server").apply { isDaemon = true; start() }
    }

    private fun handle(raw: Socket) {
        try {
            val c = PeerConn.accept(raw, store, opts, enroller)
            if (!running) return c.close()
            conns += c
            c.start(onConn(c)) { conns -= c }
        } catch (e: Enrolled) {
            onEnrolled(e.peer, e.code)
        } catch (e: Exception) {
            runCatching { raw.close() } // refused or broken: nothing reaches the protocol
        }
    }

    fun stop() {
        running = false
        runCatching { server.close() }
        conns.toList().forEach { it.close() }
    }

    companion object {
        const val DEFAULT_PORT = 47613
    }
}
