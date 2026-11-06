//go:build emulator

package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"scouterkit"
)

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
		footerEnd := s.font.Ascent + (s.rows+3)*s.font.LineSpacing + s.font.Descent
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
	s.now = func() time.Time { return time.Unix(100, 0) }
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
