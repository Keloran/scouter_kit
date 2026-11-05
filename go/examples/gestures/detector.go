package main

import "time"

// Gestures, as the program reports them (NOTIFY a; wGesture).
const (
	gestureNod    = 1
	gestureDouble = 2
	gestureShake  = 3 // needs the gyroscope (GYR_Y, M8): nothing raises it yet
)

// params are the detector's thresholds in the program's units: pitch in
// 1/16 degree, times in samples (20 ms), motion as a squared change in mg².
type params struct {
	start, depth, depth2                 int32
	minLen, maxLen, quiet, guard, window uint32
	maxLen2                              uint32 // of the second dip
	motion2                              uint32
}

type thresholds struct {
	start, depth, depth2                   float64 // degrees
	min, max, second, double, quiet, guard time.Duration
	motion                                 int // mg of change over 80 ms
}

func defaultThresholds() thresholds {
	return thresholds{start: 3, depth: 8, depth2: 5, min: 100 * time.Millisecond, max: 800 * time.Millisecond,
		second: 500 * time.Millisecond,
		quiet:  300 * time.Millisecond, guard: 240 * time.Millisecond, double: 400 * time.Millisecond, motion: 60}
}

func (t thresholds) params() params {
	q4 := func(d float64) int32 { return int32(d*16 + 0.5) }
	n := func(d time.Duration) uint32 { return uint32((d + samplePeriodD/2) / samplePeriodD) }
	return params{start: q4(t.start), depth: q4(t.depth), depth2: q4(t.depth2), minLen: n(t.min), maxLen: n(t.max),
		quiet: n(t.quiet), guard: max(1, n(t.guard)), window: n(t.double), motion2: uint32(t.motion * t.motion),
		maxLen2: n(t.second)}
}

const samplePeriodD = 20 * time.Millisecond

// detector is the program's nod detector in Go, step for step: the
// reference the program is checked against.
//
// At rest a reference follows the pitch once the head has been still (no
// change above the motion threshold between two samples) for guard samples. A nod starts when the
// pitch drops start below it, provided the head was still for quiet samples
// up to guard samples before; it is one when the dip reaches depth and the
// pitch comes half way back, between minLen and maxLen samples after the
// start. A second dip that starts within window samples, reaches depth2
// below the peak since (at most the reference) and comes half way back
// within window samples makes a double nod (no minimum length: it follows a
// nod); otherwise the nod is reported
// when that fails or the window closes.
type detector struct {
	params
	s, q, s0, ret  uint32
	state          int
	ref, low, peak int32
	acc            [4][3]int32
	hist           [4]int32
	ring           []uint32
}

func newDetector(p params) *detector { return &detector{params: p, ring: make([]uint32, p.guard)} }

const unknownPitch = -0x80000000

// step takes one sample and returns the gesture it completes, or 0.
func (d *detector) step(x sample) int {
	d.s++
	if x.pitch == unknownPitch {
		d.q, d.state = 0, 0
		return 0
	}
	h := d.s % 4
	d.hist[h] = x.pitch
	p := (d.hist[0] + d.hist[1] + d.hist[2] + d.hist[3]) << 2
	var m uint32
	for i, v := range [3]int32{x.ax, x.ay, x.az} {
		e := uint32(v - d.acc[h][i])
		m += e * e
		d.acc[h][i] = v
	}
	d.q++
	if m > d.motion2 {
		d.q = 0
	}
	k := d.s % d.guard
	old := d.ring[k]
	d.ring[k] = d.q
	switch d.state {
	case 0:
		switch {
		case p >= d.ref-d.start:
			if d.q >= d.guard {
				d.ref += (p - d.ref) >> 3
			}
		case old < d.quiet:
			d.ref = p
		default:
			d.state, d.s0, d.low = 1, d.s, p
		}
	case 1:
		d.low = min(d.low, p)
		n := d.s - d.s0
		dip := d.ref - d.low
		switch {
		case n > d.maxLen:
			d.state, d.ref = 0, p
		case dip < d.depth || 2*(p-d.low) < dip:
		case n < d.minLen:
			d.state, d.ref = 0, p
		default:
			d.state, d.ret, d.peak = 2, d.s, min(p, d.ref)
		}
	case 2:
		d.peak = max(d.peak, min(p, d.ref))
		if d.s-d.ret > d.window {
			return d.report(gestureNod, p)
		}
		if p < d.peak-d.start {
			d.state, d.s0, d.low = 3, d.s, p
		}
	case 3:
		d.low = min(d.low, p)
		n := d.s - d.s0
		dip := d.peak - d.low
		switch {
		case dip < d.depth2:
			if d.s-d.ret > d.window {
				return d.report(gestureNod, p)
			}
		case 2*(p-d.low) < dip:
			if n > d.maxLen2 {
				return d.report(gestureNod, p)
			}
		default:
			return d.report(gestureDouble, p)
		}
	}
	return 0
}

func (d *detector) report(kind int, p int32) int {
	d.state, d.ref = 0, p
	return kind
}

// detection is a gesture the detector reported at sample index at (0-based).
type detection struct {
	kind int
	at   int
}

func detect(p params, samples []sample) []detection {
	d := newDetector(p)
	var out []detection
	for i, x := range samples {
		if k := d.step(x); k != 0 {
			out = append(out, detection{k, i})
		}
	}
	return out
}
