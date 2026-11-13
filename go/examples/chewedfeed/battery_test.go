package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"scouterkit"
)

func TestHostBatteryFilteringAndMultipleBatteries(t *testing.T) {
	root := t.TempDir()
	write := func(name string, fields map[string]string) {
		t.Helper()
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		for field, text := range fields {
			if err := os.WriteFile(filepath.Join(dir, field), []byte(text+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if hostBattery(root) != nil {
		t.Fatal("missing battery displayed as zero")
	}
	write("peripheral", map[string]string{"type": "Battery", "scope": "Device", "capacity": "99"})
	write("AC", map[string]string{"type": "Mains", "capacity": "100"})
	if hostBattery(root) != nil {
		t.Fatal("peripheral or mains counted as host battery")
	}
	write("BAT1", map[string]string{"type": "Battery", "capacity": "80", "energy_now": "80", "energy_full": "100"})
	write("BAT2", map[string]string{"type": "Battery", "capacity": "40", "energy_now": "80", "energy_full": "200"})
	if p := hostBattery(root); p == nil || *p != 53 {
		t.Fatalf("energy-weighted battery: %v", p)
	}
	write("BAT2", map[string]string{"energy_now": "unavailable"})
	if p := hostBattery(root); p == nil || *p != 60 {
		t.Fatalf("capacity fallback: %v", p)
	}
	write("BAT1", map[string]string{"capacity": "0", "energy_now": "0"})
	write("BAT2", map[string]string{"capacity": "invalid"})
	if p := hostBattery(root); p == nil || *p != 0 {
		t.Fatalf("zero battery treated as unknown: %v", p)
	}
	if batteryPercent(nil) != "--%" {
		t.Fatal("unknown battery should be explicit")
	}
}

func TestWriteGlassesStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "glasses.json")
	n := 57
	s := batteryStatus{arms: []armBattery{{arm: scouterkit.Left, percent: &n}, {arm: scouterkit.Right}}}
	if err := writeGlassesStatus(path, s, time.Unix(1700000000, 0)); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), `{"case":null,"left":57,"right":null,"updated":1700000000}`+"\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	c := 80
	s.casePct, s.caseAt = &c, time.Unix(1699999000, 0)
	if err := writeGlassesStatus(path, s, time.Unix(1700000000, 0)); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	if got, want := string(b), `{"case":80,"case_updated":1699999000,"left":57,"right":null,"updated":1700000000}`+"\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
