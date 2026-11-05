package speech

import (
	"sync"
	"sync/atomic"
	"time"

	"scouterkit"
)

// Update is the recogniser's text for an utterance: partial while it goes
// on (the utterance so far), final at its end.
type Update struct {
	Utterance  int
	Text       string
	Final      bool
	Start, End int64         // the samples it covers, on the sample clock
	Arrived    int64         // NowUS when the last of them arrived
	Spent      time.Duration // the recogniser's time
}

// Stats are a stream's counters.
type Stats struct {
	Frames, Concealed                     int
	Utterances, Dropped, Partials, Finals int
	Unvoiced                              int           // utterances the voice activity model found no speech in
	Speech, Decoding                      time.Duration // audio in utterances; the recogniser's time
	Level, Floor                          float64       // the last frame's RMS and the noise floor (full scale 1)
	Quiet                                 time.Duration // since speech was last heard
}

// Stream runs a Segmenter and a Recognizer over the frames. With a
// Denoiser the segmenter takes its levels from the denoised audio, and so
// does the recogniser its audio with DenoiseSpeech. A recogniser that is
// Voiced decodes an utterance only once it has speech in it; a final one
// without has an empty text.
type Stream struct {
	Rec           Recognizer
	Seg           *Segmenter
	Denoiser      *Denoiser // nil: none
	DenoiseSpeech bool
	Step          int // new speech, in samples, between partial results (0: finals only)
	Prompt        func(utterance int) string

	mu    sync.Mutex
	stats Stats
}

// Stats returns the counters so far.
func (s *Stream) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}

type job struct {
	Update
	pcm []float32
}

// Run reads frames until their channel closes, decodes what is left and
// closes updates. Prompt, if set, gives the text said before the utterance,
// at each decode (on the recogniser's goroutine).
func (s *Stream) Run(frames <-chan scouterkit.MicFrame, updates chan<- Update) error {
	defer close(updates)
	jobs := make(chan job)
	var failed error
	var broken atomic.Bool
	var inflight atomic.Int32 // jobs sent minus jobs done
	done := make(chan struct{})
	voiced, _ := s.Rec.(Voiced)
	go func() {
		defer close(done)
		confirmed := 0
		for j := range jobs {
			if failed != nil {
				inflight.Add(-1)
				continue
			}
			if voiced != nil && j.Utterance != confirmed {
				if !voiced.Voiced(j.pcm) {
					if j.Final {
						s.mu.Lock()
						s.stats.Unvoiced++
						s.mu.Unlock()
						updates <- j.Update
					}
					inflight.Add(-1)
					continue
				}
				confirmed = j.Utterance
			}
			prompt := ""
			if s.Prompt != nil {
				prompt = s.Prompt(j.Utterance)
			}
			t0 := time.Now()
			text, err := s.Rec.Transcribe(j.pcm, prompt)
			if err != nil {
				failed = err
				broken.Store(true)
				inflight.Add(-1)
				continue
			}
			j.Text, j.Spent = text, time.Since(t0)
			s.mu.Lock()
			s.stats.Decoding += j.Spent
			if j.Final {
				s.stats.Finals++
			} else {
				s.stats.Partials++
			}
			s.mu.Unlock()
			updates <- j.Update
			inflight.Add(-1)
		}
	}()
	var queue []job
	var pcm, clean []float32
	var host int64
	utt, partial := 0, 0
	cut := func(final bool) job {
		a := s.Seg.Audio()
		return job{Update{Utterance: utt, Final: final, Start: s.Seg.Start(), End: s.Seg.Start() + int64(len(a)),
			Arrived: host}, append([]float32(nil), a...)}
	}
	for (frames != nil || len(queue) > 0) && !broken.Load() {
		var send chan job
		var head job
		if len(queue) > 0 {
			send, head = jobs, queue[0]
		}
		select {
		case send <- head:
			inflight.Add(1)
			queue = queue[1:]
			continue
		case f, ok := <-frames:
			if !ok {
				frames = nil
				if s.Seg.Speaking() {
					queue = append(queue, cut(true))
					s.mu.Lock()
					s.stats.Speech += time.Duration(len(s.Seg.Audio())) * time.Second / scouterkit.MicSampleRate
					s.mu.Unlock()
					s.Seg.Reset()
				}
				continue
			}
			host = f.HostUS
			x := make([]float32, len(f.PCM))
			for i, v := range f.PCM {
				x[i] = float32(v) / 32768
			}
			pcm = append(pcm, x...)
			if s.Denoiser != nil {
				clean = append(clean, s.Denoiser.Process(x)...)
			}
			s.mu.Lock()
			s.stats.Frames++
			if f.Concealed {
				s.stats.Concealed++
			}
			s.mu.Unlock()
		}
		for len(pcm) >= FrameSamples && (s.Denoiser == nil || len(clean) >= FrameSamples) {
			x, audio := pcm[:FrameSamples], []float32(nil)
			if s.Denoiser != nil {
				x, audio = clean[:FrameSamples], x
				if s.DenoiseSpeech {
					audio = nil
				}
				clean = clean[FrameSamples:]
			}
			ev := s.Seg.Frame(x, audio)
			pcm = pcm[FrameSamples:]
			s.mu.Lock()
			s.stats.Level, s.stats.Floor = s.Seg.Level(), s.Seg.Floor()
			if s.Seg.Speaking() {
				s.stats.Quiet = 0
			} else {
				s.stats.Quiet += 10 * time.Millisecond
			}
			switch ev {
			case Started:
				utt++
				partial = 0
				s.stats.Utterances++
			case Dropped:
				s.stats.Dropped++
			case Ended:
				s.stats.Speech += time.Duration(len(s.Seg.Audio())) * time.Second / scouterkit.MicSampleRate
			}
			s.mu.Unlock()
			switch {
			case ev == Ended:
				queue = append(queue, cut(true))
			case ev == Speaking && s.Step > 0 && len(queue) == 0 && inflight.Load() <= 0 &&
				len(s.Seg.Audio())-partial >= s.Step:
				inflight.Add(1)
				jobs <- cut(false)
				partial = len(s.Seg.Audio())
			}
		}
	}
	close(jobs)
	<-done
	return failed
}
