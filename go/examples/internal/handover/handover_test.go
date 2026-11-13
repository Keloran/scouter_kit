package handover

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"reflect"
	"slices"
	"testing"
)

type fixtureStep struct {
	Event    Event    `json:"event"`
	Expected State    `json:"expected"`
	Effects  []Effect `json:"effects"`
}

type fixtureScenario struct {
	Name    string        `json:"name"`
	Initial State         `json:"initial"`
	Steps   []fixtureStep `json:"steps"`
}

func loadScenarios(t *testing.T) []fixtureScenario {
	t.Helper()
	b, err := os.ReadFile("../../../../tests/handover/transitions.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Version   int               `json:"version"`
		Scenarios []fixtureScenario `json:"scenarios"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if f.Version != 1 || len(f.Scenarios) == 0 {
		t.Fatalf("transitions.json: version %d, %d scenarios", f.Version, len(f.Scenarios))
	}
	return f.Scenarios
}

// Every step of the shared fixtures, state and effects exactly.
func TestFixtures(t *testing.T) {
	steps := 0
	for _, sc := range loadScenarios(t) {
		t.Run(sc.Name, func(t *testing.T) {
			s := sc.Initial
			for i, step := range sc.Steps {
				got := Reduce(s, step.Event)
				if got.State != step.Expected {
					t.Fatalf("step %d (%s): state\n got  %+v\n want %+v", i, step.Event.Kind, got.State, step.Expected)
				}
				if !reflect.DeepEqual(got.Effects, step.Effects) {
					t.Fatalf("step %d (%s): effects\n got  %+v\n want %+v", i, step.Event.Kind, got.Effects, step.Effects)
				}
				if err := checkStep(s, step.Event, got); err != nil {
					t.Fatalf("step %d: %v", i, err)
				}
				// Delivered twice, the same callback changes nothing more than once.
				if again := Reduce(got.State, step.Event); again.State.Generation != got.State.Generation && step.Event.Generation == got.State.Generation {
					t.Fatalf("step %d: a duplicate advanced the generation", i)
				}
				s = got.State
				steps++
			}
		})
	}
	if steps < 60 {
		t.Fatalf("only %d fixture steps", steps)
	}
}

// checkState: controller invariants (contract.py validate_state).
func checkState(s State) error {
	switch {
	case s.Pinned && s.Role != Phone:
		return fmt.Errorf("laptop pin")
	case s.PeerPinned && s.Role != Laptop:
		return fmt.Errorf("peer laptop pin")
	case s.Phase == Active && !(s.Eligible && s.Arms.both() && s.RendererReady):
		return fmt.Errorf("active before ready")
	case s.Phase == Active && s.Permission == NoPermission:
		return fmt.Errorf("active without permission")
	case (s.Phase == Active || s.Phase == Acquiring) && s.PeerActive && s.PeerReachable:
		return fmt.Errorf("two owners")
	case s.RendererReady && s.Phase != Active:
		return fmt.Errorf("renderer outside active")
	case s.Phase == Acquiring && (!s.Eligible || s.Permission == NoPermission):
		return fmt.Errorf("acquiring without permission")
	case s.Permission == Standalone && s.Enrolled:
		return fmt.Errorf("standalone on an enrolled host")
	case (s.Phase == Idle || s.Phase == Requesting || s.Phase == Recovering) && (s.Arms.any() || s.RendererReady):
		return fmt.Errorf("inactive state keeps resources")
	}
	return nil
}

// checkStep: temporal safety (contract.py validate_step).
func checkStep(before State, e Event, tr Transition) error {
	after := tr.State
	if err := checkState(after); err != nil {
		return fmt.Errorf("%s: %v (%+v)", e.Kind, err, after)
	}
	var names []EffectKind
	for _, f := range tr.Effects {
		if f.Generation != after.Generation || f.Transfer != after.Transfer {
			return fmt.Errorf("effect %s stamped with an old generation or transfer", f.Kind)
		}
		names = append(names, f.Kind)
	}
	has := func(k EffectKind) bool { return slices.Contains(names, k) }
	if len(names) == 0 {
		return fmt.Errorf("%s: no effects", e.Kind)
	}
	if e.Generation != before.Generation && (before != after || !reflect.DeepEqual(names, []EffectKind{Ignore})) {
		return fmt.Errorf("stale %s changed state", e.Kind)
	}
	if after.Generation < before.Generation {
		return fmt.Errorf("generation regressed")
	}
	if has(SendRequest) {
		if !after.Eligible || !after.PeerReachable {
			return fmt.Errorf("request without eligibility or a control path")
		}
		if after.Role == Phone && !after.Pinned && after.PeerEligible {
			return fmt.Errorf("phone overrode the laptop preference")
		}
		if after.Role == Laptop && (after.PeerPinned || e.Kind != PreferLocal || e.Data.StableMs < LaptopStabilityMs) {
			return fmt.Errorf("unstable laptop preference or pinned phone")
		}
	}
	if after.Transfer != before.Transfer {
		if e.Kind == ReleaseRequest || e.Kind == ReleaseNotice {
			if after.Transfer != e.Data.Transfer {
				return fmt.Errorf("wrong incoming transfer")
			}
		} else if after.Transfer == "" || after.Transfer != e.AttemptNonce {
			return fmt.Errorf("new transfer without the injected nonce")
		}
	}
	if has(AcquireBoth) {
		if after.Phase != Acquiring || after.Generation <= before.Generation || after.Transfer == "" {
			return fmt.Errorf("unfenced acquisition")
		}
		if !has(Schedule) {
			return fmt.Errorf("acquisition without a deadline")
		}
		proof := e.Kind == ReleasedEvent && e.Data.Transfer == before.Transfer
		if before.PeerReachable && before.PeerActive && !proof {
			return fmt.Errorf("acquisition before the owner released")
		}
		if after.Permission == Released && !proof {
			return fmt.Errorf("released permission without proof")
		}
		if after.Permission == Probe {
			if before.PeerReachable || e.Kind != Timeout {
				return fmt.Errorf("probe with a control path")
			}
			extra := int64(0)
			if after.Role == Phone {
				extra = PhoneProbeExtraMs
			}
			minimum := map[Timer]int64{PeerSuspect: PeerSuspectMs + extra, RetryTimer: RetryInitialMs + extra, ColdStartTimer: coldStartMs(after.Role)}
			m, ok := minimum[e.Data.Timer]
			if !ok || e.Data.ElapsedMs < m || e.Data.Timer == ColdStartTimer && before.PeerActive {
				return fmt.Errorf("early probe (%s %d ms)", e.Data.Timer, e.Data.ElapsedMs)
			}
		}
	}
	if has(SendReleased) {
		d := e.Data
		if e.Kind != ReleaseComplete || !d.BothDown || !d.WorkersStopped || !d.ReconnectDisabled {
			return fmt.Errorf("released without confirmation")
		}
		if after.Arms.any() || after.RendererReady {
			return fmt.Errorf("released while holding links")
		}
	}
	if has(SendReady) || has(Redraw) {
		if after.Phase != Active || e.Kind != Initialized || !e.Data.RendererReady {
			return fmt.Errorf("premature ready")
		}
		if has(SendReady) && slices.Index(names, Redraw) > slices.Index(names, SendReady) {
			return fmt.Errorf("ready before redraw")
		}
	}
	if has(ReleaseBoth) {
		if after.Generation <= before.Generation || after.RendererReady {
			return fmt.Errorf("release not fenced")
		}
		if e.Kind == ReleaseRequest {
			d := e.Data
			if d.Target == before.Role || before.Role == Phone && before.Pinned && d.Reason != "manual" {
				return fmt.Errorf("refused release given up")
			}
			if before.Role == Laptop && before.Eligible && !(d.Reason == "manual" && d.PeerPinned) {
				return fmt.Errorf("laptop preference violated")
			}
		}
	}
	if e.Kind == ClockAdjusted && (before != after || !reflect.DeepEqual(names, []EffectKind{Ignore})) {
		return fmt.Errorf("wall clock changed ownership")
	}
	if e.Kind == RouteLost && before.Phase == Active && (after.Phase != Active || has(ReleaseBoth)) {
		return fmt.Errorf("partition evicted a healthy owner")
	}
	return nil
}

// sim drives Reduce with a fake monotonic clock: schedule effects arm timers,
// cancel_timers clears them, and time advancing fires them as timeout events.
type sim struct {
	r      *rand.Rand
	s      State
	now    int64
	timers map[Timer][2]int64 // timer -> scheduled at, delay
	nonce  int
}

func (m *sim) apply(e Event) (Transition, error) {
	before := m.s
	tr := Reduce(before, e)
	if err := checkStep(before, e, tr); err != nil {
		return tr, err
	}
	for _, f := range tr.Effects {
		switch f.Kind {
		case Schedule:
			m.timers[f.Timer] = [2]int64{m.now, f.DelayMs}
		case CancelTimers:
			clear(m.timers)
		}
	}
	m.s = tr.State
	return tr, nil
}

func (m *sim) freshNonce() string {
	m.nonce++
	return fmt.Sprintf("%032x", m.nonce)
}

func (m *sim) randomEvent() Event {
	r, s := m.r, m.s
	gen := s.Generation
	if r.Intn(10) == 0 && gen > 0 {
		gen = uint64(r.Int63n(int64(gen))) // a stale callback
	}
	e := Event{Generation: gen, AttemptNonce: m.freshNonce()}
	if r.Intn(4) == 0 {
		e.AttemptNonce = ""
	}
	b := func() bool { return r.Intn(2) == 0 }
	transfer := s.Transfer
	if transfer == "" || r.Intn(3) == 0 {
		transfer = m.freshNonce()
	}
	switch r.Intn(17) {
	case 0:
		e.Kind, e.Data.Eligible = Start, b()
	case 1:
		e.Kind, e.Data.Eligible = Availability, b()
	case 2:
		e.Kind = PeerStatus
		e.Data.Active, e.Data.Reachable, e.Data.Eligible, e.Data.Pinned = b(), b(), b(), b() && s.Role == Laptop
	case 3:
		e.Kind, e.Data.StableMs = PreferLocal, r.Int63n(6000)
	case 4:
		target := Laptop
		if b() {
			target = Phone
		}
		reason := "preference"
		if b() {
			reason = "manual"
		}
		e.Kind, e.Data.Transfer, e.Data.Target, e.Data.Reason, e.Data.PeerPinned = ReleaseRequest, m.freshNonce(), target, reason, b()
	case 5:
		e.Kind, e.Data.Transfer = ReleasedEvent, transfer
	case 6:
		e.Kind, e.Data.Transfer = ReleaseNotice, m.freshNonce()
	case 7:
		e.Kind = ReleaseComplete
		e.Data.BothDown, e.Data.WorkersStopped, e.Data.ReconnectDisabled = r.Intn(4) > 0, r.Intn(4) > 0, r.Intn(4) > 0
	case 8:
		e.Kind, e.Data.Left, e.Data.Right = ArmsChanged, b(), b()
	case 9:
		e.Kind, e.Data.RendererReady = Initialized, r.Intn(4) > 0
	case 10:
		timers := []Timer{StabilityTimer, ReleaseTimer, AcquireTimer, PartialArmTimer, PeerSuspect, RetryTimer, ColdStartTimer}
		e.Kind, e.Data.Timer, e.Data.ElapsedMs = Timeout, timers[r.Intn(len(timers))], r.Int63n(70000)
	case 11:
		e.Kind = RouteLost
	case 12:
		e.Kind = RouteRestored
	case 13:
		e.Kind, e.Data.Enabled = Pin, b()
	case 14:
		e.Kind, e.Data.WallDeltaMs = ClockAdjusted, r.Int63n(172800000)-86400000
	case 15:
		e.Kind, e.Data.Reason = Failed, "acquire_failed"
	case 16:
		e.Kind = Stop
	}
	return e
}

// advance moves the clock and fires the timers that are due, oldest first.
func (m *sim) advance(ms int64) error {
	m.now += ms
	for {
		var due Timer
		var at int64
		for t, v := range m.timers {
			if m.now-v[0] >= v[1] && (due == NoTimer || v[0]+v[1] < at) {
				due, at = t, v[0]+v[1]
			}
		}
		if due == NoTimer {
			return nil
		}
		v := m.timers[due]
		delete(m.timers, due)
		e := Event{Kind: Timeout, Generation: m.s.Generation, AttemptNonce: m.freshNonce(),
			Data: EventData{Timer: due, ElapsedMs: m.now - v[0]}}
		if _, err := m.apply(e); err != nil {
			return err
		}
	}
}

// Random sequences for both roles, enrolled or not: every step keeps the
// contract's safety rules; effects always come with a bounded deadline.
func TestGeneratedSequences(t *testing.T) {
	for seed := int64(1); seed <= 400; seed++ {
		r := rand.New(rand.NewSource(seed))
		role := Laptop
		if seed%2 == 0 {
			role = Phone
		}
		m := &sim{r: r, s: NewState(role, seed%5 != 0), timers: make(map[Timer][2]int64)}
		for i := 0; i < 300; i++ {
			var err error
			if r.Intn(3) == 0 {
				err = m.advance(r.Int63n(20000))
			} else {
				before := m.s
				var tr Transition
				tr, err = m.apply(m.randomEvent())
				if err == nil {
					err = bounded(before, tr)
				}
			}
			if err != nil {
				t.Fatalf("seed %d step %d: %v", seed, i, err)
			}
		}
	}
}

// bounded: entering acquiring or yielding with native work always arms its deadline.
func bounded(before State, tr Transition) error {
	after := tr.State
	sched := map[Timer]bool{}
	for _, f := range tr.Effects {
		if f.Kind == Schedule {
			sched[f.Timer] = true
		}
	}
	if after.Phase == Acquiring && before.Phase != Acquiring && !sched[AcquireTimer] {
		return fmt.Errorf("acquiring without a deadline")
	}
	if after.Phase == Yielding && before.Phase != Yielding && before.Phase != Idle && !sched[ReleaseTimer] {
		return fmt.Errorf("releasing without a deadline")
	}
	return nil
}

// A healthy owner without a control path keeps the arms whatever the network,
// the peer's silence, the timers or the wall clock do.
func TestActiveOwnerStableUnderFlapping(t *testing.T) {
	for _, role := range []Role{Laptop, Phone} {
		r := rand.New(rand.NewSource(7))
		s := State{Role: role, Enrolled: true, Eligible: true, Phase: Active, Arms: Arms{true, true}, RendererReady: true,
			Generation: 4, Transfer: fmt.Sprintf("%032x", 4), Permission: Released, ReleaseIntent: NoRelease}
		for i := 0; i < 2000; i++ {
			e := Event{Generation: s.Generation, AttemptNonce: fmt.Sprintf("%032x", 100+i)}
			switch r.Intn(6) {
			case 0:
				e.Kind = RouteLost
			case 1:
				e.Kind = RouteRestored
			case 2:
				e.Kind = PeerStatus
				e.Data.Reachable, e.Data.Eligible = r.Intn(2) == 0, r.Intn(2) == 0
			case 3:
				e.Kind, e.Data.Timer, e.Data.ElapsedMs = Timeout, []Timer{PeerSuspect, RetryTimer, ColdStartTimer, StabilityTimer}[r.Intn(4)], r.Int63n(120000)
			case 4:
				e.Kind, e.Data.WallDeltaMs = ClockAdjusted, r.Int63n(86400000)
			case 5:
				e.Kind, e.Data.StableMs = PreferLocal, r.Int63n(10000)
			}
			tr := Reduce(s, e)
			if err := checkStep(s, e, tr); err != nil {
				t.Fatalf("%s step %d: %v", role, i, err)
			}
			if tr.State.Phase != Active || tr.State.Generation != s.Generation {
				t.Fatalf("%s step %d: %s took the arms from a healthy owner (%+v)", role, i, e.Kind, tr.State)
			}
			s = tr.State
		}
	}
}

// No grant before release: a requester only acquires on the peer's released
// for its own transfer, after any number of stale or foreign ones.
func TestNoAcquireBeforeMatchingRelease(t *testing.T) {
	s := State{Role: Laptop, Enrolled: true, Eligible: true, Phase: Idle, PeerReachable: true, PeerActive: true,
		PeerEligible: true, Permission: NoPermission, ReleaseIntent: NoRelease}
	s = Reduce(s, Event{Kind: PreferLocal, Data: EventData{StableMs: LaptopStabilityMs}, AttemptNonce: fmt.Sprintf("%032x", 1)}).State
	if s.Phase != Requesting {
		t.Fatalf("no request: %+v", s)
	}
	for i := 2; i < 50; i++ {
		for _, e := range []Event{
			{Kind: ReleasedEvent, Generation: s.Generation, Data: EventData{Transfer: fmt.Sprintf("%032x", i)}},
			{Kind: ReleasedEvent, Generation: s.Generation - 1, Data: EventData{Transfer: s.Transfer}},
			{Kind: Timeout, Generation: s.Generation, Data: EventData{Timer: PeerSuspect, ElapsedMs: 90000}, AttemptNonce: fmt.Sprintf("%032x", 1000+i)},
		} {
			if tr := Reduce(s, e); tr.State.Phase == Acquiring {
				t.Fatalf("acquired on %+v", e)
			}
		}
	}
	tr := Reduce(s, Event{Kind: ReleasedEvent, Generation: s.Generation, Data: EventData{Transfer: s.Transfer}})
	if tr.State.Phase != Acquiring || tr.State.Permission != Released {
		t.Fatalf("matching release did not grant: %+v", tr.State)
	}
}

func TestRetryDelay(t *testing.T) {
	prev := int64(0)
	for n := 0; n < 40; n++ {
		d := RetryDelayMs(n, 0)
		if d < prev || d > RetryMaxMs {
			t.Fatalf("retry %d: %d ms", n, d)
		}
		if j := RetryDelayMs(n, 1); j < d || float64(j) > float64(d)*(1+RetryJitterFraction) {
			t.Fatalf("retry %d: jitter %d ms on %d", n, j, d)
		}
		prev = d
	}
	if RetryDelayMs(0, 0) != RetryInitialMs || RetryDelayMs(1, 0) != 2*RetryInitialMs || RetryDelayMs(99, 2) != RetryMaxMs*6/5 {
		t.Fatal("retry schedule")
	}
}
