package main

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"scouterkit"
	"scouterkit/examples/internal/demo"
)

// word is one shown word: up to three glyph runs (before the ORP letter, the
// letter, after it) with their pen positions, and how long it stays.
type word struct {
	text  string
	runs  [3][]uint32
	x     [3]int
	micro uint32
}

// split cuts a text into words, marking the last word of each paragraph.
func split(text string) (words []string, ends map[int]bool) {
	ends = map[int]bool{}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	for _, p := range strings.Split(text, "\n\n") {
		w := strings.Fields(p)
		if len(w) == 0 {
			continue
		}
		words = append(words, w...)
		ends[len(words)-1] = true
	}
	return words, ends
}

// orp is the index (in runes) of the letter to fix the eye on: about a
// third into the word's letters, as the RSVP readers do.
func orp(w string) int {
	r := []rune(w)
	first, last := 0, len(r)-1
	for first < last && !unicode.IsLetter(r[first]) && !unicode.IsDigit(r[first]) {
		first++
	}
	for last > first && !unicode.IsLetter(r[last]) && !unicode.IsDigit(r[last]) {
		last--
	}
	n := last - first + 1
	switch {
	case n <= 1:
		return first
	case n <= 5:
		return first + 1
	case n <= 9:
		return first + 2
	case n <= 13:
		return first + 3
	}
	return first + 4
}

// pause is how many times the base time a word stays: longer words and the
// ends of clauses, sentences and paragraphs a little longer.
func pause(w string, paragraphEnd bool) float64 {
	f := 1.0
	if utf8.RuneCountInString(w) > 8 {
		f = 1.3
	}
	last, _ := utf8.DecodeLastRuneInString(strings.TrimRight(w, `"'”’)»`))
	switch {
	case paragraphEnd:
		f = max(f, 2.5)
	case strings.ContainsRune(".!?…", last):
		f = max(f, 2)
	case strings.ContainsRune(",;:–—", last):
		f = max(f, 1.5)
	}
	return f
}

// layoutWord places a word with its ORP letter centred on focus (or the
// whole word centred, without ORP), splitting it if its glyphs need more
// than maxBytes of varints.
func layoutWord(f *scouterkit.Font, w string, focus int, useORP bool, maxBytes int) []word {
	if len(demo.Varints(f.GlyphIndices(w))) > maxBytes {
		r := []rune(w)
		half := len(r) / 2
		return append(layoutWord(f, string(r[:half])+"-", focus, useORP, maxBytes),
			layoutWord(f, string(r[half:]), focus, useORP, maxBytes)...)
	}
	out := word{text: w}
	if !useORP {
		out.runs[0] = f.GlyphIndices(w)
		out.x[0] = focus - f.Measure(w).Width/2
		return []word{out}
	}
	r := []rune(w)
	i := orp(w)
	before, letter, after := string(r[:i]), string(r[i:i+1]), string(r[i+1:])
	adv := f.Measure(letter).Width
	out.x[1] = focus - adv/2
	out.x[0] = out.x[1] - (f.Measure(before+letter).Width - adv)
	out.x[2] = out.x[1] + f.Measure(letter+after).Width - f.Measure(after).Width
	for k, s := range []string{before, letter, after} {
		if s != "" {
			out.runs[k] = f.GlyphIndices(s)
		}
	}
	return []word{out}
}

// prepare lays out every word and gives it its time at wpm words a minute.
func prepare(f *scouterkit.Font, text string, wpm, focus int, useORP bool, maxBytes int) []word {
	ws, ends := split(text)
	base := 60e6 / float64(wpm)
	var out []word
	for i, w := range ws {
		parts := layoutWord(f, w, focus, useORP, maxBytes)
		for k := range parts {
			parts[k].micro = uint32(base)
		}
		parts[len(parts)-1].micro = uint32(base * pause(w, ends[i]))
		out = append(out, parts...)
	}
	return out
}
