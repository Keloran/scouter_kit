package main

import "scouterkit"

// State words, from layout.state.
const (
	wShown    = iota // 0 hidden (panel off), 1 shown, 2 shown until wHideAt, 3 shown by the host
	wHideAt          // device time
	wMinute          // the minute of the day shown
	wNext            // device time of the next minute
	wMailMin         // the host's time: the minute of the day...
	wMailNext        // ...and the next minute's device time (USER0 takes them)
	stateWords
)

const (
	clockText   = 4 * stateWords // 5 glyphs, HH:MM
	batteryText = clockText + 8  // 7 glyph slots, the last one '%'
	stateBytes  = batteryText + 8
	poolWords   = 64
	codeBytes   = 1536
)

// band is a bitmap the program draws text into and shows at panel (x, y),
// with the pen at (px, baseline).
type band struct {
	w, h, x, y, px, baseline int
	addr                     uint32
}

func (b band) stride() int { return b.w / 2 }

// guard rows under each band: a bitmap reaches down to the end of the arena.
func (b band) bytes() int { return b.w / 2 * 2 * b.h }

// face is the dashboard on one arm.
type face struct {
	clock, battery      band
	clockFont, battFont uint32
	colon, space, dash  uint32
	letter              uint32 // 'L' or 'R'
	angle, hysteresis   int    // degrees: shown above angle, hidden again below angle - hysteresis
	hold                uint32 // µs shown after the head goes down
}

type layout struct{ base, state uint32 }

func (l layout) word(i int) scouterkit.Word { return scouterkit.Word(l.state/4 + uint32(i)) }

// program is the dashboard. TILT (PITCH leaves its range) shows it when the
// head is up and hides it (panel off) when it is down again, after f.hold;
// TIMER advances the clock every minute (redrawn while shown) and ends the
// hold; BATTERY redraws the battery while shown; USER0 takes the host's time,
// USER1 shows it until USER2 hides it (the head does not). Each change
// NOTIFYs (wShown, pitch).
func program(l layout, f face) *scouterkit.Program {
	p := scouterkit.NewProgramAt(l.base, poolWords)
	p.PanelPolicy(scouterkit.NoLabel)
	c := p.Const
	r := scouterkit.R
	n := func(v int) scouterkit.Word { return c(uint32(int32(v))) }
	shown, hideAt, minute, next := l.word(wShown), l.word(wHideAt), l.word(wMinute), l.word(wNext)
	mailMin, mailNext := l.word(wMailMin), l.word(wMailNext)
	prop := func(id scouterkit.Property) scouterkit.Word { return c(uint32(id)) }
	timeNow, pitch := prop(scouterkit.PropTimeUS), prop(scouterkit.PropPitch)
	lo, hi := prop(scouterkit.PropPitchLo), prop(scouterkit.PropPitchHi)

	drawClock, drawBattery, show, hide := p.Label(), p.Label(), p.Label(), p.Label()
	armTimer := p.Label()

	p.Bind(drawClock)
	p.DivU(r(4), minute, c(60))
	p.ModU(r(5), minute, c(60))
	text := c(l.state + clockText)
	p.DivU(r(6), r(4), c(10))
	p.Store(8, text, c(0), r(6))
	p.ModU(r(6), r(4), c(10))
	p.Store(8, text, c(1), r(6))
	p.DivU(r(6), r(5), c(10))
	p.Store(8, text, c(3), r(6))
	p.ModU(r(6), r(5), c(10))
	p.Store(8, text, c(4), r(6))
	cb := f.clock
	p.Fill(c(cb.addr), n(cb.stride()), c(0), c(0), n(cb.w), n(cb.h), c(0))
	p.Glyphs(c(f.clockFont), c(cb.addr), n(cb.stride()), n(cb.px), n(cb.baseline), c(2), c(15), text, c(5))
	p.Draw(c(cb.addr), n(cb.stride()), c(0), c(0), n(cb.x), n(cb.y), n(cb.w), n(cb.h))
	p.Ret()

	digit, unknown, letter := p.Label(), p.Label(), p.Label()
	bt := c(l.state + batteryText)
	p.Bind(drawBattery)
	p.SysRead(r(0), prop(scouterkit.PropBattPct))
	p.Mov(r(1), c(6))
	p.BltU(c(100), r(0), unknown)
	p.Bind(digit)
	p.Sub(r(1), r(1), c(1))
	p.ModU(r(2), r(0), c(10))
	p.Store(8, bt, r(1), r(2))
	p.DivU(r(0), r(0), c(10))
	p.Bne(r(0), c(0), digit)
	p.Jmp(letter)
	p.Bind(unknown)
	p.Sub(r(1), r(1), c(1))
	p.Store(8, bt, r(1), c(f.dash))
	p.Bind(letter)
	p.Sub(r(1), r(1), c(1))
	p.Store(8, bt, r(1), c(f.space))
	p.Sub(r(1), r(1), c(1))
	p.Store(8, bt, r(1), c(f.letter))
	p.Add(r(3), r(1), bt)
	p.Sub(r(4), c(7), r(1))
	bb := f.battery
	p.Fill(c(bb.addr), n(bb.stride()), c(0), c(0), n(bb.w), n(bb.h), c(0))
	p.Glyphs(c(f.battFont), c(bb.addr), n(bb.stride()), n(bb.px), n(bb.baseline), c(2), c(15), r(3), r(4))
	p.Draw(c(bb.addr), n(bb.stride()), c(0), c(0), n(bb.x), n(bb.y), n(bb.w), n(bb.h))
	p.Ret()

	drawAll, marked, upRange, pin, pinned := p.Label(), p.Label(), p.Label(), p.Label(), p.Label()
	p.Bind(drawAll)
	p.Call(drawClock)
	p.Call(drawBattery)
	p.Ret()

	p.Bind(show)
	p.Beq(shown, c(3), upRange)
	p.Beq(shown, c(1), upRange)
	p.Bne(shown, c(0), marked)
	p.Call(drawAll)
	p.Bind(marked)
	p.Mov(shown, c(1))
	p.SysRead(r(7), pitch)
	p.Notify(shown, r(7))
	p.Bind(upRange)
	p.SysWrite(lo, n(f.angle-f.hysteresis))
	p.SysWrite(hi, c(90))
	p.Ret()

	p.Bind(pin)
	p.Bne(shown, c(0), pinned)
	p.Call(drawAll)
	p.Bind(pinned)
	p.Mov(shown, c(3))
	p.SysRead(r(7), pitch)
	p.Notify(shown, r(7))
	p.SysWrite(lo, n(-90))
	p.SysWrite(hi, c(90))
	p.Ret()

	p.Bind(hide)
	p.SysWrite(prop(scouterkit.PropPanel), c(0))
	p.Mov(shown, c(0))
	p.SysWrite(lo, n(-90))
	p.SysWrite(hi, n(f.angle))
	p.SysRead(r(7), pitch)
	p.Notify(shown, r(7))
	p.Ret()

	start := p.Label()
	p.Bind(start)
	p.Mov(minute, mailMin)
	p.Mov(next, mailNext)
	p.Call(hide)
	p.Jmp(armTimer)

	tilt, up, down, done := p.Label(), p.Label(), p.Label(), p.Label()
	p.Bind(tilt)
	p.SysRead(r(0), pitch)
	p.Beq(r(0), c(scouterkit.Unknown), done)
	p.BgeS(r(0), n(f.angle), up)
	p.Bind(down)
	p.Bne(shown, c(1), armTimer)
	if f.hold == 0 {
		p.Call(hide)
	} else {
		p.Mov(shown, c(2))
		p.SysRead(r(1), timeNow)
		p.Add(hideAt, r(1), n(int(f.hold)))
		p.SysWrite(lo, n(-90))
		p.SysWrite(hi, n(f.angle))
		p.Notify(shown, r(0))
	}
	p.Jmp(armTimer)
	p.Bind(up)
	p.Call(show)
	p.Jmp(armTimer)

	tick, sameMinute := p.Label(), p.Label()
	p.Bind(tick)
	p.SysRead(r(1), timeNow)
	p.Sub(r(2), r(1), next)
	p.BltS(r(2), c(0), sameMinute)
	p.Add(minute, minute, c(1))
	p.ModU(minute, minute, c(1440))
	p.Add(next, next, c(60000000))
	p.Beq(shown, c(0), sameMinute)
	p.Call(drawClock)
	p.Bind(sameMinute)
	p.Bne(shown, c(2), armTimer)
	p.Sub(r(2), r(1), hideAt)
	p.BltS(r(2), c(0), armTimer)
	p.Call(hide)

	p.Bind(armTimer)
	p.SysWrite(prop(scouterkit.PropTimer), next)
	p.Bne(shown, c(2), done)
	p.Sub(r(2), hideAt, next)
	p.BgeS(r(2), c(0), done)
	p.SysWrite(prop(scouterkit.PropTimer), hideAt)
	p.Bind(done)
	p.End()

	battery := p.Label()
	p.Bind(battery)
	p.Beq(shown, c(0), done)
	p.Call(drawBattery)
	p.End()

	sync, forceShow, forceHide := p.Label(), p.Label(), p.Label()
	p.Bind(sync)
	p.Mov(minute, mailMin)
	p.Mov(next, mailNext)
	p.Beq(shown, c(0), armTimer)
	p.Call(drawClock)
	p.Jmp(armTimer)
	p.Bind(forceShow)
	p.Call(pin)
	p.Jmp(armTimer)
	p.Bind(forceHide)
	p.Call(hide)
	p.Jmp(armTimer)

	p.On(scouterkit.TriggerStart, start)
	p.On(scouterkit.TriggerTilt, tilt)
	p.On(scouterkit.TriggerTimer, tick)
	p.On(scouterkit.TriggerBattery, battery)
	p.On(scouterkit.TriggerUser0, sync)
	p.On(scouterkit.TriggerUser1, forceShow)
	p.On(scouterkit.TriggerUser2, forceHide)
	p.Changes(l.state, stateBytes)
	p.Changes(f.clock.addr, uint32(f.clock.bytes()))
	p.Changes(f.battery.addr, uint32(f.battery.bytes()))
	return p
}
