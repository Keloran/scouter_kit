package main

import (
	"strings"
	"unicode/utf8"

	"scouterkit"
	"scouterkit/examples/internal/demo"
)

// line is one laid-out line of the book: its glyphs, as varints in a record.
type line struct {
	text   string
	glyphs []uint32
	words  int
}

// paragraphs splits a text at blank lines and joins the rest of its lines.
func paragraphs(text string) [][]string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	var out [][]string
	for _, p := range strings.Split(text, "\n\n") {
		if w := strings.Fields(p); len(w) > 0 {
			out = append(out, w)
		}
	}
	return out
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

// wrap lays the text out in lines at most width pixels wide whose glyphs
// fit in maxBytes of varints, a blank line between paragraphs.
func wrap(text string, f *scouterkit.Font, width, maxBytes int) []line {
	fits := func(s string) bool {
		return f.Measure(s).Width <= width && len(demo.Varints(f.GlyphIndices(s))) <= maxBytes
	}
	var out []line
	add := func(s string, words int) { out = append(out, line{text: s, glyphs: f.GlyphIndices(s), words: words}) }
	for i, p := range paragraphs(text) {
		if i > 0 {
			add("", 0)
		}
		cur, n := "", 0
		for _, w := range p {
			for w != "" && !fits(w) {
				_, first := utf8.DecodeRuneInString(w)
				k := len(w)
				for k > first && !fits(w[:k]) {
					_, size := utf8.DecodeLastRuneInString(w[:k])
					k -= size
				}
				if cur != "" {
					add(cur, n)
					cur, n = "", 0
				}
				add(w[:k], 1)
				w = w[k:]
			}
			if w == "" {
				continue
			}
			if cur == "" {
				cur, n = w, 1
			} else if s := cur + " " + w; fits(s) {
				cur, n = s, n+1
			} else {
				add(cur, n)
				cur, n = w, 1
			}
		}
		if cur != "" {
			add(cur, n)
		}
	}
	return out
}
