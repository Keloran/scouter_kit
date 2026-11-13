package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Laptop availability from systemd-logind (org.freedesktop.login1(5)): the
// LidClosed property (emits-change), PrepareForSleep and PrepareForShutdown.
// Needs no privileges: signals come from `gdbus monitor` (an ordinary match
// subscription), properties from `busctl get-property`, and the optional delay
// lock from `systemd-inhibit`.

type lidState int

const (
	// lidUnknown: no bus, or no LidClosed property. Counts as open so a
	// desktop, or a laptop without logind, keeps working on its own. Desktops
	// without a lid switch report LidClosed=false, so they read as open too.
	lidUnknown lidState = iota
	lidOpen
	lidClosed
)

type powerKind int

const (
	powerLid         powerKind = iota // lid changed, or logind (re)read after connecting
	powerSleep                        // PrepareForSleep(true); call Done once released
	powerShutdown                     // PrepareForShutdown(true); call Done once released
	powerResume                       // sleep finished or shutdown cancelled
	powerUnavailable                  // logind unreachable or lost; Err says why; lid unknown
)

type powerState struct {
	Lid                    lidState
	Sleeping, ShuttingDown bool
	Available              bool // logind is being observed
}

// eligible: the laptop may own the glasses. A closed lid rules it out even when
// logind is set not to suspend on lid close; resuming with the lid closed stays
// ineligible until the lid opens.
func (s powerState) eligible() bool {
	return s.Lid != lidClosed && !s.Sleeping && !s.ShuttingDown
}

type powerEvent struct {
	Kind  powerKind
	State powerState
	Err   error
	// Done (powerSleep, powerShutdown) releases the delay lock early; it is
	// released anyway after powerWatcher.cleanup. Safe to call more than once.
	Done func()
}

type busSignalKind int

const (
	sigLid      busSignalKind = iota // value = closed
	sigLidStale                      // LidClosed invalidated: read it again
	sigSleep
	sigShutdown
)

type busSignal struct {
	kind  busSignalKind
	value bool
}

type busProps struct {
	lid                lidState
	sleeping, shutdown bool
}

// powerBus is logind, or a fake in tests.
type powerBus interface {
	// subscribe returns once signals are matched, so a read after it misses
	// nothing. The channel closes when the bus or logind goes away.
	subscribe(ctx context.Context) (<-chan busSignal, error)
	read(ctx context.Context) (busProps, error)
	readLid(ctx context.Context) lidState
	// inhibit takes a delay lock on sleep and shutdown; release drops it.
	inhibit() (release func(), err error)
}

type powerWatcher struct {
	bus     powerBus
	cleanup time.Duration // longest the delay lock is held after PrepareFor*
	retry   time.Duration // first reconnect delay; doubles up to retryMax
}

const retryMax = 30 * time.Second

func newPowerWatcher() *powerWatcher {
	// logind caps delay locks too (InhibitDelayMaxSec, 5 s by default).
	return &powerWatcher{bus: logindBus{}, cleanup: 3 * time.Second, retry: time.Second}
}

// watch reports changes until ctx ends, then closes the channel. Events are
// only sent on a change, so repeated signals are ignored.
func (w *powerWatcher) watch(ctx context.Context) <-chan powerEvent {
	out := make(chan powerEvent, 8)
	go func() {
		defer close(out)
		w.run(ctx, out)
	}()
	return out
}

type powerRun struct {
	w        *powerWatcher
	ctx      context.Context
	out      chan<- powerEvent
	state    powerState
	release  func()        // delay lock held, or nil
	released chan struct{} // closed by Done
	deadline *time.Timer
}

func (w *powerWatcher) run(ctx context.Context, out chan<- powerEvent) {
	r := &powerRun{w: w, ctx: ctx, out: out}
	defer r.unlock()
	delay, reported := w.retry, false
	for ctx.Err() == nil {
		sigs, props, err := r.connect()
		if err != nil {
			if r.state.Available || !reported {
				r.unlock()
				r.state = powerState{}
				r.send(powerEvent{Kind: powerUnavailable, Err: err})
			}
			reported = true
			select {
			case <-ctx.Done():
			case <-time.After(delay):
			}
			delay = min(2*delay, retryMax)
			continue
		}
		delay, reported = w.retry, true
		r.state = powerState{Available: true, Lid: props.lid}
		r.send(powerEvent{Kind: powerLid})
		r.lock()
		r.sleep(sigSleep, props.sleeping)
		r.sleep(sigShutdown, props.shutdown)
		if r.loop(sigs) {
			return
		}
		r.unlock()
		r.state = powerState{}
		r.send(powerEvent{Kind: powerUnavailable, Err: errors.New("logind signals stopped; lid and suspend unknown")})
	}
}

func (r *powerRun) connect() (<-chan busSignal, busProps, error) {
	sigs, err := r.w.bus.subscribe(r.ctx)
	if err != nil {
		return nil, busProps{}, err
	}
	props, err := r.w.bus.read(r.ctx)
	if err != nil {
		// Drain so the subscriber can stop; it ends with ctx or its process.
		go func() {
			for range sigs {
			}
		}()
		return nil, busProps{}, err
	}
	return sigs, props, nil
}

// loop handles signals until the bus is lost (false) or ctx ends (true).
func (r *powerRun) loop(sigs <-chan busSignal) bool {
	for {
		var timeout <-chan time.Time
		if r.deadline != nil {
			timeout = r.deadline.C
		}
		select {
		case <-r.ctx.Done():
			return true
		case <-r.released:
			r.unlock()
		case <-timeout:
			r.unlock()
		case sig, ok := <-sigs:
			if !ok {
				return false
			}
			switch sig.kind {
			case sigLid:
				lid := lidOpen
				if sig.value {
					lid = lidClosed
				}
				r.lid(lid)
			case sigLidStale:
				r.lid(r.w.bus.readLid(r.ctx))
			case sigSleep, sigShutdown:
				r.sleep(sig.kind, sig.value)
			}
		}
	}
}

func (r *powerRun) lid(lid lidState) {
	if lid != r.state.Lid {
		r.state.Lid = lid
		r.send(powerEvent{Kind: powerLid})
	}
}

func (r *powerRun) sleep(kind busSignalKind, on bool) {
	flag, event := &r.state.Sleeping, powerSleep
	if kind == sigShutdown {
		flag, event = &r.state.ShuttingDown, powerShutdown
	}
	if *flag == on {
		return
	}
	*flag = on
	if !on {
		// Back before the deadline: keep the lock for the next time.
		if r.deadline != nil {
			r.deadline.Stop()
			r.deadline = nil
		}
		r.released = nil
		if !r.state.Sleeping && !r.state.ShuttingDown {
			r.lock()
		}
		r.send(powerEvent{Kind: powerResume})
		return
	}
	var once sync.Once
	released := make(chan struct{})
	r.released = released
	if r.deadline != nil {
		r.deadline.Stop()
	}
	r.deadline = time.NewTimer(r.w.cleanup)
	r.send(powerEvent{Kind: event, Done: func() { once.Do(func() { close(released) }) }})
}

func (r *powerRun) send(e powerEvent) {
	e.State = r.state
	select {
	case r.out <- e:
	case <-r.ctx.Done():
	}
}

func (r *powerRun) lock() {
	if r.release != nil {
		return
	}
	// Without the lock, sleep just doesn't wait for cleanup.
	if release, err := r.w.bus.inhibit(); err == nil {
		r.release = release
	}
}

func (r *powerRun) unlock() {
	if r.deadline != nil {
		r.deadline.Stop()
		r.deadline = nil
	}
	r.released = nil
	if r.release != nil {
		r.release()
		r.release = nil
	}
}

// logindBus is the system bus, through gdbus, busctl and systemd-inhibit.
type logindBus struct{}

const (
	login1Path    = "/org/freedesktop/login1"
	login1Manager = "org.freedesktop.login1.Manager"
)

var (
	gdbusPrepare = regexp.MustCompile(`^/org/freedesktop/login1: org\.freedesktop\.login1\.Manager\.` +
		`(PrepareForSleep|PrepareForShutdown) \((true|false),\)$`)
	gdbusLid = regexp.MustCompile(`'LidClosed': <(true|false)>`)
)

// parseGdbusLine decodes one `gdbus monitor` signal line.
func parseGdbusLine(line string) (busSignal, bool) {
	if m := gdbusPrepare.FindStringSubmatch(line); m != nil {
		kind := sigSleep
		if m[1] == "PrepareForShutdown" {
			kind = sigShutdown
		}
		return busSignal{kind: kind, value: m[2] == "true"}, true
	}
	if !strings.HasPrefix(line, login1Path+": org.freedesktop.DBus.Properties.PropertiesChanged ('"+login1Manager+"',") {
		return busSignal{}, false
	}
	if m := gdbusLid.FindStringSubmatch(line); m != nil {
		return busSignal{kind: sigLid, value: m[1] == "true"}, true
	}
	if i := strings.LastIndex(line, "["); i >= 0 && strings.Contains(line[i:], "'LidClosed'") {
		return busSignal{kind: sigLidStale}, true
	}
	return busSignal{}, false
}

func (logindBus) subscribe(ctx context.Context) (<-chan busSignal, error) {
	// gdbus subscribes when it sees the owner and prints this line; the
	// AddMatch is sent before it. ponytail: a change in the microseconds
	// before the match lands is missed until the next one.
	cmd := exec.CommandContext(ctx, "gdbus", "monitor", "--system",
		"--dest", "org.freedesktop.login1", "--object-path", login1Path)
	cmd.Stderr = io.Discard
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, errors.New("cannot start gdbus; lid and suspend monitoring unavailable")
	}
	sigs := make(chan busSignal, 8)
	ready := make(chan error, 1)
	go func() {
		defer close(sigs)
		defer func() {
			cmd.Process.Kill()
			cmd.Wait()
		}()
		subscribed := false
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case strings.HasPrefix(line, "The name org.freedesktop.login1 is owned by"):
				if !subscribed {
					subscribed = true
					ready <- nil
				}
			case strings.Contains(line, "does not have an owner"):
				// logind left (or never ran): reconnect for a fresh read.
				if !subscribed {
					ready <- errors.New("logind is not running; lid and suspend unknown")
				}
				return
			default:
				if sig, ok := parseGdbusLine(line); ok {
					select {
					case sigs <- sig:
					case <-ctx.Done():
						return
					}
				}
			}
		}
		if !subscribed {
			ready <- errors.New("cannot reach the system bus; lid and suspend unknown")
		}
	}()
	select {
	case err := <-ready:
		if err != nil {
			return nil, err
		}
		return sigs, nil
	case <-time.After(5 * time.Second):
		cmd.Process.Kill()
		return nil, errors.New("gdbus did not subscribe to logind; lid and suspend unknown")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func logindBool(ctx context.Context, name string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "busctl", "--system", "--json=short", "get-property",
		"org.freedesktop.login1", login1Path, login1Manager, name).Output()
	if err != nil {
		return false, err
	}
	var v struct {
		Type string
		Data bool
	}
	if json.Unmarshal(out, &v) != nil || v.Type != "b" {
		return false, errors.New("unexpected " + name + " value")
	}
	return v.Data, nil
}

func (logindBus) read(ctx context.Context) (busProps, error) {
	sleeping, err := logindBool(ctx, "PreparingForSleep")
	if err != nil {
		return busProps{}, errors.New("cannot read logind; lid and suspend unknown")
	}
	shutdown, _ := logindBool(ctx, "PreparingForShutdown") // missing: not shutting down
	return busProps{lid: logindBus{}.readLid(ctx), sleeping: sleeping, shutdown: shutdown}, nil
}

func (logindBus) readLid(ctx context.Context) lidState {
	closed, err := logindBool(ctx, "LidClosed")
	switch {
	case err != nil:
		return lidUnknown
	case closed:
		return lidClosed
	}
	return lidOpen
}

func (logindBus) inhibit() (func(), error) {
	// The lock lasts while cat runs: closing its stdin, or this process
	// exiting, releases it.
	cmd := exec.Command("systemd-inhibit", "--what=sleep:shutdown", "--mode=delay",
		"--who=chewedfeed", "--why=Release the glasses", "cat")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return func() {
		stdin.Close()
		cmd.Wait()
	}, nil
}
