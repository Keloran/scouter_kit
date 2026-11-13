package handover

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"testing"
)

// tests/handover/generated_sequences.json: seeded random event sequences and
// a digest of the transitions this reducer produces for them. The Kotlin
// reducer (examples/chewedfeed_android) must produce the same digests.
// go test -run CrossPlatform -update-sequences rewrites the file.
var updateSequences = flag.Bool("update-sequences", false, "rewrite tests/handover/generated_sequences.json")

type generatedSequence struct {
	Seed    int64       `json:"seed"`
	Initial schemaState `json:"initial"`
	Events  []wireEvent `json:"events"`
	Digest  string      `json:"digest"`
}

// wireEvent is controller.schema.json's event with only the kind's own data fields.
type wireEvent struct {
	Kind         EventKind      `json:"kind"`
	Generation   uint64         `json:"generation"`
	Data         map[string]any `json:"data"`
	AttemptNonce *string        `json:"attempt_nonce"`
}

// schemaState is a State as controller.schema.json writes it: transfer null, not "".
type schemaState struct{ State }

func (s schemaState) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(s.State)
	if err != nil || s.Transfer != "" {
		return b, err
	}
	var m map[string]any
	json.Unmarshal(b, &m)
	m["transfer"] = nil
	return json.Marshal(m)
}

func (w wireEvent) event() Event {
	b, _ := json.Marshal(w.Data)
	var d EventData
	json.Unmarshal(b, &d)
	e := Event{Kind: w.Kind, Generation: w.Generation, Data: d}
	if w.AttemptNonce != nil {
		e.AttemptNonce = *w.AttemptNonce
	}
	return e
}

func b01(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// canonical is the text both reducers digest: the state, then each effect.
func canonical(t Transition) string {
	s := t.State
	parts := []string{strings.Join([]string{string(s.Role), b01(s.Enrolled), b01(s.Eligible), b01(s.Pinned), string(s.Phase),
		b01(s.Arms.Left), b01(s.Arms.Right), b01(s.RendererReady), fmt.Sprint(s.Generation), dash(s.Transfer),
		b01(s.PeerReachable), b01(s.PeerActive), string(s.Permission), string(s.ReleaseIntent), b01(s.PeerEligible),
		b01(s.PeerPinned)}, ",")}
	for _, f := range t.Effects {
		parts = append(parts, strings.Join([]string{string(f.Kind), fmt.Sprint(f.Generation), dash(f.Transfer),
			dash(string(f.Timer)), fmt.Sprint(f.DelayMs)}, ","))
	}
	return strings.Join(parts, ";")
}

func digest(initial State, events []wireEvent) string {
	h := sha256.New()
	s := initial
	for _, w := range events {
		t := Reduce(s, w.event())
		fmt.Fprintln(h, canonical(t))
		s = t.State
	}
	return hex.EncodeToString(h.Sum(nil))
}

// randomWireEvent: like sim.randomEvent, with only the kind's fields.
func randomWireEvent(r *rand.Rand, s State, nonce func() string) wireEvent {
	gen := s.Generation
	if r.Intn(10) == 0 && gen > 0 {
		gen = uint64(r.Int63n(int64(gen)))
	}
	b := func() bool { return r.Intn(2) == 0 }
	transfer := s.Transfer
	if transfer == "" || r.Intn(3) == 0 {
		transfer = nonce()
	}
	timers := []Timer{StabilityTimer, ReleaseTimer, AcquireTimer, PartialArmTimer, PeerSuspect, RetryTimer, ColdStartTimer}
	var kind EventKind
	data := map[string]any{}
	switch r.Intn(17) {
	case 0:
		kind, data["eligible"] = Start, b()
	case 1:
		kind, data["eligible"] = Availability, b()
	case 2:
		kind = PeerStatus
		data["active"], data["reachable"], data["eligible"], data["pinned"] = b(), b(), b(), b() && s.Role == Laptop
	case 3:
		kind, data["stable_ms"] = PreferLocal, r.Int63n(6000)
	case 4:
		target, reason := Laptop, "preference"
		if b() {
			target = Phone
		}
		if b() {
			reason = "manual"
		}
		kind = ReleaseRequest
		data["transfer"], data["target"], data["reason"], data["peer_pinned"] = nonce(), target, reason, b()
	case 5:
		kind, data["transfer"] = ReleasedEvent, transfer
	case 6:
		kind, data["transfer"] = ReleaseNotice, nonce()
	case 7:
		kind = ReleaseComplete
		data["both_down"], data["workers_stopped"], data["reconnect_disabled"] = r.Intn(4) > 0, r.Intn(4) > 0, r.Intn(4) > 0
	case 8:
		kind, data["left"], data["right"] = ArmsChanged, b(), b()
	case 9:
		kind, data["renderer_ready"] = Initialized, r.Intn(4) > 0
	case 10:
		kind, data["timer"], data["elapsed_ms"] = Timeout, timers[r.Intn(len(timers))], r.Int63n(70000)
	case 11:
		kind = RouteLost
	case 12:
		kind = RouteRestored
	case 13:
		kind, data["enabled"] = Pin, b()
	case 14:
		kind, data["wall_delta_ms"] = ClockAdjusted, r.Int63n(172800000)-86400000
	case 15:
		kind, data["reason"] = Failed, "acquire_failed"
	case 16:
		kind = Stop
	}
	w := wireEvent{Kind: kind, Generation: gen, Data: data}
	if r.Intn(4) > 0 {
		n := nonce()
		w.AttemptNonce = &n
	}
	return w
}

func generateSequences() []generatedSequence {
	var out []generatedSequence
	for seed := int64(1); seed <= 24; seed++ {
		r := rand.New(rand.NewSource(seed))
		role := Laptop
		if seed%2 == 0 {
			role = Phone
		}
		n := 0
		nonce := func() string { n++; return fmt.Sprintf("%032x", seed<<32|int64(n)) }
		initial := NewState(role, seed%6 != 0)
		s, events := initial, []wireEvent{}
		for i := 0; i < 150; i++ {
			w := randomWireEvent(r, s, nonce)
			events = append(events, w)
			s = Reduce(s, w.event()).State
		}
		out = append(out, generatedSequence{Seed: seed, Initial: schemaState{initial}, Events: events, Digest: digest(initial, events)})
	}
	return out
}

func TestCrossPlatformSequences(t *testing.T) {
	path := fixtures + "generated_sequences.json"
	if *updateSequences {
		b, err := json.Marshal(struct {
			Version     int                 `json:"version"`
			Description string              `json:"description"`
			Sequences   []generatedSequence `json:"sequences"`
		}{1, "Seeded random reducer inputs; digest = SHA-256 of one canonical line per step (see crossplatform_test.go canonical).", generateSequences()})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var f struct{ Sequences []generatedSequence }
	readFixture(t, "generated_sequences.json", &f)
	if len(f.Sequences) < 20 {
		t.Fatalf("%d sequences", len(f.Sequences))
	}
	for _, q := range f.Sequences {
		if got := digest(q.Initial.State, q.Events); got != q.Digest {
			t.Errorf("seed %d: digest %s, file %s (reducer changed: regenerate and update the Kotlin side)", q.Seed, got, q.Digest)
		}
	}
}
