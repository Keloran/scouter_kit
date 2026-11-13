package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeBus: each subscribe takes the next connection (or the next error);
// a test sends signals on it and closes it to drop the bus.
type fakeBus struct {
	mu        sync.Mutex
	conns     chan chan busSignal
	subErr    []error
	props     busProps
	readErr   error
	lid       lidState
	locks     int // held
	taken     int
	noInhibit bool
}

func newFakeBus(props busProps) *fakeBus {
	return &fakeBus{conns: make(chan chan busSignal, 4), props: props, lid: props.lid}
}

func (b *fakeBus) connect() chan busSignal {
	c := make(chan busSignal, 8)
	b.conns <- c
	return c
}

func (b *fakeBus) subscribe(ctx context.Context) (<-chan busSignal, error) {
	b.mu.Lock()
	if len(b.subErr) > 0 {
		err := b.subErr[0]
		b.subErr = b.subErr[1:]
		b.mu.Unlock()
		return nil, err
	}
	b.mu.Unlock()
	select {
	case c := <-b.conns:
		return c, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (b *fakeBus) read(context.Context) (busProps, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.props, b.readErr
}

func (b *fakeBus) readLid(context.Context) lidState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lid
}

func (b *fakeBus) inhibit() (func(), error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.noInhibit {
		return nil, errors.New("no logind")
	}
	b.locks++
	b.taken++
	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			b.locks--
			b.mu.Unlock()
		})
	}, nil
}

func (b *fakeBus) held() (int, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.locks, b.taken
}

func startPower(t *testing.T, bus *fakeBus, cleanup time.Duration) (<-chan powerEvent, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w := &powerWatcher{bus: bus, cleanup: cleanup, retry: time.Millisecond}
	return w.watch(ctx), cancel
}

func next(t *testing.T, events <-chan powerEvent) powerEvent {
	t.Helper()
	select {
	case e, ok := <-events:
		if !ok {
			t.Fatal("events closed")
		}
		return e
	case <-time.After(2 * time.Second):
		t.Fatal("no event")
	}
	return powerEvent{}
}

func expect(t *testing.T, events <-chan powerEvent, kind powerKind, eligible bool) powerEvent {
	t.Helper()
	e := next(t, events)
	if e.Kind != kind || e.State.eligible() != eligible {
		t.Fatalf("got kind %d eligible %t (%+v), want kind %d eligible %t", e.Kind, e.State.eligible(), e.State, kind, eligible)
	}
	return e
}

func quiet(t *testing.T, events <-chan powerEvent) {
	t.Helper()
	select {
	case e := <-events:
		t.Fatalf("unexpected event %d %+v", e.Kind, e.State)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestPowerInitialClosedLid(t *testing.T) {
	bus := newFakeBus(busProps{lid: lidClosed})
	bus.connect()
	events, _ := startPower(t, bus, time.Second)
	e := expect(t, events, powerLid, false)
	if e.State.Lid != lidClosed || !e.State.Available {
		t.Fatalf("state %+v", e.State)
	}
}

func TestPowerMissingLidCountsAsOpen(t *testing.T) {
	bus := newFakeBus(busProps{lid: lidUnknown})
	c := bus.connect()
	events, _ := startPower(t, bus, time.Second)
	expect(t, events, powerLid, true)
	// Invalidated LidClosed is re-read; still unknown, so nothing changes.
	c <- busSignal{kind: sigLidStale}
	quiet(t, events)
}

func TestPowerSignalOrdering(t *testing.T) {
	bus := newFakeBus(busProps{lid: lidOpen})
	c := bus.connect()
	events, _ := startPower(t, bus, time.Second)
	expect(t, events, powerLid, true)

	// Lid closed with lid sleep disabled: ineligible without a suspend.
	c <- busSignal{kind: sigLid, value: true}
	expect(t, events, powerLid, false)
	c <- busSignal{kind: sigLid, value: true} // duplicate
	c <- busSignal{kind: sigSleep, value: true}
	e := expect(t, events, powerSleep, false)
	e.Done()
	c <- busSignal{kind: sigSleep, value: false}
	// Resumed with the lid still closed: still not eligible.
	expect(t, events, powerResume, false)
	bus.mu.Lock()
	bus.lid = lidOpen
	bus.mu.Unlock()
	c <- busSignal{kind: sigLidStale}
	expect(t, events, powerLid, true)
	c <- busSignal{kind: sigShutdown, value: true}
	expect(t, events, powerShutdown, false)
	c <- busSignal{kind: sigShutdown, value: false}
	expect(t, events, powerResume, true)
}

func TestPowerDuplicateSuspend(t *testing.T) {
	bus := newFakeBus(busProps{lid: lidOpen})
	c := bus.connect()
	events, _ := startPower(t, bus, time.Second)
	expect(t, events, powerLid, true)
	c <- busSignal{kind: sigSleep, value: true}
	c <- busSignal{kind: sigSleep, value: true}
	expect(t, events, powerSleep, false)
	quiet(t, events)
	c <- busSignal{kind: sigSleep, value: false}
	c <- busSignal{kind: sigSleep, value: false}
	expect(t, events, powerResume, true)
	quiet(t, events)
}

func TestPowerStartupWhileSuspending(t *testing.T) {
	bus := newFakeBus(busProps{lid: lidOpen, sleeping: true})
	c := bus.connect()
	events, _ := startPower(t, bus, time.Second)
	expect(t, events, powerLid, true)
	expect(t, events, powerSleep, false).Done()
	c <- busSignal{kind: sigSleep, value: false}
	expect(t, events, powerResume, true)
}

func TestPowerUnavailableAndReconnect(t *testing.T) {
	bus := newFakeBus(busProps{lid: lidClosed})
	bus.subErr = []error{errors.New("no system bus"), errors.New("no system bus")}
	events, _ := startPower(t, bus, time.Second)
	// Reported once, and eligible: desktop-only use keeps working.
	e := expect(t, events, powerUnavailable, true)
	if e.Err == nil {
		t.Fatal("no error")
	}
	quiet(t, events)
	c := bus.connect()
	expect(t, events, powerLid, false)
	if locks, _ := bus.held(); locks != 1 {
		t.Fatalf("locks %d", locks)
	}

	close(c) // bus lost
	expect(t, events, powerUnavailable, true)
	if locks, _ := bus.held(); locks != 0 {
		t.Fatalf("lock kept after bus loss: %d", locks)
	}
	bus.mu.Lock()
	bus.props.lid = lidOpen
	bus.mu.Unlock()
	bus.connect()
	expect(t, events, powerLid, true)
}

func TestPowerReadFailure(t *testing.T) {
	bus := newFakeBus(busProps{lid: lidOpen})
	bus.readErr = errors.New("cannot read logind")
	bus.connect()
	events, _ := startPower(t, bus, time.Second)
	expect(t, events, powerUnavailable, true)
	bus.mu.Lock()
	bus.readErr = nil
	bus.mu.Unlock()
	bus.connect()
	expect(t, events, powerLid, true)
}

func TestPowerCancellation(t *testing.T) {
	bus := newFakeBus(busProps{lid: lidOpen})
	bus.connect()
	events, cancel := startPower(t, bus, time.Second)
	expect(t, events, powerLid, true)
	cancel()
	select {
	case _, ok := <-events:
		if ok {
			t.Fatal("event after cancel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("not closed after cancel")
	}
	if locks, _ := bus.held(); locks != 0 {
		t.Fatalf("lock kept after cancel: %d", locks)
	}
}

func TestPowerInhibitorDeadline(t *testing.T) {
	bus := newFakeBus(busProps{lid: lidOpen})
	c := bus.connect()
	events, _ := startPower(t, bus, 50*time.Millisecond)
	expect(t, events, powerLid, true)

	// Nobody calls Done: the lock goes at the deadline.
	c <- busSignal{kind: sigSleep, value: true}
	start := time.Now()
	expect(t, events, powerSleep, false)
	for locks, _ := bus.held(); locks != 0; locks, _ = bus.held() {
		if time.Since(start) > time.Second {
			t.Fatal("delay lock not released at the deadline")
		}
		time.Sleep(5 * time.Millisecond)
	}
	c <- busSignal{kind: sigSleep, value: false}
	expect(t, events, powerResume, true)
	if locks, taken := bus.held(); locks != 1 || taken != 2 {
		t.Fatalf("after resume: locks %d taken %d", locks, taken)
	}
}

func TestPowerDoneReleasesLock(t *testing.T) {
	bus := newFakeBus(busProps{lid: lidOpen})
	c := bus.connect()
	events, _ := startPower(t, bus, time.Hour)
	expect(t, events, powerLid, true)
	c <- busSignal{kind: sigSleep, value: true}
	expect(t, events, powerSleep, false).Done()
	start := time.Now()
	for locks, _ := bus.held(); locks != 0; locks, _ = bus.held() {
		if time.Since(start) > time.Second {
			t.Fatal("Done did not release the lock")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestPowerWithoutInhibitor(t *testing.T) {
	bus := newFakeBus(busProps{lid: lidOpen})
	bus.noInhibit = true
	c := bus.connect()
	events, _ := startPower(t, bus, time.Second)
	expect(t, events, powerLid, true)
	c <- busSignal{kind: sigSleep, value: true}
	e := expect(t, events, powerSleep, false)
	e.Done()
	e.Done()
	c <- busSignal{kind: sigSleep, value: false}
	expect(t, events, powerResume, true)
}

func TestParseGdbusLine(t *testing.T) {
	for _, tc := range []struct {
		line string
		want busSignal
		ok   bool
	}{
		{"/org/freedesktop/login1: org.freedesktop.login1.Manager.PrepareForSleep (true,)", busSignal{sigSleep, true}, true},
		{"/org/freedesktop/login1: org.freedesktop.login1.Manager.PrepareForSleep (false,)", busSignal{sigSleep, false}, true},
		{"/org/freedesktop/login1: org.freedesktop.login1.Manager.PrepareForShutdown (true,)", busSignal{sigShutdown, true}, true},
		{"/org/freedesktop/login1: org.freedesktop.login1.Manager.PrepareForShutdownWithMetadata (true, @a{sv} {})", busSignal{}, false},
		{"/org/freedesktop/login1: org.freedesktop.DBus.Properties.PropertiesChanged ('org.freedesktop.login1.Manager', {'LidClosed': <true>}, @as [])", busSignal{sigLid, true}, true},
		{"/org/freedesktop/login1: org.freedesktop.DBus.Properties.PropertiesChanged ('org.freedesktop.login1.Manager', {'LidClosed': <false>, 'Docked': <false>}, @as [])", busSignal{sigLid, false}, true},
		{"/org/freedesktop/login1: org.freedesktop.DBus.Properties.PropertiesChanged ('org.freedesktop.login1.Manager', @a{sv} {}, ['LidClosed'])", busSignal{kind: sigLidStale}, true},
		{"/org/freedesktop/login1: org.freedesktop.DBus.Properties.PropertiesChanged ('org.freedesktop.login1.Manager', {'IdleHint': <true>}, @as [])", busSignal{}, false},
		{"/org/freedesktop/login1/session/_31: org.freedesktop.DBus.Properties.PropertiesChanged ('org.freedesktop.login1.Session', {'LidClosed': <true>}, @as [])", busSignal{}, false},
		{"/org/freedesktop/login1: org.freedesktop.login1.Manager.SessionNew ('3', objectpath '/org/freedesktop/login1/session/_33')", busSignal{}, false},
	} {
		got, ok := parseGdbusLine(tc.line)
		if ok != tc.ok || got != tc.want {
			t.Errorf("%q: got %+v %t, want %+v %t", tc.line, got, ok, tc.want, tc.ok)
		}
	}
}
