package main

import (
	"image"
	"image/color"
	"image/gif"
	"math/rand"
	"testing"
	"time"

	"scouterkit/examples/internal/demo"
)

// testGIF is a w x h animation of n frames over a gradient: a bright square
// moving right, frame 3 the same as frame 2, frame 5 noise all over, frame 6
// noise but at the sides.
func testGIF(w, h, n int) *gif.GIF {
	pal := make(color.Palette, 64)
	for i := range pal {
		pal[i] = color.Gray{uint8(4 * i)}
	}
	pal[63] = color.Gray{255}
	rng := rand.New(rand.NewSource(1))
	noise := make([]uint8, w*h)
	g := &gif.GIF{Config: image.Config{Width: w, Height: h, ColorModel: pal}}
	for i := 0; i < n; i++ {
		f := image.NewPaletted(image.Rect(0, 0, w, h), pal)
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				v := uint8(1 + 50*x/w)
				if i == 5 || i == 6 && x >= 10 && x < w-10 {
					noise[y*w+x] = uint8(rng.Intn(64))
				}
				if i == 5 || i == 6 {
					v = noise[y*w+x]
				}
				f.SetColorIndex(x, y, v)
			}
		}
		sx := 10 + 20*min(i, 2) + 20*max(0, i-3)
		for y := h / 4; y < h/2; y++ {
			for x := sx; x < sx+w/8; x++ {
				f.SetColorIndex(x, y, 63)
			}
		}
		g.Image = append(g.Image, f)
		g.Delay = append(g.Delay, 4+i%3)
		g.Disposal = append(g.Disposal, gif.DisposalNone)
	}
	return g
}

func TestDeltasReplay(t *testing.T) {
	src := compose(testGIF(400, 100, 7))
	for _, dither := range []bool{false, true} {
		a := src.build(image.Rect(0, 0, 400, 100), 400, 100, 7, options{gamma: 1, dither: dither})
		canvas := demo.NewImage(a.w, a.h)
		copy(canvas.Pix, a.frames[len(a.frames)-1].Pix)
		chunks := 0
		for i, d := range a.deltas {
			if d.x%2 != 0 || d.w%2 != 0 || d.w*d.h > 0 && len(d.chunks) == 0 || d.full != (d.w == a.w) {
				t.Fatalf("frame %d: rect %+v", i, d)
			}
			n := d.rowBytes(a.w)
			for _, c := range d.chunks {
				rows, ok := demo.Unpack(c.packed, n*c.h)
				if !ok || !d.full && n*c.h > scratchBytes {
					t.Fatalf("frame %d: chunk of %d rows does not unpack", i, c.h)
				}
				for r := 0; r < c.h; r++ {
					for x := 0; x < d.w; x++ {
						v := rows[r*n+x/2]
						if x%2 == 0 {
							v >>= 4
						}
						canvas.Set(d.x+x, c.y+r, v&15)
					}
				}
				chunks++
			}
			if !canvas.Equal(a.frames[i]) {
				n, x, y := demo.Diff(canvas, a.frames[i])
				t.Fatalf("dither %v, frame %d: %d pixels differ, first at (%d, %d)", dither, i, n, x, y)
			}
		}
		if a.deltas[3].w != 0 || !a.deltas[5].full || a.deltas[6].full || len(a.deltas[6].chunks) < 2 {
			t.Errorf("frame 3 %+v, frame 5 full %v, frame 6 in %d chunks", a.deltas[3], a.deltas[5].full, len(a.deltas[6].chunks))
		}
		t.Logf("dither %v: %d chunks, %d bytes in the arena", dither, chunks, a.bytes())
	}
}

func TestFitBudget(t *testing.T) {
	src := compose(testGIF(400, 100, 7))
	slow := &source{frames: src.frames}
	for range src.delays {
		slow.delays = append(slow.delays, 500*time.Millisecond)
	}
	full, err := slow.fit(1<<20, 740, 471, 0, options{gamma: 1})
	if err != nil || full.w != 400 || full.h != 100 || full.dropped != 0 || full.load() > capShare {
		t.Fatalf("%v, %dx%d, %d dropped, load %.2f", err, full.w, full.h, full.dropped, full.load())
	}
	fast, err := src.fit(1<<20, 740, 471, 0, options{gamma: 1})
	if err != nil || fast.w >= 400 || fast.load() > capShare {
		t.Fatalf("%v, %dx%d, load %.2f", err, fast.w, fast.h, fast.load())
	}
	small, err := slow.fit(full.bytes()/3, 740, 471, 0, options{gamma: 1})
	if err != nil || small.bytes() > full.bytes()/3 || small.w >= 400 || small.dropped != 0 {
		t.Fatalf("%v: %dx%d, %d bytes, %d dropped", err, small.w, small.h, small.bytes(), small.dropped)
	}
	few, err := src.fit(6000, 740, 471, 0, options{gamma: 1})
	var loop time.Duration
	for _, d := range src.delays {
		loop += d
	}
	if err != nil || few.bytes() > 6000 || few.dropped == 0 || few.w < 160 || few.duration() != loop {
		t.Fatalf("%v: %dx%d, %d bytes, %d dropped, %v per loop", err, few.w, few.h, few.bytes(), few.dropped, few.duration())
	}
	t.Logf("40-60 ms frames: %dx%d (load %.2f); budget %d: %dx%d; budget 6000: %d frames at %dx%d", fast.w, fast.h,
		fast.load(), full.bytes()/3, small.w, small.h, len(few.frames), few.w, few.h)
}
