package main

import (
	"encoding/binary"

	"scouterkit"
	"scouterkit/examples/internal/demo"
)

// State words, from layout.state.
const (
	wFrame    = iota // the next frame to show, composed in the canvas
	wDue             // its device time
	wLoop            // loops begun since the start
	wFull            // 1: draw the whole canvas (the panel is black)
	wMode            // 1: step mode (USER3 draws one frame, no timer)
	wLoops           // stop after this many loops (0: never)
	wFin             // 1: the next tick switches the panel off
	wShown           // the frame on the panel
	wMailLoop        // the host's anchor: loop wMailLoop starts at wMailTime
	wMailTime
	wSnapLoop // the program's copy of the mailbox
	wSnapTime
	wProbe0 // device times around a frame shown and composed for the host
	wProbe1
	wProbe2
	stateWords
)

const poolWords = 40

// layout of the arena from base: the program (table, pool, code), its
// state, the frame and chunk tables, the packed frames, the canvas and the
// scratch rows.
type layout struct {
	base, state, frames, chunks, data, canvas, scratch, end uint32
}

func newLayout(base uint32, a *anim) layout {
	l := layout{base: base, state: base + 1024}
	l.frames = l.state + 4*stateWords
	l.chunks = l.frames + 32*uint32(len(a.frames))
	n := 0
	for _, d := range a.deltas {
		n += len(d.chunks)
	}
	l.data = l.chunks + 32*uint32(n)
	l.canvas = l.data
	for _, d := range a.deltas {
		for _, c := range d.chunks {
			l.canvas += uint32(len(c.packed)+3) &^ 3
		}
	}
	l.scratch = l.canvas + uint32((canvasStride(a.w)*a.h+3)&^3)
	l.end = l.scratch + uint32(a.scratch())
	return l
}

func (l layout) word(i int) scouterkit.Word { return scouterkit.Word(l.state/4 + uint32(i)) }

// tables are the frame table (per frame: first chunk entry, chunks, x, y,
// w, h, delay, 0), the chunk table (source, packed bytes, destination,
// bytes, y, h, 1 to BLIT it from the scratch rows, 0) and the packed
// frames, from l.frames to l.canvas.
func tables(l layout, a *anim) []byte {
	out := make([]byte, l.canvas-l.frames)
	put := func(at uint32, v ...uint32) {
		for i, x := range v {
			binary.LittleEndian.PutUint32(out[at-l.frames+4*uint32(i):], x)
		}
	}
	entry, src := l.chunks, l.data
	for i, d := range a.deltas {
		put(l.frames+32*uint32(i), entry, uint32(len(d.chunks)), uint32(d.x), uint32(d.y), uint32(d.w), uint32(d.h), a.delays[i], 0)
		for _, c := range d.chunks {
			dst, blit := l.scratch, uint32(1)
			if d.full {
				dst, blit = l.canvas+uint32(c.y*canvasStride(a.w)), 0
			}
			put(entry, src, uint32(len(c.packed)), dst, uint32(d.rowBytes(a.w)*c.h), uint32(c.y), uint32(c.h), blit, 0)
			copy(out[src-l.frames:], c.packed)
			entry += 32
			src += uint32(len(c.packed)+3) &^ 3
		}
	}
	return out
}

// program plays the frames with the canvas at panel (x, y). START
// composes frame 0 in the canvas (which holds the last frame); then each
// TIMER (and USER3 in step mode) first DRAWs the changed rectangle of the
// frame composed for it, so both eyes change at its time whatever came
// before, arms TIMER for the next frame and composes that one: UNPACK each
// chunk, straight into the canvas or into the scratch rows and BLIT. USER0
// arms the timer. Host code may CALL probeAt to show and compose one frame
// and get the time of each half.
func program(l layout, a *anim, x, y int) (p *scouterkit.Program, probeAt uint32) {
	p = scouterkit.NewProgramAt(l.base, poolWords)
	p.PanelPolicy(scouterkit.NoLabel)
	c := p.Const
	r := scouterkit.R
	at := func(v int) scouterkit.Word { return c(uint32(int32(v))) }
	frame, due, loop, full, mode := l.word(wFrame), l.word(wDue), l.word(wLoop), l.word(wFull), l.word(wMode)
	loops, fin, snapLoop, snapTime := l.word(wLoops), l.word(wFin), l.word(wSnapLoop), l.word(wSnapTime)
	shown := l.word(wShown)
	timer, panel := c(uint32(scouterkit.PropTimer)), c(uint32(scouterkit.PropPanel))
	stride := c(uint32(canvasStride(a.w)))

	record := func() { // r1 = the frame record of wFrame
		p.Mul(r(1), frame, c(32))
		p.Add(r(1), r(1), c(l.frames))
	}

	compose, next, unpacked, composed := p.Label(), p.Label(), p.Label(), p.Label()
	p.Bind(compose) // frame wFrame into the canvas; uses r1...r13
	record()
	p.Load(32, r(2), r(1), c(0))
	p.Load(32, r(3), r(1), c(1))
	p.Load(32, r(4), r(1), c(2))
	p.Load(32, r(5), r(1), c(4))
	p.Shr(r(6), r(5), c(1))
	p.Beq(r(3), c(0), composed)
	p.Bind(next)
	p.Load(32, r(7), r(2), c(0))
	p.Load(32, r(8), r(2), c(1))
	p.Load(32, r(13), r(2), c(2))
	p.Load(32, r(11), r(2), c(3))
	p.Unpack(r(13), r(7), r(8), r(11))
	p.Load(32, r(10), r(2), c(6))
	p.Beq(r(10), c(0), unpacked)
	p.Load(32, r(9), r(2), c(4))
	p.Load(32, r(10), r(2), c(5))
	p.Blit(c(l.scratch), r(6), c(0), c(0), c(l.canvas), stride, r(4), r(9), r(5), r(10), c(0), c(15))
	p.Bind(unpacked)
	p.Add(r(2), r(2), c(32))
	p.Sub(r(3), r(3), c(1))
	p.Bne(r(3), c(0), next)
	p.Bind(composed)
	p.Ret()

	show, rect := p.Label(), p.Label()
	p.Bind(show) // the changed rectangle of frame wFrame (the whole canvas first) onto the panel
	p.Mov(shown, frame)
	p.Beq(full, c(0), rect)
	p.Draw(c(l.canvas), stride, c(0), c(0), at(x), at(y), c(uint32(a.w+1)), c(uint32(a.h)))
	p.Mov(full, c(0))
	p.Ret()
	p.Bind(rect)
	record()
	p.Load(32, r(4), r(1), c(2))
	p.Load(32, r(9), r(1), c(3))
	p.Load(32, r(5), r(1), c(4))
	p.Load(32, r(10), r(1), c(5))
	p.Add(r(11), r(4), at(x))
	p.Add(r(12), r(9), at(y))
	p.Add(r(13), r(5), c(1))
	p.Draw(c(l.canvas), stride, r(4), r(9), r(11), r(12), r(13), r(10))
	p.Ret()

	advance, wrapped, advanced := p.Label(), p.Label(), p.Label()
	p.Bind(advance) // the next frame and its time; at a wrap the anchor, and the last loop
	record()
	p.Load(32, r(7), r(1), c(6))
	p.Add(due, due, r(7))
	p.Add(frame, frame, c(1))
	p.Bne(frame, c(uint32(len(a.frames))), advanced)
	p.Mov(frame, c(0))
	p.Add(loop, loop, c(1))
	p.Copy(c(l.state+4*wSnapLoop), c(l.state+4*wMailLoop), c(8))
	p.Bne(snapLoop, loop, wrapped)
	p.Mov(due, snapTime)
	p.Bind(wrapped)
	p.Bne(mode, c(0), advanced)
	p.Notify(loop, due)
	p.Bne(loop, loops, advanced)
	p.Mov(fin, c(1))
	p.Bind(advanced)
	p.Ret()

	start, sync, synced := p.Label(), p.Label(), p.Label()
	p.Bind(start)
	p.Call(compose)
	p.Bind(sync)
	p.Bne(mode, c(0), synced)
	p.SysWrite(timer, due)
	p.Bind(synced)
	p.End()

	tick, notified, stepped, off := p.Label(), p.Label(), p.Label(), p.Label()
	p.Bind(tick)
	p.Bne(fin, c(0), off)
	p.Call(show)
	p.Beq(mode, c(0), notified)
	p.Notify(shown, loop)
	p.Bind(notified)
	p.Call(advance)
	p.Bne(mode, c(0), stepped)
	p.SysWrite(timer, due)
	p.Bind(stepped)
	p.Call(compose)
	p.End()
	p.Bind(off)
	p.SysWrite(panel, c(0))
	p.End()

	t0, t1, t2 := l.word(wProbe0), l.word(wProbe1), l.word(wProbe2)
	now := c(uint32(scouterkit.PropTimeUS))
	probe := p.Label()
	probeAt = p.Here()
	p.Bind(probe) // for host code (CALL): show the frame and compose the next; NOTIFY (frame shown, µs of the DRAW | µs of the next frame's composing << 16)
	p.SysRead(t0, now)
	p.Call(show)
	p.SysRead(t1, now)
	p.Call(advance)
	p.Call(compose)
	p.SysRead(t2, now)
	p.Sub(t2, t2, t1)
	p.Sub(t1, t1, t0)
	p.Shl(t2, t2, c(16))
	p.Or(t1, t1, t2)
	p.Notify(shown, t1)
	p.Ret()

	p.On(scouterkit.TriggerStart, start)
	p.On(scouterkit.TriggerUser0, sync)
	p.On(scouterkit.TriggerTimer, tick)
	p.On(scouterkit.TriggerUser3, tick)
	p.Changes(l.state, 4*stateWords)
	p.Changes(l.canvas, l.end-l.canvas)
	return p, probeAt
}

// state is the initial state: frame 0 due at due, the canvas holding the
// last frame, the whole canvas drawn first.
func state(due uint32, step bool, loops int) []byte {
	v := make([]uint32, stateWords)
	v[wDue], v[wFull], v[wLoops], v[wMailLoop] = due, 1, uint32(loops), 0xFFFFFFFF
	if step {
		v[wMode] = 1
	}
	return demo.Words(v...)
}

// upload queues the tables, the packed frames, the canvas (the last frame),
// the state and the program.
func upload(arm scouterkit.Arm, l layout, a *anim, p *scouterkit.Program, st []byte) error {
	arm.WriteArena(l.frames, tables(l, a))
	canvas := demo.NewImage(2*canvasStride(a.w), a.h)
	canvas.Paste(a.frames[len(a.frames)-1], 0, 0)
	arm.WriteArena(l.canvas, canvas.Rows(0, a.h))
	arm.WriteArena(l.state, st)
	return arm.Load(p)
}
