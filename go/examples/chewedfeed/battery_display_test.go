//go:build emulator

package main

import (
	"bytes"
	"context"
	"testing"
	"time"

	"scouterkit"
)

func TestBatteryReadDoesNotWakePanelsAndWorkerStops(t *testing.T) {
	if err := scouterkit.Init(scouterkit.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	defer scouterkit.Deinit()
	arms := []scouterkit.Arm{scouterkit.Left, scouterkit.Right}
	for _, arm := range arms {
		arm.SetPanel(false)
	}
	status := readBatteries(t.TempDir(), arms)
	if got := status.glassesText(); got != "L 72%  R 72%" {
		t.Fatalf("per-arm battery readings: %s", got)
	}
	for _, arm := range arms {
		p, err := arm.Props(scouterkit.PropPanel)
		if err != nil || p[0] != 0 {
			t.Fatal("reading battery woke the display")
		}
	}
	updates, stop := monitorBatteries(context.Background(), arms)
	select {
	case <-updates:
	case <-time.After(3 * time.Second):
		t.Fatal("battery worker did not produce its initial reading")
	}
	stop()
	if _, ok := <-updates; ok {
		t.Fatal("battery worker did not close")
	}
}

func TestBatteryRowUsesOldFooterPositionAndSurvivesNotifications(t *testing.T) {
	for _, scale := range []float64{1.5, 2, 3} {
		cfg := scouterkit.DefaultConfig()
		cfg.DX, cfg.DY = -10, -8
		if err := scouterkit.Init(cfg); err != nil {
			t.Fatal(err)
		}
		func() {
			defer scouterkit.Deinit()
			s, err := newScreenScale(cfg, scale)
			if err != nil {
				t.Fatal(err)
			}
			oldBaseline := s.font.Ascent + ((s.h/s.font.LineSpacing)-1-footerRaiseRows)*s.font.LineSpacing - footerLift
			if s.footerBaseline != oldBaseline {
				t.Fatal("footer did not move into the old battery row")
			}
			host, left, right := 82, 71, 68
			s.batteries = batteryStatus{host: &host, arms: []armBattery{{arm: scouterkit.Left, percent: &left}, {arm: scouterkit.Right, percent: &right}}}
			p := page{title: "Site", lines: []string{"PRs: 3"}, status: "Fetched 12:00"}
			s.draw(p, 0, 1, false)
			before := s.bitmap.Image()
			start := (s.footerBaseline - s.font.Ascent) * before.Stride
			s.render(p, 0, 1, false, &notification{App: "Test", Body: "Notification text"})
			after := s.bitmap.Image()
			if !bytes.Equal(before.Pix[start:], after.Pix[start:]) {
				t.Fatal("notification changed either status row")
			}
			panelHasPixels := false
			footerTop := s.footerBaseline - s.footerFont.Ascent - 5
			for y := 0; y < footerTop && !panelHasPixels; y++ {
				for x := s.statusX; x < s.w; x++ {
					if before.GrayAt(x, y).Y != 0 {
						panelHasPixels = true
						break
					}
				}
			}
			if !panelHasPixels {
				t.Fatal("right battery panel is blank")
			}
		}()
	}
}
