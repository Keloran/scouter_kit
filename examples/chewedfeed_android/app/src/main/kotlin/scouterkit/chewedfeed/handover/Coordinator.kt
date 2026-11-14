package scouterkit.chewedfeed.handover

import scouterkit.chewedfeed.bluetooth.Cancellable
import scouterkit.chewedfeed.bluetooth.Scheduler

// The glasses as the coordinator sees them. One operation at a time (the coordinator guarantees it): acquire ends
// with both arms connected and the dashboard fully repainted (done(true)) or failed (done(false)); release ends
// with both arms down, workers stopped and reconnects disabled. abort cuts a release that hangs.
interface OwnershipNative {
    fun acquire(done: (Boolean) -> Unit)
    fun release(done: () -> Unit)
    fun abort()
}

// One authenticated connection to the laptop (PeerConn, or a fake). send returns false when the message could
// not be queued (the connection is then closed and reported down; no acknowledgement is ever invented).
interface OwnershipLink {
    val peerSession: String
    fun negotiated(capability: String): Boolean
    fun send(kind: String, epoch: Long, transfer: String, body: Map<String, Any?>): Boolean
    fun expect(transfer: String)
    fun close()
}

// What the app shows: who holds the glasses as far as this phone knows (this, peer, unknown: enrolled but the
// laptop is not reachable, none), the phase and whether the laptop is connected.
data class OwnershipStatus(val owner: String, val phase: Phase, val laptopConnected: Boolean, val enrolled: Boolean, val pinned: Boolean)

// The phone's ownership controller (go/examples/chewedfeed coordinator.go, phone side): it owns the reducer state
// and runs its effects. All calls on the scheduler's thread. The laptop is preferred: the phone never takes the
// glasses from a reachable, active laptop without its release, and gives them back when the laptop asks (unless
// pinned with "Keep on this phone"). Navigation travels through snapshots (only for a released handover).
class Coordinator(
    enrolled: Boolean,
    private val native: OwnershipNative,
    private val scheduler: Scheduler,
    private val clock: () -> Long, // monotonic ms
    private val snapshots: SnapshotBridge,
    private val onStatus: (OwnershipStatus) -> Unit = {},
    private val nonce: () -> String = PeerConn::newNonce,
    private val jitter: () -> Double = Math::random,
    // The laptop's shared readings: passed on as they come, never an ownership event.
    private val remote: RemoteTelemetry? = null,
    private val onTelemetry: () -> Unit = {},
) {
    var state = State(Role.PHONE, enrolled)
        private set
    private var peer: OwnershipLink? = null
    private var peerSnapshots = false
    private var snapshotNotice = false
    private var lastStatus = ""
    private var reason = "startup"
    private var peerReason = "preference"
    private val timers = HashMap<String, Cancellable>()
    private var op: Op? = null
    private var releaseAfter: Long? = null // a release asked for while an acquire runs
    private var failures = 0
    private var stopped = false

    private class Op(val release: Boolean, val generation: Long) {
        @Volatile var cancelled = false
    }

    // The laptop's view of this phone, for heartbeats.
    fun heartbeat(): Map<String, Any?> = linkedMapOf("phase" to state.phase.wire, "ready_epoch" to state.generation)

    fun status() = OwnershipStatus(
        owner = when {
            state.phase == Phase.ACTIVE || state.arms.any -> "this"
            state.peerActive && state.peerReachable -> "peer"
            state.enrolled && !state.peerReachable -> "unknown"
            else -> "none"
        },
        phase = state.phase, laptopConnected = peer != null, enrolled = state.enrolled, pinned = state.pinned,
    )

    // Once, when the service starts: eligible is whether this phone may hold the glasses now.
    fun start(eligible: Boolean, pinned: Boolean) {
        apply(Event.Start(eligible, state.generation, nonce()))
        if (pinned) pin(true)
    }

    // The phone's own conditions changed (the user's start/stop, Bluetooth, permission, pairing).
    fun availability(eligible: Boolean, why: String) {
        if (stopped) return
        reason = why
        if (eligible && (state.phase == Phase.YIELDING || state.phase == Phase.DEGRADED)) return publish()
        val was = state.eligible
        apply(Event.Availability(eligible, state.generation, nonce()))
        if (eligible && !was && state.enrolled && state.phase == Phase.RECOVERING && !state.peerReachable) apply(Event.RouteLost(state.generation))
    }

    // "Keep the glasses on this phone".
    fun pin(enabled: Boolean) {
        if (!stopped) apply(Event.Pin(enabled, state.generation, nonce()))
    }

    // The user stopped the connection: release and stay idle.
    fun stop() {
        if (stopped) return
        reason = "stopped"
        apply(Event.Stop(state.generation))
    }

    // Shut down (the service ends): timers, the link and any operation go; the glasses are released.
    fun close() {
        stopped = true
        timers.values.forEach { it.cancel() }
        timers.clear()
        peer?.close()
        peer = null
        op?.cancelled = true
    }

    // A new authenticated connection; only one at a time (a second one is closed).
    fun linkUp(link: OwnershipLink) {
        if (stopped) return link.close()
        if (peer != null) return link.close()
        peer = link
        peerSnapshots = link.negotiated(Wire.CAP_SNAPSHOT)
        remote?.linkUp(link.negotiated(Wire.CAP_TELEMETRY)) // a laptop that stopped sharing: its data is cleared
        snapshotNotice = false
        lastStatus = ""
        apply(Event.RouteRestored(state.generation))
    }

    fun linkDown(link: OwnershipLink) {
        if (peer !== link) return
        snapshots.finish(false)
        peer = null
        peerSnapshots = false
        snapshotNotice = false
        reason = "peer_lost"
        apply(Event.RouteLost(state.generation))
    }

    // A message from the laptop, already checked by its Stream.
    fun message(link: OwnershipLink, m: Message) {
        if (stopped || peer !== link) return
        val b = m.body
        val transfer = m.transfer ?: ""
        fun bool(k: String) = b[k] == true
        when (m.kind) {
            "status" -> {
                apply(Event.PeerStatus(active = b["phase"] == "active", reachable = true, eligible = bool("eligible"),
                    pinned = bool("pinned"), generation = state.generation, attemptNonce = nonce()))
                prefer()
            }
            "request" -> {
                reason = b["reason"] as? String ?: "preference"
                val target = Role.entries.firstOrNull { it.wire == b["target"] } ?: return
                apply(Event.ReleaseRequest(transfer, reason, target, bool("pinned"), state.generation, nonce()))
            }
            "release" -> {
                val previous = state.phase
                apply(Event.ReleaseNotice(transfer, state.generation, nonce()))
                if (state.phase == Phase.REQUESTING && state.transfer == transfer) {
                    if (previous != Phase.REQUESTING && peerSnapshots) snapshots.requested(link.peerSession, transfer)
                    snapshotNotice = true
                }
            }
            "snapshot" -> if (peerSnapshots && snapshotNotice && state.phase == Phase.REQUESTING && transfer == state.transfer && m.session == link.peerSession) {
                snapshots.receive(m.session, transfer, StrictJson.write(b))
            }
            "released" -> apply(Event.Released(transfer, state.generation, nonce()))
            "telemetry" -> if (remote?.accept(m.session, b) == true) onTelemetry()
            "ready" -> apply(Event.PeerStatus(true, true, state.peerEligible, state.peerPinned, state.generation, nonce()))
            "heartbeat" -> {
                apply(Event.PeerStatus(b["phase"] == "active", true, state.peerEligible, state.peerPinned, state.generation, nonce()))
                prefer()
            }
            "availability" -> {
                (b["reason"] as? String)?.let { peerReason = it }
                apply(Event.PeerStatus(state.peerActive, true, bool("eligible"), state.peerPinned, state.generation, nonce()))
                prefer()
            }
        }
    }

    // The phone asks for the glasses when it is pinned or the laptop cannot hold them (lid closed, asleep); the
    // reducer decides (desktop coordinator.go prefer, phone side: no stability wait).
    private fun prefer() {
        if (state.phase == Phase.IDLE && state.eligible && state.peerReachable) apply(Event.PreferLocal(0, state.generation, nonce()))
    }

    private fun apply(e: Event) {
        val previous = state.phase
        val tr = reduce(state, e)
        state = tr.state
        if (previous == Phase.REQUESTING && state.phase != Phase.REQUESTING) {
            snapshots.finish(state.phase == Phase.ACQUIRING && state.permission == Permission.RELEASED)
            snapshotNotice = false
        }
        for (fx in tr.effects) effect(fx)
        publish()
    }

    private fun publish() = onStatus(status())

    private fun send(kind: String, transfer: String, body: Map<String, Any?>) {
        val p = peer ?: return
        if (!p.send(kind, state.generation, transfer, body)) p.close() // reported down by the connection
    }

    private fun sendStatus() {
        val s = state
        val body = linkedMapOf("role" to "phone", "phase" to s.phase.wire, "eligible" to s.eligible, "pinned" to s.pinned,
            "arms" to linkedMapOf("left" to s.arms.left, "right" to s.arms.right), "renderer_ready" to s.rendererReady,
            "transfer" to s.transfer, "ready_epoch" to s.generation)
        val encoded = StrictJson.write(body)
        if (peer == null || encoded == lastStatus) return
        lastStatus = encoded
        send("status", "", body)
    }

    private fun effect(f: Effect) {
        val s = state
        val transfer = f.transfer ?: ""
        when (f.kind) {
            EffectKind.ACQUIRE_BOTH -> begin(false, f.generation)
            EffectKind.RELEASE_BOTH -> {
                if (s.releaseIntent == ReleaseIntent.ROLLBACK) failures++
                begin(true, f.generation)
            }
            EffectKind.FORCE_DISCONNECT -> {
                native.abort()
                begin(true, f.generation)
            }
            EffectKind.CANCEL_TIMERS -> {
                timers.values.forEach { it.cancel() }
                timers.clear()
            }
            EffectKind.SCHEDULE -> {
                val timer = f.timer ?: return
                // The backoff, plus the phone's probe extra the reducer waits for (the laptop gets the first chance).
                val delay = if (timer == Timer.RETRY) Policy.retryDelayMs(maxOf(failures - 1, 0), jitter()) + Policy.PHONE_PROBE_EXTRA_MS else f.delayMs
                schedule(timer.wire, f.generation, delay) { elapsed -> apply(Event.Timeout(timer, elapsed, f.generation, nonce())) }
            }
            EffectKind.REDRAW -> failures = 0 // the dashboard repainted fully before Initialized
            EffectKind.SEND_REQUEST -> {
                snapshotNotice = false
                if (peerSnapshots) snapshots.requested(peer?.peerSession ?: "", transfer)
                peer?.expect(transfer)
                val pinned = s.pinned
                send("request", transfer, linkedMapOf("target" to "phone", "reason" to if (pinned) "manual" else peerReason, "pinned" to pinned))
                schedule("request_reply", f.generation, Policy.RELEASE_DEADLINE_MS) {
                    if (state.phase == Phase.REQUESTING) peer?.close() // no answer: the link is suspect
                }
            }
            EffectKind.SEND_RELEASE -> {
                send("release", transfer, linkedMapOf("target" to "laptop", "reason" to reason))
                if (peerSnapshots && s.arms.both) snapshots.export()?.let { snap ->
                    send("snapshot", transfer, linkedMapOf("snapshot" to StrictJson.parse(snap.encode().toByteArray(), UiSnapshot.MAX_BYTES)))
                }
            }
            EffectKind.SEND_RELEASED -> send("released", transfer, linkedMapOf("target" to "laptop",
                "arms" to linkedMapOf("left" to false, "right" to false), "reconnect_disabled" to true, "workers_stopped" to true))
            EffectKind.SEND_ACQUIRING -> send("acquiring", transfer, linkedMapOf("target" to "phone", "mode" to s.permission.wire))
            EffectKind.SEND_READY -> send("ready", transfer, linkedMapOf("target" to "phone",
                "arms" to linkedMapOf("left" to s.arms.left, "right" to s.arms.right), "renderer_ready" to true))
            EffectKind.SEND_ABORT -> send("abort", transfer, linkedMapOf("reason" to reason, "released_both" to !s.arms.any))
            EffectKind.PUBLISH_STATUS, EffectKind.RECONCILE -> sendStatus()
            EffectKind.IGNORE -> {}
        }
    }

    private fun schedule(name: String, generation: Long, delayMs: Long, fire: (Long) -> Unit) {
        timers.remove(name)?.cancel()
        val started = clock()
        timers[name] = scheduler.postDelayed(maxOf(0, delayMs)) {
            if (stopped || generation != state.generation) return@postDelayed
            timers.remove(name)
            fire(clock() - started)
        }
    }

    // One native operation at a time; a release asked for during an acquire cancels it and runs after it.
    private fun begin(release: Boolean, generation: Long) {
        op?.let { running ->
            if (release) {
                running.cancelled = true
                releaseAfter = generation
            }
            return
        }
        val o = Op(release, generation)
        op = o
        if (release) native.release { scheduler.post { result(o, true) } }
        else native.acquire { ok -> scheduler.post { result(o, ok) } }
    }

    private fun result(o: Op, ok: Boolean) {
        if (op !== o) return
        op = null
        releaseAfter?.let {
            releaseAfter = null
            return begin(true, it)
        }
        if (stopped || o.generation != state.generation) {
            if (!o.release && ok) begin(true, state.generation) // late acquire nobody wants: let go again
            return
        }
        if (o.release) {
            apply(Event.ReleaseComplete(bothDown = true, workersStopped = true, reconnectDisabled = true, generation = state.generation))
            if (state.phase == Phase.IDLE && state.eligible && state.enrolled && !state.peerReachable) apply(Event.RouteLost(state.generation))
        } else if (!ok) {
            reason = "acquire_failed"
            apply(Event.Failed(reason, state.generation, nonce()))
        } else {
            apply(Event.ArmsChanged(true, true, state.generation))
            apply(Event.Initialized(true, state.generation))
        }
    }

    // The glasses session ended by itself while this phone held them (arms lost, Bluetooth off).
    fun lost() {
        if (op == null && (state.phase == Phase.ACTIVE || state.phase == Phase.ACQUIRING)) {
            reason = "acquire_failed"
            apply(Event.Failed(reason, state.generation, nonce()))
        }
    }
}
