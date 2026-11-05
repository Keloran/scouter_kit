package speech

import (
	"flag"
	"testing"
	"time"
)

var liveFlag = flag.String("live", "", "a streaming model directory for sherpa-onnx (empty: skip those tests)")

func openLive(t testing.TB) Live {
	t.Helper()
	if !Streaming || *liveFlag == "" {
		t.Skip("needs -tags sherpa and -args -live DIR")
	}
	l, err := OpenLive(*liveFlag, *threadsFlag)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Close)
	return l
}

// The streaming recogniser on each test sentence alone, fed 10 ms at a
// time after 0.5 s of quiet: word errors and its time per second of speech.
func TestLiveSentences(t *testing.T) {
	l := openLive(t)
	per := map[string][2]int{}
	var errs, words int
	var audio, spent time.Duration
	for _, s := range corpus(t) {
		pcm := append(append(make([]float32, 8000), s.pcm...), make([]float32, 24000)...)
		t0 := time.Now()
		text := ""
		for k := 0; k+160 <= len(pcm); k += 160 {
			text, _ = l.Accept(pcm[k : k+160])
		}
		if fin := l.Finish(); fin != "" {
			text = fin
		}
		l.Reset()
		spent += time.Since(t0)
		audio += time.Duration(len(pcm)) * time.Second / 16000
		e, n := WER(Words(s.text), Words(text))
		per[s.voice] = [2]int{per[s.voice][0] + e, per[s.voice][1] + n}
		errs, words = errs+e, words+n
		if e > 0 && testing.Verbose() {
			t.Logf("%s: %q -> %q", s.voice, s.text, text)
		}
	}
	for v, c := range per {
		t.Logf("%s: WER %.1f %% (%d/%d)", v, 100*float64(c[0])/float64(c[1]), c[0], c[1])
	}
	t.Logf("all: WER %.1f %% (%d/%d); %.0f ms per second of speech", 100*float64(errs)/float64(words), errs, words,
		1000*spent.Seconds()/audio.Seconds())
}
