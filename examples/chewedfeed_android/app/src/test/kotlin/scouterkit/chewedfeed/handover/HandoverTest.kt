package scouterkit.chewedfeed.handover

import org.json.JSONArray
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test
import java.io.File
import java.security.MessageDigest
import kotlin.random.Random

// The shared fixtures in tests/handover, also run against the Go reducer.
private fun fixture(name: String): JSONObject {
    var dir: File? = File("").absoluteFile
    while (dir != null && !File(dir, "tests/handover/$name").isFile) dir = dir.parentFile
    return JSONObject(File(dir ?: error("tests/handover not found"), "tests/handover/$name").readText())
}

private fun <T> enumOf(values: Array<T>, wire: String, name: (T) -> String) = values.first { name(it) == wire }

private fun JSONObject.nullableString(key: String) = if (isNull(key)) null else getString(key)

private fun stateOf(o: JSONObject) = State(
    role = enumOf(Role.entries.toTypedArray(), o.getString("role")) { it.wire },
    enrolled = o.getBoolean("enrolled"),
    eligible = o.getBoolean("eligible"),
    pinned = o.getBoolean("pinned"),
    phase = enumOf(Phase.entries.toTypedArray(), o.getString("phase")) { it.wire },
    arms = o.getJSONObject("arms").let { Arms(it.getBoolean("left"), it.getBoolean("right")) },
    rendererReady = o.getBoolean("renderer_ready"),
    generation = o.getLong("generation"),
    transfer = o.nullableString("transfer"),
    peerReachable = o.getBoolean("peer_reachable"),
    peerActive = o.getBoolean("peer_active"),
    permission = enumOf(Permission.entries.toTypedArray(), o.getString("permission")) { it.wire },
    releaseIntent = enumOf(ReleaseIntent.entries.toTypedArray(), o.getString("release_intent")) { it.wire },
    peerEligible = o.getBoolean("peer_eligible"),
    peerPinned = o.getBoolean("peer_pinned"),
)

private fun eventOf(o: JSONObject): Event {
    val g = o.getLong("generation")
    val n = o.nullableString("attempt_nonce")
    val d = o.getJSONObject("data")
    fun role(k: String) = enumOf(Role.entries.toTypedArray(), d.getString(k)) { it.wire }
    return when (val kind = o.getString("kind")) {
        "start" -> Event.Start(d.getBoolean("eligible"), g, n)
        "availability" -> Event.Availability(d.getBoolean("eligible"), g, n)
        "peer_status" -> Event.PeerStatus(d.getBoolean("active"), d.getBoolean("reachable"), d.getBoolean("eligible"), d.getBoolean("pinned"), g, n)
        "prefer_local" -> Event.PreferLocal(d.getLong("stable_ms"), g, n)
        "release_request" -> Event.ReleaseRequest(d.getString("transfer"), d.getString("reason"), role("target"), d.getBoolean("peer_pinned"), g, n)
        "released" -> Event.Released(d.getString("transfer"), g, n)
        "release_notice" -> Event.ReleaseNotice(d.getString("transfer"), g, n)
        "release_complete" -> Event.ReleaseComplete(d.getBoolean("both_down"), d.getBoolean("workers_stopped"), d.getBoolean("reconnect_disabled"), g, n)
        "arms_changed" -> Event.ArmsChanged(d.getBoolean("left"), d.getBoolean("right"), g, n)
        "initialized" -> Event.Initialized(d.getBoolean("renderer_ready"), g, n)
        "timeout" -> Event.Timeout(enumOf(Timer.entries.toTypedArray(), d.getString("timer")) { it.wire }, d.getLong("elapsed_ms"), g, n)
        "route_lost" -> Event.RouteLost(g, n)
        "route_restored" -> Event.RouteRestored(g, n)
        "pin" -> Event.Pin(d.getBoolean("enabled"), g, n)
        "clock_adjusted" -> Event.ClockAdjusted(d.getLong("wall_delta_ms"), g, n)
        "failed" -> Event.Failed(d.getString("reason"), g, n)
        "stop" -> Event.Stop(g, n)
        else -> error("unknown event $kind")
    }
}

private fun effectOf(o: JSONObject) = Effect(
    kind = enumOf(EffectKind.entries.toTypedArray(), o.getString("kind")) { it.wire },
    generation = o.getLong("generation"),
    transfer = o.nullableString("transfer"),
    timer = o.nullableString("timer")?.let { t -> enumOf(Timer.entries.toTypedArray(), t) { it.wire } },
    delayMs = o.getLong("delay_ms"),
)

private fun JSONArray.objects() = List(length()) { getJSONObject(it) }

// go/examples/internal/handover/crossplatform_test.go canonical.
private fun canonical(t: Transition): String {
    fun b(v: Boolean) = if (v) "1" else "0"
    val s = t.state
    val parts = mutableListOf(listOf(s.role.wire, b(s.enrolled), b(s.eligible), b(s.pinned), s.phase.wire, b(s.arms.left),
        b(s.arms.right), b(s.rendererReady), s.generation.toString(), s.transfer ?: "-", b(s.peerReachable), b(s.peerActive),
        s.permission.wire, s.releaseIntent.wire, b(s.peerEligible), b(s.peerPinned)).joinToString(","))
    for (f in t.effects) parts += listOf(f.kind.wire, f.generation.toString(), f.transfer ?: "-", f.timer?.wire ?: "-", f.delayMs.toString()).joinToString(",")
    return parts.joinToString(";")
}

class HandoverTest {
    @Test
    fun inactiveStopRetiresCallbacksAndAllowsResume() {
        for (role in Role.entries) for (enrolled in listOf(false, true)) {
            for (phase in listOf(Phase.IDLE, Phase.RECOVERING)) {
                val initial = State(role, enrolled, eligible = true, phase = phase, generation = 7,
                    transfer = "1".repeat(32))
                val stopped = reduce(initial, Event.Stop(7))
                assertEquals(Phase.IDLE, stopped.state.phase)
                assertEquals(false, stopped.state.eligible)
                assertEquals(8L, stopped.state.generation)
                assertEquals(listOf(EffectKind.CANCEL_TIMERS, EffectKind.PUBLISH_STATUS), stopped.effects.map { it.kind })
                val resumed = reduce(stopped.state, Event.Availability(true, 8, "2".repeat(32))).state
                assertEquals(if (enrolled) Phase.RECOVERING else Phase.ACQUIRING, resumed.phase)
                for (event in listOf(Event.Timeout(Timer.RETRY, 60000, 7, "3".repeat(32)),
                    Event.Released("1".repeat(32), 7), Event.Stop(7))) {
                    val late = reduce(resumed, event)
                    assertEquals(resumed, late.state)
                    assertEquals(listOf(EffectKind.IGNORE), late.effects.map { it.kind })
                }
            }
        }
    }

    @Test
    fun unavailableRecoveryRetiresCallbacks() {
        for (role in Role.entries) {
            val initial = State(role, enrolled = true, eligible = true, phase = Phase.RECOVERING, generation = 7)
            val unavailable = reduce(initial, Event.Availability(false, 7))
            assertEquals(Phase.IDLE, unavailable.state.phase)
            assertEquals(false, unavailable.state.eligible)
            assertEquals(8L, unavailable.state.generation)
            assertEquals(listOf(EffectKind.CANCEL_TIMERS, EffectKind.PUBLISH_STATUS), unavailable.effects.map { it.kind })
            val resumed = reduce(unavailable.state, Event.Availability(true, 8)).state
            assertEquals(Phase.RECOVERING, resumed.phase)
            assertEquals(resumed, reduce(resumed, Event.Timeout(Timer.PEER_SUSPECT, 60000, 7)).state)
        }
    }

    @Test
    fun sharedFixtures() {
        var steps = 0
        for (sc in fixture("transitions.json").getJSONArray("scenarios").objects()) {
            var s = stateOf(sc.getJSONObject("initial"))
            for ((i, step) in sc.getJSONArray("steps").objects().withIndex()) {
                val e = eventOf(step.getJSONObject("event"))
                val t = reduce(s, e)
                val where = "${sc.getString("name")} step $i (${step.getJSONObject("event").getString("kind")})"
                assertEquals(where, stateOf(step.getJSONObject("expected")), t.state)
                assertEquals(where, step.getJSONArray("effects").objects().map(::effectOf), t.effects)
                checkStep(s, e, t)?.let { fail("$where: $it") }
                s = t.state
                steps++
            }
        }
        assertTrue("only $steps fixture steps", steps >= 90)
    }

    // The same digests as the Go reducer over seeded random sequences: equivalent beyond the fixtures.
    @Test
    fun sameTransitionsAsGo() {
        val seqs = fixture("generated_sequences.json").getJSONArray("sequences").objects()
        assertTrue(seqs.size >= 20)
        for (q in seqs) {
            val sha = MessageDigest.getInstance("SHA-256")
            var s = stateOf(q.getJSONObject("initial"))
            for (e in q.getJSONArray("events").objects()) {
                val t = reduce(s, eventOf(e))
                sha.update((canonical(t) + "\n").toByteArray())
                s = t.state
            }
            val digest = sha.digest().joinToString("") { "%02x".format(it) }
            assertEquals("seed ${q.getLong("seed")}", q.getString("digest"), digest)
        }
    }

    // Random sequences on a fake monotonic clock: every step keeps the contract's safety rules.
    @Test
    fun generatedSequences() {
        for (seed in 1..300) {
            val r = Random(seed)
            val sim = Sim(r, State(if (seed % 2 == 0) Role.PHONE else Role.LAPTOP, enrolled = seed % 5 != 0))
            repeat(300) { i ->
                val err = if (r.nextInt(3) == 0) sim.advance(r.nextLong(20000)) else sim.apply(sim.randomEvent())
                if (err != null) fail("seed $seed step $i: $err")
            }
        }
    }

    // A healthy owner without a control path keeps the arms whatever the relay, the peer's silence, the timers
    // or the wall clock do.
    @Test
    fun healthyOwnerSurvivesRelayOutage() {
        for (role in Role.entries) {
            val r = Random(7)
            var s = State(role, enrolled = true, eligible = true, phase = Phase.ACTIVE, arms = Arms(true, true), rendererReady = true,
                generation = 4, transfer = "%032x".format(4), permission = Permission.RELEASED)
            repeat(2000) { i ->
                val g = s.generation
                val n = "%032x".format(100 + i)
                val e = when (r.nextInt(6)) {
                    0 -> Event.RouteLost(g, n)
                    1 -> Event.RouteRestored(g, n)
                    2 -> Event.PeerStatus(false, r.nextBoolean(), r.nextBoolean(), false, g, n)
                    3 -> Event.Timeout(listOf(Timer.PEER_SUSPECT, Timer.RETRY, Timer.COLD_START, Timer.STABILITY)[r.nextInt(4)], r.nextLong(120000), g, n)
                    4 -> Event.ClockAdjusted(r.nextLong(86400000), g, n)
                    else -> Event.PreferLocal(r.nextLong(10000), g, n)
                }
                val t = reduce(s, e)
                checkStep(s, e, t)?.let { fail("$role step $i: $it") }
                assertEquals("$role step $i: $e evicted the owner", Phase.ACTIVE, t.state.phase)
                assertEquals(s.generation, t.state.generation)
                s = t.state
            }
        }
    }

    // One arm only: rollback after the grace, never ready.
    @Test
    fun partialArmsRollBack() {
        var s = State(Role.PHONE, enrolled = true, eligible = true, phase = Phase.ACQUIRING, generation = 1,
            transfer = "1".repeat(32), permission = Permission.RELEASED)
        s = reduce(s, Event.ArmsChanged(true, false, 1)).state
        assertEquals(Phase.ACQUIRING, reduce(s, Event.Initialized(true, 1)).state.phase)
        val t = reduce(s, Event.Timeout(Timer.PARTIAL_ARMS, Policy.PARTIAL_ARMS_MS, 1))
        assertEquals(Phase.YIELDING, t.state.phase)
        assertEquals(ReleaseIntent.ROLLBACK, t.state.releaseIntent)
        assertTrue(t.effects.any { it.kind == EffectKind.RELEASE_BOTH })
    }

    // A restarted phone starts from a fresh State: an in-flight grant of the old process is ignored and the
    // first thing it does is reconcile.
    @Test
    fun restartDiscardsInFlightTransfer() {
        val t = reduce(State(Role.PHONE, enrolled = true), Event.Start(true))
        assertEquals(Phase.RECOVERING, t.state.phase)
        assertEquals(EffectKind.RECONCILE, t.effects.first().kind)
        val late = reduce(t.state, Event.Released("2".repeat(32), 0))
        assertEquals(listOf(EffectKind.IGNORE), late.effects.map { it.kind })
    }

    // Yielded or unavailable: no event without a new decision schedules a retry or acquisition.
    @Test
    fun noBusyRetryWhileYieldedOrUnavailable() {
        val idleOff = State(Role.PHONE, enrolled = true, eligible = false, phase = Phase.IDLE)
        for (timer in Timer.entries) for (elapsed in listOf(0L, 5000L, 60000L, 600000L)) {
            val t = reduce(idleOff, Event.Timeout(timer, elapsed, 0, "3".repeat(32)))
            assertTrue("$timer $elapsed: ${t.effects}", t.effects.none { it.kind == EffectKind.SCHEDULE || it.kind == EffectKind.ACQUIRE_BOTH })
        }
        val yielded = reduce(State(Role.PHONE, enrolled = true, eligible = true, phase = Phase.ACTIVE, arms = Arms(true, true),
            rendererReady = true, generation = 1, transfer = "1".repeat(32), permission = Permission.RELEASED, peerReachable = true),
            Event.ReleaseRequest("2".repeat(32), "preference", Role.LAPTOP, false, 1)).state
        val down = reduce(yielded, Event.ReleaseComplete(true, true, true, yielded.generation))
        assertEquals(Phase.IDLE, down.state.phase)
        assertTrue(down.effects.none { it.kind == EffectKind.SCHEDULE })
    }

    @Test
    fun retryDelayBounds() {
        var prev = 0L
        for (n in 0 until 40) {
            val d = Policy.retryDelayMs(n, 0.0)
            assertTrue(d >= prev && d <= Policy.RETRY_MAX_MS)
            val j = Policy.retryDelayMs(n, 1.0)
            assertTrue(j >= d && j <= (d * (1 + Policy.RETRY_JITTER_FRACTION)).toLong())
            prev = d
        }
        assertEquals(Policy.RETRY_INITIAL_MS, Policy.retryDelayMs(0, 0.0))
        assertEquals(Policy.RETRY_MAX_MS * 6 / 5, Policy.retryDelayMs(99, 2.0))
    }
}

// Safety checks of contract.py validate_state and validate_step: null when the step is fine.
internal fun checkStep(before: State, e: Event, t: Transition): String? {
    val a = t.state
    checkState(a)?.let { return "$it ($a)" }
    val names = t.effects.map { it.kind }
    if (names.isEmpty()) return "no effects"
    if (t.effects.any { it.generation != a.generation || it.transfer != a.transfer }) return "effect stamped with an old generation"
    if (e.generation != before.generation && (before != a || names != listOf(EffectKind.IGNORE))) return "stale event changed state"
    if (a.generation < before.generation) return "generation regressed"
    if (EffectKind.SEND_REQUEST in names) {
        if (!a.eligible || !a.peerReachable) return "request without eligibility or a control path"
        if (a.role == Role.PHONE && !a.pinned && a.peerEligible) return "phone overrode the laptop preference"
        if (a.role == Role.LAPTOP && (a.peerPinned || e !is Event.PreferLocal || e.stableMs < Policy.LAPTOP_STABILITY_MS)) return "unstable laptop preference"
    }
    if (a.transfer != before.transfer) {
        val incoming = (e as? Event.ReleaseRequest)?.transfer ?: (e as? Event.ReleaseNotice)?.transfer
        if (incoming != null) { if (a.transfer != incoming) return "wrong incoming transfer" }
        else if (a.transfer == null || a.transfer != e.attemptNonce) return "new transfer without the injected nonce"
    }
    if (EffectKind.ACQUIRE_BOTH in names) {
        if (a.phase != Phase.ACQUIRING || a.generation <= before.generation || a.transfer == null) return "unfenced acquisition"
        if (t.effects.none { it.timer == Timer.ACQUIRE }) return "acquisition without a deadline"
        val proof = e is Event.Released && e.transfer == before.transfer
        if (before.peerReachable && before.peerActive && !proof) return "acquisition before the owner released"
        if (a.permission == Permission.RELEASED && !proof) return "released permission without proof"
        if (a.permission == Permission.PROBE) {
            if (before.peerReachable || e !is Event.Timeout) return "probe with a control path"
            val extra = if (a.role == Role.PHONE) Policy.PHONE_PROBE_EXTRA_MS else 0
            val min = when (e.timer) {
                Timer.PEER_SUSPECT -> Policy.PEER_SUSPECT_MS + extra
                Timer.RETRY -> Policy.RETRY_INITIAL_MS + extra
                Timer.COLD_START -> if (a.role == Role.PHONE) Policy.PHONE_COLD_START_MS else Policy.LAPTOP_COLD_START_MS
                else -> return "probe on ${e.timer}"
            }
            if (e.elapsedMs < min || e.timer == Timer.COLD_START && before.peerActive) return "early probe"
        }
    }
    if (EffectKind.SEND_RELEASED in names) {
        if (e !is Event.ReleaseComplete || !e.bothDown || !e.workersStopped || !e.reconnectDisabled) return "released without confirmation"
        if (a.arms.any || a.rendererReady) return "released while holding links"
    }
    if (EffectKind.SEND_READY in names || EffectKind.REDRAW in names) {
        if (a.phase != Phase.ACTIVE || e !is Event.Initialized || !e.rendererReady) return "premature ready"
        if (EffectKind.SEND_READY in names && names.indexOf(EffectKind.REDRAW) > names.indexOf(EffectKind.SEND_READY)) return "ready before redraw"
    }
    if (EffectKind.RELEASE_BOTH in names && (a.generation <= before.generation || a.rendererReady)) return "release not fenced"
    if (e is Event.ClockAdjusted && (before != a || names != listOf(EffectKind.IGNORE))) return "wall clock changed ownership"
    if (e is Event.RouteLost && before.phase == Phase.ACTIVE && (a.phase != Phase.ACTIVE || EffectKind.RELEASE_BOTH in names)) return "partition evicted a healthy owner"
    return null
}

private fun checkState(s: State): String? = when {
    s.pinned && s.role != Role.PHONE -> "laptop pin"
    s.peerPinned && s.role != Role.LAPTOP -> "peer laptop pin"
    s.phase == Phase.ACTIVE && !(s.eligible && s.arms.both && s.rendererReady) -> "active before ready"
    s.phase == Phase.ACTIVE && s.permission == Permission.NONE -> "active without permission"
    (s.phase == Phase.ACTIVE || s.phase == Phase.ACQUIRING) && s.peerActive && s.peerReachable -> "two owners"
    s.rendererReady && s.phase != Phase.ACTIVE -> "renderer outside active"
    s.phase == Phase.ACQUIRING && (!s.eligible || s.permission == Permission.NONE) -> "acquiring without permission"
    s.permission == Permission.STANDALONE && s.enrolled -> "standalone on an enrolled host"
    (s.phase == Phase.IDLE || s.phase == Phase.REQUESTING || s.phase == Phase.RECOVERING) && (s.arms.any || s.rendererReady) -> "inactive state keeps resources"
    else -> null
}

// Drives reduce with a fake monotonic clock: schedule effects arm timers, cancel_timers clears them, advancing
// time fires them as timeout events.
private class Sim(val r: Random, var s: State) {
    var now = 0L
    val timers = mutableMapOf<Timer, Pair<Long, Long>>()
    var nonce = 0

    fun next() = "%032x".format(++nonce)

    fun apply(e: Event): String? {
        val before = s
        val t = reduce(before, e)
        checkStep(before, e, t)?.let { return "${e::class.simpleName}: $it" }
        if (t.state.phase == Phase.ACQUIRING && before.phase != Phase.ACQUIRING && t.effects.none { it.timer == Timer.ACQUIRE }) return "acquiring without a deadline"
        if (t.state.phase == Phase.YIELDING && before.phase != Phase.YIELDING && before.phase != Phase.IDLE && t.effects.none { it.timer == Timer.RELEASE }) return "releasing without a deadline"
        for (f in t.effects) when (f.kind) {
            EffectKind.SCHEDULE -> timers[f.timer!!] = now to f.delayMs
            EffectKind.CANCEL_TIMERS -> timers.clear()
            else -> {}
        }
        s = t.state
        return null
    }

    fun advance(ms: Long): String? {
        now += ms
        while (true) {
            val due = timers.filter { (_, v) -> now - v.first >= v.second }.minByOrNull { (_, v) -> v.first + v.second } ?: return null
            timers.remove(due.key)
            apply(Event.Timeout(due.key, now - due.value.first, s.generation, next()))?.let { return it }
        }
    }

    fun randomEvent(): Event {
        var g = s.generation
        if (r.nextInt(10) == 0 && g > 0) g = r.nextLong(g)
        val n = if (r.nextInt(4) == 0) null else next()
        val b = { r.nextBoolean() }
        val transfer = if (s.transfer == null || r.nextInt(3) == 0) next() else s.transfer!!
        return when (r.nextInt(17)) {
            0 -> Event.Start(b(), g, n)
            1 -> Event.Availability(b(), g, n)
            2 -> Event.PeerStatus(b(), b(), b(), b() && s.role == Role.LAPTOP, g, n)
            3 -> Event.PreferLocal(r.nextLong(6000), g, n)
            4 -> Event.ReleaseRequest(next(), if (b()) "manual" else "preference", if (b()) Role.PHONE else Role.LAPTOP, b(), g, n)
            5 -> Event.Released(transfer, g, n)
            6 -> Event.ReleaseNotice(next(), g, n)
            7 -> Event.ReleaseComplete(r.nextInt(4) > 0, r.nextInt(4) > 0, r.nextInt(4) > 0, g, n)
            8 -> Event.ArmsChanged(b(), b(), g, n)
            9 -> Event.Initialized(r.nextInt(4) > 0, g, n)
            10 -> Event.Timeout(Timer.entries[r.nextInt(Timer.entries.size)], r.nextLong(70000), g, n)
            11 -> Event.RouteLost(g, n)
            12 -> Event.RouteRestored(g, n)
            13 -> Event.Pin(b(), g, n)
            14 -> Event.ClockAdjusted(r.nextLong(172800000) - 86400000, g, n)
            15 -> Event.Failed("acquire_failed", g, n)
            else -> Event.Stop(g, n)
        }
    }
}

