package handover

import (
	"fmt"
	"slices"
	"testing"
)

func waitingOwner(role Role, phase Phase) State {
	s := NewState(role, true)
	s.Phase, s.Eligible = phase, true
	s.PeerReachable, s.PeerActive, s.PeerEligible = true, true, true
	s.Generation, s.Transfer = 7, "11111111111111111111111111111111"
	return s
}

func TestRouteLossEventuallyProbes(t *testing.T) {
	for _, role := range []Role{Laptop, Phone} {
		for _, phase := range []Phase{Idle, Requesting} {
			t.Run(fmt.Sprintf("%s/%s", role, phase), func(t *testing.T) {
				initial := waitingOwner(role, phase)
				m := &sim{s: initial, timers: make(map[Timer][2]int64)}
				apply := func(e Event) Transition {
					t.Helper()
					tr, err := m.apply(e)
					if err != nil {
						t.Fatal(err)
					}
					return tr
				}
				apply(Event{Kind: RouteLost, Generation: initial.Generation})
				// A release callback already queued by the dead route cannot authorize anything.
				apply(Event{Kind: ReleasedEvent, Generation: initial.Generation,
					Data: EventData{Transfer: initial.Transfer}})
				if m.s.Phase != Recovering {
					t.Fatalf("stranded in %s", m.s.Phase)
				}
				delay := int64(PeerSuspectMs)
				if role == Phone {
					delay += PhoneProbeExtraMs
				}
				if err := m.advance(delay - 1); err != nil {
					t.Fatal(err)
				}
				if m.s.Phase != Recovering {
					t.Fatal("probe before suspect deadline")
				}
				if err := m.advance(1); err != nil {
					t.Fatal(err)
				}
				if m.s.Phase != Acquiring || m.s.Permission != Probe || m.s.Transfer == initial.Transfer {
					t.Fatalf("no fresh non-preemptive probe at deadline: %+v", m.s)
				}
				if _, ok := m.timers[AcquireTimer]; !ok {
					t.Fatal("probe has no acquisition deadline")
				}
				// The old owner becoming reachable during the probe forces teardown.
				tr := apply(Event{Kind: RouteRestored, Generation: m.s.Generation})
				if tr.State.Phase != Yielding || !slices.ContainsFunc(tr.Effects, func(f Effect) bool { return f.Kind == ReleaseBoth }) {
					t.Fatalf("probe kept arms after owner returned: %+v", tr)
				}
			})
		}
	}
}

func TestRestoredOwnerPreventsProbe(t *testing.T) {
	s := waitingOwner(Laptop, Requesting)
	m := &sim{s: s, timers: make(map[Timer][2]int64)}
	for _, kind := range []EventKind{RouteLost, RouteRestored} {
		if _, err := m.apply(Event{Kind: kind, Generation: m.s.Generation}); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.advance(PeerSuspectMs); err != nil {
		t.Fatal(err)
	}
	if m.s.Phase == Acquiring || m.s.Permission != NoPermission {
		t.Fatalf("probe took permission from a reachable owner: %+v", m.s)
	}
}

func TestPhoneRollbackRetryEventuallyProbes(t *testing.T) {
	s := waitingOwner(Phone, Acquiring)
	s.PeerReachable, s.Permission = false, Probe
	m := &sim{s: s, timers: make(map[Timer][2]int64)}
	for _, e := range []Event{
		{Kind: Failed, Generation: s.Generation},
		{Kind: ReleaseComplete, Generation: s.Generation + 1,
			Data: EventData{BothDown: true, WorkersStopped: true, ReconnectDisabled: true}},
	} {
		if _, err := m.apply(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.advance(RetryInitialMs + PhoneProbeExtraMs - 1); err != nil {
		t.Fatal(err)
	}
	if m.s.Phase != Recovering {
		t.Fatal("retry before phone delay")
	}
	if err := m.advance(1); err != nil {
		t.Fatal(err)
	}
	if m.s.Phase != Acquiring || m.s.Permission != Probe {
		t.Fatalf("rollback stranded the phone after its only retry timer: %+v", m.s)
	}
}

func TestStopRetiresRequest(t *testing.T) {
	s := waitingOwner(Laptop, Requesting)
	m := &sim{s: s, timers: map[Timer][2]int64{PeerSuspect: {0, PeerSuspectMs}}}
	if _, err := m.apply(Event{Kind: Stop, Generation: s.Generation}); err != nil {
		t.Fatal(err)
	}
	if m.s.Eligible || m.s.Phase != Idle || len(m.timers) != 0 || m.s.Generation <= s.Generation {
		t.Fatalf("stopped request is still live: %+v", m.s)
	}
	for _, generation := range []uint64{s.Generation, m.s.Generation} {
		tr, err := m.apply(Event{Kind: ReleasedEvent, Generation: generation, Data: EventData{Transfer: s.Transfer}})
		if err != nil {
			t.Fatal(err)
		}
		if tr.State.Phase != Idle || tr.State.Eligible {
			t.Fatalf("late release revived stopped request: %+v", tr.State)
		}
	}
}

func TestRouteLossPreservesNativeWork(t *testing.T) {
	for _, phase := range []Phase{Active, Acquiring, Yielding} {
		s := waitingOwner(Laptop, phase)
		s.PeerActive, s.Permission = false, Released
		if phase == Active {
			s.Arms, s.RendererReady = Arms{true, true}, true
		}
		tr := Reduce(s, Event{Kind: RouteLost, Generation: s.Generation})
		if tr.State.Phase != phase || tr.State.Generation != s.Generation || tr.State.Arms != s.Arms {
			t.Fatalf("%s native work changed: %+v", phase, tr)
		}
		if slices.ContainsFunc(tr.Effects, func(f Effect) bool { return f.Kind == CancelTimers || f.Kind == ReleaseBoth }) {
			t.Fatalf("%s lost its native deadline or ownership", phase)
		}
	}
}
