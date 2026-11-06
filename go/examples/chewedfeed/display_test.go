//go:build emulator

package main

import (
	"testing"

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
