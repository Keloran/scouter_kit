package main

import (
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/gif"
	"math"
	"os"
	"time"

	"scouterkit/examples/internal/demo"
	"scouterkit/examples/internal/picture"
)

// source is a decoded GIF: each frame composed over the previous ones, as
// luma, with its delay.
type source struct {
	frames []*image.Gray
	delays []time.Duration
}

func decode(path string) (*source, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	g, err := gif.DecodeAll(f)
	if err != nil {
		return nil, err
	}
	return compose(g), nil
}

func compose(g *gif.GIF) *source {
	canvas := image.NewRGBA(image.Rect(0, 0, g.Config.Width, g.Config.Height))
	s := &source{}
	for i, frame := range g.Image {
		disposal := byte(0)
		if i < len(g.Disposal) {
			disposal = g.Disposal[i]
		}
		var previous *image.RGBA
		if disposal == gif.DisposalPrevious {
			previous = image.NewRGBA(canvas.Rect)
			copy(previous.Pix, canvas.Pix)
		}
		draw.Draw(canvas, frame.Rect, frame, frame.Rect.Min, draw.Over)
		s.frames = append(s.frames, picture.Luma(canvas))
		d := time.Duration(g.Delay[i]) * 10 * time.Millisecond
		if d < 20*time.Millisecond {
			d = 100 * time.Millisecond // as browsers do
		}
		s.delays = append(s.delays, d)
		switch disposal {
		case gif.DisposalBackground:
			draw.Draw(canvas, frame.Rect, image.Transparent, image.Point{}, draw.Src)
		case gif.DisposalPrevious:
			canvas = previous
		}
	}
	return s
}

// crop is the union of the frames' content without their dark margin
// (black is see-through on the glasses).
func (s *source) crop() image.Rectangle {
	var r image.Rectangle
	for _, f := range s.frames {
		r = r.Union(picture.Trim(f, 8).Rect)
	}
	return r
}

type options struct {
	gamma  float64
	dither bool
}

// chunk is a band of a frame's changed rectangle, PackBits-packed.
type chunk struct {
	y, h   int
	packed []byte
}

// delta is what changes from the previous frame (the last one for frame 0):
// an even-aligned rectangle (w = 0: nothing). A rectangle as wide as the
// canvas is one chunk of whole canvas rows, unpacked straight into it;
// others are bands of at most scratchBytes, unpacked into the scratch rows
// and BLITted.
type delta struct {
	x, y, w, h int
	full       bool
	chunks     []chunk
}

// canvasStride is a byte more than a row of w pixels needs, so that a DRAW
// can be an odd number of pixels wide: the panel takes an odd-width row in
// one window, an even one in two.
func canvasStride(w int) int { return w/2 + 1 }

func (d delta) rowBytes(w int) int {
	if d.full {
		return canvasStride(w)
	}
	return d.w / 2
}

const scratchBytes = 8192

// anim is a GIF prepared for the arena: frames as panel levels (the host's
// expectation), their deltas and delays.
type anim struct {
	w, h    int
	frames  []*demo.Image
	deltas  []delta
	delays  []uint32 // µs
	scale   float64
	dropped int
}

// bytes is the arena the animation needs besides the program.
func (a *anim) bytes() int {
	n := canvasStride(a.w)*a.h + a.scratch() + 32*len(a.frames)
	for _, d := range a.deltas {
		n += 32 * len(d.chunks)
		for _, c := range d.chunks {
			n += (len(c.packed) + 3) &^ 3
		}
	}
	return n
}

// What drawing costs the arm, µs (g1v0 0.5.6, single instructions timed
// with TIME_US in host code, logs/demos/08-probe.txt): an odd-width DRAW
// row 46 + 0.196 per byte, UNPACK about 0.2 per byte of real frames (0.123
// for plain runs), BLIT and FILL 0.105...0.12 per byte; per chunk and frame
// for the instructions around them.
const (
	drawRow    = 46.0
	drawByte   = 0.196
	unpackByte = 0.2
	blitByte   = 0.105
	chunkCost  = 30.0
	frameCost  = 300.0
	capShare   = 0.25 // of any 100 ms (a program gets 30 %)
)

// cost is the arm's time for frame i, µs.
func (a *anim) cost(i int) float64 {
	d := a.deltas[i]
	t := frameCost
	for _, c := range d.chunks {
		n := float64(d.rowBytes(a.w) * c.h)
		t += chunkCost + unpackByte*n
		if !d.full {
			t += blitByte * n
		}
	}
	if d.w > 0 {
		t += float64(d.h) * (drawRow + drawByte*float64((d.w+2)/2))
	}
	return t
}

// load is the largest share of any 100 ms that the frames starting in it
// cost, over a loop and into the next.
func (a *anim) load() float64 {
	n := len(a.frames)
	at := make([]float64, 2*n+1)
	for i := 0; i < 2*n; i++ {
		at[i+1] = at[i] + float64(a.delays[i%n])
	}
	top := 0.0
	for i := 0; i < n; i++ {
		sum := 0.0
		for j := i; j < 2*n && at[j] < at[i]+100000; j++ {
			sum += a.cost(j % n)
		}
		top = max(top, sum/100000)
	}
	return top
}

// fits reports whether the animation fits in budget bytes and in the arms'
// run-time cap.
func (a *anim) fits(budget int) bool { return a.bytes() <= budget && a.load() <= capShare }

func (a *anim) scratch() int {
	n := 0
	for _, d := range a.deltas {
		for _, c := range d.chunks {
			if !d.full {
				n = max(n, d.w/2*c.h)
			}
		}
	}
	return (n + 3) &^ 3
}

func (a *anim) duration() time.Duration {
	var d time.Duration
	for _, v := range a.delays {
		d += time.Duration(v) * time.Microsecond
	}
	return d
}

// resize averages the source pixels under each of the w x h pixels.
func resize(g *image.Gray, r image.Rectangle, w, h int) []float64 {
	out := make([]float64, w*h)
	sw, sh := r.Dx(), r.Dy()
	for y := 0; y < h; y++ {
		y0, y1 := r.Min.Y+y*sh/h, r.Min.Y+max((y+1)*sh/h, y*sh/h+1)
		for x := 0; x < w; x++ {
			x0, x1 := r.Min.X+x*sw/w, r.Min.X+max((x+1)*sw/w, x*sw/w+1)
			sum := 0
			for sy := y0; sy < y1; sy++ {
				row := g.Pix[g.PixOffset(x0, sy):]
				for i := 0; i < x1-x0; i++ {
					sum += int(row[i])
				}
			}
			out[y*w+x] = float64(sum) / float64((y1-y0)*(x1-x0))
		}
	}
	return out
}

func level(v float64) uint8 { return uint8(min(15, max(0, math.Floor(v*15/255+0.5)))) }

// build makes the animation from the first n frames at w x h.
func (s *source) build(r image.Rectangle, w, h, n int, o options) *anim {
	a := &anim{w: w, h: h}
	var prev []float64
	for i := 0; i < n; i++ {
		v := resize(s.frames[i], r, w, h)
		for k := range v {
			v[k] = 255 * math.Pow(v[k]/255, o.gamma)
		}
		m := demo.NewImage(w, h)
		switch {
		case !o.dither:
			for k := range v {
				m.Pix[k] = level(v[k])
			}
		case prev == nil:
			dither(m, v, image.Rect(0, 0, w, h))
		default:
			copy(m.Pix, a.frames[i-1].Pix)
			dither(m, v, changed(prev, v, w, h))
		}
		prev = v
		a.frames = append(a.frames, m)
		a.delays = append(a.delays, uint32(s.delays[i].Microseconds()))
	}
	for i := range a.frames {
		a.deltas = append(a.deltas, encode(a.frames[(i+n-1)%n], a.frames[i]))
	}
	return a
}

// changed is the bounding box of the pixels that differ.
func changed(a, b []float64, w, h int) image.Rectangle {
	var r image.Rectangle
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if a[y*w+x] != b[y*w+x] {
				r = r.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	return r
}

// dither quantizes r of v into m with Floyd–Steinberg (serpentine), the
// error kept inside r, so the rest of m stays as it is.
func dither(m *demo.Image, v []float64, r image.Rectangle) {
	e := make([]float64, len(v))
	copy(e, v)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		x0, x1, dx := r.Min.X, r.Max.X, 1
		if (y-r.Min.Y)%2 == 1 {
			x0, x1, dx = r.Max.X-1, r.Min.X-1, -1
		}
		for x := x0; x != x1; x += dx {
			i := y*m.W + x
			q := level(e[i])
			m.Pix[i] = q
			err := e[i] - float64(q)*17
			spread := func(x, y int, f float64) {
				if image.Pt(x, y).In(r) {
					e[y*m.W+x] += err * f
				}
			}
			spread(x+dx, y, 7.0/16)
			spread(x-dx, y+1, 3.0/16)
			spread(x, y+1, 5.0/16)
			spread(x+dx, y+1, 1.0/16)
		}
	}
}

// encode is the delta from frame a to frame b.
func encode(a, b *demo.Image) delta {
	x0, y0, x1, y1 := b.W, b.H, 0, 0
	for i := range b.Pix {
		if a.Pix[i] != b.Pix[i] {
			x, y := i%b.W, i/b.W
			x0, y0, x1, y1 = min(x0, x), min(y0, y), max(x1, x+1), max(y1, y+1)
		}
	}
	if x1 == 0 {
		return delta{}
	}
	x0, x1 = x0&^1, (x1+1)&^1
	d := delta{x: x0, y: y0, w: x1 - x0, h: y1 - y0, full: x0 == 0 && x1 == b.W}
	if d.full {
		rows := demo.NewImage(2*canvasStride(b.W), d.h)
		for y := 0; y < d.h; y++ {
			copy(rows.Pix[y*rows.W:], b.Pix[(y0+y)*b.W:(y0+y+1)*b.W])
		}
		d.chunks = []chunk{{y: y0, h: d.h, packed: demo.PackBits(rows.Rows(0, d.h))}}
		return d
	}
	rect := demo.NewImage(d.w, d.h)
	for y := 0; y < d.h; y++ {
		copy(rect.Pix[y*d.w:(y+1)*d.w], b.Pix[(y0+y)*b.W+x0:])
	}
	band := max(1, scratchBytes/(d.w/2))
	for y := 0; y < d.h; y += band {
		h := min(band, d.h-y)
		d.chunks = append(d.chunks, chunk{y: y0 + y, h: h, packed: demo.PackBits(rect.Rows(y, h))})
	}
	return d
}

// every keeps every k-th frame, each shown for the delays of the frames it
// stands for, so a loop keeps its length.
func (s *source) every(k int) *source {
	out := &source{}
	for i := 0; i < len(s.frames); i += k {
		var d time.Duration
		for j := i; j < min(i+k, len(s.frames)); j++ {
			d += s.delays[j]
		}
		out.frames = append(out.frames, s.frames[i])
		out.delays = append(out.delays, d)
	}
	return out
}

// minWidth is the narrowest picture fit makes before it keeps fewer frames.
const minWidth = 160

// fit prepares the animation for budget bytes of arena and the arms'
// run-time cap: every frame at the largest scale that fits (at most 1:1 and
// the display's maxW x maxH), down to minWidth; below that every second
// frame, every third, ..., each again at the largest scale that fits.
// scale > 0 forces a scale.
func (s *source) fit(budget, maxW, maxH int, scale float64, o options) (*anim, error) {
	if len(s.frames) == 0 {
		return nil, errors.New("no frames")
	}
	r := s.crop()
	top := min(1, float64(maxW)/float64(r.Dx()), float64(maxH)/float64(r.Dy()))
	low := min(top, minWidth/float64(r.Dx()))
	if scale > 0 {
		top, low = min(scale, top), min(scale, top)
	}
	for k := 1; k <= len(s.frames); k++ {
		src := s.every(k)
		at := func(f float64) *anim {
			w := max(2, int(float64(r.Dx())*f)&^1)
			h := max(1, int(float64(r.Dy())*f))
			a := src.build(r, w, h, len(src.frames), o)
			a.scale, a.dropped = f, len(s.frames)-len(src.frames)
			return a
		}
		if a := at(top); a.fits(budget) {
			return a, nil
		}
		best := at(low)
		if !best.fits(budget) {
			continue
		}
		lo, hi := low, top
		for i := 0; i < 8; i++ {
			mid := (lo + hi) / 2
			if a := at(mid); a.fits(budget) {
				best, lo = a, mid
			} else {
				hi = mid
			}
		}
		return best, nil
	}
	return nil, fmt.Errorf("not even one frame fits in %d bytes and the arms' time", budget)
}
