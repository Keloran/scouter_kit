package main

// ansi is the xterm palette's first 16 colours.
var ansi = [16][3]int{
	{0, 0, 0}, {205, 0, 0}, {0, 205, 0}, {205, 205, 0}, {0, 0, 238}, {205, 0, 205}, {0, 205, 205}, {229, 229, 229},
	{127, 127, 127}, {255, 0, 0}, {0, 255, 0}, {255, 255, 0}, {92, 92, 255}, {255, 0, 255}, {0, 255, 255},
	{255, 255, 255},
}

func rgb(c color) (int, int, int) {
	if c&direct != 0 {
		return int(c >> 16 & 255), int(c >> 8 & 255), int(c & 255)
	}
	i := int(c) - 1
	switch {
	case i < 16:
		return ansi[i][0], ansi[i][1], ansi[i][2]
	case i < 232:
		i -= 16
		steps := [6]int{0, 95, 135, 175, 215, 255}
		return steps[i/36], steps[i/6%6], steps[i%6]
	}
	v := 8 + 10*(i-232)
	return v, v, v
}

const defaultLevel = 12

// levels maps a style to panel levels: the text's from its colour's
// brightest channel (5...15: a saturated colour stands out, grey is dimmer,
// a dark colour stays visible) and the background's from its luma (0...9),
// then reverse video, and enough contrast between the two.
func levels(st style) (fg, bg uint8, line bool) {
	fg, bg = defaultLevel, 0
	if st.fg != 0 {
		r, g, b := rgb(st.fg)
		fg = uint8(5 + (max(r, g, b)*10+127)/255)
	}
	if st.bg != 0 {
		bg = uint8((luma(st.bg)*9 + 127) / 255)
	}
	if st.attr&attrBold != 0 {
		fg = min(15, fg+3)
	}
	if st.attr&attrFaint != 0 {
		fg = max(4, fg*2/3)
	}
	if st.attr&attrReverse != 0 {
		fg, bg = bg, fg
	}
	if st.attr&attrInvisible != 0 {
		fg = bg
	} else if bg > 0 && max(fg, bg)-min(fg, bg) < 6 {
		if bg >= 8 {
			fg = 0
		} else {
			fg = min(15, bg+6)
		}
	}
	return fg, bg, st.attr&attrUnderline != 0
}

func luma(c color) int {
	r, g, b := rgb(c)
	return (2126*r + 7152*g + 722*b) / 10000
}

// encode is a row's record: runs of cells with equal levels, blank cells
// (spaces without a background or underline) left out between runs; glyph
// gives a cell's glyph index.
func encode(cells []cell, glyph func(rune) uint32) []byte {
	out := []byte{0}
	blank := func(c cell, bg uint8, line bool) bool {
		_, b, l := levels(c.st)
		return c.r == ' ' && b == 0 && !l && bg == 0 && !line
	}
	for x := 0; x < len(cells) && out[0] < maxRuns; {
		fg, bg, line := levels(cells[x].st)
		if blank(cells[x], bg, line) {
			x++
			continue
		}
		last := out[0] == maxRuns-1 // takes the rest of the row as it is
		end, ink := x, x
		for end < len(cells) && end-x < 80 {
			c := cells[end]
			if blank(c, bg, line) {
				if end-ink >= 7 && !last {
					break
				}
				end++
				continue
			}
			if f, b, l := levels(c.st); !last && (f != fg || b != bg || l != line) {
				break
			}
			end++
			ink = end
		}
		end = ink
		var text []uint32
		for _, c := range cells[x:end] {
			text = append(text, glyph(c.r))
		}
		v := varints(text)
		line8 := byte(0)
		if line {
			line8 = 1
		}
		out = append(out, byte(x), byte(end-x), bg<<4|fg, line8, byte(len(v)))
		out = append(out, v...)
		out[0]++
		x = end
	}
	return out
}

func varints(glyphs []uint32) []byte {
	var out []byte
	for _, g := range glyphs {
		for g >= 0x80 {
			out = append(out, byte(g)|0x80)
			g >>= 7
		}
		out = append(out, byte(g))
	}
	return out
}

// screen is what the arms hold: the row each slot was last rendered with,
// and the slot of row 0.
type screen struct {
	slots []string
	rows  []row
	top   int
	fresh bool // the next frame renders and presents everything
}

// frame is one update for the arms.
type frame struct {
	top, curSlot, curX int
	full               bool
	dirty              []byte
	recs               map[int][]byte
}

// next is the frame that brings the arms to these rows; scrolled is how
// many rows the screen scrolled up meanwhile: the ring turns by as much if
// that leaves fewer rows to send. It returns false when nothing changed.
func (s *screen) next(rows []row, scrolled int) (frame, bool) {
	n := len(rows)
	changed := func(k int) int {
		d := 0
		for y, r := range rows {
			if s.slots[(y+s.top+k)%n] != r.key() {
				d++
			}
		}
		return d
	}
	k := 0
	if scrolled > 0 && scrolled < n && !s.fresh && changed(scrolled) < changed(0) {
		k = scrolled
	}
	s.top = (s.top + k) % n
	f := frame{top: s.top, curSlot: -1, dirty: make([]byte, n), recs: map[int][]byte{}}
	for y, r := range rows {
		slot := (y + s.top) % n
		if key := r.key(); s.fresh || s.slots[slot] != key {
			f.dirty[slot] = 1
			f.recs[slot] = r.rec
			s.slots[slot] = key
		}
		if r.cursor >= 0 {
			f.curSlot, f.curX = slot, r.cursor
		}
	}
	f.full = s.fresh || k != 0 || len(f.recs) > n/2
	s.fresh = false
	s.rows = rows
	return f, k != 0 || len(f.recs) > 0
}
