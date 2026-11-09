//go:build emulator

package main

import (
	"bytes"
	"strings"
	"testing"

	"scouterkit"
)

func TestLargeTextPreservesFooterAndNotificationArea(t *testing.T) {
	cfg := scouterkit.DefaultConfig()
	cfg.DX, cfg.DY = -8, -6
	if err := scouterkit.Init(cfg); err != nil {
		t.Fatal(err)
	}
	defer scouterkit.Deinit()
	normal, err := newScreen(cfg)
	if err != nil {
		t.Fatal(err)
	}
	p := page{title: "Large numbers 0123456789", status: "Fetched 12:00"}
	for i := 0; i < 30; i++ {
		p.lines = append(p.lines, "Content 0123456789 gjpqy")
	}
	normal.draw(p, 0, 1, false)
	width, rows := normal.measure("0123456789"), normal.rows
	scouterkit.UnloadAll()
	large, err := newScreenScale(cfg, 3)
	if err != nil {
		t.Fatal(err)
	}
	if large.measure("0123456789") != width*3/2 {
		t.Fatal("large text is not 50% bigger")
	}
	if large.rows < 1 || large.rows >= rows {
		t.Fatalf("invalid large text capacity: %d (normal %d)", large.rows, rows)
	}
	large.draw(p, 0, 1, false)
	start := large.footerBaseline - large.footerFont.Ascent
	before := large.bitmap.Image()
	if large.footerFont.Measure("0123456789").Width != width*3/2 {
		t.Fatal("status line did not scale with the content")
	}
	footerEnd := large.footerBaseline + large.footerFont.Descent
	if footerEnd > large.h-2*large.font.LineSpacing {
		t.Fatal("scaled footer does not leave two clear rows below it")
	}
	for _, pixel := range before.Pix[footerEnd*before.Stride:] {
		if pixel != 0 {
			t.Fatal("pixels below the scaled footer")
		}
	}
	large.render(p, 0, 1, false, &notification{App: "Test", Body: strings.Repeat("Long notification ", 100)})
	after := large.bitmap.Image()
	if !bytes.Equal(before.Pix[start*before.Stride:], after.Pix[start*after.Stride:]) {
		t.Fatal("large notification overwrote the footer")
	}
}

func TestFractionalStatusLineScalesAndStaysClearOfNotifications(t *testing.T) {
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
	if s.footerFont.LineSpacing != s.font.LineSpacing || s.footerFont.Measure("12:34").Width != s.measure("12:34") {
		t.Fatal("fractional status text does not match the main text scale")
	}
	p := page{title: "Site", status: "Fetched 12:00"}
	for i := 0; i < 100; i++ {
		p.lines = append(p.lines, "Content gjpqy")
	}
	s.draw(p, 1, 13, false)
	before := s.bitmap.Image()
	start := (s.footerBaseline - s.footerFont.Ascent) * before.Stride
	s.render(p, 1, 13, false, &notification{App: "Test", Body: strings.Repeat("Long notification ", 100)})
	after := s.bitmap.Image()
	if !bytes.Equal(before.Pix[start:], after.Pix[start:]) {
		t.Fatal("notification overwrote the scaled status line or bottom margin")
	}
}

func TestDisplayReadbackAndWake(t *testing.T) {
	cfg := scouterkit.DefaultConfig()
	if err := scouterkit.Init(cfg); err != nil {
		t.Fatal(err)
	}
	defer scouterkit.Deinit()
	s, err := newScreen(cfg)
	if err != nil {
		t.Fatal(err)
	}
	p := page{title: "Chewedfeed", lines: []string{"Projects: 12", "Health: 70"}, status: "Updated 12:00"}
	for pass := 0; pass < 2; pass++ {
		s.draw(p, 0, 1, false)
		img := s.bitmap.Image()
		for y := 0; y < s.h; y++ {
			for x := 0; x < 4; x++ {
				if img.GrayAt(x, y).Y != 0 {
					t.Fatalf("left padding contains pixels at (%d, %d)", x, y)
				}
			}
		}
		footerEnd := s.footerBaseline + s.font.Descent
		if footerEnd > s.h-2*s.font.LineSpacing {
			t.Fatal("footer does not leave two text rows clear below it")
		}
		for y := footerEnd; y < s.h; y++ {
			for x := 0; x < s.w; x++ {
				if img.GrayAt(x, y).Y != 0 {
					t.Fatalf("pixel below raised footer at (%d, %d)", x, y)
				}
			}
		}
		for _, arm := range []scouterkit.Arm{scouterkit.Left, scouterkit.Right} {
			got, want, err := arm.PanelCRC(0, scouterkit.PanelHeight)
			if err != nil || got != want {
				t.Fatalf("pass %d, %s panel mismatch: %08x != %08x (%v)", pass, arm, got, want, err)
			}
			if !arm.State().PanelOn {
				t.Fatal("draw did not wake panel")
			}
		}
		s.hide([]scouterkit.Arm{scouterkit.Left, scouterkit.Right})
	}
}

func TestNotificationOverlayPreservesFooterAndRestoresDashboard(t *testing.T) {
	if err := scouterkit.Init(scouterkit.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	defer scouterkit.Deinit()
	s, err := newScreen(scouterkit.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	p := page{title: "Site", lines: []string{"Progress: 75%", "Stage: launched"}, status: "Updated 12:00"}
	s.draw(p, 2, 13, false)
	before := s.bitmap.Image()
	n := notification{App: "Demo", Summary: "Build finished", Body: strings.Repeat("Detailed message ", 100)}
	s.render(p, 2, 13, false, &n)
	after := s.bitmap.Image()
	footer := (s.rows + 2) * s.font.LineSpacing * before.Stride
	if !bytes.Equal(before.Pix[footer:], after.Pix[footer:]) {
		t.Fatal("notification overwrote the dashboard status/time footer or bottom buffer")
	}
	if bytes.Equal(before.Pix[:footer], after.Pix[:footer]) {
		t.Fatal("notification did not replace the main content")
	}
	for _, arm := range []scouterkit.Arm{scouterkit.Left, scouterkit.Right} {
		got, want, err := arm.PanelCRC(0, scouterkit.PanelHeight)
		if err != nil || got != want {
			t.Fatalf("notification panel mismatch on %s: %08x != %08x (%v)", arm, got, want, err)
		}
	}
	s.draw(p, 2, 13, false)
	if !bytes.Equal(before.Pix, s.bitmap.Image().Pix) {
		t.Fatal("dashboard was not restored after the popup")
	}
}
