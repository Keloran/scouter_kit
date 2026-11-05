//go:build emulator

package main

import (
	"encoding/binary"
	"testing"
	"time"

	"scouterkit"
	"scouterkit/examples/internal/demo"
)

func defaults() config {
	return config{ref: 0.8, hud: 2, label: 0.8, menu: 0.6, near: 0.6, fps: 25, cycle: 4 * time.Second}
}

func setup(t *testing.T, c config) *scene {
	t.Helper()
	cfg := scouterkit.DefaultConfig()
	if err := scouterkit.Init(cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(scouterkit.Deinit)
	s, err := newScene(cfg, demo.Present(), c)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.load(c); err != nil {
		t.Fatal(err)
	}
	if err := s.start(); err != nil {
		t.Fatal(err)
	}
	return s
}

// composed is the arm's panel with the still elements and the slider at
// frame k, as the scene places them.
func (s *scene) composed(arm scouterkit.Arm, k int) *demo.Image {
	m := demo.NewPanel()
	for _, e := range s.still {
		x, y := s.at(arm, e, e.disparity(s.g))
		m.Paste(e.image, x, y)
	}
	x, y := s.at(arm, s.slider, s.ds[k])
	m.Paste(s.slider.image, x, y)
	return m
}

func samePanel(t *testing.T, arm scouterkit.Arm, want *demo.Image) {
	t.Helper()
	got := demo.FromPanel(arm.EmulatedPanel())
	if n, x, y := demo.Diff(got, want); n != 0 {
		t.Fatalf("%s arm: %d pixels differ from the composed panel, the first at (%d, %d)", arm, n, x, y)
	}
}

// The read-back check finds every element at its depth's offsets on both
// panels; the emulated panels, read directly, hold exactly those elements.
func TestOffsets(t *testing.T) {
	s := setup(t, defaults())
	if err := s.check(); err != nil {
		t.Fatal(err)
	}
	for _, arm := range s.arms {
		samePanel(t, arm, s.composed(arm, 3*len(s.ds)/4))
	}
	want := map[string]int{"HUD": 40, "label": 100, "menu": 133}
	for _, e := range s.still {
		if d := e.disparity(s.g); d != want[e.name] {
			t.Errorf("%s: disparity %d, want %d", e.name, d, want[e.name])
		}
	}
	if s.ds[0] != 0 || s.ds[len(s.ds)/2] != 133 {
		t.Errorf("slider from %d to %d px, want 0 to 133", s.ds[0], s.ds[len(s.ds)/2])
	}
}

// Nearer than 150 px of disparity is refused.
func TestTooNear(t *testing.T) {
	cfg := scouterkit.DefaultConfig()
	if err := scouterkit.Init(cfg); err != nil {
		t.Fatal(err)
	}
	defer scouterkit.Deinit()
	c := defaults()
	c.menu = 0.5
	if _, err := newScene(cfg, demo.Present(), c); err == nil {
		t.Fatal("a menu at 0.5 m (160 px) was accepted")
	}
}

// The timed slider: one frame per period on each arm, both arms within
// 2 ms of each other; once stopped, the panels hold the last frame drawn.
func TestTimed(t *testing.T) {
	s := setup(t, defaults())
	s.animate(200 * time.Millisecond)
	var skew demo.Skew
	last := map[scouterkit.Arm]scouterkit.Event{}
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		for _, arm := range s.arms {
			e, ok := arm.NextEvent(5 * time.Millisecond)
			if !ok || e.Type != scouterkit.EventNotify {
				continue
			}
			if p, ok := last[arm]; ok {
				if e.B != (p.B+1)%uint32(len(s.ds)) {
					t.Fatalf("%s arm: frame %d after %d", arm, e.B, p.B)
				}
				if d := int32(e.DeviceUS - p.DeviceUS); d < int32(s.period)-3000 || d > int32(s.period)+3000 {
					t.Errorf("%s arm: frame %d %d µs after the one before", arm, e.B, d)
				}
			}
			last[arm] = e
			if d, ok := skew.Add(arm, e.A, e); ok && (d > 2 || d < -2) {
				t.Errorf("frame %d: eyes %.1f ms apart", e.A, d)
			}
		}
	}
	if len(last) != len(s.arms) || last[s.arms[0]].B < 20 {
		t.Fatalf("frames seen: %v", last)
	}
	t.Log(skew.String())
	for _, arm := range s.arms {
		if err := arm.Stop(); err != nil {
			t.Fatal(err)
		}
		st, err := arm.ReadArena(s.l.state+4*wFrame, 4)
		if err != nil {
			t.Fatal(err)
		}
		k := (int(binary.LittleEndian.Uint32(st)) + len(s.ds) - 1) % len(s.ds)
		samePanel(t, arm, s.composed(arm, k))
	}
}

// With -cycles 1 the arms switch the panels off after one cycle.
func TestCycles(t *testing.T) {
	c := defaults()
	c.cycle, c.cycles = 400*time.Millisecond, 1
	s := setup(t, c)
	s.animate(100 * time.Millisecond)
	time.Sleep(800 * time.Millisecond)
	for _, arm := range s.arms {
		v, err := arm.Props(scouterkit.PropPanel)
		if err != nil || v[0] != 0 {
			t.Fatalf("%s arm: panel %v after the last cycle (%v)", arm, v, err)
		}
	}
	if err := demo.Halted(s.arms); err != nil {
		t.Fatal(err)
	}
}

// An anchor takes effect at the end of a cycle: here 30 ms after the cycle
// would have started, so the next frame 0 comes 30 ms late, and the frames
// after it one period apart again.
func TestAnchor(t *testing.T) {
	c := defaults()
	c.cycle = 400 * time.Millisecond
	s := setup(t, c)
	s.animate(100 * time.Millisecond)
	for _, arm := range s.arms {
		if _, err := demo.WaitNotify(arm, 3*time.Second); err != nil {
			t.Fatal(err)
		}
	}
	cycle := int64(len(s.ds)) * int64(s.period)
	now := scouterkit.NowUS()
	k := (now + 200000 - s.t0 + cycle - 1) / cycle
	want := s.t0 + k*cycle + 30000
	for _, arm := range s.arms {
		arm.WriteArena(s.l.state+4*wAnchor, demo.Words(arm.DeviceTime(time.Duration(want-now)*time.Microsecond)))
	}
	for _, arm := range s.arms {
		var zero scouterkit.Event
		for seen := 0; seen < 3; {
			e, err := demo.WaitNotify(arm, 3*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if at := demo.HostUS(arm, e.DeviceUS); e.B == 0 && at > want-20000 {
				zero = e
				if d := at - want; d < -1000 || d > 5000 {
					t.Fatalf("%s arm: frame 0 %d µs after the anchor", arm, d)
				}
			}
			if zero.DeviceUS != 0 && e.B > 0 {
				if d := int32(e.DeviceUS - zero.DeviceUS); d < int32(e.B)*int32(s.period)-3000 || d > int32(e.B)*int32(s.period)+3000 {
					t.Fatalf("%s arm: frame %d %d µs after frame 0", arm, e.B, d)
				}
				seen++
			}
		}
	}
}
