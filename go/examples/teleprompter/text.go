package main

import (
	"sort"
	"strings"
	"unicode/utf8"

	"scouterkit"
	"scouterkit/examples/internal/demo"
	"scouterkit/examples/internal/speech"
)

// line is one laid-out line of the script.
type line struct {
	text   string
	glyphs []uint32
	words  []string // its words as shown
	tokens []int    // per word: how many script words (speech.Words) it holds
	cuts   []int    // per word: the glyphs of the words before it and a space
	first  int      // the script word of its first word
}

// script is the text laid out, and its words as the tracker reads them.
type script struct {
	lines []line
	words []string // speech.Words of the whole text
}

// charset is the text's characters, for LoadFont.
func charset(text string) string {
	seen := map[rune]bool{}
	var b strings.Builder
	for _, r := range text {
		if !seen[r] && r >= ' ' {
			seen[r] = true
			b.WriteRune(r)
		}
	}
	return b.String()
}

// layout lays the text out in lines at most width pixels wide whose glyphs
// fit in maxBytes of varints, a blank line between paragraphs.
func layoutScript(text string, f *scouterkit.Font, width, maxBytes int) script {
	fits := func(s string) bool {
		return f.Measure(s).Width <= width && len(demo.Varints(f.GlyphIndices(s))) <= maxBytes
	}
	var sc script
	var cur []string
	flush := func() {
		ln := line{text: strings.Join(cur, " "), words: cur, first: len(sc.words)}
		ln.glyphs = f.GlyphIndices(ln.text)
		for i, w := range cur {
			if i == 0 {
				ln.cuts = append(ln.cuts, 0)
			} else {
				ln.cuts = append(ln.cuts, len(f.GlyphIndices(strings.Join(cur[:i], " ")+" ")))
			}
			t := speech.Words(w)
			ln.tokens = append(ln.tokens, len(t))
			sc.words = append(sc.words, t...)
		}
		sc.lines = append(sc.lines, ln)
		cur = nil
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	for i, p := range strings.Split(text, "\n\n") {
		ws := strings.Fields(p)
		if len(ws) == 0 {
			continue
		}
		if i > 0 && len(sc.lines) > 0 {
			flush()
		}
		for _, w := range ws {
			for w != "" && !fits(w) {
				_, first := utf8.DecodeRuneInString(w)
				k := len(w)
				for k > first && !fits(w[:k]) {
					_, size := utf8.DecodeLastRuneInString(w[:k])
					k -= size
				}
				if len(cur) > 0 {
					flush()
				}
				cur = []string{w[:k]}
				flush()
				w = w[k:]
			}
			if w == "" {
				continue
			}
			if len(cur) > 0 && !fits(strings.Join(cur, " ")+" "+w) {
				flush()
			}
			cur = append(cur, w)
		}
		if len(cur) > 0 {
			flush()
		}
	}
	return sc
}

// at is where script word pos is: its line and how many of the line's words
// come before it. pos == len(words): past the last line.
func (sc script) at(pos int) (lineIdx, wordsBefore int) {
	if pos >= len(sc.words) {
		return len(sc.lines), 0
	}
	k := sort.Search(len(sc.lines), func(i int) bool { return sc.lines[i].first > pos }) - 1
	for k > 0 && len(sc.lines[k].words) == 0 {
		k--
	}
	ln := sc.lines[k]
	n := ln.first
	for i, t := range ln.tokens {
		if n+t > pos {
			return k, i
		}
		n += t
	}
	return k, len(ln.words)
}

// view is what shows script word pos at the reading row.
func (sc script) view(f *demo.Font, g geometry, pos int) view {
	k, before := sc.at(pos)
	v := view{pos: g.target(min(k, len(sc.lines)-1)), cur: k, splitX: margin}
	if k >= len(sc.lines) {
		return v
	}
	if ln := sc.lines[k]; before > 0 && before < len(ln.cuts) {
		v.split = ln.cuts[before]
		v.splitX = margin + f.Draw(demo.NewImage(1, 1), 0, 0, ln.glyphs[:v.split], 15)
	}
	return v
}

// before is up to n shown words before script word pos: the recogniser's
// prompt.
func (sc script) before(pos, n int) string {
	k, before := sc.at(pos)
	var out []string
	for i := k; i >= 0 && len(out) < n; i-- {
		if i >= len(sc.lines) {
			continue
		}
		ws := sc.lines[i].words
		if i == k {
			ws = ws[:before]
		}
		out = append(append([]string(nil), ws...), out...)
	}
	return strings.Join(out[max(0, len(out)-n):], " ")
}
