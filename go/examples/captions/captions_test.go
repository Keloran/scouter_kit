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
	"sync/atomic"
	"testing"
	"time"

	"scouterkit"
	"scouterkit/examples/internal/demo"
	"scouterkit/examples/internal/speech"
)

var (
	modelFlag  = flag.String("model", "", "whisper.cpp model for the end-to-end test (empty: skip it)")
	vadFlag    = flag.String("vad", "", "Silero voice activity model for the end-to-end test")
	liveFlag   = flag.String("live", "", "a streaming model for sherpa-onnx for the end-to-end test (empty: skip it)")
	corpusFlag = flag.String("corpus", "", "natural test speech: DIR/sentences.txt and DIR/VOICE/N.wav")
	voiceFlag  = flag.String("voice", "5", "the -corpus voice")
)

// corpusSession is the -corpus sentences in one voice, a second of quiet
// first and pauses between them, as a WAV file, and their text.
func corpusSession(t *testing.T, n int) (string, string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(*corpusFlag, "sentences.txt"))
	if err != nil {
		t.Fatal(err)
	}
	texts := strings.Split(strings.TrimSpace(string(b)), "\n")
	texts = texts[:min(n, len(texts))]
	pcm := make([]float32, 16000)
	for i := range texts {
		p, err := speech.Decode(filepath.Join(*corpusFlag, *voiceFlag, fmt.Sprintf("%d.wav", i+1)))
		if err != nil {
			t.Fatal(err)
		}
		pcm = append(append(pcm, p...), make([]float32, 9600+3200*(i%3))...)
	}
	return audioFile(t, pcm), strings.Join(texts, " ")
}

// The streaming recogniser end to end on natural test speech: the word
// error rate, the latency to both eyes, the DRAWs' timing
// (go test -tags emulator,sherpa ./examples/captions -args -live DIR -corpus DIR).
func TestCaptionsLive(t *testing.T) {
	if !speech.Streaming || *liveFlag == "" || *corpusFlag == "" {
		t.Skip("needs -tags sherpa and -args -live DIR -corpus DIR")
	}
	wav, text := corpusSession(t, 16)
	ref := filepath.Join(t.TempDir(), "ref.txt")
	if err := os.WriteFile(ref, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	o := testOptions()
	o.Audio, o.Pace, o.ref, o.Live, o.Codec, o.Threads = wav, true, ref, *liveFlag, 32, 4
	res, err := run(demo.Flags{}, o)
	if err != nil {
		t.Fatal(err)
	}
	sort.Float64s(res.lat)
	t.Logf("WER %.1f %%, %d updates, newest audio to both eyes: median %.2f s, 90 %% %.2f s", 100*res.wer, res.updates,
		res.lat[len(res.lat)/2], res.lat[len(res.lat)*9/10])
	if res.wer > 0.1 {
		t.Errorf("WER %.1f %%", 100*res.wer)
	}
}

// fake stands in for the recogniser: a text whose length follows the audio.
type fake struct{ calls atomic.Int32 }

func (f *fake) Transcribe(pcm []float32, prompt string) (string, error) {
	n := f.calls.Add(1)
	words := strings.Fields("the quick brown fox jumps over the lazy dog while żółw zażółca gęślą jaźń")
	var b []string
	for i := 0; i < len(pcm)/4000; i++ {
		b = append(b, words[(i+int(n))%len(words)])
	}
	return strings.Join(b, " "), nil
}

func (f *fake) Close() {}

// bursts is speech-like sound: three bursts of a 150 Hz buzz, modulated at
// 4 Hz, between pauses with a little noise.
func bursts() []float32 {
	rng := rand.New(rand.NewSource(1))
	var pcm []float32
	add := func(d float64, loud bool) {
		for i := 0; i < int(d*16000); i++ {
			v := 0.0005 * rng.NormFloat64()
			if loud {
				t := float64(i) / 16000
				for h := 1; h <= 8; h++ {
					v += 0.05 / float64(h) * math.Sin(2*math.Pi*150*float64(h)*t)
				}
				v *= 0.6 + 0.4*math.Sin(2*math.Pi*4*t)
			}
			pcm = append(pcm, float32(v))
		}
	}
	add(1, false)
	for k := 0; k < 3; k++ {
		add(1.5+0.5*float64(k), true)
		add(1.5, false)
	}
	return pcm
}

func audioFile(t *testing.T, pcm []float32) string {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	path := filepath.Join(t.TempDir(), "speech.wav")
	if err := speech.WriteWAV(path, pcm); err != nil {
		t.Fatal(err)
	}
	return path
}

func testOptions() options {
	o := options{lines: 3, w: 540, y: -1, tail: 8, clear: 8 * time.Second}
	o.Rate, o.Threads, o.Pace, o.Denoise, o.Step = 32, 8, false, true, 500*time.Millisecond
	return o
}

// Three bursts through the segmenter and a stand-in recogniser, each caption
// read back from both emulated panels.
func TestCaptionsCheck(t *testing.T) {
	o := testOptions()
	o.Audio, o.check, o.rec = audioFile(t, bursts()), true, &fake{}
	res, err := run(demo.Flags{}, o)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(strings.Fields(res.final)); n < 15 || res.updates < 3 {
		t.Fatalf("%d caption updates, final texts of %d words", res.updates, n)
	}
	t.Logf("%d caption updates, each as expected on both panels", res.updates)
}

// The band goes blank (the panel in standby) when the host says so, and
// the next caption shows the whole band again.
func TestCaptionsBlank(t *testing.T) {
	cfg := scouterkit.DefaultConfig()
	if err := scouterkit.Init(cfg); err != nil {
		t.Fatal(err)
	}
	defer scouterkit.Deinit()
	arms := demo.Present()
	d, err := newDisplay(cfg, arms, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	d.set([]line{{"one line", 8}, {"and another", 4}}, 0)
	if err := d.sync(); err != nil {
		t.Fatal(err)
	}
	d.off()
	if err := d.sync(); err != nil {
		t.Fatal(err)
	}
	for _, arm := range arms {
		v, err := arm.Props(scouterkit.PropPanel)
		if err != nil || v[0] != 2 {
			t.Fatalf("%s arm: PANEL %v (%v), want 2", arm, v, err)
		}
	}
	d.set([]line{{"one line", 8}, {"and a third", 11}}, 0)
	if err := d.sync(); err != nil {
		t.Fatal(err)
	}
}

// slow stands in for a recogniser that falls behind: each decode takes d.
type slow struct {
	fake
	d time.Duration
}

func (s *slow) Transcribe(pcm []float32, prompt string) (string, error) {
	time.Sleep(s.d)
	return s.fake.Transcribe(pcm, prompt)
}

// A recogniser that stalls longer than -clear while someone speaks does not
// blank the band; quiet does.
func TestCaptionsBlankOnlyInQuiet(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	var pcm []float32
	add := func(d float64, loud bool) {
		for i := 0; i < int(d*16000); i++ {
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
	add(6, true)
	add(4, false)
	add(2, true)
	add(4, false)
	o := testOptions()
	o.Audio, o.Pace, o.clear, o.rec = audioFile(t, pcm), true, 1500*time.Millisecond, &slow{d: 2500 * time.Millisecond}
	res, err := run(demo.Flags{}, o)
	if err != nil {
		t.Fatal(err)
	}
	for _, at := range res.blanks {
		if at > 1 && at < 7.5 || at > 11 && at < 13.5 {
			t.Fatalf("blanked at %.1f s, during speech (blanks at %v)", at, res.blanks)
		}
	}
	if len(res.blanks) < 2 {
		t.Fatalf("blanks at %v: want one after each burst", res.blanks)
	}
	t.Logf("blanks at %.1f s, none during speech although each decode took 2.5 s", res.blanks)
}

// The microphone: on (the indicator lit) while the command runs, off after;
// a louder sound starts an utterance that reaches the band.
func TestCaptionsMic(t *testing.T) {
	o := testOptions()
	o.rec, o.stay = &fake{}, 2500*time.Millisecond
	lit := make(chan error, 1)
	go func() {
		for !scouterkit.Right.MicState().On {
			time.Sleep(20 * time.Millisecond)
		}
		scouterkit.Right.SetEmulatedMic(300, 0.002)
		time.Sleep(800 * time.Millisecond)
		scouterkit.Right.SetEmulatedMic(300, 0.3)
		got, want, err := scouterkit.Right.PanelCRC(4, 16)
		if err == nil && got == want {
			err = fmt.Errorf("panel rows 4-19 read back as drawn: no indicator")
		}
		lit <- err
	}()
	res, err := run(demo.Flags{}, o)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-lit; err != nil {
		t.Fatal(err)
	}
	m := res.mic
	if m.On || m.Stopped != scouterkit.MicStoppedByHost || m.Frames < 150 {
		t.Fatalf("microphone after the run: %+v", m)
	}
	if res.updates == 0 {
		t.Fatal("no caption from the microphone")
	}
	t.Logf("%d frames, %d caption updates, the microphone off after", m.Frames, res.updates)
}

// Synthetic speech end to end with the recogniser: the word error rate, and
// from the newest audio of each caption to both eyes showing it
// (go test -tags emulator,whisper ./examples/captions -args -model FILE).
func TestCaptionsSpeech(t *testing.T) {
	if !speech.Available || *modelFlag == "" {
		t.Skip("needs -tags whisper and -args -model ggml-*.bin")
	}
	if _, err := exec.LookPath("espeak-ng"); err != nil {
		t.Skip("no espeak-ng")
	}
	texts := []string{
		"Good morning everyone, and thank you for coming to this short meeting.",
		"Today we will look at the new schedule for the spring and the summer.",
		"The first trains will leave a little earlier, and the last ones a little later.",
		"Please tell your teams before the end of the week.",
	}
	pcm := make([]float32, 16000)
	for _, s := range texts {
		p, err := speech.Synth(s, "en-us", 160)
		if err != nil {
			t.Fatal(err)
		}
		pcm = append(append(pcm, p...), make([]float32, 16000)...)
	}
	ref := filepath.Join(t.TempDir(), "ref.txt")
	if err := os.WriteFile(ref, []byte(strings.Join(texts, " ")), 0o644); err != nil {
		t.Fatal(err)
	}
	o := testOptions()
	o.Audio, o.Pace, o.ref, o.Model, o.VAD, o.Language, o.Codec = audioFile(t, pcm), true, ref, *modelFlag, *vadFlag, "en", 32
	res, err := run(demo.Flags{}, o)
	if err != nil {
		t.Fatal(err)
	}
	sort.Float64s(res.lat)
	t.Logf("WER %.1f %%, %d caption updates, newest audio to both eyes: median %.2f s, max %.2f s", 100*res.wer,
		res.updates, res.lat[len(res.lat)/2], res.lat[len(res.lat)-1])
	if res.wer > 0.1 {
		t.Errorf("WER %.1f %% above 10 %%", 100*res.wer)
	}
}
