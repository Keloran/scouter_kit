package main

import (
	"fmt"

	"scouterkit"
	"scouterkit/examples/internal/demo"
)

// State words, from layout.state. The host writes wTop...wFont.
const (
	wTop     = iota // the slot that holds row 0
	wFull           // 1: present the whole area, else the dirty slots
	wCurSlot        // the slot whose row has the cursor (none: 0xFFFFFFFF)
	wCurX           // the cursor's column
	wShowAt         // device time of the present
	wFrame          // NOTIFYd after the present
	wFont           // the font's address
	wNext           // the next slot to render
	stateWords
)

const (
	recordBytes = 512 // u8 runs; each u8 x, u8 cells, u8 bg<<4|fg, u8 1: underline, u8 n, n varint bytes
	maxRuns     = 32
	rowsPerTask = 6 // rows rendered per activation, within the jump budget
	codeBytes   = 1024
	poolWords   = 64
	noCursor    = 0xFFFFFFFF
)

// geometry is the terminal as the arms draw it: cols x rows cells of cw x
// ch pixels, the baseline ascent below a cell's top, at (x, y) of the
// virtual display.
type geometry struct {
	cols, rows int
	cw, ch     int
	ascent     int
	x, y       int
}

// w is the area's width in pixels, even (4-bpp bytes).
func (g geometry) w() int { return (g.cols*g.cw + 1) &^ 1 }

// The arena: code, state, a dirty byte per slot, the slots' records, and
// the area's bitmap, a ring of slots: row r is in slot (r + wTop) % rows,
// so a scroll moves wTop and redraws the rows that came in.
type layout struct{ base, state, dirty, ring, area, end uint32 }

func newLayout(base uint32, g geometry) layout {
	l := layout{base: base, state: base + codeBytes}
	l.dirty = l.state + 4*stateWords
	l.ring = l.dirty + uint32((g.rows+3)&^3)
	l.area = l.ring + uint32(g.rows*recordBytes)
	l.end = l.area + uint32(g.w()/2*g.rows*g.ch)
	return l
}

func (l layout) word(i int) scouterkit.Word { return scouterkit.Word(l.state/4 + uint32(i)) }

// program draws the terminal at panel (px, py). USER0 (the host wrote a
// frame) renders the dirty slots into the area, rowsPerTask per activation
// (it RAISEs USER0 again for the rest), then RAISEs USER1, which waits for
// wShowAt, DRAWs the dirty rows (or the whole area, from wTop on) and
// NOTIFYs (wFrame, 0).
func program(l layout, g geometry, px, py int) *scouterkit.Program {
	p := scouterkit.NewProgramAt(l.base, poolWords)
	p.PanelPolicy(scouterkit.NoLabel)
	c := p.Const
	r := scouterkit.R
	n := func(v int) scouterkit.Word { return c(uint32(int32(v))) }
	w := l.word
	top, full, curSlot, curX, showAt, frame, font, next := w(wTop), w(wFull), w(wCurSlot), w(wCurX), w(wShowAt),
		w(wFrame), w(wFont), w(wNext)
	area, stride := c(l.area), n(g.w()/2)

	render, runs, noBg, noLine, runsDone, noCur := p.Label(), p.Label(), p.Label(), p.Label(), p.Label(), p.Label()
	p.Bind(render) // r1 = the slot; uses r2...r13
	p.Mul(r(2), r(1), n(recordBytes))
	p.Add(r(2), r(2), c(l.ring))
	p.Mul(r(3), r(1), n(g.ch))
	p.Fill(area, stride, c(0), r(3), n(g.w()), n(g.ch), c(0))
	p.Load(8, r(4), r(2), c(0))
	p.Add(r(5), r(2), c(1))
	p.Bind(runs)
	p.Beq(r(4), c(0), runsDone)
	p.Load(8, r(6), r(5), c(0))
	p.Mul(r(6), r(6), n(g.cw))
	p.Load(8, r(7), r(5), c(1))
	p.Mul(r(7), r(7), n(g.cw))
	p.Load(8, r(8), r(5), c(2))
	p.Shr(r(9), r(8), c(4))
	p.And(r(8), r(8), c(15))
	p.Beq(r(9), c(0), noBg)
	p.Fill(area, stride, r(6), r(3), r(7), n(g.ch), r(9))
	p.Bind(noBg)
	p.Load(8, r(10), r(5), c(3))
	p.Load(8, r(11), r(5), c(4))
	p.Add(r(12), r(5), c(5))
	p.Add(r(13), r(3), n(g.ascent))
	p.Glyphs(font, area, stride, r(6), r(13), c(2), r(8), r(12), r(11))
	p.Beq(r(10), c(0), noLine)
	p.Add(r(13), r(3), n(g.ch-1))
	p.Fill(area, stride, r(6), r(13), r(7), c(1), r(8))
	p.Bind(noLine)
	p.Add(r(5), r(12), r(11))
	p.Sub(r(4), r(4), c(1))
	p.Jmp(runs)
	p.Bind(runsDone)
	p.Bne(r(1), curSlot, noCur)
	p.Mul(r(6), curX, n(g.cw))
	p.Add(r(13), r(3), n(g.ch-2))
	p.Fill(area, stride, r(6), r(13), n(g.cw), c(2), c(15))
	p.Bind(noCur)
	p.Ret()

	chunk, scan, rendered := p.Label(), p.Label(), p.Label()
	p.Bind(chunk)
	p.Mov(r(0), c(0))
	p.Bind(scan)
	p.BgeU(next, n(g.rows), rendered)
	p.Mov(r(1), next)
	p.Add(next, next, c(1))
	p.Load(8, r(2), c(l.dirty), r(1))
	p.Beq(r(2), c(0), scan)
	p.Call(render)
	p.Add(r(0), r(0), c(1))
	p.BltU(r(0), n(rowsPerTask), scan)
	p.Raise(c(uint32(scouterkit.TriggerUser0)))
	p.End()
	p.Bind(rendered)
	p.Mov(next, c(0))
	p.Raise(c(uint32(scouterkit.TriggerUser1)))
	p.End()

	present, slots, skip, shown := p.Label(), p.Label(), p.Label(), p.Label()
	p.Bind(present)
	p.SleepUntil(showAt)
	p.Beq(full, c(0), slots)
	p.Sub(r(1), n(g.rows), top)
	p.Mul(r(1), r(1), n(g.ch))
	p.Mul(r(2), top, n(g.ch))
	p.Draw(area, stride, c(0), r(2), n(px), n(py), n(g.w()), r(1))
	p.Add(r(3), r(1), n(py))
	p.Draw(area, stride, c(0), c(0), n(px), r(3), n(g.w()), r(2))
	p.Jmp(shown)
	p.Bind(slots)
	p.Mov(r(1), c(0))
	p.Bind(skip)
	p.BgeU(r(1), n(g.rows), shown)
	p.Load(8, r(2), c(l.dirty), r(1))
	p.Add(r(1), r(1), c(1))
	p.Beq(r(2), c(0), skip)
	p.Sub(r(3), r(1), c(1))
	p.Mul(r(4), r(3), n(g.ch))
	p.Add(r(3), r(3), n(g.rows))
	p.Sub(r(3), r(3), top)
	p.ModU(r(3), r(3), n(g.rows))
	p.Mul(r(3), r(3), n(g.ch))
	p.Add(r(3), r(3), n(py))
	p.Draw(area, stride, c(0), r(4), n(px), r(3), n(g.w()), n(g.ch))
	p.Jmp(skip)
	p.Bind(shown)
	p.Notify(frame, c(0))
	p.End()

	p.On(scouterkit.TriggerUser0, chunk)
	p.On(scouterkit.TriggerUser1, present)
	p.Changes(l.state+4*wNext, 4)
	p.Changes(l.area, l.end-l.area)
	return p
}

// row is a screen row as the arms draw it: its record and the cursor's
// column (-1: not in this row).
type row struct {
	rec    []byte
	cursor int
}

func (r row) key() string { return fmt.Sprintf("%d:%s", r.cursor, r.rec) }

// draw renders the row the way the program renders a slot.
func (r row) draw(f *demo.Font, g geometry) *demo.Image {
	img := demo.NewImage(g.w(), g.ch)
	rec := r.rec
	at := 1
	for i := 0; i < int(rec[0]); i++ {
		x, cells, st, line, nb := int(rec[at]), int(rec[at+1]), rec[at+2], rec[at+3], int(rec[at+4])
		fg, bg := st&15, st>>4
		px, pw := x*g.cw, cells*g.cw
		if bg != 0 {
			img.Fill(px, 0, pw, g.ch, bg)
		}
		f.Draw(img, px, g.ascent, unvarints(rec[at+5:at+5+nb]), fg)
		if line != 0 {
			img.Fill(px, g.ch-1, pw, 1, fg)
		}
		at += 5 + nb
	}
	if r.cursor >= 0 {
		img.Fill(r.cursor*g.cw, g.ch-2, g.cw, 2, 15)
	}
	return img
}

func unvarints(b []byte) []uint32 {
	var out []uint32
	var v uint32
	var shift uint
	for _, c := range b {
		v |= uint32(c&0x7f) << shift
		shift += 7
		if c&0x80 == 0 {
			out = append(out, v)
			v, shift = 0, 0
		}
	}
	return out
}
