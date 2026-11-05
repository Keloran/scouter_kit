//go:build emulator

package main

import (
	"path/filepath"
	"testing"
	"time"

	"scouterkit"
	"scouterkit/examples/internal/demo"
)

func setup(t *testing.T, c config) *loaded {
	t.Helper()
	cfg := scouterkit.DefaultConfig()
	if err := scouterkit.Init(cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(scouterkit.Deinit)
	ld, err := load(cfg, demo.Present(), c)
	if err != nil {
		t.Fatal(err)
	}
	return ld
}

func defaults() config {
	return config{th: defaultThresholds(), lines: 5, rate: 52, hold: time.Second}
}

// The program on both emulated arms detects exactly what the Go model does
// on synthetic traces: 50 single and 50 double nods, 10 minutes of walking
// and 4 minutes at rest; the rates are those of the acceptance (docs/review-
// v5.md §9).
func TestReplayEqualsModel(t *testing.T) {
	ld := setup(t, defaults())
	var traces []trace
	for seed := int64(1); seed <= 10; seed++ {
		traces = append(traces, nodTrace(100+seed, gestureNod, 5), nodTrace(1100+seed, gestureDouble, 5))
	}
	traces = append(traces, walkingTrace(2101, 300), walkingTrace(2102, 300), stillTrace(3101, 120, true),
		stillTrace(3102, 120, true))
	scores := map[string]*score{}
	for _, tr := range traces {
		want := detect(ld.pr, tr.samples)
		for _, arm := range ld.arms {
			got, err := ld.replay(arm, tr.samples)
			if err != nil {
				t.Fatal(err)
			}
			if !sameDetections(got, want) {
				t.Fatalf("%s arm, %s: detected %v, the model %v", arm, tr.name, got, want)
			}
		}
		if scores[tr.name] == nil {
			scores[tr.name] = &score{}
		}
		scores[tr.name].add(tr, want, ld.pr)
	}
	for name, s := range scores {
		t.Logf("%s: %v", name, s)
	}
	for _, name := range []string{"nods", "double nods"} {
		if s := scores[name]; s.found*10 < s.gestures*9 || s.falses > 0 {
			t.Errorf("%s: %v", name, s)
		}
	}
	if f := scores["walking"].falses + scores["still"].falses; f > 1 {
		t.Errorf("%d false gestures walking and at rest", f)
	}
}

// feed sets the arms' sensor values from samples every 20 ms, in real time.
func feed(arms []scouterkit.Arm, samples []sample) {
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for _, x := range samples {
		for _, arm := range arms {
			arm.SetEmulatedProperty(scouterkit.PropPitch, uint32(x.pitch))
			arm.SetEmulatedProperty(scouterkit.PropAccX, uint32(x.ax))
			arm.SetEmulatedProperty(scouterkit.PropAccY, uint32(x.ay))
			arm.SetEmulatedProperty(scouterkit.PropAccZ, uint32(x.az))
		}
		<-tick.C
	}
}

// Live: the TIMER-driven program sees a nod and then a double nod fed in
// real time and reports them; the host relays the first report to both
// arms: the nod shows page 1, the double nod turns back to the last page
// (read back on both panels); the list hides after the hold.
func TestLive(t *testing.T) {
	c := defaults()
	c.hold = 3 * time.Second
	ld := setup(t, c)
	first, second := nodTrace(7, gestureNod, 1), nodTrace(1007, gestureDouble, 1)
	samples := append(append([]sample{}, first.samples[:first.truth[0].end+40]...),
		second.samples[second.truth[0].start-60:]...)
	feed(ld.arms, samples[:1])
	if err := ld.start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		feed(ld.arms, samples)
		close(done)
	}()
	pages := ld.g[ld.arms[0]].pages
	for _, want := range []struct{ gesture, page uint32 }{{gestureNod, 0}, {gestureDouble, uint32(pages - 1)}} {
		gestures, shown := map[scouterkit.Arm]bool{}, map[scouterkit.Arm]uint32{}
		deadline := time.Now().Add(10 * time.Second)
		for len(gestures) < len(ld.arms) || len(shown) < len(ld.arms) {
			if time.Now().After(deadline) {
				t.Fatalf("gesture %d: reported by %v, page shown by %v", want.gesture, gestures, shown)
			}
			for _, arm := range ld.arms {
				e, ok := arm.NextEvent(5 * time.Millisecond)
				switch {
				case !ok || e.Type != scouterkit.EventNotify:
				case e.A == want.gesture:
					t.Logf("%s arm: gesture %d at sample %d", arm, e.A, e.B)
					gestures[arm] = true
					ld.relay(e)
				case e.A == notePage:
					shown[arm] = e.B
				}
			}
		}
		for _, arm := range ld.arms {
			if shown[arm] != want.page {
				t.Fatalf("%s arm: page %d after gesture %d, want %d", arm, shown[arm], want.gesture, want.page)
			}
			if err := ld.checkPage(arm, int(want.page)); err != nil {
				t.Fatal(err)
			}
		}
	}
	<-done
	for _, arm := range ld.arms {
		if _, err := ld.wait(arm, noteHidden); err != nil {
			t.Fatal(err)
		}
		if v, err := arm.Props(scouterkit.PropPanel, scouterkit.PropIMURate); err != nil || v[0] != 0 || v[1] != 52 {
			t.Fatalf("%s arm: panel, IMU_RATE %v (%v)", arm, v, err)
		}
	}
}

// At rest the program samples at 50 Hz and reports nothing; with -record
// each sample comes as a NOTIFY carrying the values.
func TestIdleAndRecord(t *testing.T) {
	c := defaults()
	c.record = "x"
	ld := setup(t, c)
	tr := stillTrace(9, 4, false)
	feed(ld.arms, tr.samples[:1])
	if err := ld.start(); err != nil {
		t.Fatal(err)
	}
	go feed(ld.arms, tr.samples)
	time.Sleep(200 * time.Millisecond)
	for _, arm := range ld.arms {
		demo.Drain(arm)
	}
	seen := map[scouterkit.Arm]int{}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, arm := range ld.arms {
			e, ok := arm.NextEvent(5 * time.Millisecond)
			switch {
			case !ok || e.Type != scouterkit.EventNotify:
			case e.B>>24 == recordMark>>24:
				seen[arm]++
				if int8(e.B>>16) < -30 || int8(e.B>>16) > 30 || int16(e.A>>16) < 800 {
					t.Fatalf("%s arm: recorded pitch %d, ay %d", arm, int8(e.B>>16), int16(e.A>>16))
				}
			default:
				t.Fatalf("%s arm: NOTIFY %x %x at rest", arm, e.A, e.B)
			}
		}
	}
	for _, arm := range ld.arms {
		if seen[arm] < 90 || seen[arm] > 102 {
			t.Errorf("%s arm: %d samples recorded in 2 s", arm, seen[arm])
		}
	}
}

// The shake hook: gesture 3 (raised by no detector yet) hides the list.
func TestShakeHides(t *testing.T) {
	ld := setup(t, defaults())
	if err := ld.start(); err != nil {
		t.Fatal(err)
	}
	arm := ld.arms[0]
	ld.send(gestureNod)
	if _, err := ld.wait(arm, notePage); err != nil {
		t.Fatal(err)
	}
	ld.send(gestureShake)
	if _, err := ld.wait(arm, noteHidden); err != nil {
		t.Fatal(err)
	}
	if v, _ := arm.Props(scouterkit.PropPanel); v[0] != 0 {
		t.Fatal("the panel stayed on")
	}
}

// -record writes what the arms sampled; -replay's reader gets it back, and
// the detector finds the nod in it.
func TestRecordReplay(t *testing.T) {
	c := defaults()
	c.record = filepath.Join(t.TempDir(), "rec.csv")
	ld := setup(t, c)
	tr := nodTrace(21, gestureNod, 1)
	feed(ld.arms, tr.samples[:1])
	if err := ld.start(); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- ld.watch(c, stop) }()
	feed(ld.arms, tr.samples)
	close(stop)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	per, order, err := readRecord(c.record)
	if err != nil {
		t.Fatal(err)
	}
	if len(order) != len(ld.arms) {
		t.Fatalf("arms in the record: %v", order)
	}
	for _, arm := range order {
		s := per[arm]
		got := detect(ld.pr, s)
		t.Logf("%s arm: %d samples recorded, %v", arm, len(s), got)
		if len(s) < len(tr.samples)*9/10 || len(got) != 1 || got[0].kind != gestureNod {
			t.Errorf("%s arm: %d samples, detected %v", arm, len(s), got)
		}
	}
}
