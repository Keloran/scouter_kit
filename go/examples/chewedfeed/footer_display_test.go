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
