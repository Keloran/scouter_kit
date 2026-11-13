package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	h "scouterkit/examples/internal/handover"
	"scouterkit/examples/internal/localcontrol"
)

type ownershipTestClock struct {
	mu    sync.Mutex
	now   time.Time
	next  int
	tasks map[int]struct {
		at time.Time
		f  func()
	}
}

func newOwnershipTestClock() *ownershipTestClock {
	return &ownershipTestClock{now: time.Unix(100, 0), tasks: make(map[int]struct {
		at time.Time
		f  func()
	})}
}
func (f *ownershipTestClock) clock() ownershipClock {
	return ownershipClock{func() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now },
		func(d time.Duration, fn func()) func() {
			f.mu.Lock()
			f.next++
			id := f.next
			f.tasks[id] = struct {
				at time.Time
				f  func()
			}{f.now.Add(d), fn}
			f.mu.Unlock()
			return func() { f.mu.Lock(); delete(f.tasks, id); f.mu.Unlock() }
		}}
}
func (f *ownershipTestClock) advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	var calls []func()
	for id, task := range f.tasks {
		if !task.at.After(f.now) {
			calls = append(calls, task.f)
			delete(f.tasks, id)
		}
	}
	f.mu.Unlock()
	for _, fn := range calls {
		fn()
	}
}

type ownershipTestNative struct {
	mu                           sync.Mutex
	ready                        h.Arms
	acquires, releases, overlaps int
	busy                         bool
	acquire                      func(context.Context) error
	releaseGate                  <-chan struct{}
	releaseStarted               chan struct{}
}

func (n *ownershipTestNative) Acquire(ctx context.Context) (h.Arms, error) {
	n.mu.Lock()
	if n.busy {
		n.overlaps++
	}
	n.busy = true
	n.acquires++
	fn := n.acquire
	n.mu.Unlock()
	var err error
	if fn != nil {
		err = fn(ctx)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.busy = false
	if err == nil {
		n.ready = h.Arms{Left: true, Right: true}
	}
	return n.ready, err
}
func (n *ownershipTestNative) Release() error {
	n.mu.Lock()
	if n.busy {
		n.overlaps++
	}
	n.busy = true
	n.releases++
	gate, started := n.releaseGate, n.releaseStarted
	n.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if gate != nil {
		<-gate
	}
	n.mu.Lock()
	n.ready = h.Arms{}
	n.busy = false
	n.mu.Unlock()
	return nil
}
func (n *ownershipTestNative) Readiness() h.Arms { n.mu.Lock(); defer n.mu.Unlock(); return n.ready }
func (n *ownershipTestNative) counts() (int, int, int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.acquires, n.releases, n.overlaps
}

type ownershipTestLink struct {
	mu      sync.Mutex
	sent    []h.Message
	closed  bool
	onClose func()
}

func (l *ownershipTestLink) Send(kind string, epoch uint64, transfer string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return errors.New("closed")
	}
	m := h.Message{Kind: kind, Epoch: epoch, Body: b}
	if transfer != "" {
		m.Transfer = &transfer
	}
	l.sent = append(l.sent, m)
	return nil
}
func (l *ownershipTestLink) Expect(string) {}
func (l *ownershipTestLink) Close() {
	l.mu.Lock()
	was := l.closed
	l.closed = true
	fn := l.onClose
	l.mu.Unlock()
	if !was && fn != nil {
		fn()
	}
}
func (l *ownershipTestLink) messages(kind string) []h.Message {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []h.Message
	for _, m := range l.sent {
		if m.Kind == kind {
			out = append(out, m)
		}
	}
	return out
}

func awaitOwnership(t *testing.T, c *ownershipController, condition func(localcontrol.Status) bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if condition(c.Status()) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("ownership condition not met: %+v", c.Status())
}
func startOwnershipTest(t *testing.T, n *ownershipTestNative, enrolled, open bool) (*ownershipController, chan powerEvent, *ownershipTestClock) {
	t.Helper()
	power := make(chan powerEvent, 8)
	state := powerState{Lid: lidClosed}
	if open {
		state.Lid = lidOpen
	}
	power <- powerEvent{Kind: powerLid, State: state}
	c := newOwnershipController(n, power, enrolled)
	clock := newOwnershipTestClock()
	c.clock = clock.clock()
	c.jitter = func() float64 { return 0 }
	ctx, cancel := context.WithCancel(context.Background())
	go c.run(ctx)
	t.Cleanup(func() {
		cancel()
		select {
		case <-c.done:
		case <-time.After(3 * time.Second):
			t.Error("controller did not join teardown")
		}
		_, _, overlap := n.counts()
		if overlap != 0 {
			t.Errorf("%d overlapping native operations", overlap)
		}
	})
	return c, power, clock
}
func peerOwnershipMessage(c *ownershipController, l *ownershipTestLink, kind, transfer string, body any) {
	b, _ := json.Marshal(body)
	m := &h.Message{Kind: kind, Body: b}
	if transfer != "" {
		m.Transfer = &transfer
	}
	c.packets <- ownershipPacket{link: l, message: m}
}
func preferredOwnershipPhone(t *testing.T, n *ownershipTestNative) (*ownershipController, *ownershipTestLink, *ownershipTestClock) {
	t.Helper()
	c, power, clock := startOwnershipTest(t, n, true, false)
	l := &ownershipTestLink{}
	l.onClose = func() { c.packets <- ownershipPacket{link: l, down: true} }
	c.packets <- ownershipPacket{link: l}
	peerOwnershipMessage(c, l, "status", "", h.StatusBody{Role: h.Phone, Phase: h.Active, Eligible: true, Arms: h.Arms{Left: true, Right: true}, RendererReady: true})
	awaitOwnership(t, c, func(s localcontrol.Status) bool { return s.Owner == "peer" })
	power <- powerEvent{Kind: powerLid, State: powerState{Lid: lidOpen}}
	awaitOwnership(t, c, func(s localcontrol.Status) bool { return s.Eligible })
	peerOwnershipMessage(c, l, "status", "", h.StatusBody{Role: h.Phone, Phase: h.Active, Eligible: true})
	awaitOwnership(t, c, func(s localcontrol.Status) bool { return s.Phase == string(h.Idle) })
	clock.advance(2999 * time.Millisecond)
	if a, _, _ := n.counts(); a != 0 {
		t.Fatal("acquired before stability and peer release")
	}
	clock.advance(time.Millisecond)
	awaitOwnership(t, c, func(s localcontrol.Status) bool { return s.Phase == string(h.Requesting) })
	return c, l, clock
}

func TestOwnershipStandaloneManualRelease(t *testing.T) {
	n := &ownershipTestNative{}
	c, _, clock := startOwnershipTest(t, n, false, true)
	awaitOwnership(t, c, func(s localcontrol.Status) bool { return s.Phase == string(h.Active) })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if done, err := c.Release(ctx); err != nil || !done {
		t.Fatalf("release: %v %v", done, err)
	}
	clock.advance(time.Hour)
	if a, _, _ := n.counts(); a != 1 {
		t.Fatal("intentionally released host retried")
	}
	if done, err := c.Resume(ctx); err != nil || !done {
		t.Fatalf("resume: %v %v", done, err)
	}
}

func TestOwnershipSuspendDuringAcquire(t *testing.T) {
	started, cancelled, cleanup := make(chan struct{}), make(chan struct{}), make(chan struct{})
	n := &ownershipTestNative{acquire: func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		close(cancelled)
		<-cleanup
		return ctx.Err()
	}}
	c, power, _ := startOwnershipTest(t, n, false, true)
	defer close(cleanup)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("acquire did not start")
	}
	done := make(chan struct{}, 1)
	power <- powerEvent{Kind: powerSleep, State: powerState{Lid: lidOpen, Sleeping: true}, Done: func() { done <- struct{}{} }}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("power event did not cancel acquisition")
	}
	if _, releases, _ := n.counts(); releases != 0 {
		t.Fatal("release overlapped unfinished acquire cleanup")
	}
	select {
	case <-done:
		t.Fatal("inhibitor acknowledged before native cleanup")
	default:
	}
	cleanup <- struct{}{} // finish cancelled Init before releasing
	awaitOwnership(t, c, func(s localcontrol.Status) bool { return s.Phase == string(h.Idle) && !s.Eligible })
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("completed teardown did not drop inhibitor")
	}
}

func TestOwnershipImmediateResumeWaitsForRelease(t *testing.T) {
	gate := make(chan struct{})
	n := &ownershipTestNative{releaseGate: gate, releaseStarted: make(chan struct{}, 1)}
	c, power, _ := startOwnershipTest(t, n, false, true)
	defer close(gate)
	awaitOwnership(t, c, func(s localcontrol.Status) bool { return s.Phase == string(h.Active) })
	power <- powerEvent{Kind: powerSleep, State: powerState{Lid: lidOpen, Sleeping: true}}
	select {
	case <-n.releaseStarted:
	case <-time.After(time.Second):
		t.Fatal("release did not start")
	}
	power <- powerEvent{Kind: powerResume, State: powerState{Lid: lidOpen}}
	time.Sleep(10 * time.Millisecond)
	if a, _, _ := n.counts(); a != 1 {
		t.Fatal("resume raced the old release")
	}
	gate <- struct{}{}
	awaitOwnership(t, c, func(s localcontrol.Status) bool { a, _, _ := n.counts(); return s.Phase == string(h.Active) && a == 2 })
}

func TestOwnershipPeerReleaseProof(t *testing.T) {
	n := &ownershipTestNative{}
	c, l, _ := preferredOwnershipPhone(t, n)
	requests := l.messages("request")
	if len(requests) != 1 {
		t.Fatalf("requests: %d", len(requests))
	}
	transfer := *requests[0].Transfer
	peerOwnershipMessage(c, l, "released", "22222222222222222222222222222222", h.ReleasedBody{})
	time.Sleep(10 * time.Millisecond)
	if a, _, _ := n.counts(); a != 0 {
		t.Fatal("wrong transfer authorized acquisition")
	}
	peerOwnershipMessage(c, l, "release", transfer, h.ReleaseBody{Target: h.Laptop, Reason: "preference"})
	peerOwnershipMessage(c, l, "released", transfer, h.ReleasedBody{Target: h.Laptop, WorkersStopped: true, ReconnectDisabled: true})
	awaitOwnership(t, c, func(s localcontrol.Status) bool { return s.Phase == string(h.Active) && s.Left && s.Right })
	if len(l.messages("ready")) != 1 {
		t.Fatal("no ready after both arms and renderer")
	}
}

func TestOwnershipPeerTimeoutRetiresAttempt(t *testing.T) {
	n := &ownershipTestNative{}
	c, l, clock := preferredOwnershipPhone(t, n)
	old := *l.messages("request")[0].Transfer
	clock.advance(h.ReleaseDeadlineMs * time.Millisecond)
	awaitOwnership(t, c, func(s localcontrol.Status) bool { return s.Phase == string(h.Recovering) })
	peerOwnershipMessage(c, l, "released", old, h.ReleasedBody{Target: h.Laptop, WorkersStopped: true, ReconnectDisabled: true})
	clock.advance((h.PeerSuspectMs - 1) * time.Millisecond)
	if a, _, _ := n.counts(); a != 0 {
		t.Fatal("probe before complete suspect interval")
	}
	clock.advance(time.Millisecond)
	awaitOwnership(t, c, func(s localcontrol.Status) bool { return s.Phase == string(h.Active) })
}

func TestOwnershipReleaseAcknowledgementAfterTeardown(t *testing.T) {
	n := &ownershipTestNative{}
	c, power, _ := startOwnershipTest(t, n, false, true)
	_ = power
	awaitOwnership(t, c, func(s localcontrol.Status) bool { return s.Phase == string(h.Active) })
	gate := make(chan struct{})
	defer close(gate)
	n.mu.Lock()
	n.releaseGate = gate
	n.releaseStarted = make(chan struct{}, 1)
	n.mu.Unlock()
	// The state is enrolled only in the separate peer driver test below; manual
	// release must also wait for native teardown without any phone installed.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if done, err := c.Release(ctx); err != nil || done {
		t.Fatalf("unfinished release: %v %v", done, err)
	}
	if s := c.Status(); s.Phase != string(h.Yielding) || !s.Released {
		t.Fatalf("false completed release: %+v", s)
	}
	gate <- struct{}{}
	awaitOwnership(t, c, func(s localcontrol.Status) bool { return s.Phase == string(h.Idle) && !s.Left && !s.Right })
}

func TestOwnershipStatusDoesNotEcho(t *testing.T) {
	c, _, _ := startOwnershipTest(t, &ownershipTestNative{}, true, false)
	l := &ownershipTestLink{}
	c.packets <- ownershipPacket{link: l}
	for i := 0; i < 20; i++ {
		peerOwnershipMessage(c, l, "status", "", h.StatusBody{Role: h.Phone, Phase: h.Active, Eligible: true})
	}
	awaitOwnership(t, c, func(s localcontrol.Status) bool { return s.Owner == "peer" })
	time.Sleep(20 * time.Millisecond)
	if count := len(l.messages("status")); count > 2 {
		t.Fatalf("status echo loop: %d replies", count)
	}
}

func TestOwnershipAcquisitionFailureBackoff(t *testing.T) {
	n := &ownershipTestNative{acquire: func(context.Context) error { return errors.New("unavailable") }}
	c, _, clock := startOwnershipTest(t, n, false, true)
	awaitOwnership(t, c, func(s localcontrol.Status) bool {
		a, r, _ := n.counts()
		return s.Phase == string(h.Recovering) && a == 1 && r == 1
	})
	clock.advance(4999 * time.Millisecond)
	if a, _, _ := n.counts(); a != 1 {
		t.Fatal("early first retry")
	}
	clock.advance(time.Millisecond)
	awaitOwnership(t, c, func(s localcontrol.Status) bool {
		a, r, _ := n.counts()
		return s.Phase == string(h.Recovering) && a == 2 && r == 2
	})
	clock.advance(9999 * time.Millisecond)
	if a, _, _ := n.counts(); a != 2 {
		t.Fatal("failure backoff did not double")
	}
	clock.advance(time.Millisecond)
	awaitOwnership(t, c, func(s localcontrol.Status) bool {
		a, r, _ := n.counts()
		return s.Phase == string(h.Recovering) && a == 3 && r == 3
	})
}

func TestOwnershipPartialLossRollsBackBoth(t *testing.T) {
	n := &ownershipTestNative{}
	c, _, clock := startOwnershipTest(t, n, false, true)
	awaitOwnership(t, c, func(s localcontrol.Status) bool { return s.Phase == string(h.Active) })
	n.mu.Lock()
	n.ready.Right = false
	n.mu.Unlock()
	awaitOwnership(t, c, func(s localcontrol.Status) bool { return s.Phase == string(h.Acquiring) && !s.Right })
	clock.advance(1999 * time.Millisecond)
	if _, r, _ := n.counts(); r != 0 {
		t.Fatal("partial-arm grace cut short")
	}
	clock.advance(time.Millisecond)
	awaitOwnership(t, c, func(s localcontrol.Status) bool {
		_, r, _ := n.counts()
		return s.Phase == string(h.Recovering) && r == 1 && !s.Left && !s.Right
	})
}

func TestOwnershipManualReleaseDuringBackoff(t *testing.T) {
	var calls atomic.Int32
	n := &ownershipTestNative{acquire: func(context.Context) error {
		if calls.Add(1) == 1 {
			return errors.New("first acquisition failed")
		}
		return nil
	}}
	c, _, clock := startOwnershipTest(t, n, false, true)
	awaitOwnership(t, c, func(s localcontrol.Status) bool { return s.Phase == string(h.Recovering) })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if done, err := c.Release(ctx); err != nil || !done {
		t.Fatalf("release during backoff did not finish: %v %v", done, err)
	}
	clock.advance(time.Hour)
	if a, _, _ := n.counts(); a != 1 {
		t.Fatal("retry ran while manually released")
	}
	if done, err := c.Resume(ctx); err != nil || !done {
		t.Fatalf("resume after retired retry: %v %v", done, err)
	}
}

func TestOwnershipOfflineResumeProbes(t *testing.T) {
	n := &ownershipTestNative{}
	c, power, clock := startOwnershipTest(t, n, true, false)
	for attempt := 1; attempt <= 2; attempt++ {
		power <- powerEvent{Kind: powerResume, State: powerState{Lid: lidOpen}}
		awaitOwnership(t, c, func(s localcontrol.Status) bool { return s.Eligible && s.Phase == string(h.Recovering) })
		clock.advance((h.PeerSuspectMs - 1) * time.Millisecond)
		if a, _, _ := n.counts(); a != attempt-1 {
			t.Fatal("offline resume probed too early")
		}
		clock.advance(time.Millisecond)
		awaitOwnership(t, c, func(s localcontrol.Status) bool { return s.Phase == string(h.Active) })
		power <- powerEvent{Kind: powerLid, State: powerState{Lid: lidClosed}}
		awaitOwnership(t, c, func(s localcontrol.Status) bool { return s.Phase == string(h.Idle) && !s.Eligible })
	}
}

func TestOwnershipObserverLossPreservesIneligibility(t *testing.T) {
	n := &ownershipTestNative{}
	c, power, _ := startOwnershipTest(t, n, false, false)
	power <- powerEvent{Kind: powerUnavailable, State: powerState{Lid: lidUnknown}}
	time.Sleep(20 * time.Millisecond)
	if s := c.Status(); s.Eligible {
		t.Fatal("lost observer reopened a known closed lid")
	}
	if a, _, _ := n.counts(); a != 0 {
		t.Fatal("closed laptop acquired on observer loss")
	}
	power <- powerEvent{Kind: powerLid, State: powerState{Lid: lidOpen, Available: true}}
	awaitOwnership(t, c, func(s localcontrol.Status) bool { return s.Phase == string(h.Active) })
	power <- powerEvent{Kind: powerSleep, State: powerState{Lid: lidOpen, Sleeping: true, Available: true}}
	awaitOwnership(t, c, func(s localcontrol.Status) bool { return s.Phase == string(h.Idle) && !s.Eligible })
	power <- powerEvent{Kind: powerUnavailable, State: powerState{Lid: lidUnknown}}
	time.Sleep(20 * time.Millisecond)
	if s := c.Status(); s.Eligible {
		t.Fatal("lost observer ended known suspend")
	}
	power <- powerEvent{Kind: powerResume, State: powerState{Lid: lidOpen, Available: true}}
	awaitOwnership(t, c, func(s localcontrol.Status) bool { return s.Phase == string(h.Active) })
}

func TestOwnershipPeerReleaseWaitsThroughDeadline(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(fmt.Sprintf("route_lost=%t", lost), func(t *testing.T) {
			n := &ownershipTestNative{}
			c, l, clock := preferredOwnershipPhone(t, n)
			transfer := *l.messages("request")[0].Transfer
			peerOwnershipMessage(c, l, "release", transfer, h.ReleaseBody{Target: h.Laptop, Reason: "preference"})
			peerOwnershipMessage(c, l, "released", transfer, h.ReleasedBody{Target: h.Laptop, WorkersStopped: true, ReconnectDisabled: true})
			awaitOwnership(t, c, func(s localcontrol.Status) bool { return s.Phase == string(h.Active) })
			gate := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(gate) }) }
			defer unblock()
			n.mu.Lock()
			n.releaseGate = gate
			n.mu.Unlock()
			peerOwnershipMessage(c, l, "request", "22222222222222222222222222222222", h.RequestBody{Target: h.Phone, Reason: "manual", Pinned: true})
			awaitOwnership(t, c, func(s localcontrol.Status) bool { return s.Phase == string(h.Yielding) })
			if len(l.messages("released")) != 0 {
				t.Fatal("release acknowledged before native teardown")
			}
			clock.advance(h.ReleaseDeadlineMs * time.Millisecond)
			awaitOwnership(t, c, func(s localcontrol.Status) bool { return s.Phase == string(h.Degraded) })
			if len(l.messages("released")) != 0 {
				t.Fatal("deadline fabricated release confirmation")
			}
			if lost {
				l.Close()
			}
			unblock()
			if lost {
				awaitOwnership(t, c, func(s localcontrol.Status) bool { return s.Phase == string(h.Recovering) })
				clock.advance(h.PeerSuspectMs * time.Millisecond)
				awaitOwnership(t, c, func(s localcontrol.Status) bool { return s.Phase == string(h.Active) })
			} else {
				awaitOwnership(t, c, func(s localcontrol.Status) bool { return s.Phase == string(h.Idle) })
				if len(l.messages("released")) != 1 {
					t.Fatal("completed teardown did not acknowledge exactly once")
				}
				clock.advance(time.Hour)
				if a, _, _ := n.counts(); a != 1 {
					t.Fatal("intentionally yielded host retried while phone stayed reachable and pinned")
				}
			}
		})
	}
}
