//go:build emulator

package main

import (
	"bytes"
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

func TestSitesFooterShowsFetchTimeBesidePage(t *testing.T) {
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
	need := textInset + s.footerFont.Measure("Page 10/13").Width + 2*s.footerFont.Measure(" ").Width + iconW +
		s.footerFont.Measure(" ").Width + s.footerFont.Measure("13:55").Width
	if need > s.footerSplit() {
		t.Fatalf("page and fetch time need %d px, footer cell is %d", need, s.footerSplit())
	}
	p := page{title: "Site", status: "Fetched 13:55"}
	s.draw(p, 9, 13, false)
	with := append([]uint8(nil), s.bitmap.Image().Pix...)
	p.status = "Sampled 13:55"
	s.draw(p, 9, 13, false)
	if bytes.Equal(with, s.bitmap.Image().Pix) {
		t.Fatal("fetch time missing from the Sites footer, or shown for other sources")
	}
}
