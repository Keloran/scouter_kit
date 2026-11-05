//go:build emulator

package main

import (
	"math/rand"
	"testing"

	"scouterkit"
	"scouterkit/examples/internal/demo"
)

// bitmap models what the program does to an arm's bitmap.
type bitmap struct {
	g  geometry
	m  *demo.Image
	sc script
	f  *demo.Font
}

func (b *bitmap) apply(t *testing.T, s step, v view) {
	g, m := b.g, b.m
	switch {
	case s.full:
		m.Fill(0, 0, g.bw(), g.rows(), 0)
	case s.dy > 0:
		copy(m.Pix, m.Pix[s.dy*m.W:])
		m.Fill(0, g.rows()-s.dy, g.bw(), s.dy, 0)
	case s.dy < 0:
		copy(m.Pix[-s.dy*m.W:], m.Pix[:(g.rows()+s.dy)*m.W])
		m.Fill(0, 0, g.bw(), -s.dy, 0)
	}
	for _, k := range s.lines {
		top := k*g.lineH - v.pos
		if top+g.lineH > g.rows() {
			t.Fatalf("line %d drawn past the bitmap: rows %d...%d of %d", k, top, top+g.lineH, g.rows())
		}
		m.Fill(0, top, g.bw(), g.lineH, 0)
		y := top + g.ascent
		gl := b.sc.lines[k].glyphs
		switch st := v.state(k); st[0] {
		case 0:
			b.f.Draw(m, margin, y, gl, uint8(g.dim))
		case 1:
			b.f.Draw(m, margin, y, gl[:v.split], uint8(g.dim))
			b.f.Draw(m, v.splitX, y, gl[v.split:], 15)
		default:
			b.f.Draw(m, margin, y, gl, 15)
		}
	}
	m.Fill(0, 0, 3, g.rows(), 0)
	m.Fill(0, g.mark+2, 3, g.lineH-4, uint8(g.dim))
}

// The planner against a model of the program: after random moves (word by
// word, skips ahead and back, jumps), with and without scroll steps, the
// area equals the page drawn whole.
func TestPlan(t *testing.T) {
	if err := scouterkit.Init(scouterkit.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	defer scouterkit.Deinit()
	text := gettysburg + "\n\n" + gettysburg
	font, err := scouterkit.LoadFont(scouterkit.DepartureMono(), charset(text), 2)
	if err != nil {
		t.Fatal(err)
	}
	f, err := demo.FontOf(scouterkit.Left, font)
	if err != nil {
		t.Fatal(err)
	}
	sc := layoutScript(text, font, 540-2*margin, recordBytes-4)
	g := geometry{w: 540, h: 240, lineH: max(font.LineSpacing, font.Ascent+font.Descent), ascent: font.Ascent,
		total: len(sc.lines), ring: len(sc.lines), dim: 5}
	g.mark = g.h * 3 / 10
	rng := rand.New(rand.NewSource(7))
	for _, vmax := range []int{g.lineH, 16, 3} {
		b := &bitmap{g: g, m: demo.NewImage(g.bw(), g.rows()), sc: sc, f: f}
		pl := planner{g: g}
		pos, steps, lines := 0, 0, 0
		for i := 0; i < 400; i++ {
			switch r := rng.Intn(20); {
			case r < 14:
				pos++
			case r < 16:
				pos += 5 + rng.Intn(30)
			case r < 18:
				pos -= 1 + rng.Intn(20)
			default:
				pos = rng.Intn(len(sc.words))
			}
			pos = max(0, min(pos, len(sc.words)))
			want := sc.view(f, g, pos)
			for !pl.valid || pl.drawn != want {
				next := want
				if pl.valid {
					next.pos = pl.drawn.pos + moveBy(want.pos-pl.drawn.pos, vmax)
				}
				s := pl.plan(next)
				b.apply(t, s, next)
				steps, lines = steps+1, lines+len(s.lines)
				if len(s.lines) > maxLines {
					t.Fatalf("step with %d lines", len(s.lines))
				}
				exp := page(f, g, sc.lines, next)
				for y := 0; y < g.h; y++ {
					for x := 0; x < g.w+1; x++ {
						if b.m.At(x, y) != exp.At(x, y) {
							t.Fatalf("vmax %d, move %d to word %d: pixel (%d, %d) is %d, the page has %d (view %+v, step %+v)",
								vmax, i, pos, x, y, b.m.At(x, y), exp.At(x, y), next, s)
						}
					}
				}
			}
		}
		rows := vmax
		t.Logf("steps of at most %d rows: %d steps, %.2f lines drawn per step", rows, steps, float64(lines)/float64(steps))
	}
}
