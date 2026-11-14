package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"scouterkit"
)

func TestCaseReadingFromCaseEvents(t *testing.T) {
	ev := func(a uint32) scouterkit.Event {
		return scouterkit.Event{Type: scouterkit.EventInput, Trigger: int(scouterkit.TriggerCase), A: a}
	}
	p, charging, ok := caseReading(ev(1 | 4 | 73<<8)) // in case, charging, 73 %
	if !ok || *p != 73 || !charging {
		t.Fatalf("got %v %t %t", p, charging, ok)
	}
	if _, _, ok := caseReading(ev(1 | 0xFF<<8)); ok { // battery unknown
		t.Fatal("unknown case battery taken as a reading")
	}
	if _, _, ok := caseReading(scouterkit.Event{Type: scouterkit.EventInput, Trigger: int(scouterkit.TriggerTouch), A: 73 << 8}); ok {
		t.Fatal("a touch read as the case")
	}
	older := batteryStatus{casePct: p, caseAt: time.Unix(100, 0)}
	n := 40
	newer := batteryStatus{casePct: &n, caseCharging: true, caseAt: time.Unix(200, 0)}
	if got := older.newerCase(newer); *got.casePct != 40 || !got.caseCharging {
		t.Fatalf("newer reading lost: %+v", got)
	}
	if got := newer.newerCase(older); *got.casePct != 40 {
		t.Fatalf("older reading won: %+v", got)
	}
}

func TestHostCharging(t *testing.T) {
	root := t.TempDir()
	supply := func(name string, fields map[string]string) {
		dir := filepath.Join(root, name)
		os.MkdirAll(dir, 0o755)
		for k, v := range fields {
			os.WriteFile(filepath.Join(dir, k), []byte(v+"\n"), 0o644)
		}
	}
	supply("BAT0", map[string]string{"type": "Battery", "status": "Discharging", "capacity": "50"})
	supply("hidpp_battery_0", map[string]string{"type": "Battery", "scope": "Device", "status": "Charging"}) // a mouse
	supply("AC", map[string]string{"type": "Mains", "online": "0"})
	if hostCharging(root) {
		t.Fatal("a peripheral charging counted as the laptop")
	}
	supply("AC", map[string]string{"type": "Mains", "online": "1"})
	if !hostCharging(root) {
		t.Fatal("mains power not seen")
	}
	if hostCharging(filepath.Join(root, "missing")) {
		t.Fatal("no power_supply: charging")
	}
}

func TestChargingIconFitsThePanel(t *testing.T) {
	icon, warning := statusIcons["charging"], statusIcons["warning"]
	if len(icon[0]) > len(warning[0]) || len(icon) > 11 {
		t.Fatal("the bolt must fit where the warning goes")
	}
}
