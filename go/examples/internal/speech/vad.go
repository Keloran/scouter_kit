package speech

import "math"

// FrameSamples is the segmenter's frame: 10 ms.
const FrameSamples = 160

// Event is what a frame did to the segmenter.
type Event int

const (
	Silence  Event = iota
	Started        // an utterance began (with the pre-roll before it)
	Speaking       // it goes on
	Ended          // it ended: Audio is the whole utterance
	Dropped        // it ended too short to be speech
)

// Segmenter finds utterances by their level against a noise floor: the
// minimum of the smoothed frame level over the last 5 s. A frame's level
// starts one, a slower average (about 100 ms) keeps it going. It counts time
// in samples, not by the clock, since the glasses' audio comes in bursts.
type Segmenter struct {
	Onset, Release float64 // speech starts above Onset x the floor and goes on above Release x the floor
	MinLevel       float64 // RMS (full scale 1) that speech must also exceed to start
	PreRoll        int     // frames before the onset that the utterance keeps
	Hangover       int     // frames below release that end it
	MinSpeech      int     // shorter utterances are dropped
	MaxSpeech      int     // longer ones are cut at their quietest frame in the last 2 s

	level, slow    float64
	smooth, subMin float64
	mins           []float64
	frames         int
	speech, ended  bool
	quiet          int
	audio          []float32
	levels         []float64
	start, n       int64
	next           []float32 // the rest of an utterance that was cut
	nextLevels     []float64
	band           []*biquad
	y              []float32
}

func NewSegmenter() *Segmenter {
	return &Segmenter{Onset: 3, Release: 1.8, MinLevel: math.Pow(10, -45.0/20), PreRoll: 40, Hangover: 80,
		MinSpeech: 35, MaxSpeech: 1500}
}

// Floor is the noise floor, RMS.
func (s *Segmenter) Floor() float64 {
	m := s.subMin
	for _, v := range s.mins {
		m = min(m, v)
	}
	return m
}

// Level is the last frame's RMS.
func (s *Segmenter) Level() float64 { return s.level }

// Speaking reports whether an utterance is going on.
func (s *Segmenter) Speaking() bool { return s.speech }

// Audio is the utterance so far, Start its first sample.
func (s *Segmenter) Audio() []float32 { return s.audio }
func (s *Segmenter) Start() int64     { return s.start }

func rms(x []float32) float64 {
	var sum float64
	for _, v := range x {
		sum += float64(v) * float64(v)
	}
	return math.Sqrt(sum / float64(max(1, len(x))))
}

// Frame takes the next FrameSamples samples. Its level is that of 150 Hz to
// 4 kHz. The utterance keeps audio instead of x if it is not nil (such as x
// denoised).
func (s *Segmenter) Frame(x, audio []float32) Event {
	if s.band == nil {
		s.band = []*biquad{newBiquad(true, 150), newBiquad(true, 150), newBiquad(false, 4000)}
		s.y = make([]float32, FrameSamples)
	}
	for i, v := range x {
		f := float64(v)
		for _, b := range s.band {
			f = b.step(f)
		}
		s.y[i] = float32(f)
	}
	level := rms(s.y)
	if audio != nil {
		x = audio
	}
	s.level = level
	if s.frames == 0 {
		s.smooth, s.subMin, s.slow = level, level, level
	}
	s.smooth += 0.3 * (level - s.smooth)
	s.slow += 0.1 * (level - s.slow)
	s.subMin = min(s.subMin, s.smooth)
	if s.frames++; s.frames%25 == 0 {
		if s.mins = append(s.mins, s.subMin); len(s.mins) > 19 {
			s.mins = s.mins[1:]
		}
		s.subMin = s.smooth
	}
	floor := s.Floor()
	s.n += int64(len(x))
	if s.ended {
		s.audio, s.levels, s.ended = s.audio[:0], s.levels[:0], false
	}
	if s.next != nil {
		s.audio, s.levels = append(s.next, x...), append(s.nextLevels, level)
		s.next, s.nextLevels = nil, nil
		s.start = s.n - int64(len(s.audio))
		s.speech, s.quiet = true, 0
		return Started
	}
	if !s.speech {
		s.audio = append(s.audio, x...)
		s.levels = append(s.levels, level)
		if len(s.levels) > s.PreRoll+1 {
			s.audio = s.audio[len(x):]
			s.levels = s.levels[1:]
		}
		if level > max(s.MinLevel, s.Onset*floor) {
			s.speech, s.quiet = true, 0
			s.start = s.n - int64(len(s.audio))
			return Started
		}
		return Silence
	}
	s.audio = append(s.audio, x...)
	s.levels = append(s.levels, level)
	if s.slow > max(s.MinLevel*s.Release/s.Onset, s.Release*floor) {
		s.quiet = 0
	} else if s.quiet++; s.quiet >= s.Hangover {
		s.speech = false
		loud := len(s.levels) - s.quiet
		if loud < s.MinSpeech {
			s.audio, s.levels = s.audio[:0], s.levels[:0]
			return Dropped
		}
		keep := min(len(s.levels), loud+30)
		s.audio, s.levels, s.ended = s.audio[:keep*FrameSamples], s.levels[:keep], true
		return Ended
	}
	if len(s.levels) >= s.MaxSpeech {
		cut := len(s.levels) - 1
		for i := max(0, len(s.levels)-200); i < len(s.levels); i++ {
			if s.levels[i] < s.levels[cut] {
				cut = i
			}
		}
		cut++
		s.next = append([]float32{}, s.audio[cut*FrameSamples:]...)
		s.nextLevels = append([]float64{}, s.levels[cut:]...)
		s.audio, s.levels, s.ended = s.audio[:cut*FrameSamples], s.levels[:cut], true
		s.speech = false
		return Ended
	}
	return Speaking
}

// Reset forgets the utterance under way (the floor stays).
func (s *Segmenter) Reset() {
	s.speech, s.ended, s.quiet, s.next, s.nextLevels = false, false, 0, nil, nil
	s.audio, s.levels = s.audio[:0], s.levels[:0]
}

// biquad is a second-order Butterworth high-pass or low-pass filter.
type biquad struct{ b0, b1, b2, a1, a2, x1, x2, y1, y2 float64 }

func newBiquad(high bool, hz float64) *biquad {
	w := 2 * math.Pi * hz / 16000
	alpha, c := math.Sin(w)/math.Sqrt2, math.Cos(w)
	b0, b1 := (1-c)/2, 1-c
	if high {
		b0, b1 = (1+c)/2, -(1 + c)
	}
	a0 := 1 + alpha
	return &biquad{b0: b0 / a0, b1: b1 / a0, b2: b0 / a0, a1: -2 * c / a0, a2: (1 - alpha) / a0}
}

func (f *biquad) step(x float64) float64 {
	y := f.b0*x + f.b1*f.x1 + f.b2*f.x2 - f.a1*f.y1 - f.a2*f.y2
	f.x2, f.x1, f.y2, f.y1 = f.x1, x, f.y1, y
	return y
}
