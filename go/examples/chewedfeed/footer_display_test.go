//go:build emulator

package main

import (
	"bytes"
	"testing"
	"time"

	"scouterkit"
)

func TestClockOnlyChangesBatteryRowAndModeOnlyChangesNavigationRow(t *testing.T) {
	cfg := scouterkit.DefaultConfig()
	cfg.DX, cfg.DY = -10, -8
	if err := scouterkit.Init(cfg); err != nil {
		t.Fatal(err)
	}
	defer scouterkit.Deinit()
	s, err := newScreenScale(cfg, 1.5)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 6, 12, 30, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	host, left, right := 82, 71, 68
	s.batteries = batteryStatus{host: &host, arms: []armBattery{{arm: scouterkit.Left, percent: &left}, {arm: scouterkit.Right, percent: &right}}}
	p := page{title: "Project", lines: []string{"PRs: 3"}, status: "Fetched 12:00"}
	s.draw(p, 0, 13, false)
	before := s.bitmap.Image()
	now = now.Add(time.Minute)
	s.draw(p, 0, 13, false)
	after := s.bitmap.Image()
	start := (s.batteryBaseline - s.font.Ascent) * before.Stride
	if !bytes.Equal(before.Pix[:start], after.Pix[:start]) {
		t.Fatal("clock change affected content or the navigation status line")
	}
	if bytes.Equal(before.Pix[start:], after.Pix[start:]) {
		t.Fatal("battery row clock did not change")
	}
	s.draw(p, 0, 13, true)
	manual := s.bitmap.Image()
	if !bytes.Equal(after.Pix[start:], manual.Pix[start:]) {
		t.Fatal("manual-mode indicator changed the battery/clock row")
	}
	if bytes.Equal(after.Pix[:start], manual.Pix[:start]) {
		t.Fatal("auto and manual have identical indicators")
	}
	// The clock follows both battery groups, moving only as needed for wider values.
	clockX := func() int {
		space := s.font.Measure(" ").Width
		computer, _ := s.iconSize("computer")
		glasses, _ := s.iconSize("glasses")
		clock, _ := s.iconSize("clock")
		width := clock + space + s.font.Measure("12:31").Width
		preferred := s.w - textInset - width - 30*space
		endOfBatteries := textInset + computer + space + s.font.Measure(batteryPercent(s.batteries.host)).Width + space +
			glasses + space + s.font.Measure(s.batteries.glassesText()).Width + space
		return max(preferred, endOfBatteries)
	}
	initialClockX := clockX()
	host, left, right = 100, 100, 100
	s.draw(p, 0, 13, true)
	full := s.bitmap.Image()
	w, _ := s.iconSize("clock")
	clockWidth := w + s.font.Measure(" ").Width + s.font.Measure("12:31").Width
	fullClockX := clockX()
	for y := s.batteryBaseline - s.font.Ascent; y < s.batteryBaseline+s.font.Descent; y++ {
		from := y*full.Stride + initialClockX
		fullFrom := y*full.Stride + fullClockX
		if !bytes.Equal(manual.Pix[from:from+clockWidth], full.Pix[fullFrom:fullFrom+clockWidth]) {
			t.Fatal("clock was misplaced or overwritten by battery text")
		}
	}
}
