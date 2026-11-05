package main

import (
	"fmt"
	"testing"
)

// score matches detections to the trace's gestures: a detection of the
// gesture's kind from its start to the double-nod window plus 300 ms after
// its end finds it; one of another kind there confuses it; detections
// outside every gesture are false positives.
type score struct {
	gestures, found, confused, falses int
	lateMax                           int // samples after the gesture's end
}

func (s *score) add(tr trace, got []detection, p params) {
	used := make([]bool, len(got))
	for _, g := range tr.truth {
		s.gestures++
		hit := false
		for i, d := range got {
			if used[i] || d.at < g.start || d.at > g.end+int(p.window)+15 {
				continue
			}
			used[i] = true
			if d.kind != g.kind || hit {
				s.confused++
				continue
			}
			hit = true
			s.found++
			s.lateMax = max(s.lateMax, d.at-g.end)
		}
	}
	for _, u := range used {
		if !u {
			s.falses++
		}
	}
}

func (s score) String() string {
	return fmt.Sprintf("%d/%d found, %d confused, %d false, latest %d ms after the end", s.found, s.gestures,
		s.confused, s.falses, 20*s.lateMax)
}

// The acceptance of docs/review-v5.md §9 on synthetic traces (traces.go):
// nods and double nods found at least 9 times in 10 (here 0.95), at most
// one false positive per 10 minutes of walking, none at rest.
func TestDetectorSynthetic(t *testing.T) {
	p := defaultThresholds().params()
	var single, double, walk, rest score
	for seed := int64(1); seed <= 40; seed++ {
		tr := nodTrace(seed, gestureNod, 5)
		single.add(tr, detect(p, tr.samples), p)
		tr = nodTrace(1000+seed, gestureDouble, 5)
		double.add(tr, detect(p, tr.samples), p)
	}
	for seed := int64(1); seed <= 6; seed++ {
		tr := walkingTrace(2000+seed, 600)
		walk.add(tr, detect(p, tr.samples), p)
		tr = stillTrace(3000+seed, 300, true)
		rest.add(tr, detect(p, tr.samples), p)
	}
	t.Logf("nods: %v; double nods: %v; walking 60 min: %d false; rest 30 min: %d false", single, double,
		walk.falses, rest.falses)
	for _, s := range []score{single, double} {
		if s.found*100 < s.gestures*95 || s.confused*100 > s.gestures*3 || s.falses > 2 {
			t.Errorf("%v", s)
		}
	}
	if walk.falses > 6 {
		t.Errorf("walking: %d false positives in 60 minutes", walk.falses)
	}
	if rest.falses > 0 {
		t.Errorf("rest: %d false positives in 30 minutes", rest.falses)
	}
}

// Beyond the ranges tuned on: smaller and bigger nods, slow ones, a noisy
// sensor, the sensor nearer or farther from the pivot, harder and softer
// walking, frequent looks around at rest.
func TestDetectorVariants(t *testing.T) {
	p := defaultThresholds().params()
	type knobs struct {
		up, depth, length, noise, looks [2]float64
		walk                            float64
	}
	saved := knobs{leverUp, nodDepth, nodLength, noiseMG, lookEvery, walkScale}
	defer func() {
		leverUp, nodDepth, nodLength, noiseMG, lookEvery, walkScale = saved.up, saved.depth, saved.length,
			saved.noise, saved.looks, saved.walk
	}()
	for _, v := range []struct {
		name  string
		set   func()
		least int // per cent found
	}{
		{"small nods 6-10°", func() { nodDepth = [2]float64{6, 10} }, 85},
		{"big nods 25-40°", func() { nodDepth = [2]float64{25, 40} }, 95},
		{"slow nods 0.7-1.2 s", func() { nodLength = [2]float64{0.7, 1.2} }, 95},
		{"noise 8-12 mg", func() { noiseMG = [2]float64{8, 12} }, 90},
		{"sensor 0-2 cm above the pivot", func() { leverUp = [2]float64{0, 0.02} }, 95},
		{"sensor 10-15 cm above the pivot", func() { leverUp = [2]float64{0.10, 0.15} }, 95},
		{"walking 1.5 times harder", func() { walkScale = 1.5 }, 95},
		{"walking 0.6 times as hard", func() { walkScale = 0.6 }, 95},
		{"looks every 1-3 s", func() { lookEvery = [2]float64{1, 3} }, 95},
	} {
		leverUp, nodDepth, nodLength, noiseMG, lookEvery, walkScale = saved.up, saved.depth, saved.length,
			saved.noise, saved.looks, saved.walk
		v.set()
		var single, double score
		falses := 0
		for seed := int64(5000); seed < 5020; seed++ {
			tr := nodTrace(seed, gestureNod, 5)
			single.add(tr, detect(p, tr.samples), p)
			tr = nodTrace(15000+seed, gestureDouble, 5)
			double.add(tr, detect(p, tr.samples), p)
		}
		for seed := int64(500); seed < 503; seed++ {
			falses += len(detect(p, walkingTrace(25000+seed, 300).samples))
			falses += len(detect(p, stillTrace(35000+seed, 300, true).samples))
		}
		t.Logf("%s: nods %v; double nods %v; %d false in 15 min walking and 15 min at rest", v.name, single, double, falses)
		for _, s := range []score{single, double} {
			if s.found*100 < s.gestures*v.least || s.falses > 1 {
				t.Errorf("%s: %v", v.name, s)
			}
		}
		if falses > 0 {
			t.Errorf("%s: %d false gestures", v.name, falses)
		}
	}
}
