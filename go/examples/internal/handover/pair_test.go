package handover

import (
	"container/heap"
	"fmt"
	"math/rand"
	"testing"
)

// CF-58: the laptop's and the phone's reducers against each other, with the glasses' two arms as the shared
// resource (an arm has at most one connected host), a lossy control link, native work that takes time, and
// faults injected at random: host crashes (at any point: after a release request, after the first arm, after
// native ready before the ack), partitions, lid and suspend changes, manual pin and stop, native failures.
// After the faults stop, the hosts must agree on one active owner holding both arms, the other holding none.
// What the runtime adapters add (status exchange, prefer_local, generation-stamped callbacks) is modelled here as
// they do it; the hardware's own limit is stated in the tests' comments, not claimed away.

type pairEvent struct {
	at  int64
	seq int
	run func()
}

type pairQueue []pairEvent

func (q pairQueue) Len() int { return len(q) }
func (q pairQueue) Less(i, j int) bool {
	return q[i].at < q[j].at || q[i].at == q[j].at && q[i].seq < q[j].seq
}
func (q pairQueue) Swap(i, j int)         { q[i], q[j] = q[j], q[i] }
func (q *pairQueue) Push(x any)           { *q = append(*q, x.(pairEvent)) }
func (q *pairQueue) Pop() any             { o := *q; e := o[len(o)-1]; *q = o[:len(o)-1]; return e }
func (w *world) after(ms int64, f func()) { w.seq++; heap.Push(&w.q, pairEvent{w.now + ms, w.seq, f}) }

type host struct {
	s           State
	life        int // incremented by a crash: callbacks of an earlier process are dropped
	up          bool
	timers      map[Timer]int64 // timer -> scheduled at (by the delay in the effect)
	timerGen    map[Timer]int
	nativeGen   int  // native work epoch: a release or crash ends the connects of the previous acquire
	connecting  bool // the library keeps trying both arms until released
	eligibleAt  int64
	wantPinned  bool // the user's choice (phone): survives a crash, as the settings store does
	wantStopped bool // the user's stop: survives a crash
	lidOpen     bool // laptop: lid open and awake; phone: app usable
}

type world struct {
	t       *testing.T
	r       *rand.Rand
	now     int64
	seq     int
	q       pairQueue
	h       [2]*host // 0 laptop, 1 phone
	arm     [2]int   // owner per arm: -1, 0, 1
	armLife [2]int   // the owner's process run that connected it
	link    bool
	linkGen int
	nonce   int
	log     []string
	faults  bool
}

func newWorld(t *testing.T, seed int64) *world {
	w := &world{t: t, r: rand.New(rand.NewSource(seed)), arm: [2]int{-1, -1}, link: true}
	for i, role := range []Role{Laptop, Phone} {
		w.h[i] = &host{s: NewState(role, true), timers: map[Timer]int64{}, timerGen: map[Timer]int{}, lidOpen: true}
	}
	return w
}

func (w *world) fresh() string { w.nonce++; return fmt.Sprintf("%032x", w.nonce) }

func (w *world) logf(f string, a ...any) {
	w.log = append(w.log, fmt.Sprintf("%7d ", w.now)+fmt.Sprintf(f, a...))
	if len(w.log) > 60 {
		w.log = w.log[1:]
	}
}

func (w *world) fail(f string, a ...any) {
	w.t.Helper()
	for _, l := range w.log {
		w.t.Log(l)
	}
	w.t.Fatalf(f, a...)
}

func (w *world) eligible(i int) bool {
	h := w.h[i]
	return h.up && h.lidOpen && !h.wantStopped
}

// apply runs one event through host i's reducer and carries out the effects as the runtime adapters do.
func (w *world) apply(i int, e Event) {
	h := w.h[i]
	if !h.up {
		return
	}
	before := h.s
	tr := Reduce(before, e)
	if err := checkStep(before, e, tr); err != nil {
		w.fail("%s %s: %v", before.Role, e.Kind, err)
	}
	if err := bounded(before, tr); err != nil {
		w.fail("%s %s: %v", before.Role, e.Kind, err)
	}
	h.s = tr.State
	if before.Phase != h.s.Phase {
		w.logf("%s %s: %s -> %s gen %d", h.s.Role, e.Kind, before.Phase, h.s.Phase, h.s.Generation)
	}
	for _, f := range tr.Effects {
		w.effect(i, f)
	}
}

func (w *world) effect(i int, f Effect) {
	h, p := w.h[i], 1-i
	gen, life := h.s.Generation, h.life
	switch f.Kind {
	case Schedule:
		h.timers[f.Timer] = w.now
		h.timerGen[f.Timer]++
		tg := h.timerGen[f.Timer]
		w.after(f.DelayMs, func() {
			if h.life != life || h.timerGen[f.Timer] != tg {
				return
			}
			at, ok := h.timers[f.Timer]
			if !ok {
				return
			}
			delete(h.timers, f.Timer)
			w.apply(i, Event{Kind: Timeout, Generation: h.s.Generation, AttemptNonce: w.fresh(), Data: EventData{Timer: f.Timer, ElapsedMs: w.now - at}})
		})
	case CancelTimers:
		clear(h.timers)
	case SendRequest:
		reason := "preference"
		if h.s.Pinned {
			reason = "manual"
		}
		w.send(i, Event{Kind: ReleaseRequest, Data: EventData{Transfer: f.Transfer, Target: h.s.Role, Reason: reason, PeerPinned: h.s.Pinned}})
	case SendRelease:
		w.send(i, Event{Kind: ReleaseNotice, Data: EventData{Transfer: f.Transfer}})
	case SendReleased:
		if w.arm[0] == i || w.arm[1] == i {
			w.fail("%s sent released while holding an arm", h.s.Role)
		}
		w.send(i, Event{Kind: ReleasedEvent, Data: EventData{Transfer: f.Transfer}})
		w.sendStatus(i)
	case SendAcquiring, SendReady, SendAbort, PublishStatus, Reconcile:
		w.sendStatus(i)
	case AcquireBoth:
		if !w.eligible(i) {
			w.fail("%s acquires while stopped, suspended or closed (no automatic retry after an intentional yield)", h.s.Role)
		}
		h.nativeGen++
		h.connecting = true
		w.connect(i, h.nativeGen, gen)
	case ReleaseBoth:
		h.nativeGen++
		h.connecting = false
		ng := h.nativeGen
		w.after(100+w.r.Int63n(1500), func() { // renderer stop, then both links down
			if h.life != life || h.nativeGen != ng {
				return
			}
			w.freeArms(i)
			w.apply(i, Event{Kind: ReleaseComplete, Generation: gen, Data: EventData{BothDown: true, WorkersStopped: true, ReconnectDisabled: true}})
		})
	case ForceDisconnect:
		h.nativeGen++
		h.connecting = false
		w.freeArms(i)
		ng := h.nativeGen
		w.after(50, func() {
			if h.life == life && h.nativeGen == ng {
				w.apply(i, Event{Kind: ReleaseComplete, Generation: h.s.Generation, Data: EventData{BothDown: true, WorkersStopped: true, ReconnectDisabled: true}})
			}
		})
	}
	_ = p
}

// connect: the library connects each free arm after a while and keeps retrying an arm another host holds.
func (w *world) connect(i, ng int, gen uint64) {
	h, life := w.h[i], w.h[i].life
	var try func()
	try = func() {
		if h.life != life || h.nativeGen != ng || !h.connecting {
			return
		}
		changed := false
		for a := range w.arm {
			if w.arm[a] == -1 && w.r.Intn(3) > 0 {
				w.arm[a], w.armLife[a] = i, h.life
				changed = true
			}
		}
		if changed {
			w.apply(i, Event{Kind: ArmsChanged, Generation: gen, Data: EventData{Left: w.arm[0] == i, Right: w.arm[1] == i}})
			if w.arm[0] == i && w.arm[1] == i {
				w.after(200+w.r.Int63n(800), func() { // fonts, first frame
					if h.life == life && h.nativeGen == ng {
						w.apply(i, Event{Kind: Initialized, Generation: gen, Data: EventData{RendererReady: true}})
					}
				})
				return
			}
		}
		w.after(300+w.r.Int63n(1700), try)
	}
	w.after(200+w.r.Int63n(1500), try)
}

func (w *world) freeArms(i int) {
	for a := range w.arm {
		if w.arm[a] == i {
			w.arm[a] = -1
		}
	}
}

// send delivers one message in order after a delay, unless the link drops first or a host restarts.
func (w *world) send(i int, e Event) {
	if !w.link {
		return
	}
	lg, from, to := w.linkGen, w.h[i].life, w.h[1-i].life
	w.after(10+w.r.Int63n(300), func() {
		if w.linkGen != lg || w.h[i].life != from || w.h[1-i].life != to || !w.link {
			return
		}
		e.Generation, e.AttemptNonce = w.h[1-i].s.Generation, w.fresh()
		w.apply(1-i, e)
	})
}

func (w *world) sendStatus(i int) {
	s := w.h[i].s
	w.send(i, Event{Kind: PeerStatus, Data: EventData{Active: s.Phase == Active, Reachable: true, Eligible: s.Eligible, Pinned: s.Pinned}})
}

// tick: what the coordinators do each second: prefer_local with the stable time while idle and eligible.
func (w *world) tick() {
	for i, h := range w.h {
		if h.up && h.s.Phase == Idle && h.s.Eligible {
			w.apply(i, Event{Kind: PreferLocal, Generation: h.s.Generation, AttemptNonce: w.fresh(), Data: EventData{StableMs: w.now - h.eligibleAt}})
		}
	}
	w.after(1000, w.tick)
}

func (w *world) start(i int) {
	h := w.h[i]
	if h.up {
		return // already restarted (the quiet phase may start it before its own restart is due)
	}
	h.up = true
	h.s = NewState(h.s.Role, true)
	h.s.Pinned = h.wantPinned && h.s.Role == Phone
	h.eligibleAt = w.now
	w.apply(i, Event{Kind: Start, AttemptNonce: w.fresh(), Data: EventData{Eligible: w.eligible(i)}})
	if w.link && w.h[1-i].up {
		w.apply(i, Event{Kind: RouteRestored, Generation: h.s.Generation})
		w.apply(1-i, Event{Kind: RouteRestored, Generation: w.h[1-i].s.Generation})
		w.sendStatus(1 - i)
	}
}

// crash: the process dies; the OS drops its Bluetooth links after a moment; it starts again later, fresh
// (only the enrollment and the user's pin and stop choices persist: never an ownership grant).
func (w *world) crash(i int) {
	h := w.h[i]
	if !h.up {
		return
	}
	w.logf("%s crash in %s", h.s.Role, h.s.Phase)
	h.up, h.connecting = false, false
	old := h.life
	h.life++
	h.nativeGen++
	clear(h.timers)
	w.after(w.r.Int63n(3000), func() { // the dead process's links only
		for a := range w.arm {
			if w.arm[a] == i && w.armLife[a] == old {
				w.arm[a] = -1
			}
		}
	})
	if w.link && w.h[1-i].up {
		w.after(500, func() { w.apply(1-i, Event{Kind: RouteLost, Generation: w.h[1-i].s.Generation}) })
	}
	w.after(500+w.r.Int63n(8000), func() { w.start(i) })
}

func (w *world) setLink(up bool) {
	if w.link == up {
		return
	}
	w.link = up
	w.linkGen++
	w.logf("link %v", up)
	for i := range w.h {
		if up {
			w.apply(i, Event{Kind: RouteRestored, Generation: w.h[i].s.Generation})
		} else {
			w.apply(i, Event{Kind: RouteLost, Generation: w.h[i].s.Generation})
		}
	}
	if up {
		for i := range w.h {
			if w.h[i].up {
				w.sendStatus(i)
			}
		}
	}
}

func (w *world) availability(i int) {
	h := w.h[i]
	el := w.eligible(i)
	if el && !h.s.Eligible {
		h.eligibleAt = w.now
	}
	w.apply(i, Event{Kind: Availability, Generation: h.s.Generation, AttemptNonce: w.fresh(), Data: EventData{Eligible: el}})
}

func (w *world) fault() {
	if !w.faults {
		return
	}
	i := w.r.Intn(2)
	h := w.h[i]
	switch w.r.Intn(9) {
	case 0, 1:
		w.crash(i)
	case 2:
		w.setLink(!w.link)
	case 3, 4:
		h.lidOpen = !h.lidOpen
		w.availability(i)
	case 5:
		if h.up && h.s.Role == Phone {
			h.wantPinned = !h.wantPinned
			w.apply(i, Event{Kind: Pin, Generation: h.s.Generation, AttemptNonce: w.fresh(), Data: EventData{Enabled: h.wantPinned}})
		}
	case 6:
		h.wantStopped = !h.wantStopped
		if h.wantStopped {
			w.apply(i, Event{Kind: Stop, Generation: h.s.Generation})
		} else {
			w.availability(i)
		}
	case 7:
		if h.up && (h.s.Phase == Acquiring || h.s.Phase == Active) {
			w.apply(i, Event{Kind: Failed, Generation: h.s.Generation, Data: EventData{Reason: "acquire_failed"}})
		}
	case 8:
		// A momentary arm drop while active (out of range): the library reconnects within the grace.
		for a := range w.arm {
			if w.arm[a] == i && h.s.Phase == Active {
				w.arm[a] = -1
				w.apply(i, Event{Kind: ArmsChanged, Generation: h.s.Generation, Data: EventData{Left: w.arm[0] == i, Right: w.arm[1] == i}})
				if h.connecting {
					w.connect(i, h.nativeGen, h.s.Generation)
				}
				break
			}
		}
	}
	w.after(100+w.r.Int63n(4000), w.fault)
}

func (w *world) runUntil(t int64) {
	for w.q.Len() > 0 && w.q[0].at <= t {
		e := heap.Pop(&w.q).(pairEvent)
		w.now = e.at
		e.run()
	}
	w.now = t
}

// The expected owner once quiet: the laptop while eligible unless the phone is pinned and eligible; else the
// phone if eligible; else nobody.
func (w *world) expected() int {
	le, pe := w.eligible(0), w.eligible(1)
	switch {
	case le && !(pe && w.h[1].wantPinned):
		return 0
	case pe:
		return 1
	}
	return -1
}

var pairSeeds = int64(300)

func TestPairConverges(t *testing.T) {
	for seed := int64(1); seed <= pairSeeds; seed++ {
		w := newWorld(t, seed)
		w.faults = true
		w.start(0)
		w.after(w.r.Int63n(3000), func() { w.start(1) }) // cold starts in either order, sometimes together
		w.after(1000, w.tick)
		w.after(500, w.fault)
		w.runUntil(60_000 + w.r.Int63n(120_000))
		// Quiet: no more faults, the link up, both processes running, the user's choices as they are.
		w.faults = false
		for i := range w.h {
			if !w.h[i].up {
				w.start(i)
			}
		}
		w.setLink(true)
		w.runUntil(w.now + 180_000)
		owner := w.expected()
		for i, h := range w.h {
			holds := w.arm[0] == i || w.arm[1] == i
			if i == owner && (h.s.Phase != Active || w.arm[0] != i || w.arm[1] != i) {
				w.fail("seed %d: %s should own both arms: %+v arms %v", seed, h.s.Role, h.s, w.arm)
			}
			if i != owner && (h.s.Phase == Active || holds) {
				w.fail("seed %d: %s holds the glasses but %d should: %+v arms %v", seed, h.s.Role, owner, h.s, w.arm)
			}
		}
	}
}
