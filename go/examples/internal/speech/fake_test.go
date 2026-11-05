package speech

import (
	"errors"
	"fmt"
	"math"
	"testing"
)

// fake is a recogniser that says how much audio it got, or fails.
type fake struct {
	calls  int
	failAt int
}

func (f *fake) Transcribe(pcm []float32, prompt string) (string, error) {
	f.calls++
	if f.calls == f.failAt {
		return "", errors.New("fake failure")
	}
	return fmt.Sprint(len(pcm) / 160), nil
}

func (f *fake) Close() {}

// buzz is n bursts of a 150 Hz buzz of d seconds, after and between 1.5 s
// of quiet.
func buzz(n int, d float64) session {
	s := session{pcm: make([]float32, 24000)}
	for k := 0; k < n; k++ {
		for i := 0; i < int(d*16000); i++ {
			v := 0.0
			for h := 1; h <= 6; h++ {
				v += 0.05 / float64(h) * math.Sin(2*math.Pi*150*float64(h)*float64(i)/16000)
			}
			s.pcm = append(s.pcm, float32(v))
		}
		s.ends = append(s.ends, len(s.pcm))
		s.pcm = append(s.pcm, make([]float32, 24000)...)
	}
	return s
}

// Three bursts: three utterances, partial results while each goes on, the
// final one covering it whole.
func TestStreamUtterances(t *testing.T) {
	for _, denoise := range []bool{false, true} {
		s := buzz(3, 2)
		st := &Stream{Rec: &fake{}, Seg: NewSegmenter(), Step: 8000}
		if denoise {
			st.Denoiser, st.DenoiseSpeech = NewDenoiser(), true
		}
		got, _, _ := run(t, st, s, false)
		var finals, partials int
		for _, u := range got {
			if u.Final {
				finals++
				if frames := u.End - u.Start; frames < 2*16000 || frames > 3*16000 {
					t.Errorf("utterance %d: %d samples for a 2 s burst", u.Utterance, frames)
				}
			} else {
				partials++
			}
		}
		if finals != 3 || partials == 0 {
			t.Fatalf("denoise %v: %d finals, %d partials", denoise, finals, partials)
		}
	}
}

// A failing recogniser ends the stream with its error.
func TestStreamFails(t *testing.T) {
	st := &Stream{Rec: &fake{failAt: 2}, Seg: NewSegmenter(), Step: 8000}
	updates := make(chan Update, 64)
	err := st.Run(Play(buzz(3, 2).pcm, false, nil), updates)
	if err == nil || err.Error() != "fake failure" {
		t.Fatalf("Run: %v", err)
	}
}

type unvoiced struct{ fake }

func (u *unvoiced) Voiced([]float32) bool { return false }

// A voice activity model that finds no speech: no decoding, an empty final
// text for each utterance.
func TestStreamUnvoiced(t *testing.T) {
	r := &unvoiced{}
	st := &Stream{Rec: r, Seg: NewSegmenter(), Step: 8000}
	got, _, _ := run(t, st, buzz(3, 2), false)
	if len(got) != 3 || r.calls != 0 || st.Stats().Unvoiced != 3 {
		t.Fatalf("%d updates, %d decodes, %d unvoiced", len(got), r.calls, st.Stats().Unvoiced)
	}
	for _, u := range got {
		if !u.Final || u.Text != "" {
			t.Fatalf("update %+v", u)
		}
	}
}
