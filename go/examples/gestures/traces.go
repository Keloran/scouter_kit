package main

import (
	"math"
	"math/rand"
)

// sample is one reading as the program gets it: PITCH (degrees) and
// ACC_X/Y/Z (mg, head frame: x right, y up, z forward).
type sample struct{ pitch, ax, ay, az int32 }

// truth is a gesture in a synthetic trace: its kind and the samples at
// which it starts and ends (the head back up).
type truth struct {
	kind       int
	start, end int
}

// trace is synthetic samples every samplePeriod with the gestures in them.
type trace struct {
	name    string
	samples []sample
	truth   []truth
}

const (
	samplePeriod = 0.02 // s: the program reads the sensor every 20 ms
	imuRate      = 52.0 // Hz: the accelerometer's own rate
	gravity      = 9.81
)

// motion is the head over time: pitch (rad, positive up) and the
// acceleration of the head (m/s², world frame: x right, y up, z forward).
type motion struct {
	pitch func(t float64) float64
	accel func(t float64) [3]float64
}

// sensor is where the accelerometer sits relative to the neck's pitch
// pivot (m, head frame) and how noisy it is (mg).
type sensor struct {
	up, forward, noise float64
	phase              float64 // of the accelerometer's samples, s
}

// The ranges the synthetic traces draw from.
var (
	leverUp      = [2]float64{0.02, 0.10} // m: the sensor above the neck's pitch pivot
	leverForward = [2]float64{0.03, 0.09}
	noiseMG      = [2]float64{2, 5}
	nodDepth     = [2]float64{10, 25}   // degrees
	nodLength    = [2]float64{0.3, 0.7} // s, single nods
	walkScale    = 1.0                  // walking accelerations
	lookEvery    = [2]float64{2, 8}     // s between looks up or down at rest
)

func randomSensor(rng *rand.Rand) sensor {
	return sensor{up: uniform(rng, leverUp[0], leverUp[1]), forward: uniform(rng, leverForward[0], leverForward[1]),
		noise: uniform(rng, noiseMG[0], noiseMG[1]),
		phase: rng.Float64() / imuRate}
}

// force is the specific force the accelerometer measures at time t, in the
// head frame, g: gravity's reaction, the head's acceleration and the
// acceleration of the sensor as the head pitches about the pivot.
func (s sensor) force(m motion, t float64) [3]float64 {
	pos := func(t float64) (float64, float64) {
		th := m.pitch(t)
		return s.up*math.Cos(th) + s.forward*math.Sin(th), -s.up*math.Sin(th) + s.forward*math.Cos(th)
	}
	const h = 1e-3
	y0, z0 := pos(t - h)
	y1, z1 := pos(t)
	y2, z2 := pos(t + h)
	a := m.accel(t)
	fy := a[1] + (y0-2*y1+y2)/(h*h) + gravity
	fz := a[2] + (z0-2*z1+z2)/(h*h)
	th := m.pitch(t)
	return [3]float64{a[0] / gravity, (fy*math.Cos(th) - fz*math.Sin(th)) / gravity, (fy*math.Sin(th) + fz*math.Cos(th)) / gravity}
}

// record samples the motion as the program does: every samplePeriod, the
// accelerometer's latest sample (it samples at imuRate), with noise,
// rounded to mg, and PITCH from it as the firmware computes it.
func (s sensor) record(m motion, seconds float64, rng *rand.Rand) []sample {
	n := int(seconds / samplePeriod)
	out := make([]sample, n)
	last, cached := -1, sample{}
	for k := range out {
		j := int(math.Floor((float64(k)*samplePeriod - s.phase) * imuRate))
		if j != last {
			f := s.force(m, s.phase+float64(j)/imuRate)
			var mg [3]int32
			for i := range mg {
				mg[i] = int32(math.Round(1000*f[i] + s.noise*rng.NormFloat64()))
			}
			cached = sample{pitch: firmwarePitch(mg[2], mg[1]), ax: mg[0], ay: mg[1], az: mg[2]}
			last = j
		}
		out[k] = cached
	}
	return out
}

func firmwarePitch(z, y int32) int32 {
	d := int32(math.Round(math.Atan2(float64(z), float64(y)) * 180 / math.Pi))
	return min(90, max(-90, d))
}

func deg(d float64) float64 { return d * math.Pi / 180 }

func uniform(rng *rand.Rand, lo, hi float64) float64 { return lo + (hi-lo)*rng.Float64() }

// sway is a head at rest: slow wandering of about a degree.
func sway(rng *rand.Rand) func(t float64) float64 {
	var f, a, ph [3]float64
	for i := range f {
		f[i], a[i], ph[i] = uniform(rng, 0.05, 0.5), deg(uniform(rng, 0.1, 0.5)), uniform(rng, 0, 2*math.Pi)
	}
	return func(t float64) float64 {
		v := 0.0
		for i := range f {
			v += a[i] * math.Sin(2*math.Pi*f[i]*t+ph[i])
		}
		return v
	}
}

// dip is one nod: down by depth (rad) in down seconds, then up to -rest in
// up seconds, each half a minimum-jerk movement (as people move), from
// where the head settles back in 1.5 s.
type dip struct{ t0, down, up, depth, rest float64 }

func (d dip) end() float64 { return d.t0 + d.down + d.up }

func minJerk(u float64) float64 { return u * u * u * (10 - 15*u + 6*u*u) }

func (d dip) at(t float64) float64 {
	switch {
	case t <= d.t0:
		return 0
	case t <= d.t0+d.down:
		return -d.depth * minJerk((t-d.t0)/d.down)
	case t <= d.end():
		return -d.depth + (d.depth-d.rest)*minJerk((t-d.t0-d.down)/d.up)
	}
	return -d.rest * (1 - minJerk(math.Min(1, (t-d.end())/1.5)))
}

// maxSpeed caps a nod's peak angular velocity (rad/s): brisk nods reach
// 100...250 degrees per second.
var maxSpeed = deg(250)

func randomDip(rng *rand.Rand, t0, depth, minT, maxT float64) dip {
	frac := uniform(rng, 0.35, 0.55)
	fastest := 1.875 * depth / maxSpeed / min(frac, 1-frac)
	total := uniform(rng, max(minT, fastest), max(maxT, fastest))
	down := total * frac
	return dip{t0: t0, down: down, up: total - down, depth: depth, rest: depth * uniform(rng, -0.05, 0.2)}
}

// still is a head at rest at a random pitch, with sway and, now and then,
// a slow look up or down that holds (seated, looking around).
func still(rng *rand.Rand, seconds float64, looks bool) motion {
	base, sw := deg(uniform(rng, -25, 20)), sway(rng)
	type look struct{ t0, dur, to float64 }
	var ls []look
	for t := uniform(rng, 2, 8); looks && t < seconds; {
		l := look{t, uniform(rng, 0.8, 2.5), deg(uniform(rng, -20, 20))}
		ls = append(ls, l)
		t += l.dur + uniform(rng, lookEvery[0], lookEvery[1])
	}
	return motion{
		pitch: func(t float64) float64 {
			p, from := base, 0.0
			for _, l := range ls {
				if t < l.t0 {
					break
				}
				u := math.Min(1, (t-l.t0)/l.dur)
				p = base + from + (l.to-from)*(1-math.Cos(math.Pi*u))/2
				from = l.to
			}
			return p + sw(t)
		},
		accel: func(float64) [3]float64 { return [3]float64{} },
	}
}

// nods is rest with single (kind 1) or double (kind 2) nods every few
// seconds, 10...25 degrees deep; it returns when each ends and the length.
func nods(rng *rand.Rand, kind, count int) (motion, [][2]float64, float64) {
	base, sw := deg(uniform(rng, -20, 15)), sway(rng)
	var dips []dip
	var spans [][2]float64
	t := uniform(rng, 2, 3)
	for i := 0; i < count; i++ {
		depth := deg(uniform(rng, nodDepth[0], nodDepth[1]))
		if kind == 1 {
			dips = append(dips, randomDip(rng, t, depth, nodLength[0], nodLength[1]))
		} else {
			first := randomDip(rng, t, depth, 0.25, 0.5)
			first.rest = depth * uniform(rng, 0, 0.3)
			dips = append(dips, first, randomDip(rng, first.end()+uniform(rng, 0, 0.12), depth*uniform(rng, 0.6, 1),
				0.25, 0.5))
		}
		spans = append(spans, [2]float64{t, dips[len(dips)-1].end()})
		t = dips[len(dips)-1].end() + uniform(rng, 2.5, 4)
	}
	m := motion{
		pitch: func(t float64) float64 {
			p := base + sw(t)
			for _, d := range dips {
				p += d.at(t)
			}
			return p
		},
		accel: func(float64) [3]float64 { return [3]float64{} },
	}
	return m, spans, t
}

// walking is a head at walking pace: vertical bounce at the step rate
// with a harmonic, fore-aft and sideways sway, a damped jolt at each heel
// strike, a small pitch bob, and now and then a look down at the path.
func walking(rng *rand.Rand, seconds float64) motion {
	f := uniform(rng, 1.6, 2.2)
	k := walkScale
	av, h2, af, al := k*uniform(rng, 1.5, 3.5), uniform(rng, 0.2, 0.5), k*uniform(rng, 0.5, 1.5), k*uniform(rng, 0.5, 1.2)
	jolt, jf, tau := k*uniform(rng, 1, 4), uniform(rng, 8, 15), uniform(rng, 0.02, 0.05)
	bob, ph := deg(uniform(rng, 0.5, 2.5)), uniform(rng, 0, 2*math.Pi)
	steps := []float64{}
	amp := []float64{}
	for t := 0.0; t < seconds+1; t += (1 + 0.03*rng.NormFloat64()) / f {
		steps = append(steps, t)
		amp = append(amp, 1+0.15*rng.NormFloat64())
	}
	look := still(rng, seconds, true)
	return motion{
		pitch: func(t float64) float64 {
			return look.pitch(t) + bob*math.Sin(2*math.Pi*f*t+ph)
		},
		accel: func(t float64) [3]float64 {
			w := 2 * math.Pi * f * t
			a := [3]float64{al * math.Sin(w/2+ph), av * (math.Sin(w) + h2*math.Sin(2*w+ph)), af * math.Sin(w+1)}
			i := int(t * f)
			for k := max(0, i-2); k < min(len(steps), i+2); k++ {
				if dt := t - steps[k]; dt >= 0 && dt < 8*tau {
					j := amp[k] * jolt * math.Exp(-dt/tau) * math.Sin(2*math.Pi*jf*dt)
					a[1] += j
					a[2] += 0.5 * j
				}
			}
			return a
		},
	}
}

// Synthetic traces, each from its own seed.
func stillTrace(seed int64, seconds float64, looks bool) trace {
	rng := rand.New(rand.NewSource(seed))
	s := randomSensor(rng)
	return trace{name: "still", samples: s.record(still(rng, seconds, looks), seconds, rng)}
}

func walkingTrace(seed int64, seconds float64) trace {
	rng := rand.New(rand.NewSource(seed))
	s := randomSensor(rng)
	return trace{name: "walking", samples: s.record(walking(rng, seconds), seconds, rng)}
}

func nodTrace(seed int64, kind, count int) trace {
	rng := rand.New(rand.NewSource(seed))
	s := randomSensor(rng)
	m, spans, seconds := nods(rng, kind, count)
	tr := trace{name: map[int]string{1: "nods", 2: "double nods"}[kind], samples: s.record(m, seconds, rng)}
	for _, sp := range spans {
		tr.truth = append(tr.truth, truth{kind: kind, start: int(sp[0] / samplePeriod), end: int(sp[1] / samplePeriod)})
	}
	return tr
}
