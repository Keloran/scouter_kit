package main

import (
	"encoding/binary"

	"scouterkit"
	"scouterkit/examples/internal/demo"
)

// State words, from layout.state.
const (
	wNext      = iota // the next word
	wDue              // its device time
	wMode             // 1: step mode (USER3 shows one word, no timer)
	wFin              // 1: the next tick switches the panel off
	wAvail            // words the host has put in the ring: those below this one
	wShown            // the band on the panel
	wReady            // the band holding word wNext, 0 if none
	wWaiting          // device time since which it waits for the host (0: it does not)
	stateWords = 8
)

// NOTIFY kinds (b = kind << 24).
const (
	progress = iota
	waiting
	ended
	stepDone
	started
)

const (
	recordBytes = 48 // u32 µs, i32 x0, x1, x2, u8 n0, n1, n2, 0, then the varints
	textBytes   = recordBytes - 20
	poolWords   = 48
	codeBytes   = 1024
	tick        = 8 // rows above and below the text for the focus marks
	pad         = 4
)

// geometry is the band and the ring as the program uses them.
type geometry struct {
	w, h       int // the band (w even)
	baseline   int
	focus      int // x of the ORP letter's centre
	marks      bool
	side, mid  uint8 // levels of the runs around the ORP letter and of the letter
	total      int
	ring       int
	retry, end uint32 // µs: while the ring is empty, after the last word
	maxWait    uint32 // µs with the ring empty, then the end
}

func newGeometry(f *scouterkit.Font, w int) geometry {
	g := geometry{w: w &^ 1, side: 15, mid: 15, retry: 200000, end: 5000000}
	g.h = 2*(tick+pad) + f.Ascent + f.Descent
	g.baseline = tick + pad + f.Ascent
	g.focus = g.w * 2 / 5
	return g
}

func (g geometry) guard() int { return g.h }

// stride is a byte more than the band's pixels need, so that the DRAW is an
// odd number of pixels wide (one panel window per row).
func (g geometry) stride() int { return g.w/2 + 1 }

func (g geometry) bandBytes() uint32 { return uint32(g.stride() * (g.h + g.guard())) }

type layout struct{ base, state, band, ring, end uint32 }

// newLayout puts two bands after the state, each with guard rows under it
// (a bitmap reaches down to the end of the arena, and the glyphs of a large
// font may reach below the band), then the ring.
func newLayout(base uint32, g geometry) layout {
	l := layout{base: base, state: base + codeBytes}
	l.band = l.state + 4*stateWords
	l.ring = l.band + 2*g.bandBytes()
	l.end = l.ring + uint32(g.ring*recordBytes)
	return l
}

func (l layout) word(i int) scouterkit.Word { return scouterkit.Word(l.state/4 + uint32(i)) }

// program shows the words in a band at panel (x, y). START draws both
// bands' focus marks, shows one and prepares the first word in the other.
// Each TIMER (USER3 in step mode) DRAWs the band prepared for this word
// first, so both eyes change at its time, then arms TIMER for the next word
// and prepares it in the other band: clears its text rows and draws the
// word's three runs. It NOTIFYs (next word, kind << 24): kind 0 every 32
// words, 1 while the ring is empty, 2 at the end (also after a minute with
// the ring empty), 3 after every word in step mode, 4 once START has drawn
// the band.
func program(l layout, g geometry, font uint32, x, y int) *scouterkit.Program {
	p := scouterkit.NewProgramAt(l.base, poolWords)
	p.PanelPolicy(scouterkit.NoLabel)
	c := p.Const
	r := scouterkit.R
	n := func(v int) scouterkit.Word { return c(uint32(v)) }
	at := func(v int) scouterkit.Word { return c(uint32(int32(v))) }
	next, due, mode, fin, avail := l.word(wNext), l.word(wDue), l.word(wMode), l.word(wFin), l.word(wAvail)
	shown, ready, waitSince := l.word(wShown), l.word(wReady), l.word(wWaiting)
	timer, panel, now := n(int(scouterkit.PropTimer)), n(int(scouterkit.PropPanel)), n(int(scouterkit.PropTimeUS))
	band0, band1, stride := c(l.band), c(l.band+g.bandBytes()), n(g.stride())

	prepare, prepared := p.Label(), p.Label()
	p.Bind(prepare) // word wNext into the band not shown; uses r1...r11
	p.Mov(ready, c(0))
	p.BgeU(next, avail, prepared)
	p.Add(r(11), band0, band1)
	p.Sub(r(11), r(11), shown)
	p.ModU(r(1), next, n(g.ring))
	p.Mul(r(1), r(1), n(recordBytes))
	p.Add(r(1), r(1), c(l.ring))
	p.Fill(r(11), stride, c(0), n(tick), n(g.w), n(g.h-2*tick), c(0))
	p.Add(r(8), r(1), n(20))
	for k, level := range []uint8{g.side, g.mid, g.side} {
		p.Load(32, r(2), r(1), n(1+k))
		p.Load(8, r(5), r(1), n(16+k))
		p.Glyphs(c(font), r(11), stride, r(2), n(g.baseline), c(2), n(int(level)), r(8), r(5))
		p.Add(r(8), r(8), r(5))
	}
	p.Mov(ready, r(11))
	p.Bind(prepared)
	p.Ret()

	start, schedule, done, finished := p.Label(), p.Label(), p.Label(), p.Label()
	p.Bind(start)
	for _, b := range []scouterkit.Word{band0, band1} {
		p.Fill(b, stride, c(0), c(0), n(g.w), n(g.h), c(0))
		if g.marks {
			p.Fill(b, stride, n(g.focus-1), c(1), c(2), n(tick-2), c(6))
			p.Fill(b, stride, n(g.focus-1), n(g.h-tick+1), c(2), n(tick-2), c(6))
		}
	}
	p.Draw(band0, stride, c(0), c(0), at(x), at(y), n(g.w+1), n(g.h))
	p.Mov(shown, band0)
	p.Call(prepare)
	p.Notify(next, n(started<<24))
	p.Jmp(schedule)

	show, have, notified, finish, off := p.Label(), p.Label(), p.Label(), p.Label(), p.Label()
	p.Bind(show)
	p.Bne(fin, c(0), off)
	p.Bne(ready, c(0), have)
	p.Call(prepare)
	p.Bne(ready, c(0), have)
	p.BgeU(next, n(g.total), finish)
	waited := p.Label()
	p.SysRead(r(10), now)
	p.Bne(waitSince, c(0), waited)
	p.Mov(waitSince, r(10))
	p.Bind(waited)
	p.Sub(r(11), r(10), waitSince)
	p.BgeU(r(11), n(int(g.maxWait)), finish)
	p.Add(due, r(10), n(int(g.retry)))
	p.Notify(next, n(waiting<<24))
	p.Jmp(schedule)
	p.Bind(have)
	p.Mov(waitSince, c(0))
	p.Draw(ready, stride, c(0), c(0), at(x), at(y), n(g.w+1), n(g.h))
	p.Mov(shown, ready)
	p.ModU(r(1), next, n(g.ring))
	p.Mul(r(1), r(1), n(recordBytes))
	p.Add(r(1), r(1), c(l.ring))
	p.Load(32, r(9), r(1), c(0))
	p.Add(due, due, r(9))
	p.Add(next, next, c(1))
	p.Beq(mode, c(0), notified)
	p.Notify(next, n(stepDone<<24))
	p.Call(prepare)
	p.Jmp(schedule)
	p.Bind(notified)
	p.SysWrite(timer, due)
	p.And(r(10), next, n(31))
	p.Bne(r(10), c(0), done)
	p.Notify(next, n(progress<<24))
	p.Bind(done)
	p.Call(prepare)
	p.End()
	p.Bind(schedule)
	p.Bne(mode, c(0), finished)
	p.SysWrite(timer, due)
	p.Bind(finished)
	p.End()
	p.Bind(finish)
	p.Mov(fin, c(1))
	p.Notify(next, n(ended<<24))
	p.SysRead(r(10), now)
	p.Add(due, r(10), n(int(g.end)))
	p.Jmp(schedule)
	p.Bind(off)
	p.SysWrite(panel, c(0))
	p.End()

	sync := p.Label()
	p.Bind(sync)
	p.Jmp(schedule)

	p.On(scouterkit.TriggerStart, start)
	p.On(scouterkit.TriggerTimer, show)
	p.On(scouterkit.TriggerUser3, show)
	p.On(scouterkit.TriggerUser0, sync)
	p.Changes(l.state, 4*stateWords)
	p.Changes(l.band, l.ring-l.band)
	return p
}

func record(w word) []byte {
	b := make([]byte, recordBytes)
	binary.LittleEndian.PutUint32(b, w.micro)
	text := b[20:20]
	for k := range w.runs {
		binary.LittleEndian.PutUint32(b[4+4*k:], uint32(int32(w.x[k])))
		v := demo.Varints(w.runs[k])
		b[16+k] = byte(len(v))
		text = append(text, v...)
	}
	return b
}

// fill puts words from...to-1 in the arm's ring, then tells the program.
func fill(arm scouterkit.Arm, l layout, g geometry, ws []word, from, to int) {
	for k := from; k < to; {
		slot := k % g.ring
		n := min(to-k, g.ring-slot)
		b := make([]byte, 0, n*recordBytes)
		for i := 0; i < n; i++ {
			b = append(b, record(ws[k+i])...)
		}
		arm.WriteArena(l.ring+uint32(slot*recordBytes), b)
		k += n
	}
	arm.WriteArena(l.state+4*wAvail, demo.Words(uint32(to)))
}

// bandImage is the band showing word i (-1: none), as the program draws it.
func bandImage(f *demo.Font, g geometry, ws []word, i int) *demo.Image {
	m := demo.NewImage(g.w, g.h)
	if g.marks {
		m.Fill(g.focus-1, 1, 2, tick-2, 6)
		m.Fill(g.focus-1, g.h-tick+1, 2, tick-2, 6)
	}
	if i >= 0 {
		for k, level := range []uint8{g.side, g.mid, g.side} {
			f.Draw(m, ws[i].x[k], g.baseline, ws[i].runs[k], level)
		}
	}
	return m
}
