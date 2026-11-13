package handover

import (
	"fmt"
	"slices"
	"testing"
)

func TestInactiveStopRetiresRecovery(t *testing.T) {
	for _, role := range []Role{Laptop, Phone} {
		for _, phase := range []Phase{Idle, Recovering} {
			for _, enrolled := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/enrolled=%t", role, phase, enrolled), func(t *testing.T) {
					s := NewState(role, enrolled)
					s.Phase, s.Eligible, s.Generation = phase, true, 7
					s.Transfer = "11111111111111111111111111111111"
					m := &sim{s: s, timers: map[Timer][2]int64{RetryTimer: {0, RetryInitialMs}}}
					tr, err := m.apply(Event{Kind: Stop, Generation: s.Generation})
					if err != nil {
						t.Fatal(err)
					}
					if tr.State.Phase != Idle || tr.State.Eligible || tr.State.Generation <= s.Generation || len(m.timers) != 0 {
						t.Fatalf("stop left an inactive attempt live: %+v", tr)
					}
					for _, f := range tr.Effects {
						if slices.Contains([]EffectKind{ReleaseBoth, SendReleased, AcquireBoth}, f.Kind) {
							t.Fatal("inactive stop fabricated native work or release proof")
						}
					}
					if err := m.advance(60000); err != nil {
						t.Fatal(err)
					}
					if m.s.Phase != Idle {
						t.Fatal("retry revived a stopped host")
					}
					resume, err := m.apply(Event{Kind: Availability, Generation: m.s.Generation, Data: EventData{Eligible: true}, AttemptNonce: "22222222222222222222222222222222"})
					if err != nil {
						t.Fatal(err)
					}
					want := Recovering
					if !enrolled {
						want = Acquiring
					}
					if resume.State.Phase != want || !resume.State.Eligible {
						t.Fatalf("resume was stranded: %+v", resume)
					}
					before := m.s
					for _, e := range []Event{
						{Kind: Timeout, Generation: s.Generation, Data: EventData{Timer: RetryTimer, ElapsedMs: 60000}, AttemptNonce: "11111111111111111111111111111111"},
						{Kind: ReleasedEvent, Generation: s.Generation, Data: EventData{Transfer: s.Transfer}},
						{Kind: Stop, Generation: s.Generation},
					} {
						if _, err := m.apply(e); err != nil {
							t.Fatal(err)
						}
						if m.s != before {
							t.Fatal("retired callback changed resumed ownership")
						}
					}
				})
			}
		}
	}
}

func TestUnavailableRecoveryRetiresTimer(t *testing.T) {
	for _, role := range []Role{Laptop, Phone} {
		s := NewState(role, true)
		s.Phase, s.Eligible, s.Generation = Recovering, true, 7
		m := &sim{s: s, timers: map[Timer][2]int64{PeerSuspect: {0, PeerSuspectMs}}}
		if _, err := m.apply(Event{Kind: Availability, Generation: s.Generation, Data: EventData{Eligible: false}}); err != nil {
			t.Fatal(err)
		}
		if m.s.Phase != Idle || m.s.Eligible || m.s.Generation <= s.Generation || len(m.timers) != 0 {
			t.Fatalf("%s availability kept recovery alive: %+v", role, m.s)
		}
		if err := m.advance(60000); err != nil {
			t.Fatal(err)
		}
		if m.s.Phase != Idle {
			t.Fatal("ineligible host retried")
		}
		if _, err := m.apply(Event{Kind: Availability, Generation: m.s.Generation, Data: EventData{Eligible: true}}); err != nil {
			t.Fatal(err)
		}
		if m.s.Phase != Recovering {
			t.Fatal("fresh eligibility could not restart reconciliation")
		}
	}
}
