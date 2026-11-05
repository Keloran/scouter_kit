package main

import (
	"encoding/binary"

	"scouterkit"
	"scouterkit/examples/internal/demo"
)

// State words, from layout.state.
const (
	wPos       = iota // the page row at the top of the area's bitmap
	wTop              // the last line in it
	wDue              // device time of the next step
	wMode             // 1: step mode (USER3 scrolls one step, no timer)
	wFin              // 1: the next tick switches the panel off
	wAvail            // lines the host has put in the ring: those below this one
	wShown            // the page row at the top of the panel's area
	wReady            // 1: the bitmap holds the next step, not shown yet
	wNoted            // the last line NOTIFYd
	wWaiting          // device time since which it waits for the host (0: it does not)
	stateWords = 11
)

// NOTIFY kinds.
const (
	newLine = iota
	waiting
	ended
	stepDone
)

const (
	recordBytes = 128 // u8 varint bytes, u8 0, u16 pen x, the varints
	margin      = 6
	poolWords   = 48
	codeBytes   = 1536
)

// geometry is the book as the program scrolls it.
type geometry struct {
	w, h          int    // the text area (w even)
	lineH, ascent int    // the font's line spacing and ascent
	step          int    // rows per step
	period        uint32 // µs per step
	total         int    // lines of the book
	ring          int    // lines the ring holds
	endHold       uint32 // µs the last page stays before the panel goes off
	maxWait       uint32 // µs without the next line, then the end
}

func (g geometry) guard() int { return 2 * g.lineH }

// last is the page row at the top when the last line reaches the bottom,
// rounded up to whole steps.
func (g geometry) last() int {
	m := max(0, g.total*g.lineH-g.h)
	return (m + g.step - 1) / g.step * g.step
}

type layout struct{ base, state, area, ring, end uint32 }

// newLayout puts guard rows under the area: a bitmap reaches down to the end
// of the arena, so the lines that come in at the bottom are drawn past it.
func newLayout(base uint32, g geometry) layout {
	l := layout{base: base, state: base + codeBytes}
	l.area = l.state + 4*stateWords
	l.ring = l.area + uint32(g.w/2*(g.h+g.guard()))
	l.end = l.ring + uint32(g.ring*recordBytes)
	return l
}

func (l layout) word(i int) scouterkit.Word { return scouterkit.Word(l.state/4 + uint32(i)) }

// program scrolls the area at panel (x, y). START draws the page at wPos
// and prepares the next step. Each TIMER (USER3 in step mode) first DRAWs
// the step prepared for it, so both eyes change at its time whatever came
// before, then prepares the next one in the same bitmap (the panel keeps
// its own copy): one BLIT moves it up by g.step rows, a FILL clears the rows
// that come in and the lines that reach them are drawn. It NOTIFYs (line,
// kind<<24 | value) with the last line in the bitmap: kind 0 when a new line
// came in (with the low 24 bits of the device time its DRAW began), 1 while
// it waits for the host to put that line in the ring, 2 at the end (also
// after a minute of waiting), 3 after every step in step mode (with the row
// shown).
func program(l layout, g geometry, font uint32, x, y int) *scouterkit.Program {
	p := scouterkit.NewProgramAt(l.base, poolWords)
	p.PanelPolicy(scouterkit.NoLabel)
	c := p.Const
	r := scouterkit.R
	at := func(v int) scouterkit.Word { return c(uint32(int32(v))) }
	n := func(v int) scouterkit.Word { return c(uint32(v)) }
	pos, top, due, mode, fin, avail := l.word(wPos), l.word(wTop), l.word(wDue), l.word(wMode), l.word(wFin), l.word(wAvail)
	shown, ready, noted, waitSince := l.word(wShown), l.word(wReady), l.word(wNoted), l.word(wWaiting)
	timer, panel, now := n(int(scouterkit.PropTimer)), n(int(scouterkit.PropPanel)), n(int(scouterkit.PropTimeUS))
	area, stride := c(l.area), n(g.w/2)

	line, lineEnd := p.Label(), p.Label()
	p.Bind(line) // r1 = the line; uses r2...r6
	p.BltS(r(1), c(0), lineEnd)
	p.BgeS(r(1), avail, lineEnd)
	p.ModU(r(2), r(1), n(g.ring))
	p.Mul(r(2), r(2), n(recordBytes))
	p.Add(r(2), r(2), c(l.ring))
	p.Load(8, r(3), r(2), c(0))
	p.Load(16, r(4), r(2), c(1))
	p.Add(r(5), r(2), c(4))
	p.Mul(r(6), r(1), n(g.lineH))
	p.Add(r(6), r(6), n(g.ascent))
	p.Sub(r(6), r(6), pos)
	p.Glyphs(c(font), area, stride, r(4), r(6), c(2), c(15), r(5), r(3))
	p.Bind(lineEnd)
	p.Ret()

	prepare, ok, last := p.Label(), p.Label(), p.Label()
	p.Bind(prepare) // the next step into the bitmap: wReady 1, or 0 and r8 = the line it waits for (0xFFFFFFFF: the end)
	p.Mov(ready, c(0))
	p.Add(r(9), pos, n(g.step))
	p.BltS(n(g.last()), r(9), last)
	p.Add(r(8), r(9), n(g.h-1))
	p.DivU(r(8), r(8), n(g.lineH))
	p.BltS(r(8), avail, ok)
	p.BgeS(avail, n(g.total), ok)
	p.Ret()
	p.Bind(ok)
	p.Mov(pos, r(9))
	if g.step < g.h {
		p.Blit(area, stride, c(0), n(g.step), area, stride, c(0), c(0), n(g.w), n(g.h-g.step), c(0), c(15))
	}
	p.Fill(area, stride, c(0), n(max(0, g.h-g.step)), n(g.w), n(min(g.step, g.h)), c(0))
	p.Sub(r(1), r(8), c(2))
	p.Call(line)
	p.Add(r(1), r(1), c(1))
	p.Call(line)
	p.Add(r(1), r(1), c(1))
	p.Call(line)
	p.Mov(top, r(8))
	p.Mov(ready, c(1))
	p.Ret()
	p.Bind(last)
	p.Mov(r(8), c(0xFFFFFFFF))
	p.Ret()

	start, more, schedule, done := p.Label(), p.Label(), p.Label(), p.Label()
	p.Bind(start)
	p.Fill(area, stride, c(0), c(0), n(g.w), n(g.h), c(0))
	p.DivU(r(7), pos, n(g.lineH))
	p.Sub(r(1), r(7), c(2))
	p.Add(r(8), pos, n(g.h-1))
	p.DivU(r(8), r(8), n(g.lineH))
	p.Bind(more)
	p.Call(line)
	p.Add(r(1), r(1), c(1))
	p.BgeS(r(8), r(1), more)
	p.SysRead(r(12), now)
	p.Draw(area, stride, c(0), c(0), at(x), at(y), n(g.w), n(g.h))
	p.Mov(shown, pos)
	p.Mov(top, r(8))
	p.Mov(noted, r(8))
	p.And(r(12), r(12), n(0xFFFFFF))
	p.Notify(top, r(12))
	p.Call(prepare)
	p.Jmp(schedule)

	tick, have, stepped, prepared, atEnd, off := p.Label(), p.Label(), p.Label(), p.Label(), p.Label(), p.Label()
	p.Bind(tick)
	p.Bne(fin, c(0), off)
	p.Bne(ready, c(0), have)
	p.Call(prepare)
	p.Bne(ready, c(0), have)
	p.Beq(r(8), c(0xFFFFFFFF), atEnd)
	waited := p.Label()
	p.SysRead(r(10), now)
	p.Bne(waitSince, c(0), waited)
	p.Mov(waitSince, r(10))
	p.Bind(waited)
	p.Sub(r(11), r(10), waitSince)
	p.BgeU(r(11), n(int(g.maxWait)), atEnd)
	p.Add(due, r(10), n(int(g.period)))
	p.Notify(r(8), n(waiting<<24))
	p.Jmp(schedule)
	p.Bind(have)
	p.Mov(waitSince, c(0))
	p.SysRead(r(12), now)
	p.Draw(area, stride, c(0), c(0), at(x), at(y), n(g.w), n(g.h))
	p.Mov(shown, pos)
	p.Mov(ready, c(0))
	p.Add(due, due, n(int(g.period)))
	p.Bne(mode, c(0), stepped)
	p.SysWrite(timer, due)
	p.Beq(top, noted, prepared)
	p.Mov(noted, top)
	p.And(r(12), r(12), n(0xFFFFFF))
	p.Notify(top, r(12))
	p.Bind(prepared)
	p.Call(prepare)
	p.End()
	p.Bind(stepped)
	p.Add(r(10), shown, n(stepDone<<24))
	p.Notify(top, r(10))
	p.Call(prepare)
	p.End()
	p.Bind(schedule)
	p.Bne(mode, c(0), done)
	p.SysWrite(timer, due)
	p.Bind(done)
	p.End()
	p.Bind(atEnd)
	p.Mov(fin, c(1))
	p.Notify(top, n(ended<<24))
	p.SysRead(r(10), now)
	p.Add(due, r(10), n(int(g.endHold)))
	p.Jmp(schedule)
	p.Bind(off)
	p.SysWrite(panel, c(0))
	p.End()

	sync := p.Label()
	p.Bind(sync)
	p.Jmp(schedule)

	p.On(scouterkit.TriggerStart, start)
	p.On(scouterkit.TriggerTimer, tick)
	p.On(scouterkit.TriggerUser3, tick)
	p.On(scouterkit.TriggerUser0, sync)
	p.Changes(l.state, 4*stateWords)
	p.Changes(l.area, l.ring-l.area)
	return p
}

func record(ln line) []byte {
	b := make([]byte, recordBytes)
	v := demo.Varints(ln.glyphs)
	b[0] = byte(len(v))
	binary.LittleEndian.PutUint16(b[2:], margin)
	copy(b[4:], v)
	return b
}

// fill puts lines from...to-1 in the arm's ring, then tells the program.
func fill(arm scouterkit.Arm, l layout, g geometry, lines []line, from, to int) {
	for k := from; k < to; {
		slot := k % g.ring
		n := min(to-k, g.ring-slot)
		b := make([]byte, 0, n*recordBytes)
		for i := 0; i < n; i++ {
			b = append(b, record(lines[k+i])...)
		}
		arm.WriteArena(l.ring+uint32(slot*recordBytes), b)
		k += n
	}
	arm.WriteArena(l.state+4*wAvail, demo.Words(uint32(to)))
}

// page is the area showing page rows pos...pos+h-1, as the program draws it.
func page(f *demo.Font, g geometry, lines []line, pos int) *demo.Image {
	m := demo.NewImage(g.w, g.h)
	for k := pos/g.lineH - 2; k <= (pos+g.h-1)/g.lineH; k++ {
		if k >= 0 && k < len(lines) {
			f.Draw(m, margin, k*g.lineH+g.ascent-pos, lines[k].glyphs, 15)
		}
	}
	return m
}
