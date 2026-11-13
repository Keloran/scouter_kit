package scouterkit.chewedfeed.handover

// The phone's one listener for the laptop, shared by the glasses service (the laptop's connections, to the
// ownership coordinator) and Setup (pairing, through the enroller): it listens while either uses it, so they never
// compete for the port. Callbacks run on connection threads; set them to null to refuse connections (closed at
// once, before any message reaches anything).
class LaptopHub(val store: PeerStore, private val port: Int = PeerServer.DEFAULT_PORT) {
    val enroller = Enroller(store)
    @Volatile var heartbeat: () -> Map<String, Any?> = { linkedMapOf("phase" to "idle", "ready_epoch" to 0) }
    @Volatile var onConn: ((PeerConn) -> ((Message) -> Unit))? = null
    @Volatile var onClosed: ((PeerConn) -> Unit)? = null
    @Volatile var onEnrolled: ((Peer, String) -> Unit)? = null
    private var server: PeerServer? = null
    private val users = HashSet<String>()

    fun laptopEnrolled() = store.peers().any { it.role == "laptop" }

    // user: "service" or "pairing". Throws when the port cannot be opened.
    @Synchronized fun open(user: String) {
        if (server == null) {
            server = PeerServer(store, PeerOptions("phone", heartbeat = { heartbeat() }), enroller,
                onConn = { c -> onConn?.invoke(c) ?: run { c.close(); { } } },
                onEnrolled = { p, code -> onEnrolled?.invoke(p, code) },
                port = port,
                onClosed = { c, _ -> onClosed?.invoke(c) })
        }
        users += user
    }

    @Synchronized fun close(user: String) {
        users -= user
        if (users.isEmpty()) {
            server?.stop()
            server = null
        }
    }
}

// A laptop connection as the coordinator's link.
class ConnLink(private val c: PeerConn) : OwnershipLink {
    override val peerSession get() = c.peerSession
    override fun negotiated(capability: String) = c.negotiated(capability)
    override fun send(kind: String, epoch: Long, transfer: String, body: Map<String, Any?>) = runCatching { c.send(kind, epoch, transfer, body) }.isSuccess
    override fun expect(transfer: String) = c.expect(transfer)
    override fun close() = c.close()
}
