package main

import (
	"errors"
	"math"

	"scouterkit/examples/internal/demo"
)

// geometry is the cube and its bitmap: a cube of half-size s, seen from
// distance d, in a size x size bitmap.
type geometry struct {
	size, s, d int
	level      uint8
	spin       int    // degrees per frame about the vertical axis
	period     uint32 // µs per frame
	rate       int    // IMU_RATE while it runs
}

func (g geometry) stride() int { return g.size/2 + 1 }

// cost is the arm's time for a frame, µs: the FILL and the odd-width DRAW at
// gifplay's measured costs, about 12 edges of size/2 pixels and the
// instructions of the vertex and edge loops (on g1v0 0.5.6 a 150-pixel cube
// at 20 fps kept the arms at their 30 % cap: about 15 ms a frame).
func (g geometry) cost() float64 {
	return 0.12*float64(g.stride()*g.size) + float64(g.size)*(46+0.196*float64(g.stride())) +
		12*0.5*float64(g.size)*1.5 + 3500
}

func newGeometry(size int) (geometry, error) {
	g := geometry{size: size &^ 1, level: 15}
	g.s = g.size / 5
	g.d = 6 * g.s
	r := float64(g.s) * math.Sqrt(3)
	if r*float64(g.d)/(float64(g.d)-r) >= float64(g.size/2-1) {
		return g, errors.New("the cube does not fit its bitmap")
	}
	return g, nil
}

// sines is sin(i°) in Q14 for i = 0...449, so cos(i°) is entry i + 90.
func sines() []int32 {
	t := make([]int32, 450)
	for i := range t {
		t[i] = int32(math.Round(16384 * math.Sin(float64(i)*math.Pi/180)))
	}
	return t
}

var edges = [12][2]int{{0, 1}, {1, 3}, {3, 2}, {2, 0}, {4, 5}, {5, 7}, {7, 6}, {6, 4}, {0, 4}, {1, 5}, {2, 6}, {3, 7}}

func (g geometry) vertices() [8][3]int32 {
	var v [8][3]int32
	for i := range v {
		for k := 0; k < 3; k++ {
			v[i][k] = int32(g.s)
			if i>>k&1 == 0 {
				v[i][k] = -int32(g.s)
			}
		}
	}
	return v
}

// trig is sin and cos of a degrees (a in -90...90), as the program looks
// them up.
func trig(t []int32, a int32) (int32, int32) {
	i := uint32(360+a) % 360
	return t[i], t[i+90]
}

// project rotates the vertices by yaw, then pitch about x, then roll about
// z, all in Q14, and projects them: the program's arithmetic exactly.
func (g geometry) project(pitch, roll, yaw int32) (px, py [8]int32) {
	t := sines()
	sa, ca := trig(t, pitch)
	sb, cb := trig(t, roll)
	sy, cy := t[yaw], t[yaw+90]
	d, c := int32(g.d), int32(g.size/2)
	for i, v := range g.vertices() {
		x, y, z := v[0], v[1], v[2]
		x1 := (x*cy + z*sy) >> 14
		z1 := (z*cy - x*sy) >> 14
		y2 := (y*ca - z1*sa) >> 14
		z2 := (y*sa + z1*ca) >> 14
		x3 := (x1*cb - y2*sb) >> 14
		y3 := (x1*sb + y2*cb) >> 14
		w := z2 + d
		px[i] = c + x3*d/w
		py[i] = c - y3*d/w
	}
	return px, py
}

// render is the bitmap the program draws for these angles.
func (g geometry) render(pitch, roll, yaw int32) *demo.Image {
	m := demo.NewImage(g.size, g.size)
	px, py := g.project(pitch, roll, yaw)
	for _, e := range edges {
		line(m, int(px[e[0]]), int(py[e[0]]), int(px[e[1]]), int(py[e[1]]), g.level)
	}
	return m
}

// line sets the pixels LINE sets (VM.md §3.6): one per step along the
// longer axis, the ends ordered so that it increases.
func line(m *demo.Image, x0, y0, x1, y1 int, level uint8) {
	dx, dy := abs(x1-x0), abs(y1-y0)
	if dx >= dy {
		if x0 > x1 {
			x0, x1, y0, y1 = x1, x0, y1, y0
		}
		d, y := 2*dy-dx, y0
		for x := x0; x <= x1; x++ {
			m.Set(x, y, level)
			if d > 0 {
				y += sign(y1 - y0)
				d -= 2 * dx
			}
			d += 2 * dy
		}
		return
	}
	if y0 > y1 {
		x0, x1, y0, y1 = x1, x0, y1, y0
	}
	d, x := 2*dx-dy, x0
	for y := y0; y <= y1; y++ {
		m.Set(x, y, level)
		if d > 0 {
			x += sign(x1 - x0)
			d -= 2 * dy
		}
		d += 2 * dx
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func sign(v int) int {
	if v > 0 {
		return 1
	}
	return -1
}
