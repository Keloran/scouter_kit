//go:build emulator

package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"scouterkit"
	h "scouterkit/examples/internal/handover"
	"scouterkit/examples/internal/localcontrol"
)

// This guard audits call ordering; it is not an atomic BLE ownership model.
// The laptop uses its real emulated dashboard/session. The phone is synthetic.
type qualificationArms struct {
	mu    sync.Mutex
	owner h.Role
}

func (a *qualificationArms) claim(role h.Role) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.owner != "" {
		return false
	}
	a.owner = role
	return true
}
func (a *qualificationArms) release(role h.Role) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.owner == role {
		a.owner = ""
	}
}
func (a *qualificationArms) current() h.Role { a.mu.Lock(); defer a.mu.Unlock(); return a.owner }

type qualificationNative struct {
	base                       dashboardNative
	arms                       *qualificationArms
	mu                         sync.Mutex
	attempts, denied, releases int
	fail, partial              bool
	acquireStarted             chan struct{}
	acquireGate                <-chan struct{}
	acquireCleanup             chan struct{}
	releaseStarted             chan struct{}
	releaseGate                chan struct{}
	releasing                  bool
}

func (n *qualificationNative) Acquire(ctx context.Context) (h.Arms, error) {
	n.mu.Lock()
	n.attempts++
	started, gate, cleanup, fail, partial := n.acquireStarted, n.acquireGate, n.acquireCleanup, n.fail, n.partial
	n.mu.Unlock()
	if !n.arms.claim(h.Laptop) {
		n.mu.Lock()
		n.denied++
		n.mu.Unlock()
		return h.Arms{}, errors.New("synthetic arms busy")
	}
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			if cleanup != nil {
				<-cleanup
			}
			return h.Arms{}, ctx.Err()
		}
	}
	ready, err := n.base.Acquire(ctx)
	if err == nil && fail {
		return ready, errors.New("synthetic setup failure")
	}
	if err == nil && partial {
		ready.Right = false
	}
	return ready, err
}
func (n *qualificationNative) Release() error {
	n.mu.Lock()
	n.releases++
	n.releasing = true
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
	err := n.base.Release()
	n.arms.release(h.Laptop)
	n.mu.Lock()
	n.releasing = false
	n.releaseGate = nil
	n.mu.Unlock()
	return err
}
func (n *qualificationNative) Readiness() h.Arms {
	ready := n.base.Readiness()
	n.mu.Lock()
	partial := n.partial
	n.mu.Unlock()
	if partial {
		ready.Right = false
	}
	return ready
}
func (n *qualificationNative) Abort() {
	n.base.Abort()
	n.mu.Lock()
	if n.acquireCleanup != nil {
		select {
		case <-n.acquireCleanup:
		default:
			close(n.acquireCleanup)
		}
	}
	if n.releaseGate != nil {
		select {
		case <-n.releaseGate:
		default:
			close(n.releaseGate)
		}
	}
	n.mu.Unlock()
}
func (n *qualificationNative) counts() (int, int, int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.attempts, n.denied, n.releases
}

type desktopQualification struct {
	t             *testing.T
	ctx           context.Context
	c             *ownershipController
	n             *qualificationNative
	host          *glassesHost
	clock         *ownershipTestClock
	bus           *fakeBus
	signals       chan busSignal
	control       string
	peer          *snapshotWirePeer
	conn          *h.Conn
	peerCtx       context.Context
	phoneTransfer string
}

func newDesktopQualification(t *testing.T, enrolled bool, lid lidState) *desktopQualification {
	t.Helper()
	t.Setenv("PATH", t.TempDir()) // no desktop music/notification executables or real D-Bus
	ctx, cancel := context.WithCancel(context.Background())
	o := sessionOptions()
	o.managed = true
	o.snapshots = newSnapshotBridge()
	host := &glassesHost{o: o}
	commands := make(chan hostCommand)
	dashCtx, stopDash := context.WithCancel(context.Background())
	dashDone := make(chan struct{})
	var dashErr error
	go func() {
		defer close(dashDone)
		dashErr = dashboard(dashCtx, host, o, fakeClient(t, `[{"slug":"sample","name":"Sample"}]`), commands)
	}()
	n := &qualificationNative{base: dashboardNative{host, commands, dashDone}, arms: &qualificationArms{}}
	bus := newFakeBus(busProps{lid: lid})
	signals := bus.connect()
	w := &powerWatcher{bus: bus, cleanup: time.Second, retry: time.Millisecond}
	power := w.watch(ctx)
	c := newOwnershipController(n, power, enrolled)
	c.snapshots = o.snapshots
	clock := newOwnershipTestClock()
	c.clock = clock.clock()
	c.jitter = func() float64 { return 0 }
	control := filepath.Join(t.TempDir(), "control.sock")
	server, err := localcontrol.Listen(control, c)
	if err != nil {
		t.Fatal(err)
	}
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve() }()
	go c.run(ctx)
	q := &desktopQualification{t: t, ctx: ctx, c: c, n: n, host: host, clock: clock, bus: bus, signals: signals, control: control}
	t.Cleanup(func() {
		n.Abort()
		cancel()
		select {
		case <-c.done:
		case <-time.After(3 * time.Second):
			t.Error("controller did not join native cleanup")
		}
		stopDash()
		select {
		case <-dashDone:
			if dashErr != nil {
				t.Error(dashErr)
			}
		case <-time.After(3 * time.Second):
			t.Error("dashboard did not join workers")
		}
		for range power {
		}
		server.Close()
		if err := <-serverDone; err != nil {
			t.Error(err)
		}
	})
	return q
}

func (q *desktopQualification) lid(open bool) {
	lid := lidClosed
	if open {
		lid = lidOpen
	}
	q.bus.mu.Lock()
	q.bus.lid = lid
	q.bus.props.lid = lid
	q.bus.mu.Unlock()
	q.signals <- busSignal{kind: sigLid, value: !open}
}
func (q *desktopQualification) sleep(sleeping bool) {
	q.bus.mu.Lock()
	q.bus.props.sleeping = sleeping
	q.bus.mu.Unlock()
	q.signals <- busSignal{kind: sigSleep, value: sleeping}
}
func (q *desktopQualification) command(command string) {
	q.t.Helper()
	ctx, cancel := context.WithTimeout(q.ctx, 3*time.Second)
	defer cancel()
	r, err := localcontrol.Do(ctx, q.control, localcontrol.Request{Command: command})
	if err != nil || r.Result != localcontrol.ResultDone {
		q.t.Fatalf("control %s: %+v %v", command, r, err)
	}
}
func (q *desktopQualification) phone() {
	q.t.Helper()
	ctx, conn, peer := snapshotTLS(q.t, []string{h.CapSnapshot})
	q.conn, q.peer, q.peerCtx = conn, peer, ctx
	forward := make(chan struct{})
	go func() { defer close(forward); q.c.serveLink(q.ctx, conn) }()
	q.t.Cleanup(func() { conn.Close(); <-forward })
	q.phoneTransfer = h.NewNonce()
	q.phoneStatus(q.n.arms.current() == h.Phone, false)
}
func (q *desktopQualification) phoneStatus(active, pinned bool) {
	phase := h.Idle
	ready := h.Arms{}
	var transfer *string
	if active {
		phase = h.Active
		ready = h.Arms{Left: true, Right: true}
		transfer = &q.phoneTransfer
	}
	q.peer.write(q.t, q.peer.message(q.t, "status", "", h.StatusBody{Role: h.Phone, Phase: phase, Eligible: true, Pinned: pinned, Arms: ready, RendererReady: active, ReadyEpoch: 2, Transfer: transfer}))
}
func (q *desktopQualification) releasePhone(transfer string) {
	q.t.Helper()
	q.peer.write(q.t, q.peer.message(q.t, "release", transfer, h.ReleaseBody{Target: h.Laptop, Reason: "preference"}))
	// Stop the synthetic writer and drop both arms before the proof is sent.
	q.n.arms.release(h.Phone)
	q.peer.write(q.t, q.peer.message(q.t, "released", transfer, h.ReleasedBody{Target: h.Laptop, WorkersStopped: true, ReconnectDisabled: true}))
}
func (q *desktopQualification) requestReturn() string {
	q.t.Helper()
	q.lid(true)
	awaitOwnership(q.t, q.c, func(s localcontrol.Status) bool { return s.Eligible })
	q.phoneStatus(true, false)
	awaitOwnership(q.t, q.c, func(s localcontrol.Status) bool { return s.Phase == string(h.Idle) })
	q.clock.advance(h.LaptopStabilityMs * time.Millisecond)
	m := q.peer.read(q.t, q.peerCtx, "request")
	if m.Transfer == nil {
		q.t.Fatal("request has no transfer")
	}
	return *m.Transfer
}
func (q *desktopQualification) laptopReady() {
	q.t.Helper()
	q.peer.read(q.t, q.peerCtx, "ready")
	awaitNativeOwnership(q.t, q.c, h.Active)
	if q.n.arms.current() != h.Laptop {
		q.t.Fatal("ready without native ownership")
	}
	for _, a := range []scouterkit.Arm{scouterkit.Left, scouterkit.Right} {
		if s := a.State(); !s.Connected || s.ShownUpdate == 0 {
			q.t.Fatal("ready before fresh full frame")
		}
	}
}
func (q *desktopQualification) takePhone() {
	q.t.Helper()
	release := q.peer.read(q.t, q.peerCtx, "release")
	if release.Transfer == nil {
		q.t.Fatal("release without transfer")
	}
	q.phoneTransfer = *release.Transfer
	q.peer.read(q.t, q.peerCtx, "released")
	q.n.mu.Lock()
	releasing := q.n.releasing
	q.n.mu.Unlock()
	if releasing || q.host.Status().State != hostReleased || scouterkit.Left.State().Connected || scouterkit.Right.State().Connected {
		q.t.Fatal("release proof preceded stopped native session")
	}
	if !q.n.arms.claim(h.Phone) {
		q.t.Fatal("phone acquisition overlapped native owner")
	}
	q.peer.write(q.t, q.peer.message(q.t, "acquiring", q.phoneTransfer, h.AcquiringBody{Target: h.Phone, Mode: "released"}))
	q.peer.write(q.t, q.peer.message(q.t, "ready", q.phoneTransfer, h.ReadyBody{Target: h.Phone, Arms: h.Arms{Left: true, Right: true}, RendererReady: true}))
}

func TestDesktopQualificationStandaloneControls(t *testing.T) {
	q := newDesktopQualification(t, false, lidOpen)
	awaitNativeOwnership(t, q.c, h.Active)
	q.command(localcontrol.CmdRelease)
	before, _, _ := q.n.counts()
	q.clock.advance(time.Hour)
	if after, _, _ := q.n.counts(); after != before {
		t.Fatal("intentionally released desktop reconnected")
	}
	q.command(localcontrol.CmdResume)
	if q.host.Status().Generation != 2 {
		t.Fatal("resume reused old native handles")
	}
}

func TestDesktopQualificationPhoneAndLidRoundTrip(t *testing.T) {
	q := newDesktopQualification(t, true, lidClosed)
	if !q.n.arms.claim(h.Phone) {
		t.Fatal("initial phone ownership")
	}
	q.phone()
	awaitOwnership(t, q.c, func(s localcontrol.Status) bool { return s.Owner == "peer" })
	transfer := q.requestReturn()
	q.peer.write(t, q.peer.message(t, "release", transfer, h.ReleaseBody{Target: h.Laptop, Reason: "preference"}))
	if attempts, _, _ := q.n.counts(); attempts != 0 {
		t.Fatal("release notice authorized native acquisition")
	}
	q.n.arms.release(h.Phone)
	q.peer.write(t, q.peer.message(t, "released", transfer, h.ReleasedBody{Target: h.Laptop, WorkersStopped: true, ReconnectDisabled: true}))
	q.laptopReady()
	q.lid(false)
	q.takePhone()
	before, _, _ := q.n.counts()
	q.clock.advance(time.Hour)
	if after, _, _ := q.n.counts(); after != before {
		t.Fatal("desktop reconnected while yielded")
	}
	transfer = q.requestReturn()
	q.releasePhone(transfer)
	q.laptopReady()
	if _, denied, _ := q.n.counts(); denied != 0 {
		t.Fatal("hosts raced for the arms")
	}
}

func TestDesktopQualificationSuspendDuringAcquire(t *testing.T) {
	q := newDesktopQualification(t, false, lidClosed)
	started := make(chan struct{}, 1)
	q.n.mu.Lock()
	q.n.acquireStarted, q.n.acquireGate, q.n.acquireCleanup = started, make(chan struct{}), make(chan struct{})
	q.n.mu.Unlock()
	q.lid(true)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("acquisition did not start")
	}
	q.sleep(true)
	awaitOwnership(t, q.c, func(s localcontrol.Status) bool { return s.Phase == string(h.Yielding) })
	if held, _ := q.bus.held(); held != 1 {
		t.Fatal("delay inhibitor dropped before acquisition joined")
	}
	q.clock.advance(499 * time.Millisecond)
	if _, _, released := q.n.counts(); released != 0 {
		t.Fatal("release overlapped cancelled acquisition cleanup")
	}
	if held, _ := q.bus.held(); held != 1 {
		t.Fatal("inhibitor dropped before suspend abort budget")
	}
	q.clock.advance(time.Millisecond)
	awaitOwnership(t, q.c, func(s localcontrol.Status) bool { return s.Phase == string(h.Idle) })
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		held, _ := q.bus.held()
		if held == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("native cleanup did not release inhibitor")
		}
		time.Sleep(time.Millisecond)
	}
	if q.n.arms.current() != "" || q.host.Status().State != hostReleased {
		t.Fatal("inhibitor dropped with an old session held")
	}
	q.n.mu.Lock()
	q.n.acquireGate, q.n.acquireCleanup, q.n.acquireStarted = nil, nil, nil
	q.n.mu.Unlock()
	q.sleep(false)
	awaitNativeOwnership(t, q.c, h.Active)
}

func TestDesktopQualificationSuspendStalledRelease(t *testing.T) {
	q := newDesktopQualification(t, false, lidOpen)
	awaitNativeOwnership(t, q.c, h.Active)
	started := make(chan struct{}, 1)
	q.n.mu.Lock()
	q.n.releaseGate = make(chan struct{})
	q.n.releaseStarted = started
	q.n.mu.Unlock()
	q.sleep(true)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("suspend did not start native release")
	}
	awaitOwnership(t, q.c, func(s localcontrol.Status) bool { return s.Phase == string(h.Yielding) })
	q.clock.advance(499 * time.Millisecond)
	if held, _ := q.bus.held(); held != 1 {
		t.Fatal("inhibitor dropped before native release finished")
	}
	if q.n.arms.current() != h.Laptop {
		t.Fatal("release guard opened before cleanup")
	}
	q.clock.advance(time.Millisecond)
	awaitOwnership(t, q.c, func(s localcontrol.Status) bool { return s.Phase == string(h.Idle) })
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		held, _ := q.bus.held()
		if held == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("inhibitor did not follow completed teardown")
		}
		time.Sleep(time.Millisecond)
	}
	if scouterkit.Left.State().Connected || scouterkit.Right.State().Connected {
		t.Fatal("suspend completed with live native links")
	}
	q.sleep(false)
	awaitNativeOwnership(t, q.c, h.Active)
	q.clock.advance(time.Hour)
	if q.host.Status().Generation != 2 || !scouterkit.Left.State().Connected || !scouterkit.Right.State().Connected {
		t.Fatal("old suspend deadline affected the resumed session")
	}
}

func TestDesktopQualificationIncomingRollback(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{false: "setup-failed", true: "partial-ready"}[partial], func(t *testing.T) {
			q := newDesktopQualification(t, true, lidClosed)
			q.n.arms.claim(h.Phone)
			q.phone()
			awaitOwnership(t, q.c, func(s localcontrol.Status) bool { return s.Owner == "peer" })
			transfer := q.requestReturn()
			q.n.mu.Lock()
			q.n.fail, q.n.partial = !partial, partial
			q.n.mu.Unlock()
			q.releasePhone(transfer)
			if partial {
				awaitOwnership(t, q.c, func(s localcontrol.Status) bool { return s.Phase == string(h.Acquiring) && s.Left && !s.Right })
				q.clock.advance(h.PartialArmsMs * time.Millisecond)
			}
			q.peer.read(t, q.peerCtx, "abort")
			awaitOwnership(t, q.c, func(s localcontrol.Status) bool { return s.Phase == string(h.Recovering) })
			if q.n.arms.current() != "" || scouterkit.Left.State().Connected || scouterkit.Right.State().Connected {
				t.Fatal("rollback retained an old native session")
			}
			before, _, _ := q.n.counts()
			q.clock.advance((h.RetryInitialMs - 1) * time.Millisecond)
			if after, _, _ := q.n.counts(); after != before {
				t.Fatal("retry ran before backoff")
			}
			q.n.mu.Lock()
			q.n.fail, q.n.partial = false, false
			q.n.mu.Unlock()
			q.clock.advance(time.Millisecond)
			q.phoneStatus(false, false) // answer recovery reconciliation with fresh peer state
			request := q.peer.read(t, q.peerCtx, "request")
			if request.Transfer == nil {
				t.Fatal("retry without transfer")
			}
			q.releasePhone(*request.Transfer)
			q.laptopReady()
		})
	}
}

func TestDesktopQualificationPartitionAndCrash(t *testing.T) {
	for _, crashed := range []bool{false, true} {
		t.Run(map[bool]string{false: "healthy-owner-partition", true: "phone-crash"}[crashed], func(t *testing.T) {
			q := newDesktopQualification(t, true, lidClosed)
			q.n.arms.claim(h.Phone)
			q.phone()
			awaitOwnership(t, q.c, func(s localcontrol.Status) bool { return s.Owner == "peer" })
			q.lid(true)
			awaitOwnership(t, q.c, func(s localcontrol.Status) bool { return s.Eligible })
			q.phoneStatus(true, true)
			awaitOwnership(t, q.c, func(s localcontrol.Status) bool { return s.Eligible && s.Phase == string(h.Idle) })
			if crashed {
				q.n.arms.release(h.Phone)
			}
			q.conn.Close()
			awaitOwnership(t, q.c, func(s localcontrol.Status) bool { return s.Phase == string(h.Recovering) })
			if count, _, _ := q.n.counts(); count != 0 {
				t.Fatal("route loss immediately acquired")
			}
			q.clock.advance(h.PeerSuspectMs * time.Millisecond)
			if crashed {
				awaitNativeOwnership(t, q.c, h.Active)
				if q.n.arms.current() != h.Laptop {
					t.Fatal("crashed owner's arms not recovered")
				}
			} else {
				awaitOwnership(t, q.c, func(s localcontrol.Status) bool {
					_, denied, _ := q.n.counts()
					return denied == 1 && s.Phase == string(h.Recovering)
				})
				q.clock.advance(h.RetryInitialMs * time.Millisecond)
				awaitOwnership(t, q.c, func(s localcontrol.Status) bool {
					_, denied, _ := q.n.counts()
					return denied == 2 && s.Phase == string(h.Recovering)
				})
				if q.n.arms.current() != h.Phone || q.host.Status().Generation != 0 {
					t.Fatal("network loss preempted healthy owner")
				}
			}
		})
	}
}

func TestDesktopQualificationDuplicateProofAndOwnerPartition(t *testing.T) {
	q := newDesktopQualification(t, true, lidClosed)
	q.n.arms.claim(h.Phone)
	q.phone()
	awaitOwnership(t, q.c, func(s localcontrol.Status) bool { return s.Owner == "peer" })
	transfer := q.requestReturn()
	q.peer.write(t, q.peer.message(t, "release", transfer, h.ReleaseBody{Target: h.Laptop, Reason: "preference"}))
	q.n.arms.release(h.Phone)
	proof := q.peer.message(t, "released", transfer, h.ReleasedBody{Target: h.Laptop, WorkersStopped: true, ReconnectDisabled: true})
	q.peer.write(t, proof)
	q.peer.write(t, proof)
	select {
	case <-q.conn.Done():
	case <-q.peerCtx.Done():
		t.Fatal("replayed proof did not close stream")
	}
	awaitNativeOwnership(t, q.c, h.Active)
	before, _, released := q.n.counts()
	if before != 1 || released != 0 {
		t.Fatal("duplicate proof allocated or released an extra session")
	}
	q.clock.advance(time.Hour)
	if after, _, releaseAfter := q.n.counts(); after != before || releaseAfter != released {
		t.Fatal("control partition disrupted a healthy desktop owner")
	}
	if q.n.arms.current() != h.Laptop || !scouterkit.Left.State().Connected || !scouterkit.Right.State().Connected {
		t.Fatal("desktop owner lost arms on control partition")
	}
}

// Observe the dashboard's public progress at exact check boundaries without
// reading its UI-owned counters or depending on scheduler timing.
type qualificationLinkLog struct {
	four, six, lost chan struct{}
	allowFour, stop chan struct{}
	once            sync.Once
}

func newQualificationLinkLog() *qualificationLinkLog {
	return &qualificationLinkLog{four: make(chan struct{}, 1), six: make(chan struct{}, 1), lost: make(chan struct{}, 1), allowFour: make(chan struct{}), stop: make(chan struct{})}
}
func (l *qualificationLinkLog) Write(p []byte) (int, error) {
	line := string(p)
	if strings.Contains(line, "right arm not connected (4/5)") {
		select {
		case l.four <- struct{}{}:
		default:
		}
		select {
		case <-l.allowFour:
		case <-l.stop:
		}
	}
	if strings.Contains(line, "right arm not connected (6/5)") {
		select {
		case l.six <- struct{}{}:
		default:
		}
		<-l.stop
	}
	if strings.Contains(line, "all arms not connected for 5 checks") {
		select {
		case l.lost <- struct{}{}:
		default:
		}
	}
	return len(p), nil
}
func (l *qualificationLinkLog) unblock() { l.once.Do(func() { close(l.stop) }) }
