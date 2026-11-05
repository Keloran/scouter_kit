//go:build emulator

package main

import (
	"math/rand"
	"strings"
	"testing"

	"scouterkit"
	"scouterkit/examples/internal/demo"
	"scouterkit/examples/internal/speech"
)

// The update planner against a model of the program: captions growing
// word by word, wrapping, replaced and cleared; after every update the
// band equals the lines drawn whole.
func TestPlan(t *testing.T) {
	if err := scouterkit.Init(scouterkit.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	defer scouterkit.Deinit()
	font, err := scouterkit.LoadFont(scouterkit.DepartureMono(), "", 2)
	if err != nil {
		t.Fatal(err)
	}
	f, err := demo.FontOf(scouterkit.Left, font)
	if err != nil {
		t.Fatal(err)
	}
	g := geometry{w: 540, lines: 3, lineH: max(font.LineSpacing, font.Ascent+font.Descent), ascent: font.Ascent, tail: 8}
	m := demo.NewImage(g.bw(), g.h())
	words := strings.Fields("the quick brown fox jumps over the lazy dog while żółw zażółca gęślą jaźń and so on")
	rng := rand.New(rand.NewSource(9))
	var c captions
	drawn, valid, scrolls, drawnLines, updates := make([]line, g.lines), false, 0, 0, 0
	seg, stable, tail := 1, "", ""
	for i := 0; i < 600; i++ {
		switch r := rng.Intn(30); {
		case r == 0:
			c.reset()
		case r < 4:
			c.add(speech.Text{Segment: seg, Stable: strings.TrimSpace(stable + " " + tail), Final: true})
			seg, stable, tail = seg+1, "", ""
		case r < 12:
			stable, tail = strings.TrimSpace(stable+" "+tail), words[rng.Intn(len(words))]
			c.add(speech.Text{Segment: seg, Stable: stable, Tail: tail})
		default:
			tail = words[rng.Intn(len(words))]
			c.add(speech.Text{Segment: seg, Stable: stable, Tail: tail})
		}
		lines := c.lines(font, g.w-2*margin, g.lines)
		want := make([]line, g.lines)
		copy(want[g.lines-len(lines):], lines)
		u := g.plan(drawn, want, valid)
		if u.scroll > 0 {
			copy(m.Pix, m.Pix[u.scroll*g.lineH*m.W:])
			m.Fill(0, g.h()-u.scroll*g.lineH, g.bw(), u.scroll*g.lineH, 0)
			scrolls++
		}
		for k := 0; k < g.lines; k++ {
			if u.mask&(1<<k) != 0 {
				m.Fill(0, k*g.lineH, g.bw(), g.lineH, 0)
				gl := glyphLine(font, f, want[k])
				f.Draw(m, margin, k*g.lineH+g.ascent, gl.glyphs[:gl.split], 15)
				f.Draw(m, gl.splitX, k*g.lineH+g.ascent, gl.glyphs[gl.split:], uint8(g.tail))
				drawnLines++
			}
		}
		drawn, valid, updates = want, true, updates+1
		glyphs := make([]drawnLine, g.lines)
		for k, l := range want {
			glyphs[k] = glyphLine(font, f, l)
		}
		exp := band(f, g, glyphs)
		for y := 0; y < g.h(); y++ {
			for x := 0; x < g.w+1; x++ {
				if m.At(x, y) != exp.At(x, y) {
					t.Fatalf("update %d: pixel (%d, %d) is %d, the band has %d (plan %+v)", i, x, y, m.At(x, y), exp.At(x, y), u)
				}
			}
		}
	}
	t.Logf("%d updates: %d scrolls, %.2f lines drawn per update (of %d)", updates, scrolls, float64(drawnLines)/float64(updates), g.lines)
}
