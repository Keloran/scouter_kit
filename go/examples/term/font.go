package main

import (
	_ "embed"
	"encoding/binary"
	"sort"
	"strings"
)

// The terminal's font: Departure Mono Nerd Font Mono (OFL-1.1; its icons under
// their own licences, ../../../third_party/departure_mono_nerd), converted at the
// built-in Departure Mono's 7 x 14 cell and cut to the cell by
// font_converter.py --cell. It has all braille patterns and block elements
// (the built-in font lacks the braille, which --cell would draw).
//
//go:generate python3 ../../../font_converter/font_converter.py ../../../third_party/departure_mono_nerd/DepartureMonoNerdFontMono-Regular.otf --size 11 --supersample 8 --cell -o fonts/departure_mono_nerd.skf
//go:embed fonts/departure_mono_nerd.skf
var nerdFont []byte

// baseRanges go to the arms at the start, as far as the font has them: text,
// symbols, line drawing, block elements, braille and the powerline glyphs.
// The rest (icons, other scripts) goes as the screen shows it.
var baseRanges = [][2]rune{{0x20, 0x7e}, {0xa0, 0x24f}, {0x370, 0x4ff}, {0x2000, 0x23ff}, {0x2500, 0x27bf},
	{0x2800, 0x28ff}, {0xe0a0, 0xe0d7}}

// codepoints are the characters of a font blob (VM.md §4.2).
func codepoints(blob []byte) map[rune]bool {
	n := int(binary.LittleEndian.Uint16(blob[8:]))
	out := make(map[rune]bool, n)
	for i := 0; i < n; i++ {
		if cp := rune(binary.LittleEndian.Uint32(blob[16+12*n+4*i:])); cp != 0 {
			out[cp] = true
		}
	}
	return out
}

func baseSet(has map[rune]bool) map[rune]bool {
	out := map[rune]bool{}
	for r := range has {
		for _, b := range baseRanges {
			if r >= b[0] && r <= b[1] {
				out[r] = true
			}
		}
	}
	return out
}

// chars is the set as the characters LoadFont takes.
func chars(set map[rune]bool) string {
	rs := make([]rune, 0, len(set))
	for r := range set {
		rs = append(rs, r)
	}
	sort.Slice(rs, func(i, j int) bool { return rs[i] < rs[j] })
	var b strings.Builder
	for _, r := range rs {
		b.WriteRune(r)
	}
	return b.String()
}
