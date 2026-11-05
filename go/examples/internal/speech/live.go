package speech

import (
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"

	"scouterkit"
)

// Text is what a recogniser has for the segment under way: Stable only
// grows and is shown as final, Tail may still change.
type Text struct {
	Segment      int
	Stable, Tail string
	Final        bool  // the segment ended: Stable is all of it
	Arrived      int64 // NowUS of the newest audio in it
}

// norm is a word as compared: lower case, without punctuation around it.
func norm(w string) string {
	return strings.ToLower(strings.TrimFunc(w, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }))
}

// Agree turns a recogniser's texts of an utterance, each of the whole
// utterance so far, into Text by local agreement: a word becomes stable once
// two texts in a row have it at the same place, and the final text adds what
// comes after the stable words.
type Agree struct {
	utt          int
	prev, stable []string
}

func (a *Agree) Add(u Update) Text {
	if u.Utterance != a.utt {
		a.utt, a.prev, a.stable = u.Utterance, nil, nil
	}
	words := strings.Fields(u.Text)
	if u.Final {
		if len(words) > len(a.stable) {
			a.stable = append(a.stable, words[len(a.stable):]...)
		}
		return Text{Segment: u.Utterance, Stable: strings.Join(a.stable, " "), Final: true, Arrived: u.Arrived}
	}
	n := 0
	for n < len(words) && n < len(a.prev) && norm(words[n]) == norm(a.prev[n]) {
		n++
	}
	if n > len(a.stable) {
		a.stable = append(a.stable, words[len(a.stable):n]...)
	}
	a.prev = words
	t := Text{Segment: u.Utterance, Stable: strings.Join(a.stable, " "), Arrived: u.Arrived}
	if len(words) > len(a.stable) {
		t.Tail = strings.Join(words[len(a.stable):], " ")
	}
	return t
}

// LiveStats are a live stream's counters.
type LiveStats struct {
	Frames, Segments int
	Audio, Decoding  time.Duration
	Level, Floor     float64
	Speaking         bool
	Quiet            time.Duration // since speech was last heard
}

// LiveStream runs a streaming recogniser over the frames on a goroutine of
// its own, so that a slow decode only delays the text and never the
// frames. Its Segmenter only tells speech from quiet (Stats.Speaking,
// Stats.Quiet); the recogniser hears everything. A segment's last word is
// held back as Tail until the next word starts, since its tokens may still
// be growing.
type LiveStream struct {
	Rec      Live
	Seg      *Segmenter
	Denoiser *Denoiser

	mu    sync.Mutex
	stats LiveStats
}

func (s *LiveStream) Stats() LiveStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}

// Run reads frames until their channel closes, then gives the last text
// and closes out.
func (s *LiveStream) Run(frames <-chan scouterkit.MicFrame, out chan<- Text) error {
	defer close(out)
	var mu sync.Mutex
	var pending []float32
	var newest int64
	closed := false
	wake := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		seg, last := 1, Text{Segment: 1}
		emit := func(t Text) {
			if t != last {
				last = t
				out <- t
			}
		}
		for {
			<-wake
			mu.Lock()
			pcm, host, end := pending, newest, closed
			pending = nil
			mu.Unlock()
			t0 := time.Now()
			text, endpoint := s.Rec.Accept(pcm)
			if end {
				if f := s.Rec.Finish(); f != "" {
					text = f
				}
				endpoint = true
			}
			s.mu.Lock()
			s.stats.Decoding += time.Since(t0)
			s.mu.Unlock()
			t := Text{Segment: seg, Stable: text, Arrived: host}
			if i := strings.LastIndexByte(text, ' '); !endpoint {
				t.Stable, t.Tail = "", text
				if i >= 0 {
					t.Stable, t.Tail = text[:i], text[i+1:]
				}
			}
			if t.Stable != last.Stable || t.Tail != last.Tail {
				emit(t)
			}
			if endpoint {
				if text != "" {
					t.Final = true
					emit(t)
					seg++
					s.mu.Lock()
					s.stats.Segments++
					s.mu.Unlock()
				}
				s.Rec.Reset()
				last = Text{Segment: seg}
			}
			if end {
				return
			}
		}
	}()
	push := func(x []float32, host int64, end bool) {
		mu.Lock()
		pending = append(pending, x...)
		newest, closed = host, end
		mu.Unlock()
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	var buf []float32
	for f := range frames {
		x := make([]float32, len(f.PCM))
		for i, v := range f.PCM {
			x[i] = float32(v) / 32768
		}
		if s.Denoiser != nil {
			x = s.Denoiser.Process(x)
		}
		buf = append(buf, x...)
		for len(buf) >= FrameSamples && s.Seg != nil {
			ev := s.Seg.Frame(buf[:FrameSamples], nil)
			buf = buf[FrameSamples:]
			s.mu.Lock()
			s.stats.Level, s.stats.Floor = s.Seg.Level(), s.Seg.Floor()
			s.stats.Speaking = ev == Started || ev == Speaking
			if s.stats.Speaking {
				s.stats.Quiet = 0
			} else {
				s.stats.Quiet += 10 * time.Millisecond
			}
			s.mu.Unlock()
			if ev == Ended || ev == Dropped {
				s.Seg.Reset()
			}
		}
		if s.Seg == nil {
			buf = buf[:0]
		}
		s.mu.Lock()
		s.stats.Frames++
		s.stats.Audio += 10 * time.Millisecond
		s.mu.Unlock()
		push(x, f.HostUS, false)
	}
	push(nil, scouterkit.NowUS(), true)
	<-done
	return nil
}

// Recognition is speech to Text, live or by local agreement.
type Recognition interface {
	Run(frames <-chan scouterkit.MicFrame, out chan<- Text) error
	Quiet() time.Duration // since speech was last heard
	Summary() string
}

func (s *LiveStream) Quiet() time.Duration { return s.Stats().Quiet }

func (s *LiveStream) Summary() string {
	st := s.Stats()
	return fmt.Sprintf("%d frames (%.1f s), %d segments; the recogniser took %.0f ms per second of audio", st.Frames,
		st.Audio.Seconds(), st.Segments, 1000*st.Decoding.Seconds()/max(1e-9, st.Audio.Seconds()))
}

// AgreeStream is a Stream whose texts become Text by local agreement.
type AgreeStream struct{ *Stream }

func (a AgreeStream) Run(frames <-chan scouterkit.MicFrame, out chan<- Text) error {
	defer close(out)
	updates := make(chan Update, 16)
	errc := make(chan error, 1)
	go func() { errc <- a.Stream.Run(frames, updates) }()
	var ag Agree
	for u := range updates {
		out <- ag.Add(u)
	}
	return <-errc
}

func (a AgreeStream) Quiet() time.Duration { return a.Stats().Quiet }

func (a AgreeStream) Summary() string {
	st := a.Stats()
	return fmt.Sprintf("%d frames (%.1f s), %d utterances (%d too short, %d without speech), %d partial and %d final texts; "+
		"the recogniser took %.0f ms per second of speech", st.Frames, float64(st.Frames)/100, st.Utterances, st.Dropped,
		st.Unvoiced, st.Partials, st.Finals, 1000*st.Decoding.Seconds()/max(1e-9, st.Speech.Seconds()))
}
