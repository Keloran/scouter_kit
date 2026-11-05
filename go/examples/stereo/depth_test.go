package main

import (
	"testing"

	"scouterkit"
)

// The user's calibration: DX 100 at 0.8 m, so 80 px·m / z; each eye takes
// half of the change from DX, and the element stays where the virtual
// display has it (the mean of the eyes' positions, DX apart).
func TestDisparity(t *testing.T) {
	g := depth{cfg: scouterkit.Config{DX: 100, DY: 9}, k: 80}
	for _, c := range []struct {
		z    float64
		d    int
		l, r int
	}{
		{0, 0, 250, 250},
		{2, 40, 270, 230},
		{0.8, 100, 300, 200},
		{0.6, 133, 317, 184},
		{0.55, 145, 323, 178},
	} {
		d := g.disparity(c.z)
		l, r := g.panelX(scouterkit.Left, 300, d), g.panelX(scouterkit.Right, 300, d)
		if d != c.d || l != c.l || r != c.r {
			t.Errorf("%.2f m: disparity %d, left %d, right %d; want %d, %d, %d", c.z, d, l, r, c.d, c.l, c.r)
		}
		if l-r != d || l+r+100 < 599 || l+r+100 > 601 {
			t.Errorf("%.2f m: left %d, right %d do not straddle virtual x 300", c.z, l, r)
		}
	}
	if y := g.panelY(scouterkit.Right, 20); y != 29 {
		t.Errorf("right eye row %d for virtual row 20, want 29", y)
	}
	neg := depth{cfg: scouterkit.Config{DX: -100, DY: -9}, k: 80}
	for _, d := range []int{0, 40, 133} {
		if l, r := neg.panelX(scouterkit.Left, 300, d), neg.panelX(scouterkit.Right, 300, d); l-r != d {
			t.Errorf("DX -100, disparity %d: left %d, right %d", d, l, r)
		}
	}
}
