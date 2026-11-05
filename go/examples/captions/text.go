package main

import (
	"strings"
	"unicode/utf8"

	"scouterkit"
	"scouterkit/examples/internal/demo"
	"scouterkit/examples/internal/speech"
)

// line is a band line: its text and the byte where its words that may
// still change begin (len(text): none).
type line struct {
	text string
	tail int
}

// wrap lays words out in lines at most width pixels wide whose glyphs fit
// in maxBytes of varints, and returns each line's words as [first, end); a
// word too long for a line is broken into pieces first.
func wrap(words []string, f *scouterkit.Font, width, maxBytes int) ([]string, [][2]int) {
	fits := func(s string) bool {
		return f.Measure(s).Width <= width && len(demo.Varints(f.GlyphIndices(s))) <= maxBytes
	}
	var pieces []string
	for _, w := range words {
		for w != "" && !fits(w) {
			_, first := utf8.DecodeRuneInString(w)
			k := len(w)
			for k > first && !fits(w[:k]) {
				_, size := utf8.DecodeLastRuneInString(w[:k])
				k -= size
			}
			pieces = append(pieces, w[:k])
			w = w[k:]
		}
		if w != "" {
			pieces = append(pieces, w)
		}
	}
	var lines [][2]int
	for i := 0; i < len(pieces); {
		j := i + 1
		for j < len(pieces) && fits(strings.Join(pieces[i:j+1], " ")) {
			j++
		}
		lines = append(lines, [2]int{i, j})
		i = j
	}
	return pieces, lines
}

// captions is what the band shows: the end of the recent segments' texts,
// then the segment under way, its stable words and its tail.
type captions struct {
	done         []string
	seg          int
	stable, tail string
}

func (c *captions) add(t speech.Text) {
	if t.Segment != c.seg {
		if c.stable != "" {
			c.done = append(c.done, c.stable)
		}
		c.seg, c.stable, c.tail = t.Segment, "", ""
	}
	if t.Final {
		if t.Stable != "" {
			c.done = append(c.done, t.Stable)
		}
		c.stable, c.tail = "", ""
		c.seg = 0
	} else {
		c.stable, c.tail = t.Stable, t.Tail
	}
	if len(c.done) > 8 {
		c.done = c.done[len(c.done)-8:]
	}
}

func (c *captions) reset() { c.done, c.stable, c.tail = nil, "", "" }

// empty reports whether there is nothing to show.
func (c *captions) empty() bool { return len(c.done) == 0 && c.stable == "" && c.tail == "" }

// lines are the last n lines of the text laid out.
func (c *captions) lines(f *scouterkit.Font, width, n int) []line {
	stable := strings.Fields(strings.Join(append(append([]string(nil), c.done...), c.stable), " "))
	tail := strings.Fields(c.tail)
	pieces, ranges := wrap(append(stable, tail...), f, width, recordBytes-8)
	if len(pieces) != len(stable)+len(tail) {
		pieces, ranges = wrap(stable, f, width, recordBytes-8) // a broken word: show the stable words only
		tail = nil
	}
	var out []line
	for _, r := range ranges[max(0, len(ranges)-n):] {
		l := line{text: strings.Join(pieces[r[0]:r[1]], " ")}
		l.tail = len(l.text)
		if s := len(stable); s < r[1] {
			l.tail = 0
			if s > r[0] {
				l.tail = len(strings.Join(pieces[r[0]:s], " ")) + 1
			}
		}
		out = append(out, l)
	}
	return out
}
