package main

import "strings"

// function words count less in the alignment: they match almost anywhere.
var function = map[string]bool{}

func init() {
	for _, w := range strings.Fields("a an the and or but of to in on at by for with from as is are was were be been " +
		"it its this that these those i you he she we they me him her us them my your his our their not no so if " +
		"then than there here do does did have has had will would can could shall should may might must just also very") {
		function[w] = true
	}
}

// tracker follows a reader through the script: it aligns the last words
// recognised to the part of the script around the position (a local
// alignment that accepts small misspellings and cut-off words) and moves
// the position after the last word matched. A move back, or ahead by more
// than far words, needs a strong match or the same place twice in a row;
// after three texts in a row that matched nothing near, the whole script is
// searched (strong matches only).
type tracker struct {
	words      []string // the script, as speech.Words
	pos        int      // the next word to be read
	back       int      // script words before pos the alignment may reach
	ahead      int      // and after it
	tail       int      // recognised words aligned
	minScore   float64  // a move needs this score
	far        int
	strong     float64
	cand, seen int // the move waiting for its second look
	misses     int
}

func newTracker(words []string) *tracker {
	return &tracker{words: words, back: 40, ahead: 80, tail: 12, minScore: 1.6, far: 8, strong: 4}
}

func (t *tracker) weight(w string) float64 {
	if function[w] {
		return 0.3
	}
	return 1
}

// similar is 1 for the same word, less for one edit apart or a cut-off
// prefix, 0 otherwise.
func similar(heard, script string) float64 {
	switch {
	case heard == script:
		return 1
	case len(heard) >= 4 && len(script) >= 4 && edits(heard, script) <= 1:
		return 0.7
	case len(heard) >= 3 && strings.HasPrefix(script, heard):
		return 0.5
	}
	return 0
}

// edits is the Levenshtein distance, stopping early above 1.
func edits(a, b string) int {
	if d := len(a) - len(b); d > 1 || d < -1 {
		return 2
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			s := prev[j-1]
			if a[i-1] != b[j-1] {
				s++
			}
			cur[j] = min(s, prev[j]+1, cur[j-1]+1)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// hear takes the words recognised so far in an utterance; it reports
// whether the position moved.
func (t *tracker) hear(heard []string) bool {
	if len(heard) > t.tail {
		heard = heard[len(heard)-t.tail:]
	}
	pos, score := t.align(heard, max(0, t.pos-t.back), min(len(t.words), t.pos+t.ahead))
	if pos < 0 || score < t.minScore {
		if t.misses++; t.misses < 3 {
			return false
		}
		if pos, score = t.align(heard, 0, len(t.words)); pos < 0 || score < t.strong {
			return false
		}
	}
	t.misses = 0
	switch d := pos - t.pos; {
	case d == 0:
		return false
	case d > 0 && d <= t.far || score >= t.strong:
	case t.seen > 0 && abs(pos-t.cand) <= 2:
	default:
		t.cand, t.seen = pos, 1
		return false
	}
	t.pos, t.seen = pos, 0
	return true
}

// align is where in words[lo:hi] the heard words best end (the script word
// after the last one matched; -1: nowhere), with the match's score. Ends
// near the position win ties.
func (t *tracker) align(heard []string, lo, hi int) (int, float64) {
	m, n := len(heard), hi-lo
	if m == 0 || n <= 0 {
		return -1, 0
	}
	const mismatch, gap = -0.4, -0.3
	d := make([][]float64, m+1)
	for i := range d {
		d[i] = make([]float64, n+1)
	}
	bestRank, bestPos, bestScore := 0.0, -1, 0.0
	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			w := t.words[lo+j-1]
			s := mismatch
			if sim := similar(heard[i-1], w); sim > 0 {
				s = sim * t.weight(w)
			}
			v := max(0, d[i-1][j-1]+s, d[i-1][j]+gap, d[i][j-1]+gap)
			d[i][j] = v
			if i < m-2 || s <= 0 || v != d[i-1][j-1]+s {
				continue
			}
			if rank := v - 0.01*float64(abs(lo+j-t.pos)); rank > bestRank {
				bestRank, bestPos, bestScore = rank, lo+j, v
			}
		}
	}
	return bestPos, bestScore
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
