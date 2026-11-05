package speech

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strings"
	"testing"

	"scouterkit"
)

// session is synthetic speech as a microphone would hear it: a second of
// quiet, then the sentences with pauses between them.
type session struct {
	pcm  []float32
	ends []int // the sample after each sentence
	text []string
}

func newSession(t testing.TB, texts []string, voice string, wpm int) session {
	t.Helper()
	s := session{pcm: make([]float32, 16000), text: texts}
	for i, text := range texts {
		s.pcm = append(s.pcm, synth(t, text, voice, wpm)...)
		s.ends = append(s.ends, len(s.pcm))
		s.pcm = append(s.pcm, make([]float32, 12000+4000*(i%3))...)
	}
	return s
}

// noisy adds pink noise snr dB below the speech's RMS, and a little white
// noise everywhere (a microphone is never silent).
func (s session) noisy(snr float64, seed int64) session {
	var sum float64
	for _, v := range s.pcm {
		sum += float64(v) * float64(v)
	}
	speech := math.Sqrt(sum / float64(len(s.pcm)-16000*len(s.ends)/2))
	rng := rand.New(rand.NewSource(seed))
	noise := make([]float64, len(s.pcm))
	var b0, b1, b2, nsum float64
	for i := range noise {
		w := rng.NormFloat64()
		b0 = 0.99765*b0 + w*0.0990460
		b1 = 0.96300*b1 + w*0.2965164
		b2 = 0.57000*b2 + w*1.0526913
		noise[i] = b0 + b1 + b2 + w*0.1848
		nsum += noise[i] * noise[i]
	}
	scale := speech / math.Pow(10, snr/20) / math.Sqrt(nsum/float64(len(noise)))
	out := s
	out.pcm = make([]float32, len(s.pcm))
	for i, v := range s.pcm {
		out.pcm[i] = float32(float64(v) + scale*noise[i])
	}
	return out
}

func (s session) codec(t testing.TB, kbps int) session {
	t.Helper()
	pcm, err := Codec(s.pcm, kbps)
	if err != nil {
		t.Fatal(err)
	}
	out := s
	out.pcm = pcm
	return out
}

func (s session) words() []string { return Words(strings.Join(s.text, " ")) }

// run streams the session through a Stream and returns the final texts and
// the updates' arrival times (NowUS).
func run(t testing.TB, st *Stream, s session, pace bool) ([]Update, []int64, int64) {
	t.Helper()
	updates := make(chan Update, 16)
	t0 := scouterkit.NowUS()
	errc := make(chan error, 1)
	go func() { errc <- st.Run(Play(s.pcm, pace, nil), updates) }()
	var got []Update
	var at []int64
	for u := range updates {
		got = append(got, u)
		at = append(at, scouterkit.NowUS())
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	return got, at, t0
}

func finals(us []Update) string {
	var b []string
	for _, u := range us {
		if u.Final {
			b = append(b, u.Text)
		}
	}
	return strings.Join(b, " ")
}

// Finals only, as fast as the recogniser goes: word errors of the whole
// session clean, through LC3, and in pink noise with and without the
// denoiser (go test -tags whisper ./examples/internal/speech -args -model FILE).
func TestStreamConditions(t *testing.T) {
	r := openModel(t, "en")
	clean := newSession(t, sentences, "en-us", 160)
	for _, c := range []struct {
		name    string
		s       session
		denoise bool
	}{
		{"clean", clean, false},
		{"clean, denoiser", clean, true},
		{"LC3 32 kbps", clean.codec(t, 32), false},
		{"LC3 16 kbps", clean.codec(t, 16), false},
		{"pink noise 10 dB", clean.noisy(10, 1), false},
		{"pink noise 10 dB, denoiser", clean.noisy(10, 1), true},
		{"pink noise 5 dB", clean.noisy(5, 2), false},
		{"pink noise 5 dB, denoiser", clean.noisy(5, 2), true},
		{"pink noise 0 dB", clean.noisy(0, 3), false},
		{"pink noise 0 dB, denoiser", clean.noisy(0, 3), true},
	} {
		st := &Stream{Rec: r, Seg: NewSegmenter()}
		if c.denoise {
			st.Denoiser = NewDenoiser()
		}
		got, _, _ := run(t, st, c.s, false)
		e, n := WER(c.s.words(), Words(finals(got)))
		stats := st.Stats()
		t.Logf("%-28s WER %5.1f %% (%d/%d), %d utterances (%d sentences), %d dropped", c.name+":", 100*float64(e)/float64(n),
			e, n, stats.Utterances, len(sentences), stats.Dropped)
		if c.name == "clean" && !strings.Contains(*modelFlag, "tiny") && !strings.Contains(*modelFlag, "base") &&
			float64(e) > 0.05*float64(n) {
			t.Errorf("clean WER %.1f %% above 5 %%", 100*float64(e)/float64(n))
		}
	}
}

// In real time with partial results every half second: how long after each
// sentence's end its last word first shows, and its final text.
func TestStreamLatency(t *testing.T) {
	r := openModel(t, "en")
	s := newSession(t, sentences[:6], "en-us", 160)
	st := &Stream{Rec: r, Seg: NewSegmenter(), Step: 8000}
	got, at, t0 := run(t, st, s, true)
	var first, final []float64
	for i, end := range s.ends {
		endUS := t0 + int64(end)*1000000/16000
		last := s.text[i]
		lw := Words(last)
		word := lw[len(lw)-1]
		seenFirst, seenFinal := false, false
		for k, u := range got {
			if u.End < int64(end)-8000 || u.Start > int64(end) {
				continue
			}
			ws := Words(u.Text)
			has := false
			for _, w := range ws {
				has = has || w == word
			}
			if has && !seenFirst {
				first = append(first, float64(at[k]-endUS)/1000)
				seenFirst = true
			}
			if u.Final && !seenFinal {
				final = append(final, float64(at[k]-endUS)/1000)
				seenFinal = true
			}
		}
	}
	stats := st.Stats()
	t.Logf("%d sentences: last word first shown %s ms after its end, final %s ms; %d partials, %d finals, decoding %.0f ms per s of speech",
		len(s.ends), spread(first), spread(final), stats.Partials, stats.Finals,
		1000*stats.Decoding.Seconds()/max(1e-9, stats.Speech.Seconds()))
	if len(final) != len(s.ends) {
		t.Errorf("%d of %d sentences got a final text", len(final), len(s.ends))
	}
}

// spread is "median (min…max)".
func spread(v []float64) string {
	if len(v) == 0 {
		return "-"
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	return fmt.Sprintf("%.0f (%.0f…%.0f)", s[len(s)/2], s[0], s[len(s)-1])
}

// Sounds that are not speech but loud enough for the segmenter (a buzz,
// bursts of noise): the recogniser should make no words of them.
func TestStreamNotSpeech(t *testing.T) {
	r := openModel(t, "en")
	s := buzz(2, 2)
	rng := rand.New(rand.NewSource(4))
	for k := 0; k < 2; k++ {
		for i := 0; i < 32000; i++ {
			s.pcm = append(s.pcm, float32(0.1*rng.NormFloat64()))
		}
		s.pcm = append(s.pcm, make([]float32, 24000)...)
	}
	st := &Stream{Rec: r, Seg: NewSegmenter(), Denoiser: NewDenoiser(), DenoiseSpeech: true}
	got, _, _ := run(t, st, s, false)
	text := finals(got)
	t.Logf("%d utterances, words made up: %q", st.Stats().Utterances, text)
	if n := len(Words(text)); n > 2 {
		t.Errorf("%d words made up of non-speech", n)
	}
}
