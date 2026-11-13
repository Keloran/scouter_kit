//go:build emulator

package main

import (
	"bytes"
	"strings"
	"testing"

	"scouterkit"
)

func TestStatusPanelAndFooterUseSeparateRegions(t *testing.T) {
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
	host, left, right := 82, 71, 68
	s.batteries = batteryStatus{host: &host, arms: []armBattery{{arm: scouterkit.Left, percent: &left}, {arm: scouterkit.Right, percent: &right}}}
	p := page{title: "Project", lines: []string{"PRs: 3"}, status: "Fetched 12:00"}
	s.draw(p, 0, 13, false)
	auto := s.bitmap.Image()
	s.draw(p, 0, 13, true)
	manual := s.bitmap.Image()
	start := (s.footerBaseline - s.footerFont.Ascent) * auto.Stride
	for y := 0; y < s.h; y++ {
		for x := 0; x < s.contentW; x++ {
			if auto.GrayAt(x, y).Y != manual.GrayAt(x, y).Y {
				t.Fatal("auto/manual changed the page content")
			}
		}
	}
	if !bytes.Equal(auto.Pix[start:], manual.Pix[start:]) {
		t.Fatal("auto/manual changed the footer")
	}
	modeChanged := false
	footerTop := s.footerBaseline - s.footerFont.Ascent - 5
	for y := 0; y < footerTop && !modeChanged; y++ {
		for x := s.statusX; x < s.w; x++ {
			if auto.GrayAt(x, y).Y != manual.GrayAt(x, y).Y {
				modeChanged = true
				break
			}
		}
	}
	if !modeChanged {
		t.Fatal("auto/manual icon did not change in the status panel")
	}

	initialPanel := append([]uint8(nil), auto.Pix...)
	host, left, right = 100, 100, 100
	s.batteries = batteryStatus{host: &host, arms: []armBattery{{arm: scouterkit.Left, percent: &left}, {arm: scouterkit.Right, percent: &right}}}
	s.draw(p, 0, 13, false)
	full := s.bitmap.Image()
	changed := false
	for y := 0; y < footerTop && !changed; y++ {
		for x := s.statusX; x < s.w; x++ {
			if initialPanel[y*full.Stride+x] != full.Pix[y*full.Stride+x] {
				changed = true
				break
			}
		}
	}
	if !changed {
		t.Fatal("battery changes did not appear in the right status panel")
	}
}

func TestFetchTimeInModeBox(t *testing.T) {
	cfg := scouterkit.DefaultConfig()
	if err := scouterkit.Init(cfg); err != nil {
		t.Fatal(err)
	}
	defer scouterkit.Deinit()
	s, err := newScreenScale(cfg, 2)
	if err != nil {
		t.Fatal(err)
	}
	iconW, _ := s.iconSize("download")
	if need := iconW + s.footerFont.Measure(" 13:55").Width + 2*textInset; need > s.statusW {
		t.Fatalf("fetch time needs %d px, status panel is %d", need, s.statusW)
	}
	// The page cell only: the middle cell shows the clock, which may tick between draws.
	stride, split := s.bitmap.Image().Stride, s.footerSplit()
	pageCell := func(pix []uint8) []uint8 {
		var out []uint8
		for y := s.footerBaseline - s.footerFont.Ascent - 5; y < s.h; y++ {
			out = append(out, pix[y*stride:y*stride+split]...)
		}
		return out
	}
	draw := func(status string) []uint8 {
		s.draw(page{title: "Site", status: status}, 9, 13, false)
		return append([]uint8(nil), s.bitmap.Image().Pix...)
	}
	without := draw("Sampled 13:55")
	for _, status := range []string{"Fetched 13:55", "Cached 13:55"} {
		with := draw(status)
		if bytes.Equal(with, without) {
			t.Fatalf("%q: time missing from the status panel", status)
		}
		if !bytes.Equal(pageCell(with), pageCell(without)) {
			t.Fatalf("%q: time drawn in the footer", status)
		}
	}
}

func TestCaseBatteryRowFitsStatusPanel(t *testing.T) {
	cfg := scouterkit.DefaultConfig()
	if err := scouterkit.Init(cfg); err != nil {
		t.Fatal(err)
	}
	defer scouterkit.Deinit()
	for _, scale := range []float64{1.5, 2} {
		s, err := newScreenScale(cfg, scale)
		if err != nil {
			t.Fatal(err)
		}
		draw := func(c *int) []uint8 {
			s.batteries.casePct = c
			s.draw(page{title: "Site"}, 0, 1, false)
			return append([]uint8(nil), s.bitmap.Image().Pix...)
		}
		unknown := draw(nil)
		low := 5
		known := draw(&low)
		if bytes.Equal(unknown, known) {
			t.Fatalf("scale %g: case battery not drawn", scale)
		}
		// The warning icon after "5%" ends inside the panel: nothing new in the last two columns.
		stride := s.bitmap.Image().Stride
		for y := 0; y < s.h; y++ {
			for x := s.w - 2; x < s.w; x++ {
				if known[y*stride+x] != unknown[y*stride+x] {
					t.Fatalf("scale %g: case battery row reaches the right edge at y %d", scale, y)
				}
			}
		}
		scouterkit.UnloadAll()
	}
}

// Content text keeps textInset clear of the status divider, and a notification popup is the app and at most
// popupLines lines (CF-78).
func TestContentClearOfDividerAndShortPopups(t *testing.T) {
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
	long := strings.Repeat("Wide words ", 40)
	p := page{title: long, lines: []string{long, long, long}, status: "Fetched 12:00"}
	footerTop := s.footerBaseline - s.footerFont.Ascent - 5
	lit := func(x0, x1, y0, y1 int) bool {
		img := s.bitmap.Image()
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				if img.GrayAt(x, y).Y != 0 {
					return true
				}
			}
		}
		return false
	}
	s.render(p, 0, 1, false, nil)
	if !lit(0, s.contentW-textInset, 0, footerTop) {
		t.Fatal("the page drew nothing")
	}
	if lit(s.contentW-textInset, s.statusX-1, 0, footerTop) {
		t.Fatal("page text runs into the margin before the status divider")
	}
	// One unbroken word: wrapping cuts it without "...", so each line is filled with wide glyphs to the edge.
	s.render(p, 0, 1, false, &notification{App: "Mail", Summary: strings.Repeat("M", 500), Body: long})
	if lit(s.contentW-textInset, s.statusX-1, 0, footerTop) {
		t.Fatal("popup text runs into the margin before the status divider")
	}
	below := s.font.Ascent + popupLines*s.font.LineSpacing + s.font.Descent + 1
	if lit(0, s.contentW-textInset, below, footerTop) {
		t.Fatalf("the popup is longer than the app and %d lines", popupLines)
	}
}
