package speech

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var (
	modelFlag   = flag.String("model", "", "whisper.cpp model for the recogniser tests (ggml-*.bin; empty: skip them)")
	threadsFlag = flag.Int("threads", 8, "recogniser threads")
	vadFlag     = flag.String("vad", "", "Silero voice activity model for the recogniser tests (ggml-silero-*.bin)")
	corpusFlag  = flag.String("corpus", "", "test speech from files instead of espeak-ng: DIR/sentences.txt, one per line, and DIR/VOICE/N.wav")
)

// sentences is the synthetic test speech: plain prose without digits, so
// that the reference and the recogniser spell the same words.
var sentences = []string{
	"The morning train was late again, so we walked along the river to the old bridge.",
	"Please put the blue folder on the shelf next to the window before you leave.",
	"A small dog chased the ball across the garden and jumped into the pond.",
	"The library closes early on Sundays, but the reading room stays open until noon.",
	"She opened the letter slowly and read it twice before saying a single word.",
	"We should check the weather forecast before we decide where to go this weekend.",
	"The engineer explained that the battery lasts longer when the screen is dim.",
	"My brother bought fresh bread, cheese and apples at the market this morning.",
	"Turn left at the second corner and you will see the museum on your right.",
	"The children were singing quietly while their teacher played the piano.",
	"Nobody expected the storm to arrive so quickly, and the streets were soon empty.",
	"He forgot his umbrella on the bus and got completely wet on the way home.",
	"The new bakery on our street sells the best chocolate cake in town.",
	"After dinner we sat outside and watched the stars appear one by one.",
	"Could you send me the report as soon as the meeting is over?",
	"The mountain path was steep and narrow, but the view from the top was worth it.",
}

// voices are espeak-ng voices and speeds (words per minute).
var voices = []struct {
	name string
	wpm  int
}{{"en-us", 160}, {"en-us+f3", 150}, {"en-gb-x-rp", 170}}

// spoken is a test sentence as a voice speaks it.
type spoken struct {
	voice, text string
	pcm         []float32
}

// corpus is the test sentences in every voice: from -corpus, else espeak-ng.
func corpus(t testing.TB) []spoken {
	t.Helper()
	var out []spoken
	if *corpusFlag == "" {
		for _, v := range voices {
			for _, s := range sentences {
				out = append(out, spoken{v.name, s, synth(t, s, v.name, v.wpm)})
			}
		}
		return out
	}
	b, err := os.ReadFile(filepath.Join(*corpusFlag, "sentences.txt"))
	if err != nil {
		t.Fatal(err)
	}
	texts := strings.Split(strings.TrimSpace(string(b)), "\n")
	dirs, _ := filepath.Glob(filepath.Join(*corpusFlag, "*", "1.wav"))
	for _, d := range dirs {
		voice := filepath.Base(filepath.Dir(d))
		for i, s := range texts {
			pcm, err := Decode(filepath.Join(*corpusFlag, voice, fmt.Sprintf("%d.wav", i+1)))
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, spoken{voice, s, pcm})
		}
	}
	return out
}

// synth is Synth; it skips the test without espeak-ng.
func synth(t testing.TB, text, voice string, wpm int) []float32 {
	t.Helper()
	if _, err := exec.LookPath("espeak-ng"); err != nil {
		t.Skip("no espeak-ng")
	}
	pcm, err := Synth(text, voice, wpm)
	if err != nil {
		t.Fatal(err)
	}
	return pcm
}

func openModel(t testing.TB, language string) Recognizer {
	t.Helper()
	if !Available || *modelFlag == "" {
		t.Skip("needs -tags whisper and -args -model ggml-*.bin")
	}
	if _, err := os.Stat(*modelFlag); err != nil {
		t.Fatal(err)
	}
	r, err := Open(Model{Path: *modelFlag, Language: language, Threads: *threadsFlag, VAD: *vadFlag})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	return r
}
