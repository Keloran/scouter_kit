package main

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"sync"
	"time"

	h "scouterkit/examples/internal/handover"
	"scouterkit/examples/internal/localcontrol"
)

// The coordinator owns reducer state. Native operations run one at a time;
// cancelling an acquire never overlaps it with release or a second acquire.
type ownershipNative interface {
	Acquire(context.Context) (h.Arms, error)
	Release() error
	Readiness() h.Arms
}

type ownershipLink interface {
	Send(string, uint64, string, any) error
	Expect(string)
	Close()
}

type ownershipPacket struct {
	link    ownershipLink
	message *h.Message // nil: new connection; down: retired connection
	down    bool
}

type ownershipTimer struct {
	name       h.Timer
	generation uint64
	elapsed    int64
}

type ownershipResult struct {
	release    bool
	generation uint64
	arms       h.Arms
	err        error
}

type ownershipRequest struct {
	release bool
	reply   chan bool
}

type ownershipOperation struct {
	release    bool
	generation uint64
	cancel     context.CancelFunc
}

type ownershipClock struct {
	now   func() time.Time
	after func(time.Duration, func()) func()
}

func realOwnershipClock() ownershipClock {
	return ownershipClock{time.Now, func(d time.Duration, f func()) func() {
		t := time.AfterFunc(d, f)
		return func() { t.Stop() }
	}}
}

type ownershipController struct {
	native    ownershipNative
	power     <-chan powerEvent
	packets   chan ownershipPacket
	requests  chan ownershipRequest
	results   chan ownershipResult
	timeouts  chan ownershipTimer
	done      chan struct{}
	clock     ownershipClock
	jitter    func() float64
	mu        sync.RWMutex
	public    localcontrol.Status
	heartbeat h.HeartbeatBody

	state             h.State // everything below is used only by run
	peer              ownershipLink
	lastStatus        string
	powerState        powerState
	manual            bool
	eligibleSince     time.Time
	reason            string
	timers            map[h.Timer]func()
	op                *ownershipOperation
	releaseGeneration *uint64
	waiting           []ownershipRequest
	powerDone         []func()
	ctx               context.Context
	failures          int
}

func newOwnershipController(native ownershipNative, power <-chan powerEvent, enrolled bool) *ownershipController {
	c := &ownershipController{native: native, power: power, state: h.NewState(h.Laptop, enrolled),
		packets: make(chan ownershipPacket, 32), requests: make(chan ownershipRequest, 16),
		results: make(chan ownershipResult, 1), timeouts: make(chan ownershipTimer, 32),
		done: make(chan struct{}), clock: realOwnershipClock(), jitter: rand.Float64, timers: make(map[h.Timer]func()), reason: "startup"}
	c.publish()
	return c
}

func (c *ownershipController) Status() localcontrol.Status {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.public
}

func (c *ownershipController) Heartbeat() h.HeartbeatBody {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.heartbeat
}

func (c *ownershipController) Release(ctx context.Context) (bool, error) { return c.command(ctx, true) }
func (c *ownershipController) Resume(ctx context.Context) (bool, error)  { return c.command(ctx, false) }
func (c *ownershipController) Pin(context.Context, bool) (bool, error) {
	return false, errors.New("pin is available on the phone")
}

func (c *ownershipController) command(ctx context.Context, release bool) (bool, error) {
	r := ownershipRequest{release: release, reply: make(chan bool, 1)}
	select {
	case c.requests <- r:
	case <-ctx.Done():
		return false, ctx.Err()
	case <-c.done:
		return false, errors.New("controller stopped")
	}
	select {
	case done := <-r.reply:
		return done, nil
	case <-ctx.Done():
		return false, nil // accepted; continues after the client's deadline
	case <-c.done:
		return false, errors.New("controller stopped")
	}
}

func (c *ownershipController) publish() {
	owner := "none"
	s := c.state
	if s.Phase == h.Active || s.Arms.Left || s.Arms.Right {
		owner = "this"
	} else if s.PeerActive && s.PeerReachable {
		owner = "peer"
	} else if s.Enrolled && !s.PeerReachable {
		owner = "unknown"
	}
	c.mu.Lock()
	c.public = localcontrol.Status{Role: string(s.Role), Owner: owner, Phase: string(s.Phase),
		Eligible: s.Eligible, Released: c.manual, Reason: c.reason, Left: s.Arms.Left, Right: s.Arms.Right}
	c.heartbeat = h.HeartbeatBody{Phase: s.Phase, ReadyEpoch: s.Generation}
	c.mu.Unlock()
}

func (c *ownershipController) apply(kind h.EventKind, data h.EventData) {
	c.event(h.Event{Kind: kind, Generation: c.state.Generation, Data: data, AttemptNonce: h.NewNonce()})
}

func (c *ownershipController) event(e h.Event) {
	tr := h.Reduce(c.state, e)
	c.state = tr.State
	for _, fx := range tr.Effects {
		c.effect(fx)
	}
	c.publish()
	c.completeRequests()
}

func (c *ownershipController) send(kind, transfer string, body any) {
	if c.peer != nil {
		if err := c.peer.Send(kind, c.state.Generation, transfer, body); err != nil {
			c.peer.Close() // the route worker reports loss; never invent an acknowledgement
		}
	}
}

func (c *ownershipController) status() {
	s := c.state
	var transfer *string
	if s.Transfer != "" {
		t := s.Transfer
		transfer = &t
	}
	body := h.StatusBody{Role: h.Laptop, Phase: s.Phase, Eligible: s.Eligible,
		Arms: s.Arms, RendererReady: s.RendererReady, Transfer: transfer, ReadyEpoch: s.Generation}
	encoded, _ := json.Marshal(body)
	if c.peer == nil || string(encoded) == c.lastStatus {
		return
	}
	c.lastStatus = string(encoded)
	c.send("status", "", body)
}

func (c *ownershipController) effect(f h.Effect) {
	s := c.state
	switch f.Kind {
	case h.AcquireBoth:
		c.begin(false, f.Generation)
	case h.ReleaseBoth:
		if s.ReleaseIntent == h.Rollback {
			c.failures++
		}
		c.begin(true, f.Generation)
		if c.reason == "suspend" {
			c.schedule(h.Timer("suspend_abort"), f.Generation, 500*time.Millisecond)
		}
	case h.ForceDisconnect:
		if native, ok := c.native.(interface{ Abort() }); ok {
			native.Abort()
		}
		c.begin(true, f.Generation)
	case h.CancelTimers:
		for _, cancel := range c.timers {
			cancel()
		}
		clear(c.timers)
	case h.Schedule:
		delay := f.DelayMs
		if f.Timer == h.RetryTimer {
			delay = h.RetryDelayMs(max(c.failures-1, 0), c.jitter())
		}
		c.schedule(f.Timer, f.Generation, time.Duration(delay)*time.Millisecond)
	case h.Redraw:
		c.failures = 0 // the dashboard already completed its full repaint before Initialized
	case h.SendRequest:
		if c.peer != nil {
			c.peer.Expect(f.Transfer)
		}
		c.send("request", f.Transfer, h.RequestBody{Target: h.Laptop, Reason: "preference"})
		c.schedule(h.Timer("request_reply"), f.Generation, h.ReleaseDeadlineMs*time.Millisecond)
	case h.SendRelease:
		c.send("release", f.Transfer, h.ReleaseBody{Target: h.Phone, Reason: c.reason})
	case h.SendReleased:
		c.send("released", f.Transfer, h.ReleasedBody{Target: h.Phone, ReconnectDisabled: true, WorkersStopped: true})
	case h.SendAcquiring:
		c.send("acquiring", f.Transfer, h.AcquiringBody{Target: h.Laptop, Mode: string(s.Permission)})
	case h.SendReady:
		c.send("ready", f.Transfer, h.ReadyBody{Target: h.Laptop, Arms: s.Arms, RendererReady: true})
	case h.SendAbort:
		c.send("abort", f.Transfer, h.AbortBody{Reason: c.reason, ReleasedBoth: !s.Arms.Left && !s.Arms.Right})
	case h.PublishStatus, h.Reconcile:
		c.status()
	}
}

func (c *ownershipController) schedule(name h.Timer, generation uint64, delay time.Duration) {
	if cancel := c.timers[name]; cancel != nil {
		cancel()
	}
	started := c.clock.now()
	c.timers[name] = c.clock.after(delay, func() {
		select {
		case c.timeouts <- ownershipTimer{name, generation, c.clock.now().Sub(started).Milliseconds()}:
		case <-c.ctx.Done():
		}
	})
}

func (c *ownershipController) begin(release bool, generation uint64) {
	if c.op != nil {
		if release {
			c.op.cancel()
			g := generation
			c.releaseGeneration = &g
		}
		return
	}
	ctx, cancel := context.WithCancel(c.ctx)
	c.op = &ownershipOperation{release, generation, cancel}
	go func() {
		r := ownershipResult{release: release, generation: generation}
		if release {
			r.err = c.native.Release()
		} else {
			r.arms, r.err = c.native.Acquire(ctx)
		}
		c.results <- r // always join, including during shutdown
	}()
}

func (c *ownershipController) result(r ownershipResult) {
	c.op.cancel()
	c.op = nil
	if g := c.releaseGeneration; g != nil {
		c.releaseGeneration = nil
		c.begin(true, *g)
		return
	}
	if r.generation != c.state.Generation {
		return
	}
	if r.err != nil {
		c.reason = "acquire_failed"
		if !r.release {
			c.apply(h.Failed, h.EventData{Reason: c.reason})
		} else {
			c.publish()
		}
		return
	}
	if r.release {
		c.apply(h.ReleaseComplete, h.EventData{BothDown: true, WorkersStopped: true, ReconnectDisabled: true})
		for _, done := range c.powerDone {
			done()
		}
		c.powerDone = nil
		c.availability() // an immediate resume waits for the old session to be fully down
		if c.state.Phase == h.Idle && c.state.Eligible && c.state.Enrolled && !c.state.PeerReachable {
			// Loss while yielding could not start recovery until teardown completed.
			c.apply(h.RouteLost, h.EventData{})
		}
	} else {
		c.apply(h.ArmsChanged, h.EventData{Left: r.arms.Left, Right: r.arms.Right})
		c.apply(h.Initialized, h.EventData{RendererReady: true})
	}
}

func (c *ownershipController) availability() {
	eligible := !c.manual && c.powerState.eligible()
	wasEligible := c.state.Eligible
	if eligible && (c.state.Phase == h.Yielding || c.state.Phase == h.Degraded) {
		c.publish()
		return
	}
	if eligible && !c.state.Eligible {
		c.eligibleSince = c.clock.now()
	}
	c.apply(h.Availability, h.EventData{Eligible: eligible})
	if eligible && !wasEligible && c.state.Enrolled && c.state.Phase == h.Recovering && !c.state.PeerReachable {
		// A route already absent on resume will not emit a new loss callback.
		c.apply(h.RouteLost, h.EventData{})
	}
}

func (c *ownershipController) prefer() {
	stable := c.clock.now().Sub(c.eligibleSince).Milliseconds()
	if c.state.Phase != h.Idle || !c.state.Eligible || !c.state.PeerReachable {
		return
	}
	if stable < h.LaptopStabilityMs {
		c.schedule(h.StabilityTimer, c.state.Generation, time.Duration(h.LaptopStabilityMs-stable)*time.Millisecond)
		return
	}
	c.apply(h.PreferLocal, h.EventData{StableMs: stable})
}

func (c *ownershipController) packet(p ownershipPacket) {
	if p.down {
		if c.peer == p.link {
			c.peer = nil
			c.reason = "peer_lost"
			c.apply(h.RouteLost, h.EventData{})
		}
		return
	}
	if p.message == nil {
		if c.peer != nil {
			p.link.Close()
			return
		}
		c.peer = p.link
		c.lastStatus = ""
		c.apply(h.RouteRestored, h.EventData{})
		return
	}
	if c.peer != p.link {
		return
	}
	m := p.message
	transfer := ""
	if m.Transfer != nil {
		transfer = *m.Transfer
	}
	switch m.Kind {
	case "status":
		var b h.StatusBody
		json.Unmarshal(m.Body, &b)
		c.apply(h.PeerStatus, h.EventData{Reachable: true, Active: b.Phase == h.Active, Eligible: b.Eligible, Pinned: b.Pinned})
		if b.Pinned && c.state.Phase == h.Requesting {
			c.peer.Close()
		}
		c.prefer()
	case "request":
		var b h.RequestBody
		json.Unmarshal(m.Body, &b)
		c.apply(h.PeerStatus, h.EventData{Reachable: true, Active: c.state.PeerActive,
			Eligible: c.state.PeerEligible, Pinned: b.Pinned})
		c.reason = b.Reason
		c.apply(h.ReleaseRequest, h.EventData{Transfer: transfer, Target: b.Target, Reason: b.Reason, PeerPinned: b.Pinned})
	case "release":
		c.apply(h.ReleaseNotice, h.EventData{Transfer: transfer})
	case "released":
		c.apply(h.ReleasedEvent, h.EventData{Transfer: transfer})
	case "ready":
		c.apply(h.PeerStatus, h.EventData{Reachable: true, Active: true, Eligible: c.state.PeerEligible, Pinned: c.state.PeerPinned})
	case "heartbeat":
		var b h.HeartbeatBody
		json.Unmarshal(m.Body, &b)
		c.apply(h.PeerStatus, h.EventData{Reachable: true, Active: b.Phase == h.Active,
			Eligible: c.state.PeerEligible, Pinned: c.state.PeerPinned})
		c.prefer()
	case "availability":
		var b h.AvailabilityBody
		json.Unmarshal(m.Body, &b)
		c.apply(h.PeerStatus, h.EventData{Reachable: true, Active: c.state.PeerActive, Eligible: b.Eligible, Pinned: c.state.PeerPinned})
		c.prefer()
	}
}

func (c *ownershipController) completeRequests() {
	if c.op == nil && c.state.Phase == h.Idle {
		for _, done := range c.powerDone {
			done()
		}
		c.powerDone = nil
	}
	keep := c.waiting[:0]
	for _, r := range c.waiting {
		done := r.release && c.manual && c.op == nil && c.state.Phase == h.Idle ||
			!r.release && !c.manual && c.state.Phase == h.Active
		if done {
			r.reply <- true
		} else {
			keep = append(keep, r)
		}
	}
	c.waiting = keep
}

func (c *ownershipController) updatePower(p powerEvent) {
	next := p.State
	if p.Kind == powerUnavailable {
		// A lost observer cannot establish that a closed lid opened or sleep ended.
		if c.powerState.Lid == lidClosed {
			next.Lid = lidClosed
		}
		next.Sleeping = next.Sleeping || c.powerState.Sleeping
		next.ShuttingDown = next.ShuttingDown || c.powerState.ShuttingDown
	}
	c.powerState = next
}

func (c *ownershipController) run(ctx context.Context) {
	c.ctx = ctx
	defer close(c.done)
	defer func() {
		if native, ok := c.native.(interface{ Abort() }); ok {
			ended := make(chan struct{})
			timer := time.AfterFunc(500*time.Millisecond, func() { defer close(ended); native.Abort() })
			defer func() {
				if !timer.Stop() {
					<-ended
				}
			}()
		}
		for _, cancel := range c.timers {
			cancel()
		}
		if c.peer != nil {
			c.peer.Close()
		}
		if c.op != nil {
			c.op.cancel()
			<-c.results
		}
		c.native.Release()
		for _, done := range c.powerDone {
			done()
		}
	}()
	// Read the initial lid/suspend state before touching Bluetooth.
	select {
	case p, ok := <-c.power:
		if ok {
			c.updatePower(p)
			if p.Done != nil {
				c.powerDone = append(c.powerDone, p.Done)
			}
		}
	case <-ctx.Done():
		return
	}
	c.eligibleSince = c.clock.now()
	c.apply(h.Start, h.EventData{Eligible: c.powerState.eligible()})
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case p, ok := <-c.power:
			if !ok {
				c.power = nil
				continue
			}
			c.updatePower(p)
			if p.Done != nil {
				c.powerDone = append(c.powerDone, p.Done)
			}
			c.reason = "resume"
			if c.powerState.Sleeping || c.powerState.ShuttingDown {
				c.reason = "suspend"
			} else if c.powerState.Lid == lidClosed {
				c.reason = "lid_closed"
			}
			c.availability()
		case r := <-c.requests:
			for _, previous := range c.waiting {
				previous.reply <- false
			}
			c.waiting = nil
			c.manual, c.reason = r.release, "manual"
			c.waiting = append(c.waiting, r)
			c.availability()
			if r.release {
				c.apply(h.Stop, h.EventData{})
			}
			c.completeRequests()
		case r := <-c.results:
			c.result(r)
		case p := <-c.packets:
			c.packet(p)
		case t := <-c.timeouts:
			if t.generation != c.state.Generation {
				continue
			}
			delete(c.timers, t.name)
			if t.name == h.Timer("suspend_abort") {
				if c.state.Phase == h.Yielding || c.state.Phase == h.Degraded {
					if native, ok := c.native.(interface{ Abort() }); ok {
						native.Abort()
					}
				}
			} else if t.name == h.Timer("request_reply") {
				if c.state.Phase == h.Requesting && c.peer != nil {
					c.peer.Close()
				}
			} else if t.name == h.StabilityTimer {
				c.prefer()
			} else {
				c.event(h.Event{Kind: h.Timeout, Generation: t.generation, AttemptNonce: h.NewNonce(), Data: h.EventData{Timer: t.name, ElapsedMs: t.elapsed}})
			}
		case <-tick.C:
			if c.op == nil && (c.state.Phase == h.Active || c.state.Phase == h.Acquiring) {
				a := c.native.Readiness()
				if a != c.state.Arms {
					c.apply(h.ArmsChanged, h.EventData{Left: a.Left, Right: a.Right})
					if a.Left && a.Right {
						c.apply(h.Initialized, h.EventData{RendererReady: true})
					}
				}
			}
		}
	}
}
