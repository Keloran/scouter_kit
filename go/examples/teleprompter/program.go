package main

import (
	"encoding/binary"

	"scouterkit"
	"scouterkit/examples/internal/demo"
)

// State words, from layout.state: one drawing step, written by the host.
const (
	wSeq    = iota // the step's number, NOTIFYd when it is shown
	wDue           // the device time to show it
	wPos           // the page row at the top of the area after the step (signed)
	wCur           // the line being read: lines above it are dim
	wSplit         // the varint bytes of the current line already read
	wSplitX        // the pen x where the rest of the current line starts
	wDy            // rows the area scrolls up (down if negative) before the lines are drawn
	wFull          // 1: clear the whole bitmap first
	wD0            // rows d0...d1-1 of the area are shown
	wD1
	wN // lines to draw, then their numbers
	wLines
	maxLines   = 16
	stateWords = wLines + maxLines
)

const (
	recordBytes = 128 // u8 varint bytes, u8 0, u16 pen x, the varints
	margin      = 8
	poolWords   = 64
	codeBytes   = 1024
)

// geometry is the script as the program draws it.
type geometry struct {
	w, h          int // the text area
	lineH, ascent int // a line's box and its baseline in it
	mark          int // the reading row: the current line's top
	total, ring   int // lines of the script; lines the ring holds
	dim           int // the level of what was read
}

func (g geometry) bw() int    { return g.w + 2 }     // the bitmap: DRAW takes w + 1 columns, an odd width
func (g geometry) guard() int { return 2 * g.lineH } // the next line is drawn here before it comes into view
func (g geometry) rows() int  { return g.h + g.guard() }

// target is the page row that puts line k at the reading row.
func (g geometry) target(k int) int { return k*g.lineH - g.mark }

type layout struct{ base, state, area, ring, end uint32 }

func newLayout(base uint32, g geometry) layout {
	l := layout{base: base, state: base + codeBytes}
	l.area = l.state + 4*stateWords
	l.ring = l.area + uint32(g.bw()/2*g.rows())
	l.end = l.ring + uint32(g.ring*recordBytes)
	return l
}

func (l layout) word(i int) scouterkit.Word { return scouterkit.Word(l.state/4 + uint32(i)) }

// program draws the steps the host computes, so that each arm does only
// what changed. USER0 prepares a step in the bitmap: it scrolls by wDy rows
// with one BLIT (or clears it all, wFull) and clears the rows that came in,
// then draws the wN lines listed, each in its own box: lines above wCur at
// g.dim, the current one dim up to wSplit. The next TIMER, at wDue, DRAWs
// rows wD0...wD1-1 at panel (x, y + wD0) and NOTIFYs (wSeq, the low 24 bits
// of the device time the DRAW began), so both eyes change at the same time.
// Nothing runs between steps.
func program(l layout, g geometry, font uint32, x, y int) *scouterkit.Program {
	p := scouterkit.NewProgramAt(l.base, poolWords)
	p.PanelPolicy(scouterkit.NoLabel)
	c := p.Const
	r := scouterkit.R
	n := func(v int) scouterkit.Word { return c(uint32(int32(v))) }
	seq, due, pos, cur, split := l.word(wSeq), l.word(wDue), l.word(wPos), l.word(wCur), l.word(wSplit)
	splitX, dy, full, d0, d1, count := l.word(wSplitX), l.word(wDy), l.word(wFull), l.word(wD0), l.word(wD1), l.word(wN)
	timer, now := n(int(scouterkit.PropTimer)), n(int(scouterkit.PropTimeUS))
	area, stride, lineH, bw, rows := c(l.area), n(g.bw()/2), n(g.lineH), n(g.bw()), n(g.rows())
	dim, bright, mask := n(g.dim), c(15), c(2)

	line, skip, before, rest := p.Label(), p.Label(), p.Label(), p.Label()
	p.Bind(line) // r1 = the line; uses r2...r6
	p.BltS(r(1), c(0), skip)
	p.BgeS(r(1), n(g.total), skip)
	p.Mul(r(6), r(1), lineH)
	p.Sub(r(6), r(6), pos)
	p.Fill(area, stride, c(0), r(6), bw, lineH, c(0))
	p.Add(r(6), r(6), n(g.ascent))
	p.ModU(r(2), r(1), n(g.ring))
	p.Mul(r(2), r(2), n(recordBytes))
	p.Add(r(2), r(2), c(l.ring))
	p.Load(8, r(3), r(2), c(0))
	p.Load(16, r(4), r(2), c(1))
	p.Add(r(5), r(2), c(4))
	p.BltS(r(1), cur, before)
	p.Beq(r(1), cur, rest)
	p.Glyphs(c(font), area, stride, r(4), r(6), mask, bright, r(5), r(3))
	p.Ret()
	p.Bind(before)
	p.Glyphs(c(font), area, stride, r(4), r(6), mask, dim, r(5), r(3))
	p.Ret()
	p.Bind(rest)
	p.Glyphs(c(font), area, stride, r(4), r(6), mask, dim, r(5), split)
	p.Add(r(5), r(5), split)
	p.Sub(r(3), r(3), split)
	p.Glyphs(c(font), area, stride, splitX, r(6), mask, bright, r(5), r(3))
	p.Bind(skip)
	p.Ret()

	prepare, partial, down, lines, next, drawn := p.Label(), p.Label(), p.Label(), p.Label(), p.Label(), p.Label()
	p.Bind(prepare)
	p.Beq(full, c(0), partial)
	p.Fill(area, stride, c(0), c(0), bw, rows, c(0))
	p.Jmp(lines)
	p.Bind(partial)
	p.Beq(dy, c(0), lines)
	p.BltS(dy, c(0), down)
	p.Sub(r(2), rows, dy)
	p.Blit(area, stride, c(0), dy, area, stride, c(0), c(0), bw, r(2), c(0), c(15))
	p.Fill(area, stride, c(0), r(2), bw, dy, c(0))
	p.Jmp(lines)
	p.Bind(down)
	p.Sub(r(2), c(0), dy)
	p.Sub(r(3), rows, r(2))
	p.Blit(area, stride, c(0), c(0), area, stride, c(0), r(2), bw, r(3), c(0), c(15))
	p.Fill(area, stride, c(0), c(0), bw, r(2), c(0))
	p.Bind(lines)
	p.Mov(r(7), c(0))
	p.Bind(next)
	p.BgeU(r(7), count, drawn)
	p.Load(32, r(1), c(l.state+4*wLines), r(7))
	p.Call(line)
	p.Add(r(7), r(7), c(1))
	p.Jmp(next)
	p.Bind(drawn)
	p.Fill(area, stride, c(0), c(0), c(3), rows, c(0))
	p.Fill(area, stride, c(0), n(g.mark+2), c(3), n(g.lineH-4), dim)
	p.SysWrite(timer, due)
	p.End()

	show := p.Label()
	p.Bind(show)
	p.SysRead(r(9), now)
	p.Sub(r(10), d1, d0)
	p.Add(r(11), d0, n(y))
	p.Draw(area, stride, c(0), d0, n(x), r(11), n(g.w+1), r(10))
	p.And(r(9), r(9), n(0xFFFFFF))
	p.Notify(seq, r(9))
	p.End()

	p.On(scouterkit.TriggerUser0, prepare)
	p.On(scouterkit.TriggerTimer, show)
	p.Changes(l.area, l.ring-l.area)
	return p
}

func record(glyphs []uint32) []byte {
	b := make([]byte, recordBytes)
	v := demo.Varints(glyphs)
	b[0] = byte(len(v))
	binary.LittleEndian.PutUint16(b[2:], margin)
	copy(b[4:], v)
	return b
}

// view is what the area shows: the page row at the top, the current line,
// its glyphs already read and the pen x of the rest.
type view struct{ pos, cur, split, splitX int }

// state is how a line is drawn in a view: dim, split, or bright (also the
// current line with nothing read).
func (v view) state(k int) [2]int {
	switch {
	case k < v.cur:
		return [2]int{0, 0}
	case k == v.cur && v.split > 0:
		return [2]int{1, v.split}
	}
	return [2]int{2, 0}
}

// step is what the program does to go from one view to the next.
type step struct {
	full   bool
	dy     int
	lines  []int
	d0, d1 int
}

// planner keeps what the arms' bitmaps hold: the view drawn and the lines
// whose part inside the bitmap is drawn right in it.
type planner struct {
	g     geometry
	drawn view
	valid bool
	right map[int]bool
}

// plan is the step to view b: a scroll of the bitmap by one BLIT (or a
// clear when there is little to keep), then the lines in view or just below
// it that are not drawn right yet (they came in, the scroll uncovered them,
// or how they are drawn changed). A line is drawn only when its box ends
// inside the bitmap, so the line below the area is ready before it scrolls
// into view and a scroll of a few rows needs no line at all.
func (p *planner) plan(b view) step {
	g := p.g
	rows := g.rows()
	box := func(k int) (int, int) { return k*g.lineH - b.pos, (k+1)*g.lineH - b.pos }
	s := step{d0: 0, d1: g.h}
	dy := b.pos - p.drawn.pos
	if !p.valid || dy >= g.h-g.lineH || -dy >= g.h-g.lineH {
		s.full, p.right = true, map[int]bool{}
	} else {
		s.dy = dy
		in0, in1 := rows-dy, rows
		if dy < 0 {
			in0, in1 = 0, -dy
		}
		for k := range p.right {
			top, bottom := box(k)
			if bottom <= 0 || bottom > rows || dy != 0 && top < in1 && bottom > in0 || p.drawn.state(k) != b.state(k) {
				delete(p.right, k)
			}
		}
	}
	for k := floorDiv(b.pos, g.lineH); k*g.lineH-b.pos < g.h+g.lineH; k++ {
		if _, bottom := box(k); k < 0 || k >= g.total || bottom > rows || p.right[k] {
			continue
		}
		s.lines = append(s.lines, k)
		p.right[k] = true
	}
	if !s.full && s.dy == 0 {
		s.d0, s.d1 = g.h, 0
		for _, k := range s.lines {
			top, bottom := box(k)
			if top < g.h && bottom > 0 {
				s.d0, s.d1 = min(s.d0, max(0, top)), max(s.d1, min(g.h, bottom))
			}
		}
		s.d0 = min(s.d0, s.d1)
	}
	p.drawn, p.valid = b, true
	return s
}

// moveBy is how far the area moves toward its target in one step: a part of
// the way, at least a row, at most vmax rows.
func moveBy(d, vmax int) int {
	if d == 0 {
		return 0
	}
	v := max(1, min(vmax, (abs(d)*35+99)/100))
	if d < 0 {
		return -v
	}
	return v
}

func floorDiv(a, b int) int {
	if a < 0 {
		return -((-a + b - 1) / b)
	}
	return a / b
}

// page is the area as the program draws it in view v.
func page(f *demo.Font, g geometry, lines []line, v view) *demo.Image {
	m := demo.NewImage(g.w+1, g.h)
	for k := floorDiv(v.pos, g.lineH); k*g.lineH-v.pos < g.h; k++ {
		if k < 0 || k >= len(lines) {
			continue
		}
		y := k*g.lineH + g.ascent - v.pos
		gl := lines[k].glyphs
		switch {
		case k < v.cur:
			f.Draw(m, margin, y, gl, uint8(g.dim))
		case k == v.cur:
			f.Draw(m, margin, y, gl[:v.split], uint8(g.dim))
			f.Draw(m, v.splitX, y, gl[v.split:], 15)
		default:
			f.Draw(m, margin, y, gl, 15)
		}
	}
	m.Fill(0, 0, 3, g.h, 0)
	m.Fill(0, g.mark+2, 3, g.lineH-4, uint8(g.dim))
	return m
}
