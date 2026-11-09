package main

import (
	"context"
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
	host *int
	arms []armBattery
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
	s := batteryStatus{host: hostBattery(root)}
	for _, arm := range arms {
		reading := armBattery{arm: arm}
		if arm.State().Connected {
			if b, err := arm.Battery(); err == nil && b.Percent >= 0 && b.Percent <= 100 {
				n := b.Percent
				reading.percent = &n
			}
		}
		s.arms = append(s.arms, reading)
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
		for {
			s := readBatteries("/sys/class/power_supply", arms)
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
