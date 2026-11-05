package main

import (
	"encoding/binary"

	"scouterkit"
)

// State words, from layout.state.
const (
	wFrames = iota // frames drawn
	wYaw           // degrees, 0...359
	wDue           // device time of the next frame
	wMode          // 1: step mode (USER3 draws one frame, no timer)
	wFin           // 1: the next tick switches the panel off
	wLast          // stop after this many frames (0: never)
	wMaster        // 1: the angles come from wMailPitch, wMailRoll (the host forwards the other arm's)
	wMailPitch
	wMailRoll
	wPitch // the angles of the last frame
	wRoll
	wSinA // sin and cos of pitch, roll and yaw, Q14
	wCosA
	wSinB
	wCosB
	wSinY
	wCosY
	wTop // rows the cube covers in this frame...
	wBottom
	wLastTop // ...and in the one on the panel
	wLastBottom
	stateWords
)

const (
	poolWords = 48
	codeBytes = 1536
)

// layout from base: the program, its state, the sine table, the vertices,
// the edges, the projected points and the bitmap (a byte wider than its
// pixels, so that the DRAW is an odd number of pixels wide: one panel window
// per row; and guard rows, since a bitmap reaches down to the end of the
// arena).
type layout struct{ base, state, sines, vertices, edges, px, py, bitmap, end uint32 }

func newLayout(base uint32, g geometry) layout {
	l := layout{base: base, state: base + codeBytes}
	l.sines = l.state + 4*stateWords
	l.vertices = l.sines + 4*450
	l.edges = l.vertices + 4*24
	l.px = l.edges + 24
	l.py = l.px + 4*8
	l.bitmap = l.py + 4*8
	l.end = l.bitmap + uint32(g.stride()*(g.size+8))
	return l
}

func (l layout) word(i int) scouterkit.Word { return scouterkit.Word(l.state/4 + uint32(i)) }

// tables are the sines, the vertices and the edges, from l.sines to l.px.
func tables(l layout, g geometry) []byte {
	b := make([]byte, 0, l.px-l.sines)
	for _, v := range sines() {
		b = binary.LittleEndian.AppendUint32(b, uint32(v))
	}
	for _, v := range g.vertices() {
		for _, c := range v {
			b = binary.LittleEndian.AppendUint32(b, uint32(c))
		}
	}
	for _, e := range edges {
		b = append(b, byte(e[0]), byte(e[1]))
	}
	return b
}

// program draws the cube at panel (x, y). Each TIMER (USER3 in step mode)
// reads PITCH and ROLL (or the forwarded angles), turns the spin by g.spin,
// rotates and projects the 8 vertices with MUL, SAR and DIVS, clears the
// rows the last cube covered, draws the 12 edges with LINE into the bitmap
// and DRAWs the rows of both cubes, then arms TIMER one period on (from now
// if it is late). START sets IMU_RATE and arms the timer.
func program(l layout, g geometry, x, y int) *scouterkit.Program {
	p := scouterkit.NewProgramAt(l.base, poolWords)
	p.PanelPolicy(scouterkit.NoLabel)
	c := p.Const
	r := scouterkit.R
	n := func(v int) scouterkit.Word { return c(uint32(int32(v))) }
	w := l.word
	prop := func(id scouterkit.Property) scouterkit.Word { return c(uint32(id)) }
	frames, yaw, due, mode, fin, last := w(wFrames), w(wYaw), w(wDue), w(wMode), w(wFin), w(wLast)
	sa, ca, sb, cb, sy, cy := w(wSinA), w(wCosA), w(wSinB), w(wCosB), w(wSinY), w(wCosY)
	bitmap, stride := c(l.bitmap), n(g.stride())

	start, sync, schedule, armed, done := p.Label(), p.Label(), p.Label(), p.Label(), p.Label()
	p.Bind(start)
	p.SysWrite(prop(scouterkit.PropIMURate), n(g.rate))
	p.Bind(sync)
	p.Bne(mode, c(0), done)
	p.SysWrite(prop(scouterkit.PropTimer), due)
	p.Bind(done)
	p.End()

	frame, own, have, pitchOK, rollOK, vertex, edge, timed, off := p.Label(), p.Label(), p.Label(), p.Label(),
		p.Label(), p.Label(), p.Label(), p.Label(), p.Label()
	p.Bind(frame)
	p.Bne(fin, c(0), off)
	p.Beq(w(wMaster), c(0), own)
	p.Mov(r(1), w(wMailPitch))
	p.Mov(r(2), w(wMailRoll))
	p.Jmp(have)
	p.Bind(own)
	p.SysRead(r(1), prop(scouterkit.PropPitch))
	p.SysRead(r(2), prop(scouterkit.PropRoll))
	p.Bind(have)
	p.Bne(r(1), c(scouterkit.Unknown), pitchOK)
	p.Mov(r(1), c(0))
	p.Bind(pitchOK)
	p.Bne(r(2), c(scouterkit.Unknown), rollOK)
	p.Mov(r(2), c(0))
	p.Bind(rollOK)
	p.Mov(w(wPitch), r(1))
	p.Mov(w(wRoll), r(2))
	sines := c(l.sines)
	for _, t := range [][3]scouterkit.Word{{r(1), sa, ca}, {r(2), sb, cb}} {
		p.Add(r(3), t[0], c(360))
		p.ModU(r(3), r(3), c(360))
		p.Load(32, t[1], sines, r(3))
		p.Add(r(3), r(3), c(90))
		p.Load(32, t[2], sines, r(3))
	}
	p.Add(yaw, yaw, n(g.spin))
	p.ModU(yaw, yaw, c(360))
	p.Load(32, sy, sines, yaw)
	p.Add(r(3), yaw, c(90))
	p.Load(32, cy, sines, r(3))

	q := c(14)
	rot := func(d, a, ka, b, kb scouterkit.Word, add bool) { // d = (a·ka ± b·kb) >> 14, using r14
		p.Mul(d, a, ka)
		p.Mul(r(14), b, kb)
		if add {
			p.Add(d, d, r(14))
		} else {
			p.Sub(d, d, r(14))
		}
		p.Sar(d, d, q)
	}
	p.Mov(r(0), c(0))
	p.Bind(vertex)
	p.Mul(r(4), r(0), c(3))
	p.Load(32, r(5), c(l.vertices), r(4))
	p.Add(r(4), r(4), c(1))
	p.Load(32, r(6), c(l.vertices), r(4))
	p.Add(r(4), r(4), c(1))
	p.Load(32, r(7), c(l.vertices), r(4))
	rot(r(8), r(5), cy, r(7), sy, true)    // x1
	rot(r(9), r(7), cy, r(5), sy, false)   // z1
	rot(r(10), r(6), ca, r(9), sa, false)  // y2
	rot(r(11), r(6), sa, r(9), ca, true)   // z2
	rot(r(12), r(8), cb, r(10), sb, false) // x3
	rot(r(13), r(8), sb, r(10), cb, true)  // y3
	p.Add(r(11), r(11), n(g.d))
	p.Mul(r(12), r(12), n(g.d))
	p.DivS(r(12), r(12), r(11))
	p.Add(r(12), r(12), n(g.size/2))
	p.Mul(r(13), r(13), n(g.d))
	p.DivS(r(13), r(13), r(11))
	p.Sub(r(13), n(g.size/2), r(13))
	p.Store(32, c(l.px), r(0), r(12))
	p.Store(32, c(l.py), r(0), r(13))
	p.Add(r(0), r(0), c(1))
	p.Bne(r(0), c(8), vertex)

	top, bottom, lastTop, lastBottom := w(wTop), w(wBottom), w(wLastTop), w(wLastBottom)
	rows, lower, higher := p.Label(), p.Label(), p.Label()
	p.Load(32, top, c(l.py), c(0))
	p.Mov(bottom, top)
	p.Mov(r(0), c(1))
	p.Bind(rows)
	p.Load(32, r(1), c(l.py), r(0))
	p.BgeS(r(1), top, lower)
	p.Mov(top, r(1))
	p.Bind(lower)
	p.BgeS(bottom, r(1), higher)
	p.Mov(bottom, r(1))
	p.Bind(higher)
	p.Add(r(0), r(0), c(1))
	p.Bne(r(0), c(8), rows)
	p.Sub(r(1), lastBottom, lastTop)
	p.Add(r(1), r(1), c(1))
	p.Fill(bitmap, stride, c(0), lastTop, n(g.size), r(1), c(0))
	p.Mov(r(0), c(0))
	p.Bind(edge)
	p.Shl(r(1), r(0), c(1))
	p.Load(8, r(2), c(l.edges), r(1))
	p.Add(r(1), r(1), c(1))
	p.Load(8, r(3), c(l.edges), r(1))
	p.Load(32, r(4), c(l.px), r(2))
	p.Load(32, r(5), c(l.py), r(2))
	p.Load(32, r(6), c(l.px), r(3))
	p.Load(32, r(7), c(l.py), r(3))
	p.Line(bitmap, stride, r(4), r(5), r(6), r(7), n(int(g.level)))
	p.Add(r(0), r(0), c(1))
	p.Bne(r(0), c(12), edge)
	upper, under := p.Label(), p.Label()
	p.Mov(r(1), lastTop)
	p.BltS(r(1), top, upper)
	p.Mov(r(1), top)
	p.Bind(upper)
	p.Mov(r(2), lastBottom)
	p.BgeS(r(2), bottom, under)
	p.Mov(r(2), bottom)
	p.Bind(under)
	p.Sub(r(3), r(2), r(1))
	p.Add(r(3), r(3), c(1))
	p.Add(r(4), r(1), n(y))
	p.Draw(bitmap, stride, c(0), r(1), n(x), r(4), n(g.size+1), r(3))
	p.Mov(lastTop, top)
	p.Mov(lastBottom, bottom)
	p.Add(frames, frames, c(1))
	p.Beq(mode, c(0), timed)
	p.Notify(frames, yaw)
	p.End()
	p.Bind(timed)
	p.Bne(frames, last, schedule)
	p.Mov(fin, c(1))
	p.Bind(schedule)
	p.Add(due, due, n(int(g.period)))
	p.SysRead(r(1), prop(scouterkit.PropTimeUS))
	p.Sub(r(2), due, r(1))
	p.BgeS(r(2), c(0), armed)
	p.Add(due, r(1), n(int(g.period)))
	p.Bind(armed)
	p.SysWrite(prop(scouterkit.PropTimer), due)
	p.End()
	p.Bind(off)
	p.SysWrite(prop(scouterkit.PropPanel), c(0))
	p.SysWrite(prop(scouterkit.PropIMURate), c(13))
	p.End()

	p.On(scouterkit.TriggerStart, start)
	p.On(scouterkit.TriggerUser0, sync)
	p.On(scouterkit.TriggerTimer, frame)
	p.On(scouterkit.TriggerUser3, frame)
	p.Changes(l.state, 4*stateWords)
	p.Changes(l.px, l.end-l.px)
	return p
}
