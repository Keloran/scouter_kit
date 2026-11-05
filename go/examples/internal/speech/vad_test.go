package speech

import (
	"testing"
)

// segments runs a Segmenter over the session, its level from the denoised
// signal when denoise is set, and returns the utterances as sample ranges.
func segments(s session, seg *Segmenter, denoise bool) [][2]int64 {
	var d *Denoiser
	if denoise {
		d = NewDenoiser()
	}
	pcm := s.pcm
	if d != nil {
		pcm = d.Process(pcm)
	}
	var out [][2]int64
	for k := 0; (k+1)*FrameSamples <= len(pcm); k++ {
		if seg.Frame(pcm[k*FrameSamples:(k+1)*FrameSamples], nil) == Ended {
			out = append(out, [2]int64{seg.Start(), seg.Start() + int64(len(seg.Audio()))})
		}
	}
	return out
}

// score counts sentences that no utterance holds whole, and utterances
// holding more than one sentence.
func score(s session, utts [][2]int64) (missed, merged int) {
	starts := make([]int, len(s.ends))
	for i := range s.ends {
		if i > 0 {
			starts[i] = s.ends[i-1] + 12000 + 4000*((i-1)%3)
		} else {
			starts[i] = 16000
		}
	}
	for i := range s.ends {
		whole := false
		for _, u := range utts {
			whole = whole || u[0] <= int64(starts[i])+800 && u[1] >= int64(s.ends[i])-1600
		}
		if !whole {
			missed++
		}
	}
	for _, u := range utts {
		n := 0
		for i := range s.ends {
			if u[0] < int64(s.ends[i])-1600 && u[1] > int64(starts[i])+1600 {
				n++
			}
		}
		if n > 1 {
			merged++
		}
	}
	return missed, merged
}

// The segmenter alone: every sentence in one utterance of its own, clean and
// in pink noise, with the level from the raw and from the denoised signal.
func TestSegmenter(t *testing.T) {
	clean := newSession(t, sentences, "en-us", 160)
	for _, c := range []struct {
		name string
		s    session
	}{{"clean", clean}, {"10 dB", clean.noisy(10, 1)}, {"5 dB", clean.noisy(5, 2)}, {"0 dB", clean.noisy(0, 3)}} {
		for _, denoise := range []bool{false, true} {
			utts := segments(c.s, NewSegmenter(), denoise)
			missed, merged := score(c.s, utts)
			t.Logf("%-6s denoised level %-5v: %2d utterances for %d sentences, %d not whole, %d merged", c.name, denoise,
				len(utts), len(c.s.ends), missed, merged)
			if c.name == "clean" && (missed > 0 || merged > 0) {
				t.Errorf("clean: %d sentences not whole, %d utterances merged", missed, merged)
			}
		}
	}
}
