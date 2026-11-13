package scouterkit.chewedfeed.handover

import scouterkit.chewedfeed.handover.EffectKind.ACQUIRE_BOTH
import scouterkit.chewedfeed.handover.EffectKind.CANCEL_TIMERS
import scouterkit.chewedfeed.handover.EffectKind.FORCE_DISCONNECT
import scouterkit.chewedfeed.handover.EffectKind.IGNORE
import scouterkit.chewedfeed.handover.EffectKind.PUBLISH_STATUS
import scouterkit.chewedfeed.handover.EffectKind.RECONCILE
import scouterkit.chewedfeed.handover.EffectKind.REDRAW
import scouterkit.chewedfeed.handover.EffectKind.RELEASE_BOTH
import scouterkit.chewedfeed.handover.EffectKind.SCHEDULE
import scouterkit.chewedfeed.handover.EffectKind.SEND_ABORT
import scouterkit.chewedfeed.handover.EffectKind.SEND_ACQUIRING
import scouterkit.chewedfeed.handover.EffectKind.SEND_READY
import scouterkit.chewedfeed.handover.EffectKind.SEND_RELEASE
import scouterkit.chewedfeed.handover.EffectKind.SEND_RELEASED
import scouterkit.chewedfeed.handover.EffectKind.SEND_REQUEST
import scouterkit.chewedfeed.handover.Phase.ACQUIRING
import scouterkit.chewedfeed.handover.Phase.ACTIVE
import scouterkit.chewedfeed.handover.Phase.DEGRADED
import scouterkit.chewedfeed.handover.Phase.IDLE
import scouterkit.chewedfeed.handover.Phase.RECOVERING
import scouterkit.chewedfeed.handover.Phase.REQUESTING
import scouterkit.chewedfeed.handover.Phase.YIELDING

// One effect before its generation and transfer are stamped from the new state.
private class Fx(val kind: EffectKind, val timer: Timer? = null, val delayMs: Long = 0)

private fun fx(kind: EffectKind) = Fx(kind)
private fun schedule(timer: Timer, ms: Long) = Fx(SCHEDULE, timer, ms)
private fun done(s: State, vararg effects: Fx) =
    Transition(s, effects.map { Effect(it.kind, s.generation, s.transfer, it.timer, it.delayMs) })
private fun ignore(s: State) = done(s, fx(IGNORE))

// Reduce applies one event. Events of another generation, and events that do not apply in the current phase,
// change nothing and return a single IGNORE. It never acquires while a reachable peer is active without that
// peer's matching release, never reports released before both arms are down, the workers stopped and
// reconnects disabled, and never lets a timeout or a wall clock change take the arms from a healthy owner.
fun reduce(s: State, e: Event): Transition {
    if (e.generation != s.generation) return ignore(s)
    if (!s.enrolled && e.isPeerEvent()) return ignore(s) // no peer: nothing it says can be authentic
    return when (e) {
        is Event.Start ->
            if (s.phase != IDLE || s.generation != 0L) ignore(s) // once per process; a restart is a new State
            else start(s.copy(eligible = e.eligible), e.attemptNonce)
        is Event.Availability -> availability(s, e.eligible, e.attemptNonce)
        is Event.PeerStatus -> peerStatus(s, e)
        is Event.PreferLocal -> preferLocal(s, e.stableMs, e.attemptNonce)
        is Event.ReleaseRequest -> releaseRequest(s, e)
        is Event.Released ->
            if (s.phase != REQUESTING || e.transfer != s.transfer || !s.eligible) ignore(s)
            else acquire(s.copy(peerActive = false), s.transfer, Permission.RELEASED)
        is Event.ReleaseNotice ->
            if (s.phase != IDLE || !s.eligible || e.transfer.isEmpty()) ignore(s)
            else done(s.copy(phase = REQUESTING, transfer = e.transfer, generation = s.generation + 1), fx(PUBLISH_STATUS))
        is Event.ReleaseComplete -> releaseComplete(s, e)
        is Event.ArmsChanged -> armsChanged(s, Arms(e.left, e.right))
        is Event.Initialized -> {
            if (s.phase != ACQUIRING || !s.arms.both || !e.rendererReady) return ignore(s)
            // Both arms are ours: whoever held them before does not any more.
            val n = s.copy(phase = ACTIVE, rendererReady = true, peerActive = false)
            if (s.enrolled) done(n, fx(REDRAW), fx(SEND_READY)) else done(n, fx(REDRAW), fx(PUBLISH_STATUS))
        }
        is Event.Timeout -> timeout(s, e.timer, e.elapsedMs, e.attemptNonce)
        is Event.RouteLost -> {
            var n = s.copy(peerReachable = false)
            val effects = mutableListOf<Fx>()
            if (n.eligible && (n.phase == REQUESTING || n.phase == IDLE)) {
                // Retire the request: its late release cannot authorize a new attempt. Only the suspect
                // deadline permits a non-preemptive probe now.
                n = n.copy(phase = RECOVERING, generation = n.generation + 1)
                effects += fx(CANCEL_TIMERS)
            }
            effects += fx(PUBLISH_STATUS)
            effects += schedule(Timer.PEER_SUSPECT, probeDelay(n.role, Policy.PEER_SUSPECT_MS))
            done(n, *effects.toTypedArray())
        }
        is Event.RouteRestored -> {
            val n = s.copy(peerReachable = true)
            // The previous owner is back mid-probe: only its confirmed release may hand over.
            if (n.phase == ACQUIRING && n.peerActive) rollback(n) else done(n, fx(RECONCILE))
        }
        is Event.Pin -> pin(s, e.enabled, e.attemptNonce)
        is Event.Failed -> if (s.phase == ACQUIRING || s.phase == ACTIVE) rollback(s) else ignore(s)
        is Event.Stop -> {
            val n = s.copy(eligible = false)
            when (n.phase) {
                ACQUIRING -> rollback(n)
                ACTIVE -> yieldArms(n, n.transfer, ReleaseIntent.LOCAL_STOP, n.enrolled)
                REQUESTING -> done(n.copy(phase = IDLE, generation = n.generation + 1),
                    fx(CANCEL_TIMERS), fx(SEND_ABORT), fx(PUBLISH_STATUS))
                RECOVERING, IDLE -> done(n.copy(phase = IDLE, generation = n.generation + 1),
                    fx(CANCEL_TIMERS), fx(PUBLISH_STATUS))
                else -> done(n, fx(PUBLISH_STATUS))
            }
        }
        is Event.ClockAdjusted -> ignore(s) // deadlines are monotonic
    }
}

private fun Event.isPeerEvent() = this is Event.PeerStatus || this is Event.PreferLocal || this is Event.ReleaseRequest ||
    this is Event.Released || this is Event.ReleaseNotice || this is Event.RouteLost || this is Event.RouteRestored

private fun start(s: State, nonce: String?): Transition {
    if (!s.eligible) return done(s, fx(PUBLISH_STATUS))
    if (!s.enrolled) return if (nonce.isNullOrEmpty()) ignore(s) else acquire(s, nonce, Permission.STANDALONE)
    // Learn the peer's state before touching the arms; probe only after the cold-start delay.
    return done(s.copy(phase = RECOVERING), fx(RECONCILE), schedule(Timer.COLD_START, coldStartMs(s.role)))
}

private fun availability(s0: State, eligible: Boolean, nonce: String?): Transition {
    val was = s0.eligible
    val s = s0.copy(eligible = eligible)
    return when {
        !eligible && s.phase == RECOVERING ->
            // Retire queued recovery callbacks before a later, fresh resume.
            done(s.copy(phase = IDLE, generation = s.generation + 1), fx(CANCEL_TIMERS), fx(PUBLISH_STATUS))
        !eligible && (s.phase == ACTIVE || s.phase == ACQUIRING) ->
            // Lid closed, suspend or app unavailable: release even when pinned or not sleeping.
            if (s.phase == ACQUIRING) rollback(s)
            // The laptop tells the phone it is going (release_notice on the wire); the phone just goes.
            else yieldArms(s, if (nonce.isNullOrEmpty()) s.transfer else nonce, ReleaseIntent.HANDOVER, s.enrolled && s.role == Role.LAPTOP)
        !eligible && s.phase == REQUESTING ->
            // No longer able to take the arms: withdraw the request; a late released is ignored.
            done(s.copy(phase = IDLE), fx(SEND_ABORT), fx(PUBLISH_STATUS))
        eligible && !was && s.phase == IDLE ->
            if (!s.enrolled) {
                if (nonce.isNullOrEmpty()) done(s, fx(PUBLISH_STATUS)) else acquire(s, nonce, Permission.STANDALONE)
            } else {
                // Back (resume, lid open): reconcile first; the laptop's stability interval starts now.
                done(s.copy(phase = RECOVERING), fx(RECONCILE), schedule(Timer.STABILITY, Policy.LAPTOP_STABILITY_MS))
            }
        else -> done(s, fx(PUBLISH_STATUS))
    }
}

private fun peerStatus(s0: State, e: Event.PeerStatus): Transition {
    val s = s0.copy(peerReachable = e.reachable, peerEligible = e.eligible, peerPinned = e.pinned && s0.role == Role.LAPTOP)
    return when {
        // Both arms are ours: a peer claiming them is stale or split; reconcile, never record it.
        s.phase == ACTIVE ->
            if (e.active) done(s.copy(peerActive = false), fx(RECONCILE)) else done(s.copy(peerActive = false), fx(PUBLISH_STATUS))
        // Another host holds the arms after all: back off; only its release may hand over.
        s.phase == ACQUIRING && e.active && e.reachable -> rollback(s.copy(peerActive = true))
        else -> {
            var n = s.copy(peerActive = e.active)
            if (n.phase == RECOVERING && e.reachable) n = n.copy(phase = IDLE)
            done(n, fx(PUBLISH_STATUS))
        }
    }
}

// Asks the peer to release. The laptop asks once it has been eligible for the stability interval and the
// phone is not pinned; the phone asks when it is pinned or the laptop is not eligible.
private fun preferLocal(s: State, stableMs: Long, nonce: String?): Transition {
    if (s.phase != IDLE || !s.enrolled || !s.eligible || !s.peerReachable || nonce.isNullOrEmpty()) return ignore(s)
    if (s.role == Role.LAPTOP && (s.peerPinned || stableMs < Policy.LAPTOP_STABILITY_MS)) return ignore(s)
    if (s.role == Role.PHONE && !s.pinned && s.peerEligible) return ignore(s)
    return request(s, nonce)
}

private fun request(s: State, nonce: String) =
    done(s.copy(phase = REQUESTING, transfer = nonce, generation = s.generation + 1), fx(SEND_REQUEST))

private fun releaseRequest(s: State, e: Event.ReleaseRequest): Transition {
    val refuse = e.transfer.isEmpty() || e.target == s.role ||
        s.role == Role.PHONE && s.pinned && e.reason != "manual" ||
        s.role == Role.LAPTOP && s.eligible && !(e.reason == "manual" && e.peerPinned)
    if (refuse) return done(s, fx(PUBLISH_STATUS))
    return when (s.phase) {
        ACTIVE, ACQUIRING -> yieldArms(s, e.transfer, ReleaseIntent.HANDOVER, true)
        // Nothing native is held: confirm through the same release path, without timers.
        IDLE -> done(s.copy(phase = YIELDING, transfer = e.transfer, releaseIntent = ReleaseIntent.HANDOVER,
            generation = s.generation + 1), fx(SEND_RELEASE), fx(RELEASE_BOTH))
        else -> done(s, fx(PUBLISH_STATUS))
    }
}

// Releases the arms for transfer: the renderer goes first, then both links within the release deadline;
// released is sent only on release_complete. notify sends the release notification to the peer first.
private fun yieldArms(s: State, transfer: String?, intent: ReleaseIntent, notify: Boolean): Transition {
    val n = s.copy(phase = YIELDING, transfer = transfer, permission = Permission.NONE, releaseIntent = intent,
        rendererReady = false, generation = s.generation + 1)
    val effects = mutableListOf(fx(CANCEL_TIMERS))
    if (notify) effects += fx(SEND_RELEASE)
    effects += fx(RELEASE_BOTH)
    effects += schedule(Timer.RELEASE, Policy.RELEASE_DEADLINE_MS)
    return done(n, *effects.toTypedArray())
}

// Abandons this host's own acquisition (failure, partial arms, timeout, stop).
private fun rollback(s: State) = done(
    s.copy(phase = YIELDING, permission = Permission.NONE, releaseIntent = ReleaseIntent.ROLLBACK, rendererReady = false,
        generation = s.generation + 1),
    fx(CANCEL_TIMERS), fx(RELEASE_BOTH), schedule(Timer.RELEASE, Policy.RELEASE_DEADLINE_MS))

private fun acquire(s: State, transfer: String?, permission: Permission): Transition {
    val n = s.copy(phase = ACQUIRING, transfer = transfer, permission = permission, generation = s.generation + 1)
    val effects = mutableListOf<Fx>()
    if (permission == Permission.RELEASED) effects += fx(SEND_ACQUIRING)
    effects += fx(ACQUIRE_BOTH)
    effects += schedule(Timer.ACQUIRE, Policy.ACQUIRE_DEADLINE_MS)
    return done(n, *effects.toTypedArray())
}

private fun releaseComplete(s: State, e: Event.ReleaseComplete): Transition {
    if ((s.phase != YIELDING && s.phase != DEGRADED) || !e.bothDown || !e.workersStopped || !e.reconnectDisabled) return ignore(s)
    val n = s.copy(arms = Arms(), rendererReady = false, releaseIntent = ReleaseIntent.NONE)
    return when {
        s.releaseIntent == ReleaseIntent.ROLLBACK && s.eligible ->
            done(n.copy(phase = RECOVERING), fx(SEND_ABORT), schedule(Timer.RETRY, probeDelay(s.role, Policy.RETRY_INITIAL_MS)))
        s.releaseIntent == ReleaseIntent.ROLLBACK -> done(n.copy(phase = IDLE), fx(SEND_ABORT))
        !s.enrolled -> done(n.copy(phase = IDLE), fx(PUBLISH_STATUS))
        else -> done(n.copy(phase = IDLE), fx(SEND_RELEASED))
    }
}

private fun armsChanged(s: State, arms: Arms): Transition = when (s.phase) {
    ACQUIRING -> {
        val first = !s.arms.any
        val n = s.copy(arms = arms)
        if (arms.any && !arms.both && first) done(n, schedule(Timer.PARTIAL_ARMS, Policy.PARTIAL_ARMS_MS))
        else done(n, fx(PUBLISH_STATUS))
    }
    ACTIVE ->
        if (arms.both) ignore(s)
        // An arm dropped: not active until both are back and repainted, within the partial-arm grace.
        else done(s.copy(arms = arms, phase = ACQUIRING, rendererReady = false), fx(PUBLISH_STATUS),
            schedule(Timer.PARTIAL_ARMS, Policy.PARTIAL_ARMS_MS), schedule(Timer.ACQUIRE, Policy.ACQUIRE_DEADLINE_MS))
    else -> ignore(s)
}

private fun timeout(s: State, timer: Timer, elapsed: Long, nonce: String?): Transition {
    val extra = if (s.role == Role.PHONE) Policy.PHONE_PROBE_EXTRA_MS else 0
    when (timer) {
        Timer.ACQUIRE -> if (s.phase == ACQUIRING && elapsed >= Policy.ACQUIRE_DEADLINE_MS) return rollback(s)
        Timer.PARTIAL_ARMS -> if (s.phase == ACQUIRING && !s.arms.both && elapsed >= Policy.PARTIAL_ARMS_MS) return rollback(s)
        // Still no confirmation: force the links down, but never report released for it.
        Timer.RELEASE -> if (s.phase == YIELDING && elapsed >= Policy.RELEASE_DEADLINE_MS)
            return done(s.copy(phase = DEGRADED), fx(FORCE_DISCONNECT), fx(PUBLISH_STATUS))
        Timer.PEER_SUSPECT -> {
            if (s.peerReachable) return done(s, fx(RECONCILE))
            if (s.phase == ACTIVE) return done(s, fx(PUBLISH_STATUS)) // a healthy owner keeps the arms
            if (elapsed >= Policy.PEER_SUSPECT_MS + extra) return probe(s, nonce)
        }
        Timer.RETRY -> {
            if (s.peerReachable && s.phase == RECOVERING) return done(s, fx(RECONCILE))
            if (elapsed >= Policy.RETRY_INITIAL_MS + extra) return probe(s, nonce)
        }
        Timer.COLD_START -> {
            if (s.peerReachable && s.phase == RECOVERING) return done(s, fx(RECONCILE))
            if (!s.peerActive && elapsed >= coldStartMs(s.role)) return probe(s, nonce)
        }
        Timer.STABILITY -> {}
    }
    return ignore(s)
}

// Tries the arms when no peer can be asked. It cannot preempt: an arm held by another host stays unavailable,
// and the acquire deadline rolls back.
private fun probe(s: State, nonce: String?): Transition {
    if (s.phase != RECOVERING || !s.eligible || s.peerReachable || nonce.isNullOrEmpty()) return ignore(s)
    return acquire(s, nonce, Permission.PROBE)
}

private fun pin(s: State, enabled: Boolean, nonce: String?): Transition {
    if (s.role != Role.PHONE) return ignore(s)
    val n = s.copy(pinned = enabled)
    if (enabled && n.phase == IDLE && n.enrolled && n.eligible && n.peerReachable && !nonce.isNullOrEmpty()) return request(n, nonce)
    return done(n, fx(PUBLISH_STATUS))
}

private fun coldStartMs(r: Role) = if (r == Role.PHONE) Policy.PHONE_COLD_START_MS else Policy.LAPTOP_COLD_START_MS

// A scheduled probe must include the same phone delay checked by timeout.
private fun probeDelay(r: Role, ms: Long) = if (r == Role.PHONE) ms + Policy.PHONE_PROBE_EXTRA_MS else ms
