//go:build emulator

package main

import (
	"bytes"
	"testing"

	"scouterkit"
	"scouterkit/examples/internal/demo"
)

func TestCalibrationEyePatternsAndGeometry(t *testing.T) {
	for _, sample := range []struct {
		dx, dy int
		scale  float64
	}{{100, 9, 3}, {-30, -20, 3}, {1, -6, 1.5}, {-15, -8, 0.1}} {
		cfg := scouterkit.DefaultConfig()
		cfg.DX, cfg.DY = sample.dx, sample.dy
		if err := scouterkit.Init(cfg); err != nil {
			t.Fatal(err)
		}
		func() {
			defer scouterkit.Deinit()
			arms := []scouterkit.Arm{scouterkit.Left, scouterkit.Right}
			view, err := showCalibration(cfg, arms, sample.scale)
			if err != nil {
				t.Fatal(err)
			}
			defer demo.Stop(arms)
			vw, vh := scouterkit.Size()
			for _, arm := range arms {
				panel := arm.EmulatedPanel()
				x, y := demo.PanelAt(cfg, arm, vw/2, vh/2)
				if panel.GrayAt(x, y).Y == 0 {
					t.Fatalf("%s origin missing at %d,%d", arm, x, y)
				}
				// Only the left eye has a ruler point 80px right and 40px down.
				got := panel.GrayAt(x+80, y+40).Y
				if (arm == scouterkit.Left) != (got > 0) {
					t.Fatalf("%s ruler point has level %d", arm, got)
				}
				// Only the left eye has the hollow square around the origin.
				got = panel.GrayAt(x+5, y+5).Y
				if (arm == scouterkit.Left) != (got > 0) {
					t.Fatalf("%s square corner has level %d", arm, got)
				}
				props, err := arm.Props(scouterkit.PropPanel)
				if err != nil || len(props) != 1 || props[0] != 1 {
					t.Fatalf("%s panel power: %v (%v)", arm, props, err)
				}
			}
			before := scouterkit.Left.EmulatedPanel()
			x, y := view.x+calibrationW/2, view.y+calibrationH/2
			for _, key := range []calibrationKey{calibrationRight, calibrationDown, calibrationLeft, calibrationUp} {
				o := nudgeCalibration(options{dx: view.dx, dy: view.dy}, key)
				if err := view.move(o.dx, o.dy); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(before.Pix, scouterkit.Left.EmulatedPanel().Pix) {
					t.Fatal("nudging the right crosshair changed the left ruler")
				}
				panel := scouterkit.Right.EmulatedPanel()
				if panel.GrayAt(view.x+calibrationW/2, view.y+calibrationH/2).Y == 0 {
					t.Fatal("crosshair missing after live adjustment")
				}
			}
			if view.x+calibrationW/2 != x || view.y+calibrationH/2 != y {
				t.Fatal("opposite arrow keys did not restore the crosshair position")
			}
		}()
	}
}

func TestCalibrationLiveFontResizePreservesAlignmentAndMemory(t *testing.T) {
	cfg := scouterkit.DefaultConfig()
	cfg.DX, cfg.DY = -15, -8
	if err := scouterkit.Init(cfg); err != nil {
		t.Fatal(err)
	}
	defer scouterkit.Deinit()
	arms := []scouterkit.Arm{scouterkit.Left, scouterkit.Right}
	view, err := showCalibration(cfg, arms, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer demo.Stop(arms)
	if err := view.move(cfg.DX+2, cfg.DY-3); err != nil {
		t.Fatal(err)
	}
	x, y, free := view.x, view.y, scouterkit.FreeMemory()
	before := view.bitmaps[scouterkit.Right].Image()
	if err := view.resize(1.5); err != nil {
		t.Fatal(err)
	}
	after := view.bitmaps[scouterkit.Right].Image()
	if bytes.Equal(before.Pix, after.Pix) {
		t.Fatal("resizing did not redraw the numbers")
	}
	o := options{fontScale: 1.5}
	for _, key := range []calibrationKey{calibrationLarger, calibrationSmaller} {
		for i := 0; i < 20; i++ {
			o = nudgeCalibration(o, key)
			if err := view.resize(o.fontScale); err != nil {
				t.Fatalf("resize to %g: %v", o.fontScale, err)
			}
			if view.x != x || view.y != y || view.dx != cfg.DX+2 || view.dy != cfg.DY-3 {
				t.Fatal("font resizing changed alignment")
			}
			if scouterkit.Right.EmulatedPanel().GrayAt(x+calibrationW/2, y+calibrationH/2).Y == 0 {
				t.Fatal("font resizing erased or moved the crosshair")
			}
		}
	}
	if got := scouterkit.FreeMemory(); got != free {
		t.Fatalf("resizing leaked device memory: free %d, originally %d", got, free)
	}
}
