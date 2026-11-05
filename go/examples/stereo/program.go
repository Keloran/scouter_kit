package main

import "scouterkit"

// State words, from layout.state.
const (
	wMode   = iota // 0 timed, 1 one frame per USER3
	wDue           // device time of the next frame
	wFrame         // index into the slider's table
	wFrames        // frames drawn
	wStep          // the frame USER3 draws
	wCycles        // cycles left, 0: endless
	wFin           // 1 once the cycles are done
	wStart         // mailbox: the first frame's device time (START, USER0)
	wAnchor        // the host's time for the next cycle's first frame (0: none)
	stateWords
)

const poolWords = 64

// stillDrawn is NOTIFY a once the still elements are on the panel.
const stillDrawn = 0xFFFFFFFF

type layout struct{ base, state, table uint32 }

func (l layout) word(i int) scouterkit.Word { return scouterkit.Word(l.state/4 + uint32(i)) }

// placed is a bitmap and where this arm's panel shows it.
type placed struct {
	addr       uint32
	stride     int
	w, h, x, y int
	frames     []int32 // the slider: its panel x per frame
	period     uint32  // µs per frame
}

// program shows the still elements at START and USER1, then moves the
// slider: each TIMER DRAWs it at the frame's x from the table, NOTIFYs
// (frames, frame) and arms the next frame one period on, or at a cycle's
// end at the host's anchor if it wrote one. USER3 draws every
// frame from the current one to wStep, so each erases the one before (the
// bitmap's margins). USER0 takes a new first-frame time from the mailbox.
func program(l layout, still []placed, slider placed) *scouterkit.Program {
	p := scouterkit.NewProgramAt(l.base, poolWords)
	p.PanelPolicy(scouterkit.NoLabel)
	c := p.Const
	r := scouterkit.R
	n := func(v int) scouterkit.Word { return c(uint32(int32(v))) }
	w := l.word
	prop := func(id scouterkit.Property) scouterkit.Word { return c(uint32(id)) }
	mode, due, frame, frames, step, cycles, fin := w(wMode), w(wDue), w(wFrame), w(wFrames), w(wStep), w(wCycles), w(wFin)
	anchor := w(wAnchor)

	drawStill := p.Label()
	p.Bind(drawStill)
	for _, e := range still {
		p.Draw(c(e.addr), n(e.stride), c(0), c(0), n(e.x), n(e.y), n(e.w), n(e.h))
	}
	p.Ret()

	drawSlider := p.Label()
	p.Bind(drawSlider)
	p.Load(32, r(1), c(l.table), frame)
	s := slider
	p.Draw(c(s.addr), n(s.stride), c(0), c(0), r(1), n(s.y), n(s.w), n(s.h))
	p.Add(frames, frames, c(1))
	p.Notify(frames, frame)
	p.Ret()

	start, sync, done := p.Label(), p.Label(), p.Label()
	p.Bind(start)
	p.SysWrite(prop(scouterkit.PropPanel), c(0))
	p.Call(drawStill)
	p.Notify(c(stillDrawn), c(0))
	p.Bind(sync)
	p.Mov(due, w(wStart))
	p.Mov(frame, c(0))
	p.Mov(fin, c(0))
	p.Mov(anchor, c(0))
	p.Bne(mode, c(0), done)
	p.SysWrite(prop(scouterkit.PropTimer), due)
	p.Bind(done)
	p.End()

	tick, wrap, same, armed := p.Label(), p.Label(), p.Label(), p.Label()
	p.Bind(tick)
	p.Bne(fin, c(0), done)
	p.Call(drawSlider)
	p.Add(frame, frame, c(1))
	p.Bne(frame, n(len(s.frames)), same)
	p.Mov(frame, c(0))
	p.Beq(cycles, c(0), wrap)
	p.Sub(cycles, cycles, c(1))
	p.Bne(cycles, c(0), wrap)
	p.Mov(fin, c(1))
	p.SysWrite(prop(scouterkit.PropPanel), c(0))
	p.End()
	p.Bind(wrap)
	p.Beq(anchor, c(0), same)
	p.Mov(due, anchor)
	p.Mov(anchor, c(0))
	p.SysWrite(prop(scouterkit.PropTimer), due)
	p.End()
	p.Bind(same)
	p.Add(due, due, n(int(s.period)))
	p.SysRead(r(1), prop(scouterkit.PropTimeUS))
	p.Sub(r(2), due, r(1))
	p.BgeS(r(2), c(0), armed)
	p.Add(due, r(1), n(int(s.period)))
	p.Bind(armed)
	p.SysWrite(prop(scouterkit.PropTimer), due)
	p.End()

	walk, redraw := p.Label(), p.Label()
	p.Bind(walk)
	p.Call(drawSlider)
	p.Beq(frame, step, done)
	p.Add(frame, frame, c(1))
	p.ModU(frame, frame, n(len(s.frames)))
	p.Jmp(walk)
	p.Bind(redraw)
	p.Call(drawStill)
	p.Notify(c(stillDrawn), c(0))
	p.End()

	p.On(scouterkit.TriggerStart, start)
	p.On(scouterkit.TriggerUser0, sync)
	p.On(scouterkit.TriggerUser1, redraw)
	p.On(scouterkit.TriggerTimer, tick)
	p.On(scouterkit.TriggerUser3, walk)
	p.Changes(l.state, 4*stateWords)
	return p
}
