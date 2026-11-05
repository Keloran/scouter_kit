package speech

import (
	"math"
	"math/bits"
)

const (
	frameN = 256 // 16 ms at 16 kHz
	hop    = frameN / 2
	bins   = frameN/2 + 1
	subN   = 8  // minimum statistics: 8 sub-windows ...
	subLen = 24 // ... of 24 frames, about 1.5 s
)

// Denoiser is a spectral Wiener filter: 16 ms sqrt-Hann frames with 50 %
// overlap, the noise spectrum from the minimum of the smoothed spectrum over
// about 1.5 s, the decision-directed a priori SNR and a gain floor against
// musical noise.
type Denoiser struct {
	Floor float64 // the lowest gain (0.5: -6 dB)
	Bias  float64 // noise = Bias x the minimum

	window, re, im  []float64
	in, acc         []float64 // the last frameN input samples; the overlap-add sum
	fill            int       // input samples since the last frame
	smooth, prevS   []float64 // the smoothed spectrum; the last frame's clean power
	subMin, minimum []float64
	subs            [][]float64
	frames          int
	fft             *fft
}

func NewDenoiser() *Denoiser {
	d := &Denoiser{Floor: 0.5, Bias: 1, window: make([]float64, frameN), re: make([]float64, frameN),
		im: make([]float64, frameN), in: make([]float64, frameN), acc: make([]float64, frameN),
		smooth: make([]float64, bins), prevS: make([]float64, bins), subMin: make([]float64, bins),
		minimum: make([]float64, bins), fft: newFFT(frameN)}
	for i := range d.window {
		d.window[i] = math.Sqrt(0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/frameN))
	}
	return d
}

// Process filters x and returns as many samples, 8 ms later.
func (d *Denoiser) Process(x []float32) []float32 {
	out := make([]float32, 0, len(x))
	for _, v := range x {
		d.in[frameN-hop+d.fill] = float64(v)
		if d.fill++; d.fill == hop {
			d.frame()
			for _, s := range d.acc[:hop] {
				out = append(out, float32(s))
			}
			copy(d.acc, d.acc[hop:])
			clear(d.acc[frameN-hop:])
			copy(d.in, d.in[hop:])
			d.fill = 0
		}
	}
	return out
}

func (d *Denoiser) frame() {
	for i := range d.re {
		d.re[i], d.im[i] = d.in[i]*d.window[i], 0
	}
	d.fft.transform(d.re, d.im, false)
	first := d.frames == 0
	d.frames++
	for b := 0; b < bins; b++ {
		p := d.re[b]*d.re[b] + d.im[b]*d.im[b]
		if first {
			d.smooth[b], d.subMin[b] = p, p
		}
		d.smooth[b] = 0.85*d.smooth[b] + 0.15*p
		d.subMin[b] = min(d.subMin[b], d.smooth[b])
		m := d.subMin[b]
		for _, s := range d.subs {
			m = min(m, s[b])
		}
		noise := max(d.Bias*m, 1e-12)
		gamma := p / noise
		xi := 0.98*d.prevS[b]/noise + 0.02*max(gamma-1, 0)
		if first {
			xi = max(gamma-1, 0)
		}
		g := max(d.Floor, xi/(1+xi))
		d.prevS[b] = g * g * p
		d.re[b] *= g
		d.im[b] *= g
		if b > 0 && b < frameN/2 {
			d.re[frameN-b] *= g
			d.im[frameN-b] *= g
		}
	}
	if d.frames%subLen == 0 {
		s := append([]float64(nil), d.subMin...)
		if d.subs = append(d.subs, s); len(d.subs) > subN-1 {
			d.subs = d.subs[1:]
		}
		copy(d.subMin, d.smooth)
	}
	d.fft.transform(d.re, d.im, true)
	for i := range d.acc {
		d.acc[i] += d.re[i] * d.window[i]
	}
}

// fft is an in-place radix-2 complex FFT.
type fft struct {
	n        int
	cos, sin []float64
	rev      []int
}

func newFFT(n int) *fft {
	f := &fft{n: n, cos: make([]float64, n/2), sin: make([]float64, n/2), rev: make([]int, n)}
	for i := range f.cos {
		f.cos[i] = math.Cos(2 * math.Pi * float64(i) / float64(n))
		f.sin[i] = math.Sin(2 * math.Pi * float64(i) / float64(n))
	}
	shift := 64 - bits.TrailingZeros(uint(n))
	for i := range f.rev {
		f.rev[i] = int(bits.Reverse64(uint64(i)) >> shift)
	}
	return f
}

// transform: the inverse is scaled by 1/n.
func (f *fft) transform(re, im []float64, inverse bool) {
	for i, j := range f.rev {
		if i < j {
			re[i], re[j] = re[j], re[i]
			im[i], im[j] = im[j], im[i]
		}
	}
	sign := -1.0
	if inverse {
		sign = 1
	}
	for size := 2; size <= f.n; size *= 2 {
		half, step := size/2, f.n/size
		for start := 0; start < f.n; start += size {
			for k := 0; k < half; k++ {
				c, s := f.cos[k*step], sign*f.sin[k*step]
				a, b := start+k, start+k+half
				tr := re[b]*c - im[b]*s
				ti := re[b]*s + im[b]*c
				re[b], im[b] = re[a]-tr, im[a]-ti
				re[a], im[a] = re[a]+tr, im[a]+ti
			}
		}
	}
	if inverse {
		for i := range re {
			re[i] /= float64(f.n)
			im[i] /= float64(f.n)
		}
	}
}
