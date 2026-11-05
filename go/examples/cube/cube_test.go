//go:build emulator

package main

import (
	"encoding/binary"
	"testing"
	"time"

	"scouterkit"
	"scouterkit/examples/internal/demo"
)

func start(t *testing.T, c config, angles map[scouterkit.Arm][2]int32) *cube {
	t.Helper()
	cfg := scouterkit.DefaultConfig()
	if err := scouterkit.Init(cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(scouterkit.Deinit)
	arms := demo.Present()
	for arm, a := range angles {
		arm.SetEmulatedProperty(scouterkit.PropPitch, uint32(a[0]))
		arm.SetEmulatedProperty(scouterkit.PropRoll, uint32(a[1]))
	}
	cb, err := load(cfg, arms, c)
	if err != nil {
		t.Fatal(err)
	}
	return cb
}

// Step mode: every frame on both emulated panels equals the cube computed
// here from the angles each arm read, through head angles from -90 to 90
// and a full turn of the spin; with -master both eyes use the left's.
func TestFrames(t *testing.T) {
	cb := start(t, config{size: 200, fps: 20, spin: 7, rate: 52, check: 1},
		map[scouterkit.Arm][2]int32{scouterkit.Left: {10, -20}, scouterkit.Right: {-30, 45}})
	if err := cb.steps(3); err != nil {
		t.Fatal(err)
	}
	for k, a := range [][2]int32{{90, 0}, {-90, 90}, {0, -90}, {45, 45}, {-1, 1}, {60, -75}} {
		for _, arm := range cb.arms {
			arm.SetEmulatedProperty(scouterkit.PropPitch, uint32(a[0]))
			arm.SetEmulatedProperty(scouterkit.PropRoll, uint32(a[1]+int32(arm)))
		}
		for _, arm := range cb.arms {
			arm.Raise(scouterkit.TriggerUser3)
			if _, err := demo.WaitNotify(arm, 2*time.Second); err != nil {
				t.Fatal(err)
			}
			pitch, roll, yaw, _, err := cb.last(arm)
			if err != nil || pitch != a[0] || roll != a[1]+int32(arm) {
				t.Fatalf("%s arm: angles %d, %d, %v", arm, pitch, roll, err)
			}
			got, want := demo.FromPanel(arm.EmulatedPanel()), cb.expected(arm, pitch, roll, yaw)
			if d, x, y := demo.Diff(got, want); d != 0 {
				t.Fatalf("case %d, %s arm: %d pixels differ, first at (%d, %d)", k, arm, d, x, y)
			}
		}
	}
	for i := 0; i < 52; i++ {
		for _, arm := range cb.arms {
			arm.Raise(scouterkit.TriggerUser3)
		}
		for _, arm := range cb.arms {
			if _, err := demo.WaitNotify(arm, 2*time.Second); err != nil {
				t.Fatal(err)
			}
			pitch, roll, yaw, _, _ := cb.last(arm)
			if err := cb.check(arm, pitch, roll, yaw); err != nil {
				t.Fatalf("spin %d: %v", yaw, err)
			}
		}
	}
}

func TestMaster(t *testing.T) {
	cb := start(t, config{size: 160, fps: 20, spin: 0, rate: 52, check: 1, master: "left"},
		map[scouterkit.Arm][2]int32{scouterkit.Left: {25, -15}, scouterkit.Right: {-60, 70}})
	if err := cb.steps(2); err != nil {
		t.Fatal(err)
	}
	p, r, _, _, err := cb.last(scouterkit.Right)
	if err != nil || p != 25 || r != -15 {
		t.Fatalf("the right arm drew with %d, %d (%v)", p, r, err)
	}
}

// Timed at 15 fps for 30 frames: on time, then the panels off by themselves.
func TestTimed(t *testing.T) {
	cb := start(t, config{size: 150, fps: 15, spin: 3, rate: 52, frames: 30}, nil)
	t0 := time.Now()
	if err := cb.play(scouterkit.NowUS()+100000, false); err != nil {
		t.Fatal(err)
	}
	for _, arm := range cb.arms {
		for {
			b, err := arm.ReadArena(cb.l.state+4*wFrames, 4)
			if err != nil {
				t.Fatal(err)
			}
			if binary.LittleEndian.Uint32(b) == 30 {
				break
			}
			if time.Since(t0) > 5*time.Second {
				t.Fatalf("%s arm: %d frames after 5 s", arm, binary.LittleEndian.Uint32(b))
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	if d := time.Since(t0); d < 2000*time.Millisecond || d > 2600*time.Millisecond {
		t.Errorf("30 frames in %v", d)
	}
	time.Sleep(150 * time.Millisecond)
	for _, arm := range cb.arms {
		v, err := arm.Props(scouterkit.PropPanel, scouterkit.PropIMURate)
		if err != nil || v[0] != 0 || v[1] != 13 {
			t.Errorf("%s arm: panel, IMU rate %v after the last frame (%v)", arm, v, err)
		}
	}
	if err := demo.Halted(cb.arms); err != nil {
		t.Fatal(err)
	}
}
