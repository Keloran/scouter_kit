// Package speech has what the speech demos share: audio from the glasses'
// microphone or a file, noise suppression, a speech segmenter, the
// recogniser and its voice activity model (whisper.cpp, in builds with
// -tags whisper) and word error rates.
package speech

import (
	"strings"
	"unicode"
)

// Recognizer turns 16 kHz mono speech, samples in [-1, 1], into text.
// prompt is text that came before, to condition the recogniser on.
type Recognizer interface {
	Transcribe(pcm []float32, prompt string) (string, error)
	Close()
}

// Live is a streaming recogniser: audio goes in as it comes, and after
// each piece the text of the segment under way comes out, with whether the
// segment ended there (then Reset starts the next one).
type Live interface {
	Accept(pcm []float32) (text string, endpoint bool)
	Reset()
	Finish() string // no more audio: the segment's last text
	Close()
}

// Voiced is a recogniser with a voice activity model: it reports whether
// the audio has speech in it.
type Voiced interface {
	Voiced(pcm []float32) bool
}

// Model says which recogniser to open and how to run it.
type Model struct {
	Path     string // a whisper.cpp model file (ggml-*.bin)
	Language string // "en", "de", ..., or "auto"
	Threads  int
	VAD      string // a Silero voice activity model (ggml-silero-*.bin): Voiced is false below 250 ms of speech
}

// Words splits text into lower-case words without punctuation; an
// apostrophe inside a word stays.
func Words(text string) []string {
	f := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '\'' && r != '’'
	})
	out := f[:0]
	for _, w := range f {
		if w = strings.Trim(strings.ReplaceAll(w, "’", "'"), "'"); w != "" {
			out = append(out, w)
		}
	}
	return out
}

// WER is the word error rate of hyp against ref: substitutions, deletions
// and insertions over the words of ref.
func WER(ref, hyp []string) (errors, words int) {
	prev := make([]int, len(hyp)+1)
	cur := make([]int, len(hyp)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ref); i++ {
		cur[0] = i
		for j := 1; j <= len(hyp); j++ {
			sub := prev[j-1]
			if ref[i-1] != hyp[j-1] {
				sub++
			}
			cur[j] = min(sub, prev[j]+1, cur[j-1]+1)
		}
		prev, cur = cur, prev
	}
	return prev[len(hyp)], len(ref)
}

// unrepeat drops a run of 4 words or more that repeats the run just before
// it: Whisper can loop on a phrase.
func unrepeat(w []string) []string {
	for n := len(w) / 2; n >= 4; n-- {
		for i := 0; i+2*n <= len(w); i++ {
			same := true
			for k := 0; k < n && same; k++ {
				same = norm(w[i+k]) == norm(w[i+n+k])
			}
			if same {
				return unrepeat(append(w[:i+n:i+n], w[i+2*n:]...))
			}
		}
	}
	return w
}
