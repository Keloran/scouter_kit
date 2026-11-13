package main

import (
	"context"
	"errors"
	"sync"

	"scouterkit"
	"scouterkit/examples/internal/demo"
)

// A glasses session is one ownership of the glasses, from Init to Deinit: the
// connection, the screen's font and bitmap, the standby program, input
// reporting, the battery worker and the head-angle setting. The dashboard's
// data, navigation and notification history live outside it, so the glasses
// can be released and acquired again without restarting the process; every
// handle comes from the current session and dies with it.
type glassesSession struct {
	cfg           scouterkit.Config
	arms          []scouterkit.Arm
	screen        *screen
	standby       uint32
	cases         caseState
	batteries     <-chan batteryStatus
	stopBatteries func()
	pitchSet      bool
	missed        map[scouterkit.Arm]int
}

// TILT's lower bound while a session runs (-90, the default, is restored on close).
var pitchLo int32 = -89

type hostState int

const (
	hostReleased hostState = iota // no session: nothing is sent to the glasses
	hostActive
)

type hostStatus struct {
	State      hostState
	Generation uint64 // bumped by each successful Acquire
	Arms       int
	Err        error // why the last Acquire failed, or why the last session was lost
}

// glassesHost owns at most one session (scouterkit is process-global).
// Acquire and Release are called from the dashboard loop; Status from anywhere.
type glassesHost struct {
	f demo.Flags
	o options

	mu      sync.Mutex
	session *glassesSession
	status  hostStatus
}

func (h *glassesHost) Status() hostStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.status
}

func (h *glassesHost) current() *glassesSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.session
}

// Acquire connects and sets up a fresh session: new font and bitmap, standby
// program, input reporting and battery worker. A failure leaves nothing
// initialized.
func (h *glassesHost) Acquire(ctx context.Context) error {
	if h.current() != nil {
		return nil
	}
	s, err := openSession(ctx, h.f, h.o)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.status.Err = err
	if err != nil {
		return err
	}
	h.session = s
	h.status.State, h.status.Arms = hostActive, len(s.arms)
	h.status.Generation++
	return nil
}

// Release stops the session's workers, joins them and deinitializes before it
// returns. lost records why it was not asked for (nil: an expected release).
func (h *glassesHost) Release(lost error) {
	h.mu.Lock()
	s := h.session
	h.session = nil
	h.status.State, h.status.Arms, h.status.Err = hostReleased, 0, lost
	h.mu.Unlock()
	if s != nil {
		s.close()
	}
}

func openSession(ctx context.Context, f demo.Flags, o options) (*glassesSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	type connected struct {
		cfg  scouterkit.Config
		arms []scouterkit.Arm
		err  error
	}
	done := make(chan connected, 1)
	go func() {
		cfg, arms, err := connectDisplay(f, o)
		done <- connected{cfg, arms, err}
	}()
	var c connected
	select {
	case c = <-done:
	case <-ctx.Done():
		// ponytail: Init cannot be interrupted, so a cancel waits for it (at most
		// its 30 s scan), then deinitializes. An abortable Init would end this sooner.
		c = <-done
		if c.err == nil {
			scouterkit.Deinit()
		}
		return nil, ctx.Err()
	}
	if c.err != nil {
		return nil, c.err
	}
	s := &glassesSession{cfg: c.cfg, arms: c.arms, missed: make(map[scouterkit.Arm]int)}
	if err := s.setup(ctx, o); err != nil {
		s.close()
		return nil, err
	}
	return s, nil
}

func (s *glassesSession) setup(ctx context.Context, o options) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var err error
	if s.screen, err = newScreenScale(s.cfg, o.fontScale); err != nil {
		return err
	}
	if s.standby, err = installStandby(s.cfg, s.screen, o.fontScale, s.arms); err != nil {
		return errors.Join(errors.New("standby program"), err)
	}
	for _, arm := range s.arms {
		progress("Enabling touch controls on %s arm", arm)
		mask, err := arm.ReportInputs(1<<scouterkit.TriggerTouch | 1<<scouterkit.TriggerCase)
		if err != nil {
			return err
		}
		if mask&(1<<scouterkit.TriggerTouch) == 0 {
			progress("%s arm: touch unavailable; use automatic rotation", arm)
		}
	}
	if s.cases, err = initialCaseState(s.arms); err != nil {
		return err
	}
	s.batteries, s.stopBatteries = monitorBatteries(context.Background(), s.arms)
	if len(s.arms) > 0 {
		// A TILT range narrower than ±90 makes the firmware stream every sample; without it,
		// PITCH read on demand stayed at its first value. Restored on close (sensor settings
		// outlive the session).
		s.arms[0].SysWrite(scouterkit.PropPitchLo, uint32(pitchLo))
		s.pitchSet = true
	}
	return nil
}

// close undoes setup in reverse, then deinitializes. The standby program runs
// on: the arms show "Not connected" and their battery once the link is gone.
func (s *glassesSession) close() {
	if s.pitchSet {
		s.arms[0].SysWrite(scouterkit.PropPitchLo, uint32(pitchLo-1))
	}
	if s.stopBatteries != nil {
		s.stopBatteries() // joins: battery reads use the library
	}
	for _, arm := range s.arms {
		arm.ReportInputs(0)
	}
	scouterkit.Deinit()
}
