package main

import (
	"math/rand"
	"strings"
	"testing"

	"scouterkit/examples/internal/speech"
)

// gettysburg is the test script (A. Lincoln, 1863; public domain).
const gettysburg = `Four score and seven years ago our fathers brought forth on this continent, a new nation, conceived in Liberty, and dedicated to the proposition that all men are created equal.

Now we are engaged in a great civil war, testing whether that nation, or any nation so conceived and so dedicated, can long endure. We are met on a great battle-field of that war. We have come to dedicate a portion of that field, as a final resting place for those who here gave their lives that that nation might live. It is altogether fitting and proper that we should do this.

But, in a larger sense, we can not dedicate, we can not consecrate, we can not hallow this ground. The brave men, living and dead, who struggled here, have consecrated it, far above our poor power to add or detract. The world will little note, nor long remember what we say here, but it can never forget what they did here. It is for us the living, rather, to be dedicated here to the unfinished work which they who fought here have thus far so nobly advanced. It is rather for us to be here dedicated to the great task remaining before us, that from these honored dead we take increased devotion to that cause for which they gave the last full measure of devotion, that we here highly resolve that these dead shall not have died in vain, that this nation, under God, shall have a new birth of freedom, and that government of the people, by the people, for the people, shall not perish from the earth.`

// garble turns words into what a recogniser with about rate errors might
// hear: substitutions, deletions and insertions.
func garble(words []string, rate float64, rng *rand.Rand) []string {
	var out []string
	for _, w := range words {
		switch r := rng.Float64(); {
		case r < rate/3:
			out = append(out, w[:max(1, len(w)/2)]+"x")
		case r < 2*rate/3:
		case r < rate:
			out = append(out, w, "um")
		default:
			out = append(out, w)
		}
	}
	return out
}

// Reading the script in utterances of 5-15 words, with partial results of
// one word more each time: the position stays within two words of the
// reader at 0, 10 and 20 % word errors.
func TestTrackerFollows(t *testing.T) {
	words := speech.Words(gettysburg)
	for _, rate := range []float64{0, 0.1, 0.2} {
		rng := rand.New(rand.NewSource(1))
		tr := newTracker(words)
		var worst, sum, n int
		for start := 0; start < len(words); {
			end := min(len(words), start+5+rng.Intn(11))
			heard := garble(words[start:end], rate, rng)
			for k := 1; k <= len(heard); k++ {
				tr.hear(heard[:k])
			}
			lag := end - tr.pos
			worst, sum, n = max(worst, abs(lag)), sum+abs(lag), n+1
			start = end
		}
		t.Logf("%2.0f %% errors: at each utterance's end the position is %.1f words off on average, %d at most; at the end %d of %d",
			100*rate, float64(sum)/float64(n), worst, tr.pos, len(words))
		if worst > 2 || tr.pos < len(words)-1 {
			t.Errorf("%.0f %% errors: up to %d words off, ended at %d of %d", 100*rate, worst, tr.pos, len(words))
		}
	}
}

// A skipped sentence is followed; talk that is not in the script moves
// nothing; reading a sentence again goes back to it.
func TestTrackerSkipsAndReturns(t *testing.T) {
	words := speech.Words(gettysburg)
	find := func(s string) int {
		w := speech.Words(s)
		for i := range words {
			if strings.Join(words[i:min(len(words), i+len(w))], " ") == strings.Join(w, " ") {
				return i
			}
		}
		t.Fatalf("%q not in the script", s)
		return 0
	}
	tr := newTracker(words)
	tr.hear(speech.Words("four score and seven years ago our fathers"))
	if tr.pos != 8 {
		t.Fatalf("after the first words: at %d, want 8", tr.pos)
	}
	skip := find("now we are engaged in a great civil war")
	tr.hear(speech.Words("now we are engaged in a great civil war"))
	if want := skip + 9; tr.pos != want {
		t.Fatalf("after a skipped sentence: at %d, want %d", tr.pos, want)
	}
	before := tr.pos
	tr.hear(speech.Words("sorry let me get some water please give me a moment"))
	if tr.pos != before {
		t.Fatalf("talk outside the script moved the position from %d to %d", before, tr.pos)
	}
	tr.hear(speech.Words("conceived in liberty and dedicated to the proposition"))
	if want := find("conceived in liberty") + 8; tr.pos != want {
		t.Fatalf("reading again: at %d, want %d", tr.pos, want)
	}
}

func TestScriptBefore(t *testing.T) {
	sc := script{lines: []line{{words: strings.Fields("Four score and"), tokens: []int{1, 1, 1}},
		{words: strings.Fields("seven battle-field ago"), tokens: []int{1, 2, 1}, first: 3}},
		words: speech.Words("Four score and seven battle-field ago")}
	for _, c := range []struct {
		pos, line, before int
		prompt            string
	}{
		{0, 0, 0, ""},
		{3, 1, 0, "Four score and"},
		{5, 1, 1, "Four score and seven"}, // "field" is in "battle-field"
		{6, 1, 2, "Four score and seven battle-field"},
		{7, 2, 0, "Four score and seven battle-field ago"},
	} {
		if k, before := sc.at(c.pos); k != c.line || before != c.before {
			t.Errorf("at(%d) = %d, %d; want %d, %d", c.pos, k, before, c.line, c.before)
		}
		if got := sc.before(c.pos, 40); got != c.prompt {
			t.Errorf("before(%d) = %q, want %q", c.pos, got, c.prompt)
		}
	}
}
