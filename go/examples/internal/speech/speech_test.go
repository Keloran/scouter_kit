package speech

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestWords(t *testing.T) {
	got := Words("Don’t stop: the bus — it's late, 'really' late!")
	want := []string{"don't", "stop", "the", "bus", "it's", "late", "really", "late"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Words: %q, want %q", got, want)
	}
}

func TestWER(t *testing.T) {
	for _, c := range []struct {
		ref, hyp string
		errors   int
	}{
		{"a b c d", "a b c d", 0},
		{"a b c d", "a x c d", 1},
		{"a b c d", "a c d", 1},
		{"a b c d", "a b b c d e", 2},
		{"a b", "", 2},
	} {
		if e, n := WER(Words(c.ref), Words(c.hyp)); e != c.errors || n != len(Words(c.ref)) {
			t.Errorf("WER(%q, %q) = %d/%d, want %d", c.ref, c.hyp, e, n, c.errors)
		}
	}
}

// The recogniser on each test sentence alone, per voice: word errors and
// decode time (go test -tags whisper ./examples/internal/speech -args -model FILE).
func TestRecognizerSentences(t *testing.T) {
	r := openModel(t, "en")
	per := map[string][2]int{}
	var errs, words int
	var audio, spent time.Duration
	for _, s := range corpus(t) {
		t0 := time.Now()
		text, err := r.Transcribe(s.pcm, "")
		if err != nil {
			t.Fatal(err)
		}
		spent += time.Since(t0)
		audio += time.Duration(len(s.pcm)) * time.Second / 16000
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
	t.Logf("all: WER %.1f %% (%d/%d); decode %.0f ms per second of speech (%v for %v)", 100*float64(errs)/float64(words),
		errs, words, 1000*spent.Seconds()/audio.Seconds(), spent.Round(time.Millisecond), audio.Round(time.Second))
	if strings.Contains(*modelFlag, "tiny") {
		return
	}
	if float64(errs) > 0.15*float64(words) {
		t.Errorf("WER %.1f %% above 15 %%", 100*float64(errs)/float64(words))
	}
}

// Other languages through -lang, with espeak-ng's voices for them.
func TestRecognizerLanguages(t *testing.T) {
	for _, c := range []struct {
		lang, voice string
		texts       []string
	}{
		{"de", "de", []string{
			"Heute Morgen kam der Zug wieder zu spät, also gingen wir am Fluss entlang zur alten Brücke.",
			"Bitte leg die blaue Mappe auf das Regal neben dem Fenster.",
			"Die Bibliothek schließt am Sonntag früh, aber der Lesesaal bleibt bis Mittag geöffnet.",
			"Nach dem Abendessen saßen wir draußen und sahen den Sternen zu.",
		}},
		{"pl", "pl", []string{
			"Dzisiaj rano pociąg znowu się spóźnił, więc poszliśmy pieszo wzdłuż rzeki.",
			"Proszę położyć niebieską teczkę na półce obok okna.",
			"Biblioteka jest zamknięta w niedzielę, ale czytelnia działa do południa.",
			"Po kolacji siedzieliśmy na zewnątrz i patrzyliśmy na gwiazdy.",
		}},
	} {
		r := openModel(t, c.lang)
		var errs, words int
		for _, s := range c.texts {
			text, err := r.Transcribe(synth(t, s, c.voice, 150), "")
			if err != nil {
				t.Fatal(err)
			}
			e, n := WER(Words(s), Words(text))
			errs, words = errs+e, words+n
			if e > 0 && testing.Verbose() {
				t.Logf("%s: %q -> %q", c.lang, s, text)
			}
		}
		t.Logf("%s: WER %.1f %% (%d/%d)", c.lang, 100*float64(errs)/float64(words), errs, words)
	}
}

func TestUnrepeat(t *testing.T) {
	in := strings.Fields("a small dog chased the ball. a small dog chased the ball. a small dog chased the ball. then it slept")
	if got := strings.Join(unrepeat(in), " "); got != "a small dog chased the ball. then it slept" {
		t.Fatalf("unrepeat: %q", got)
	}
	if got := strings.Join(unrepeat(strings.Fields("no no no no it is fine")), " "); got != "no no no no it is fine" {
		t.Fatalf("unrepeat: %q", got)
	}
}
