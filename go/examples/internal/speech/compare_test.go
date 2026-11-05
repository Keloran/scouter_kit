package speech

import (
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"scouterkit"
)

var (
	voiceFlag = flag.String("voice", "5", "the -corpus voice of the timed sessions")
	loadFlag  = flag.Int("load", 0, "busy threads beside the recogniser during the timed sessions (other agents' load)")
	countFlag = flag.Int("sentences", 8, "sentences in a timed session")
)

// timed is a session whose words' ends are known: each sentence's voiced part
// shared out among its words by their letters.
type timed struct {
	session
	words []string
	ends  []int // the sample after each word
}

func timedSession(t testing.TB) timed {
	t.Helper()
	if *corpusFlag == "" {
		t.Skip("needs -args -corpus DIR (natural test speech)")
	}
	b, err := os.ReadFile(filepath.Join(*corpusFlag, "sentences.txt"))
	if err != nil {
		t.Fatal(err)
	}
	texts := strings.Split(strings.TrimSpace(string(b)), "\n")
	texts = texts[:min(len(texts), *countFlag)]
	var ts timed
	ts.pcm = make([]float32, 16000)
	ts.text = texts
	for i, text := range texts {
		pcm, err := Decode(filepath.Join(*corpusFlag, *voiceFlag, fmt.Sprintf("%d.wav", i+1)))
		if err != nil {
			t.Fatal(err)
		}
		first, last := voiced(pcm)
		base := len(ts.pcm)
		ws := strings.Fields(text)
		total := 0
		for _, w := range ws {
			total += len(Words(w)[0]) + 1
		}
		at := 0
		for _, w := range ws {
			for _, n := range Words(w) {
				at += len(n) + 1
				ts.words = append(ts.words, n)
				ts.ends = append(ts.ends, base+first+(last-first)*at/total)
			}
		}
		ts.pcm = append(ts.pcm, pcm...)
		ts.session.ends = append(ts.session.ends, len(ts.pcm))
		ts.pcm = append(ts.pcm, make([]float32, 9600+3200*(i%3))...)
	}
	return ts
}

// voiced is the first and last sample of the frames above -40 dBFS.
func voiced(pcm []float32) (int, int) {
	first, last := -1, 0
	for k := 0; (k+1)*160 <= len(pcm); k++ {
		if rms(pcm[k*160:(k+1)*160]) > 0.01 {
			if first < 0 {
				first = k * 160
			}
			last = (k + 1) * 160
		}
	}
	return max(0, first), last
}

// burn keeps n threads busy until stop.
func burn(n int) (stop func()) {
	var quit atomic.Bool
	for i := 0; i < n; i++ {
		go func() {
			runtime.LockOSThread()
			x := 1.0
			for !quit.Load() {
				for j := 0; j < 1000; j++ {
					x = math.Sqrt(x + 1)
				}
			}
		}()
	}
	return func() { quit.Store(true) }
}

// shown is a run's texts with their times.
type shown struct {
	texts []Text
	at    []int64
}

// outcome is what a timed run gives: word errors of the stable text, how
// long after its end each word became stable (and was first shown right),
// how often shown words changed, and the recogniser's share of the time.
type outcome struct {
	wer                float64
	stable, first      []float64 // s
	changes, retracted int
	minutes, rtf       float64
}

func measure(ts timed, sh shown, t0 int64, spent time.Duration) outcome {
	var o outcome
	var hyp []string
	var stableAt []int64
	var segStable, segShown []string
	seg := 0
	for k, t := range sh.texts {
		if t.Segment != seg {
			seg, segStable, segShown = t.Segment, nil, nil
		}
		st, tail := strings.Fields(t.Stable), strings.Fields(t.Tail)
		if len(st) < len(segStable) || !prefix(segStable, st) {
			o.retracted++
		}
		all := append(append([]string(nil), st...), tail...)
		if !prefix(segShown, all) && len(segShown) > 0 {
			o.changes++
		}
		for i := len(segStable); i < len(st); i++ {
			hyp = append(hyp, norm(st[i]))
			stableAt = append(stableAt, sh.at[k])
		}
		segStable, segShown = st, all
	}
	// first shown right: the first time a final stable word stood at its place
	firstAt := make([]int64, len(hyp))
	base, seg, segStart := 0, 0, 0
	for k, t := range sh.texts {
		if t.Segment != seg {
			seg, segStart = t.Segment, base
		}
		all := append(strings.Fields(t.Stable), strings.Fields(t.Tail)...)
		for i, w := range all {
			if j := segStart + i; j < len(hyp) && firstAt[j] == 0 && norm(w) == hyp[j] {
				firstAt[j] = sh.at[k]
			}
		}
		if t.Final {
			base = segStart + len(strings.Fields(t.Stable))
		}
	}
	e, n := WER(ts.words, hyp)
	o.wer = float64(e) / float64(max(1, n))
	for _, p := range align(ts.words, hyp) {
		end := t0 + int64(ts.ends[p[0]])*1000000/16000
		o.stable = append(o.stable, float64(stableAt[p[1]]-end)/1e6)
		if firstAt[p[1]] != 0 {
			o.first = append(o.first, float64(firstAt[p[1]]-end)/1e6)
		}
	}
	o.minutes = float64(len(ts.pcm)) / 16000 / 60
	o.rtf = spent.Seconds() / (float64(len(ts.pcm)) / 16000)
	return o
}

func prefix(a, b []string) bool {
	if len(a) > len(b) {
		return false
	}
	for i := range a {
		if norm(a[i]) != norm(b[i]) {
			return false
		}
	}
	return true
}

// align pairs the equal words of ref and hyp along a minimal edit path.
func align(ref, hyp []string) [][2]int {
	d := make([][]int, len(ref)+1)
	for i := range d {
		d[i] = make([]int, len(hyp)+1)
		d[i][0] = i
	}
	for j := range d[0] {
		d[0][j] = j
	}
	for i := 1; i <= len(ref); i++ {
		for j := 1; j <= len(hyp); j++ {
			c := 1
			if ref[i-1] == hyp[j-1] {
				c = 0
			}
			d[i][j] = min(d[i-1][j-1]+c, d[i-1][j]+1, d[i][j-1]+1)
		}
	}
	var out [][2]int
	for i, j := len(ref), len(hyp); i > 0 && j > 0; {
		switch {
		case ref[i-1] == hyp[j-1] && d[i][j] == d[i-1][j-1]:
			out = append(out, [2]int{i - 1, j - 1})
			i, j = i-1, j-1
		case d[i][j] == d[i-1][j]+1:
			i--
		case d[i][j] == d[i][j-1]+1:
			j--
		default:
			i, j = i-1, j-1
		}
	}
	return out
}

func pct(v []float64, p float64) float64 {
	if len(v) == 0 {
		return math.NaN()
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	return s[min(len(s)-1, int(p*float64(len(s))))]
}

func (o outcome) String() string {
	return fmt.Sprintf("WER %4.1f %%; stable %4.2f s median, %4.2f s 90 %%; first shown %4.2f / %4.2f s; "+
		"%3.0f changes and %d retractions a minute; recogniser %3.0f %% of real time",
		100*o.wer, pct(o.stable, 0.5), pct(o.stable, 0.9), pct(o.first, 0.5), pct(o.first, 0.9),
		float64(o.changes)/o.minutes, int(float64(o.retracted)/o.minutes), 100*o.rtf)
}

func runWhisper(t testing.TB, r Recognizer, s session) (shown, int64, time.Duration) {
	st := &Stream{Rec: r, Seg: NewSegmenter(), Denoiser: NewDenoiser(), DenoiseSpeech: true, Step: 8000}
	got, at, t0 := run(t, st, s, true)
	var sh shown
	var a Agree
	for k, u := range got {
		sh.texts = append(sh.texts, a.Add(u))
		sh.at = append(sh.at, at[k])
	}
	return sh, t0, st.Stats().Decoding
}

func runLive(t testing.TB, l Live, s session) (shown, int64, time.Duration) {
	ls := &LiveStream{Rec: l, Seg: NewSegmenter(), Denoiser: NewDenoiser()}
	out := make(chan Text, 64)
	t0 := scouterkit.NowUS()
	errc := make(chan error, 1)
	go func() { errc <- ls.Run(Play(s.pcm, true, nil), out) }()
	var sh shown
	for x := range out {
		sh.texts = append(sh.texts, x)
		sh.at = append(sh.at, scouterkit.NowUS())
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	return sh, t0, ls.Stats().Decoding
}

// Real time, clean and in pink noise, with -load busy threads: whisper with
// local agreement and/or the streaming model (-model, -live).
func TestCompareTimed(t *testing.T) {
	ts := timedSession(t)
	var w Recognizer
	var l Live
	if Available && *modelFlag != "" {
		w = openModel(t, "en")
	}
	if Streaming && *liveFlag != "" {
		l = openLive(t)
	}
	if w == nil && l == nil {
		t.Skip("needs -model and/or -live")
	}
	stop := burn(*loadFlag)
	defer stop()
	for _, c := range []struct {
		name string
		s    timed
	}{{"clean", ts}, {"pink noise 5 dB", timed{ts.session.noisy(5, 11), ts.words, ts.ends}}} {
		if w != nil {
			sh, t0, spent := runWhisper(t, w, c.s.session)
			t.Logf("whisper %-16s %v", c.name+":", measure(c.s, sh, t0, spent))
		}
		if l != nil {
			sh, t0, spent := runLive(t, l, c.s.session)
			t.Logf("live    %-16s %v", c.name+":", measure(c.s, sh, t0, spent))
		}
	}
}
