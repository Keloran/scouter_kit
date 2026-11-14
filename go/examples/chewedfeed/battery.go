package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"scouterkit"
)

type armBattery struct {
	arm     scouterkit.Arm
	percent *int
}

type batteryStatus struct {
	host         *int
	hostCharging bool // on mains power, or a system battery charging
	arms         []armBattery
	// The case reports its battery only while an arm is in it (its CASE events and props): the last value seen.
	casePct      *int
	caseCharging bool
	caseAt       time.Time
}

// hostCharging: the laptop is on mains power, or one of its system batteries is charging.
func hostCharging(root string) bool {
	entries, err := os.ReadDir(root)
	if err != nil {
		return false
	}
	read := func(dir, field string) string {
		b, _ := os.ReadFile(filepath.Join(root, dir, field))
		return strings.TrimSpace(string(b))
	}
	for _, entry := range entries {
		dir := entry.Name()
		switch read(dir, "type") {
		case "Mains":
			if read(dir, "online") == "1" {
				return true
			}
		case "Battery":
			if read(dir, "scope") != "Device" && read(dir, "status") == "Charging" {
				return true
			}
		}
	}
	return false
}

// newerCase keeps whichever case reading is more recent (a CASE event can be newer than the last poll).
func (s batteryStatus) newerCase(other batteryStatus) batteryStatus {
	if other.casePct != nil && (s.casePct == nil || other.caseAt.After(s.caseAt)) {
		s.casePct, s.caseCharging, s.caseAt = other.casePct, other.caseCharging, other.caseAt
	}
	return s
}

// Only system batteries count: peripherals can also appear in power_supply.
func hostBattery(root string) *int {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	read := func(dir, field string) string {
		b, _ := os.ReadFile(filepath.Join(root, dir, field))
		return strings.TrimSpace(string(b))
	}
	var sum, count int
	var energyNow, energyFull float64
	weighted := true
	for _, entry := range entries {
		dir := entry.Name()
		if read(dir, "type") != "Battery" || read(dir, "scope") == "Device" {
			continue
		}
		n, err := strconv.Atoi(read(dir, "capacity"))
		if err != nil || n < 0 || n > 100 {
			continue
		}
		sum, count = sum+n, count+1
		now, errNow := strconv.ParseFloat(read(dir, "energy_now"), 64)
		full, errFull := strconv.ParseFloat(read(dir, "energy_full"), 64)
		if errNow != nil || errFull != nil || full <= 0 || now < 0 || math.IsNaN(full) || math.IsNaN(now) || math.IsInf(full, 0) || math.IsInf(now, 0) {
			weighted = false
		} else {
			energyNow, energyFull = energyNow+now, energyFull+full
		}
	}
	if count == 0 {
		return nil
	}
	percent := int(math.Round(float64(sum) / float64(count)))
	if weighted && energyFull > 0 {
		percent = int(math.Round(100 * energyNow / energyFull))
	}
	percent = max(0, min(100, percent))
	return &percent
}

func readBatteries(root string, arms []scouterkit.Arm) batteryStatus {
	s := batteryStatus{host: hostBattery(root), hostCharging: hostCharging(root)}
	for _, arm := range arms {
		reading := armBattery{arm: arm}
		if arm.State().Connected {
			if b, err := arm.Battery(); err == nil && b.Percent >= 0 && b.Percent <= 100 {
				n := b.Percent
				reading.percent = &n
			}
		}
		s.arms = append(s.arms, reading)
		if s.casePct == nil && arm.State().Connected {
			if v, err := arm.Props(scouterkit.PropCaseBattPct, scouterkit.PropCaseCharging); err == nil && v[0] <= 100 {
				n := int(v[0])
				s.casePct, s.caseCharging, s.caseAt = &n, v[1] == 1, time.Now()
			}
		}
	}
	return s
}

// Join the worker before deinitializing the glasses: battery reads use the library.
func monitorBatteries(parent context.Context, arms []scouterkit.Arm) (<-chan batteryStatus, func()) {
	ctx, cancel := context.WithCancel(parent)
	updates := make(chan batteryStatus, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(updates)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		var lastCase *int
		var lastCaseCharging bool
		var lastCaseAt time.Time
		for {
			s := readBatteries("/sys/class/power_supply", arms)
			if s.casePct == nil {
				s.casePct, s.caseCharging, s.caseAt = lastCase, lastCaseCharging, lastCaseAt
			}
			lastCase, lastCaseCharging, lastCaseAt = s.casePct, s.caseCharging, s.caseAt
			select {
			case updates <- s:
			case <-ctx.Done():
				return
			}
			select {
			case <-ticker.C:
			case <-ctx.Done():
				return
			}
		}
	}()
	return updates, func() { cancel(); <-done }
}

func batteryPercent(n *int) string {
	if n == nil {
		return "--%"
	}
	return fmt.Sprintf("%d%%", *n)
}

func (s batteryStatus) glassesText() string {
	if len(s.arms) == 0 {
		return "--%"
	}
	var parts []string
	for _, b := range s.arms {
		name := "L"
		if b.arm == scouterkit.Right {
			name = "R"
		}
		parts = append(parts, name+" "+batteryPercent(b.percent))
	}
	return strings.Join(parts, "  ")
}

// Published for the Omarchy bar plugin, which cannot reach the glasses itself.
// Arms without a reading are null; the plugin treats an old "updated" as stale.
func writeGlassesStatus(path string, s batteryStatus, now time.Time) error {
	out := map[string]any{"left": nil, "right": nil, "case": nil, "updated": now.Unix()}
	if s.casePct != nil {
		out["case"], out["case_updated"] = *s.casePct, s.caseAt.Unix()
	}
	for _, b := range s.arms {
		name := "left"
		if b.arm == scouterkit.Right {
			name = "right"
		}
		if b.percent != nil {
			out[name] = *b.percent
		}
	}
	data, err := json.Marshal(out)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".glasses-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func glassesStatusPath() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "chewedfeed", "glasses.json")
}
