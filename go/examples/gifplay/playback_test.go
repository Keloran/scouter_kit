//go:build emulator

package main

import (
	"testing"
	"time"

	"scouterkit"
	"scouterkit/examples/internal/demo"
)

func initEmulator(t *testing.T) (scouterkit.Config, []scouterkit.Arm) {
	t.Helper()
	cfg := scouterkit.DefaultConfig()
	if err := scouterkit.Init(cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(scouterkit.Deinit)
	return cfg, demo.Present()
}

// Step mode: every frame and the wrap drawn exactly as the host composed
// them, on both emulated panels; then two timed loops, the second one moved
// by an anchor, and the panels off after the last loop.
func TestPlayback(t *testing.T) {
	cfg, arms := initEmulator(t)
	src := compose(testGIF(400, 100, 7))
	pl, err := load(cfg, arms, src, config{check: true, loops: 2, opts: options{gamma: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if err := pl.verify(); err != nil {
		t.Fatal(err)
	}
	if err := pl.start(); err != nil {
		t.Fatal(err)
	}
	n := len(pl.a.frames)
	for k := 0; k <= n; k++ {
		if err := pl.step(k, n); err != nil {
			t.Fatal(err)
		}
		for _, arm := range arms {
			got, want := demo.FromPanel(arm.EmulatedPanel()), pl.expected(arm, k%n)
			if d, x, y := demo.Diff(got, want); d != 0 {
				t.Fatalf("%s arm, frame %d: %d pixels differ, first at (%d, %d)", arm, k%n, d, x, y)
			}
		}
	}
	h0 := scouterkit.NowUS() + 300000
	pl.play(h0)
	rest := int64(0)
	for i := 1; i < n; i++ {
		rest += int64(pl.a.delays[i])
	}
	tl := timeline{first: h0 + rest, period: pl.a.duration().Microseconds()}
	moved := timeline{first: tl.first + 40000, period: tl.period}
	pl.anchor(moved, 2)
	for loop := uint32(1); loop <= 2; loop++ {
		var at [2]int64
		for i, arm := range arms {
			e, err := demo.WaitNotify(arm, 3*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			want := tl.loopStart(loop)
			if loop == 2 {
				want = moved.loopStart(loop)
			}
			if e.A != loop || abs(demo.HostUS(arm, e.B)-want) > 2000 {
				t.Fatalf("%s arm: loop %d due %d µs from the host's time", arm, e.A, demo.HostUS(arm, e.B)-want)
			}
			at[i] = demo.HostUS(arm, e.DeviceUS)
		}
		if d := abs(at[0] - at[1]); d > 5000 {
			t.Errorf("loop %d: eyes %d µs apart", loop, d)
		}
	}
	time.Sleep(time.Duration(pl.a.delays[0])*time.Microsecond + 100*time.Millisecond)
	for _, arm := range arms {
		v, err := arm.Props(scouterkit.PropPanel)
		if err != nil || v[0] != 0 {
			t.Errorf("%s arm: panel %v after the last loop, %v", arm, v, err)
		}
	}
	if err := demo.Halted(arms); err != nil {
		t.Error(err)
	}
}

// Timed from the start (the whole canvas first), stopped mid-way: the panel
// shows the frame the program's state says.
func TestPlayStop(t *testing.T) {
	cfg, arms := initEmulator(t)
	pl, err := load(cfg, arms, compose(testGIF(200, 60, 5)), config{opts: options{gamma: 1}})
	if err != nil {
		t.Fatal(err)
	}
	for _, arm := range arms {
		arm.WriteArena(pl.l.state+4*wDue, demo.Words(arm.DeviceTime(100*time.Millisecond)))
	}
	if err := pl.start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(700 * time.Millisecond)
	for _, arm := range arms {
		if err := arm.Stop(); err != nil {
			t.Fatal(err)
		}
		if err := pl.checkStopped(arm); err != nil {
			t.Fatal(err)
		}
	}
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
