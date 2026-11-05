//go:build emulator

package main

import (
	"flag"
	"fmt"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"scouterkit/examples/internal/demo"
	"scouterkit/examples/internal/speech"
)

var (
	modelFlag  = flag.String("model", "", "whisper.cpp model for the end-to-end test (empty: skip it)")
	vadFlag    = flag.String("vad", "", "Silero voice activity model for the end-to-end test")
	liveFlag   = flag.String("live", "", "a streaming model for sherpa-onnx for the end-to-end test (empty: skip it)")
	corpusFlag = flag.String("corpus", "", "the script read by natural test speech: DIR/sentences.txt, DIR/VOICE/N.wav")
	voiceFlag  = flag.String("voice", "5", "the -corpus voice")
)

// The streaming recogniser end to end, the script read sentence by
// sentence in natural test speech: after each sentence's end, how long until
// the position passes its last word; the steps' timing
// (go test -tags emulator,sherpa ./examples/teleprompter -args -live DIR -corpus DIR).
func TestTeleprompterLive(t *testing.T) {
	if !speech.Streaming || *liveFlag == "" || *corpusFlag == "" {
		t.Skip("needs -tags sherpa and -args -live DIR -corpus DIR")
	}
	b, err := os.ReadFile(filepath.Join(*corpusFlag, "sentences.txt"))
	if err != nil {
		t.Fatal(err)
	}
	texts := strings.Split(strings.TrimSpace(string(b)), "\n")
	pcm := make([]float32, 16000)
	var ends, lastWord []int
	n := 0
	for i, s := range texts {
		p, err := speech.Decode(filepath.Join(*corpusFlag, *voiceFlag, fmt.Sprintf("%d.wav", i+1)))
		if err != nil {
			t.Fatal(err)
		}
		pcm = append(append(pcm, p...), make([]float32, 4800)...)
		n += len(speech.Words(s))
		ends, lastWord = append(ends, len(pcm)-4800), append(lastWord, n)
	}
	wav := filepath.Join(t.TempDir(), "speech.wav")
	if err := speech.WriteWAV(wav, pcm); err != nil {
		t.Fatal(err)
	}
	o := testOptions(t, strings.Join(texts, " "))
	o.Audio, o.Codec, o.Live, o.Threads = wav, 32, *liveFlag, 4
	res, err := run(demo.Flags{}, o)
	if err != nil {
		t.Fatal(err)
	}
	var lag []float64
	for i, e := range ends {
		endUS := res.t0 + int64(e)*1000000/16000
		got := math.Inf(1)
		for _, m := range res.moves {
			if m.pos >= lastWord[i]-1 {
				got = float64(m.at-endUS) / 1e6
				break
			}
		}
		lag = append(lag, got)
	}
	sort.Float64s(lag)
	t.Logf("at word %d of %d; after a sentence's end its last word reached: median %.2f s, max %.2f s; %d steps",
		res.end, n, lag[len(lag)/2], lag[len(lag)-1], res.steps)
	if res.end < n-2 || math.IsInf(lag[len(lag)-1], 1) {
		t.Errorf("ended at word %d of %d, lags %v", res.end, n, lag)
	}
}

func scriptFile(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "script.txt")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func testOptions(t *testing.T, text string) options {
	o := options{path: scriptFile(t, text), w: 540, h: 240, mark: 0.3, rows: 16, frame: 125 * time.Millisecond, dim: 5, prompt: true}
	o.Rate, o.Threads, o.Pace, o.Denoise, o.Step = 32, 8, true, true, 400*time.Millisecond
	return o
}

// Step mode through the first 300 words of the script: after every scroll
// step both emulated panels equal the page drawn here, with a ring smaller
// than the script so that it is refilled on the way.
func TestTeleprompterCheck(t *testing.T) {
	o := testOptions(t, strings.Repeat(gettysburg+"\n\n", 3))
	o.check, o.ring = 300, 40
	if _, err := run(demo.Flags{}, o); err != nil {
		t.Fatal(err)
	}
}

// reader stands in for the recogniser: it hears the script at a steady
// rate from where the last utterance ended, with some errors.
type reader struct {
	mu          sync.Mutex
	words       []string
	base, heard int     // the script word the utterance began at; words it held
	first       float32 // the utterance's first sample
	rng         *rand.Rand
}

func (r *reader) Transcribe(pcm []float32, prompt string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if pcm[0] != r.first {
		r.base += r.heard
		r.first, r.heard = pcm[0], 0
	}
	r.heard = max(r.heard, min(len(r.words)-r.base, int(float64(len(pcm))/16000*2.5)))
	return strings.Join(garble(r.words[r.base:r.base+r.heard], 0.1, r.rng), " "), nil
}

func (r *reader) Close() {}

// bursts is n utterances of speech-like sound of d each, pauses between.
func bursts(n int, d float64) []float32 {
	rng := rand.New(rand.NewSource(2))
	var pcm []float32
	add := func(s float64, loud bool) {
		for i := 0; i < int(s*16000); i++ {
			v := 0.0005 * rng.NormFloat64()
			if loud {
				tt := float64(i) / 16000
				for h := 1; h <= 8; h++ {
					v += 0.05 / float64(h) * math.Sin(2*math.Pi*150*float64(h)*tt)
				}
				v *= 0.6 + 0.4*math.Sin(2*math.Pi*4*tt)
			}
			pcm = append(pcm, float32(v))
		}
	}
	add(1, false)
	for k := 0; k < n; k++ {
		add(d, true)
		add(1.2, false)
	}
	return pcm
}

// A stand-in reader at 2.5 words a second, in 4 s utterances: the arms
// follow, both eyes reach each position together.
func TestTeleprompterFollows(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	o := testOptions(t, gettysburg)
	wav := filepath.Join(t.TempDir(), "speech.wav")
	if err := speech.WriteWAV(wav, bursts(5, 4)); err != nil {
		t.Fatal(err)
	}
	o.Audio = wav
	r := &reader{words: speech.Words(gettysburg), rng: rand.New(rand.NewSource(3))}
	o.rec = r
	res, err := run(demo.Flags{}, o)
	if err != nil {
		t.Fatal(err)
	}
	if read := r.base + r.heard; abs(res.end-read) > 2 || len(res.lat) == 0 {
		t.Fatalf("ended at word %d after reading %d; %d arrivals", res.end, read, len(res.lat))
	}
	sort.Float64s(res.lat)
	t.Logf("at word %d, %d moves; newest audio to both eyes at the position: median %.2f s, max %.2f s", res.end,
		len(res.moves), res.lat[len(res.lat)/2], res.lat[len(res.lat)-1])
}

// Synthetic speech end to end with the recogniser: espeak-ng reads the
// first two paragraphs sentence by sentence; after each sentence, how long
// until the position passes its last word
// (go test -tags emulator,whisper ./examples/teleprompter -args -model FILE).
func TestTeleprompterSpeech(t *testing.T) {
	if !speech.Available || *modelFlag == "" {
		t.Skip("needs -tags whisper and -args -model ggml-*.bin")
	}
	if _, err := exec.LookPath("espeak-ng"); err != nil {
		t.Skip("no espeak-ng")
	}
	paras := strings.SplitN(gettysburg, "\n\n", 3)
	text := paras[0] + "\n\n" + paras[1]
	var sentences []string
	for _, s := range strings.SplitAfter(strings.ReplaceAll(text, "\n\n", " "), ". ") {
		if s = strings.TrimSpace(s); s != "" {
			sentences = append(sentences, s)
		}
	}
	pcm := make([]float32, 16000)
	var ends []int     // sample after each sentence
	var lastWord []int // its last script word
	n := 0
	for _, s := range sentences {
		p, err := speech.Synth(s, "en-us", 160)
		if err != nil {
			t.Fatal(err)
		}
		pcm = append(append(pcm, p...), make([]float32, 6400)...)
		n += len(speech.Words(s))
		ends, lastWord = append(ends, len(pcm)-6400), append(lastWord, n)
	}
	wav := filepath.Join(t.TempDir(), "speech.wav")
	if err := speech.WriteWAV(wav, pcm); err != nil {
		t.Fatal(err)
	}
	for _, prompt := range []bool{true, false} {
		o := testOptions(t, gettysburg)
		o.Audio, o.Codec, o.Model, o.VAD, o.Language, o.prompt = wav, 32, *modelFlag, *vadFlag, "en", prompt
		res, err := run(demo.Flags{}, o)
		if err != nil {
			t.Fatal(err)
		}
		var lag []float64
		for i, e := range ends {
			endUS := res.t0 + int64(e)*1000000/16000
			got := math.Inf(1)
			for _, m := range res.moves {
				if m.pos >= lastWord[i]-1 {
					got = float64(m.at-endUS) / 1e6
					break
				}
			}
			lag = append(lag, got)
		}
		sort.Float64s(lag)
		t.Logf("prompt %v: at word %d of %d read; after a sentence's end its last word reached: median %.2f s, max %.2f s",
			prompt, res.end, n, lag[len(lag)/2], lag[len(lag)-1])
		if res.end < n-2 || math.IsInf(lag[len(lag)-1], 1) {
			t.Errorf("prompt %v: ended at word %d of %d, lags %v", prompt, res.end, n, lag)
		}
	}
}
