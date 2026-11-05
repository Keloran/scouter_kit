//go:build emulator

package main

import (
	"encoding/binary"
	"testing"
	"time"

	"scouterkit"
	"scouterkit/examples/internal/demo"
)

func start(t *testing.T, c config) *board {
	t.Helper()
	cfg := scouterkit.DefaultConfig()
	if err := scouterkit.Init(cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(scouterkit.Deinit)
	arms := demo.Present()
	for _, arm := range arms {
		arm.SetEmulatedProperty(scouterkit.PropPitch, 0)
	}
	b, err := load(cfg, arms, c)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.start(); err != nil {
		t.Fatal(err)
	}
	return b
}

func pitch(arm scouterkit.Arm, deg int32) { arm.SetEmulatedProperty(scouterkit.PropPitch, uint32(deg)) }

// The emulated arms' pitch goes up, down, up and down again within the
// hold: shown, hiding, hidden after the hold, shown, hiding cancelled; the
// panels are read back each time it shows.
func TestTilt(t *testing.T) {
	b := start(t, config{angle: 20, hysteresis: 5, hold: 400 * time.Millisecond})
	for _, arm := range b.arms {
		if err := panelOff(arm); err != nil {
			t.Fatal(err)
		}
		pitch(arm, 18)
		pitch(arm, 30)
		if p, err := b.wait(arm, 1); err != nil || p != 30 {
			t.Fatalf("%s arm: %v, pitch %d", arm, err, p)
		}
		if err := b.checkShown(arm); err != nil {
			t.Fatal(err)
		}
		pitch(arm, 17)
		time.Sleep(50 * time.Millisecond)
		pitch(arm, 12)
		if _, err := b.wait(arm, 2); err != nil {
			t.Fatal(err)
		}
		at := time.Now()
		if _, err := b.wait(arm, 0); err != nil {
			t.Fatal(err)
		}
		if d := time.Since(at); d < 300*time.Millisecond || d > 600*time.Millisecond {
			t.Errorf("%s arm: hidden %v after the head went down", arm, d)
		}
		if err := panelOff(arm); err != nil {
			t.Fatal(err)
		}
		pitch(arm, 40)
		if _, err := b.wait(arm, 1); err != nil {
			t.Fatal(err)
		}
		pitch(arm, 0)
		if _, err := b.wait(arm, 2); err != nil {
			t.Fatal(err)
		}
		pitch(arm, 25)
		if _, err := b.wait(arm, 1); err != nil {
			t.Fatal(err)
		}
		time.Sleep(600 * time.Millisecond)
		if err := b.checkShown(arm); err != nil {
			t.Fatalf("after the cancelled hide: %v", err)
		}
	}
}

// USER1/USER2 as in -check; the battery redrawn on a change while shown;
// the clock redrawn at the minute, and corrected by the host (USER0).
func TestClockAndBattery(t *testing.T) {
	b := start(t, config{angle: 20, hysteresis: 5})
	if err := b.check(); err != nil {
		t.Fatal(err)
	}
	for _, arm := range b.arms {
		arm.Raise(scouterkit.TriggerUser1)
		if _, err := b.wait(arm, 3); err != nil {
			t.Fatal(err)
		}
		pitch(arm, 40)
		pitch(arm, -10)
		arm.SetEmulatedProperty(scouterkit.PropBattPct, 100)
		time.Sleep(100 * time.Millisecond)
		if err := b.checkShown(arm); err != nil {
			t.Fatalf("battery 100: %v", err)
		}
		arm.SetEmulatedProperty(scouterkit.PropBattPct, scouterkit.Unknown)
		time.Sleep(100 * time.Millisecond)
		if err := b.checkShown(arm); err != nil {
			t.Fatalf("battery unknown: %v", err)
		}
		arm.WriteArena(b.l.state+4*wMailMin, demo.Words(23*60+59, arm.DeviceTime(300*time.Millisecond)))
		arm.Raise(scouterkit.TriggerUser0)
		time.Sleep(600 * time.Millisecond)
		st, err := arm.ReadArena(b.l.state+4*wMinute, 4)
		if err != nil || binary.LittleEndian.Uint32(st) != 0 {
			t.Fatalf("%s arm: minute %v after midnight, %v", arm, st, err)
		}
		if err := b.checkShown(arm); err != nil {
			t.Fatalf("after midnight: %v", err)
		}
	}
	if err := demo.Halted(b.arms); err != nil {
		t.Fatal(err)
	}
}
