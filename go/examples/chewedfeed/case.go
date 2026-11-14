package main

import (
	"errors"
	"fmt"
	"time"

	"scouterkit"
	"scouterkit/examples/internal/demo"
)

type caseState map[scouterkit.Arm]bool

const (
	caseGuardNotice = 0x43415345 // "CASE": B is IN_CASE
	linkNotice      = 0x4C494E4B // "LINK": the host is back after the offline face; repaint everything
)

func caseEvent(e scouterkit.Event) (inCase, ok bool) {
	if e.Type == scouterkit.EventNotify && e.A == caseGuardNotice && e.B <= 1 {
		return e.B == 1, true
	}
	if e.Type == scouterkit.EventInput && e.Trigger == int(scouterkit.TriggerCase) {
		// CASE reports lid, charging and case battery bits too; bit 0 is IN_CASE.
		return e.A&1 != 0, true
	}
	return false, false
}

// caseReading: what a CASE input event says about the case itself (bits 2 CASE_CHARGING, 8-15 CASE_BATT_PCT,
// 0xFF unknown; VM.md §6.3). The arms send one when the case state changes, so the case's battery is known even
// when no poll happened while the glasses were in it.
func caseReading(e scouterkit.Event) (percent *int, charging, ok bool) {
	if e.Type != scouterkit.EventInput || e.Trigger != int(scouterkit.TriggerCase) {
		return nil, false, false
	}
	if p := int(e.A>>8) & 0xFF; p <= 100 {
		percent = &p
	}
	return percent, e.A&4 != 0, percent != nil
}

func (s caseState) any() bool {
	for _, inCase := range s {
		if inCase {
			return true
		}
	}
	return false
}

func initialCaseState(arms []scouterkit.Arm) (caseState, error) {
	s := make(caseState)
	for _, arm := range arms {
		values, err := arm.Props(scouterkit.PropInCase)
		if err != nil || len(values) != 1 || values[0] > 1 {
			return nil, fmt.Errorf("%s arm case sensor unavailable", arm)
		}
		s[arm] = values[0] == 1
	}
	return s, nil
}

// band is a bitmap the program draws text into and shows at panel (x, y), pen at (px, baseline).
type band struct {
	w, h, x, y, px, baseline int
	addr                     uint32
}

func (b band) stride() int { return b.w / 2 }

// Guard rows under each band, as in the dashboard demo: a bitmap reaches down to the end of the arena.
func (b band) bytes() int { return b.w / 2 * 2 * b.h }

// State words: wOffline 1 while the offline face is shown; wMinute the minute of the day shown and
// wNext the device time of the next minute; wMailMin/wMailNext the host's time, taken at START and USER0.
const (
	wOffline = iota
	wMinute
	wNext
	wMailMin
	wMailNext
	stateWords
)

const (
	offlineText    = "Not connected"
	offlineGlyphs  = " %-0123456789:LNRcdenot"
	standbyCode    = 1024
	standbyPool    = 64
	standbyMsg     = 4 * stateWords  // the message's glyph indices
	standbyBatt    = standbyMsg + 16 // 7 glyph slots, "L 100%" right-aligned, the last one '%'
	standbyClock   = standbyBatt + 8 // 5 glyph slots, HH:MM
	standbyState   = standbyClock + 8
	linkCheckEvery = 2_000_000 // µs
)

// standbyFace is what one arm draws without the host, in its own panel coordinates.
type standbyFace struct {
	msg, batt, clock band
	own, peer        battRow // peer.y -1: no arm-to-arm link
	font, msgLen     uint32
	zero, space      uint32 // glyphs; digits follow zero
	dash, colon      uint32
}

// battRow is one status panel row: its letter's glyph and the band's panel y on this arm.
type battRow struct {
	letter uint32
	y      int
}

// installStandby loads a program that runs on with the host away. It switches the panel off when
// the arm goes into the case (firmware 0.5.60 leaves that to applications) and checks LINK every 2 s:
// without a host it blanks the panel and shows "Not connected" over the page title, both arms'
// batteries in the status panel's L and R rows and HH:MM in the footer, kept by the arm's clock. The
// arms swap their BATT_PCT over the arm-to-arm link, so both eyes show the same image (images that
// differ per eye do not fuse reliably). When the link is back it NOTIFYs linkNotice and leaves the panel
// to the host, which should then call syncStandbyClock. It returns the program's state address.
func installStandby(cfg scouterkit.Config, s *screen, scale float64, arms []scouterkit.Arm) (uint32, error) {
	font, err := loadDisplayFont(offlineGlyphs, scale)
	if err != nil {
		return 0, err
	}
	zero := font.GlyphIndices("0")[0]
	for i, g := range font.GlyphIndices("0123456789") {
		if g != zero+uint32(i) {
			return 0, errors.New("standby font digits are not consecutive")
		}
	}
	msgGlyphs := font.GlyphIndices(offlineText)
	pad := 2
	text := func(sample string) band {
		return band{w: (font.Measure(sample).Width + 2*pad + 1) &^ 1, h: font.Ascent + font.Descent + 2*pad,
			px: pad, baseline: pad + font.Ascent}
	}
	msg, batt, clock := text(offlineText), text("L 100%"), text("00:00")
	base, err := scouterkit.Alloc(standbyCode + standbyState + msg.bytes() + batt.bytes() + clock.bytes())
	if err != nil {
		return 0, err
	}
	state := base + standbyCode
	msg.addr = state + standbyState
	batt.addr = msg.addr + uint32(msg.bytes())
	clock.addr = batt.addr + uint32(batt.bytes())

	// The same places the host draws: the title line, the status panel's L/R battery rows and the
	// footer's middle cell (centred).
	_, iconH := s.iconSize("glasses")
	lRow := s.footerFont.Ascent + iconH + 2 + s.footerFont.LineSpacing
	rRow := lRow + s.footerFont.LineSpacing
	split := s.footerSplit()
	clockX := split + (s.w-split-clock.w)/2
	g := func(r string) uint32 { return font.GlyphIndices(r)[0] }
	// Firmware without the arm-to-arm link (and the emulator) refuses PEER_LINK: each eye then
	// shows only its own row. chewedfeed sets the reported inputs again after this.
	peerLink := len(arms) == 2
	for _, arm := range arms {
		mask, err := arm.ReportInputs(1 << scouterkit.TriggerPeer)
		peerLink = peerLink && err == nil && mask&(1<<scouterkit.TriggerPeer) != 0
		arm.ReportInputs(0)
	}
	progress("Standby screen: both batteries on each eye (arm-to-arm link): %t", peerLink)
	glyphs := make([]byte, len(msgGlyphs))
	for i, gl := range msgGlyphs {
		glyphs[i] = byte(gl) // fewer than 128 glyphs: one LEB128 byte each
	}
	for _, arm := range arms {
		f := standbyFace{msg: msg, batt: batt, clock: clock, font: font.Address(), msgLen: uint32(len(msgGlyphs)),
			zero: zero, space: g(" "), dash: g("-"), colon: g(":")}
		f.msg.x, f.msg.y = demo.PanelAt(cfg, arm, s.x+textInset-pad, s.y+s.font.Ascent-msg.baseline)
		x, lY := demo.PanelAt(cfg, arm, s.x+s.statusX+textInset-pad, s.y+lRow-batt.baseline)
		_, rY := demo.PanelAt(cfg, arm, 0, s.y+rRow-batt.baseline)
		f.batt.x = x
		f.own, f.peer = battRow{g("L"), lY}, battRow{g("R"), rY}
		if arm == scouterkit.Right {
			f.own, f.peer = f.peer, f.own
		}
		if !peerLink {
			f.peer.y = -1
		}
		f.clock.x, f.clock.y = demo.PanelAt(cfg, arm, s.x+clockX, s.y+s.footerBaseline-clock.baseline)
		p := standbyProgram(base, state, f)
		if p.Here() > state {
			return 0, fmt.Errorf("standby program of %d bytes overlaps its state", p.Here()-base)
		}
		arm.WriteArena(state, make([]byte, 4))
		arm.WriteArena(state+standbyMsg, glyphs)
		arm.WriteArena(state+standbyBatt+6, []byte{byte(g("%"))})
		arm.WriteArena(state+standbyClock+2, []byte{byte(f.colon)})
		writeStandbyClock(arm, state)
		if err := arm.Load(p); err != nil {
			return 0, err
		}
	}
	// Start both before waiting, so each arm's first PEER_SEND finds the other's link on.
	for _, arm := range arms {
		if err := arm.Start(base); err != nil {
			return 0, err
		}
	}
	for _, arm := range arms {
		if _, err := demo.WaitNotify(arm, 5*time.Second); err != nil {
			return 0, err
		}
	}
	return state, nil
}

// writeStandbyClock puts the local minute of the day and the next minute's start in this arm's clock
// into the program's mailbox; START takes it, later USER0 (syncStandbyClock).
func writeStandbyClock(arm scouterkit.Arm, state uint32) {
	now := time.Now()
	next := now.Truncate(time.Minute).Add(time.Minute)
	arm.WriteArena(state+4*wMailMin, demo.Words(uint32(now.Hour()*60+now.Minute()), arm.DeviceTime(time.Until(next))))
}

// syncStandbyClock corrects the arm's clock drift and DST changes.
func syncStandbyClock(arm scouterkit.Arm, state uint32) {
	writeStandbyClock(arm, state)
	arm.Raise(scouterkit.TriggerUser0)
}

func standbyProgram(base, state uint32, f standbyFace) *scouterkit.Program {
	p := scouterkit.NewProgramAt(base, standbyPool)
	c := p.Const
	r := scouterkit.R
	n := func(v int) scouterkit.Word { return c(uint32(int32(v))) }
	prop := func(id scouterkit.Property) scouterkit.Word { return c(uint32(id)) }
	word := func(i int) scouterkit.Word { return scouterkit.Word(state/4 + uint32(i)) }
	offline, minute, next := word(wOffline), word(wMinute), word(wNext)
	mailMin, mailNext := word(wMailMin), word(wMailNext)
	peerLink := f.peer.y >= 0

	// drawBattery: r0 the percentage (over 100: unknown), r5 the letter's glyph, r6 the band's panel y.
	drawBattery, digit, unknown, letterAt := p.Label(), p.Label(), p.Label(), p.Label()
	batt, bt := f.batt, c(state+standbyBatt)
	p.Bind(drawBattery)
	p.Mov(r(1), c(6))
	p.BltU(c(100), r(0), unknown)
	p.Bind(digit)
	p.Sub(r(1), r(1), c(1))
	p.ModU(r(2), r(0), c(10))
	p.Add(r(2), r(2), c(f.zero))
	p.Store(8, bt, r(1), r(2))
	p.DivU(r(0), r(0), c(10))
	p.Bne(r(0), c(0), digit)
	p.Jmp(letterAt)
	p.Bind(unknown)
	p.Sub(r(1), r(1), c(1))
	p.Store(8, bt, r(1), c(f.dash))
	p.Bind(letterAt)
	p.Sub(r(1), r(1), c(1))
	p.Store(8, bt, r(1), c(f.space))
	p.Sub(r(1), r(1), c(1))
	p.Store(8, bt, r(1), r(5))
	p.Add(r(3), r(1), bt)
	p.Sub(r(4), c(7), r(1))
	p.Fill(c(batt.addr), n(batt.stride()), c(0), c(0), n(batt.w), n(batt.h), c(0))
	p.Glyphs(c(f.font), c(batt.addr), n(batt.stride()), n(batt.px), n(batt.baseline), c(2), c(15), r(3), r(4))
	p.Draw(c(batt.addr), n(batt.stride()), c(0), c(0), n(batt.x), r(6), n(batt.w), n(batt.h))
	p.Ret()

	drawBatteries := p.Label()
	p.Bind(drawBatteries)
	p.SysRead(r(0), prop(scouterkit.PropBattPct))
	p.Mov(r(5), c(f.own.letter))
	p.Mov(r(6), n(f.own.y))
	p.Call(drawBattery)
	if peerLink {
		p.SysRead(r(0), prop(scouterkit.PropPeerMsg)) // UNKNOWN until the other arm sent one
		p.Mov(r(5), c(f.peer.letter))
		p.Mov(r(6), n(f.peer.y))
		p.Call(drawBattery)
	}
	p.Ret()

	drawClock := p.Label()
	cb, ct := f.clock, c(state+standbyClock)
	p.Bind(drawClock)
	p.DivU(r(4), minute, c(60))
	p.ModU(r(5), minute, c(60))
	for i, v := range []struct {
		div bool
		src int
	}{{true, 4}, {false, 4}, {true, 5}, {false, 5}} {
		if v.div {
			p.DivU(r(6), r(v.src), c(10))
		} else {
			p.ModU(r(6), r(v.src), c(10))
		}
		p.Add(r(6), r(6), c(f.zero))
		p.Store(8, ct, c(uint32([]int{0, 1, 3, 4}[i])), r(6))
	}
	p.Fill(c(cb.addr), n(cb.stride()), c(0), c(0), n(cb.w), n(cb.h), c(0))
	p.Glyphs(c(f.font), c(cb.addr), n(cb.stride()), n(cb.px), n(cb.baseline), c(2), c(15), ct, c(5))
	p.Draw(c(cb.addr), n(cb.stride()), c(0), c(0), n(cb.x), n(cb.y), n(cb.w), n(cb.h))
	p.Ret()

	sendBattery := p.Label()
	p.Bind(sendBattery)
	if peerLink {
		p.SysRead(r(0), prop(scouterkit.PropBattPct))
		p.SysWrite(prop(scouterkit.PropPeerSend), r(0))
	}
	p.Ret()

	start, caseCheck, notCased, linkCheck, sameMinute, lost, end := p.Label(), p.Label(), p.Label(), p.Label(), p.Label(), p.Label(), p.Label()
	p.Bind(start)
	p.Mov(minute, mailMin)
	p.Mov(next, mailNext)
	if peerLink {
		p.SysWrite(prop(scouterkit.PropPeerLink), c(1))
		p.Call(sendBattery)
	}
	p.Bind(caseCheck) // and CASE
	p.SysRead(r(1), prop(scouterkit.PropInCase))
	p.Bne(r(1), c(1), notCased)
	p.SysWrite(prop(scouterkit.PropPanel), c(0))
	p.Mov(offline, c(0)) // draw the offline face again once out of the case
	p.Bind(notCased)
	p.Notify(c(caseGuardNotice), r(1))
	p.Bind(linkCheck) // and TIMER
	p.SysRead(r(0), prop(scouterkit.PropTimeUS))
	p.Add(r(1), r(0), c(linkCheckEvery))
	p.SysWrite(prop(scouterkit.PropTimer), r(1))
	// One minute per check: a check every 2 s catches up with any gap.
	p.Sub(r(2), r(0), next)
	p.BltS(r(2), c(0), sameMinute)
	p.Add(minute, minute, c(1))
	p.ModU(minute, minute, c(1440))
	p.Add(next, next, c(60_000_000))
	p.Beq(offline, c(0), sameMinute)
	p.Call(drawClock)
	p.Bind(sameMinute)
	p.SysRead(r(1), prop(scouterkit.PropInCase))
	p.Beq(r(1), c(1), end)
	p.SysRead(r(2), prop(scouterkit.PropLink))
	p.Beq(r(2), c(0), lost)
	p.Beq(offline, c(0), end)
	p.Mov(offline, c(0))
	p.Notify(c(linkNotice), c(1))
	p.End()
	p.Bind(lost)
	p.Bne(offline, c(0), end)
	p.Mov(offline, c(1))
	p.Call(sendBattery)
	p.SysWrite(prop(scouterkit.PropPanel), c(0)) // blank: DRAW powers it on again with all pixels 0
	mb := f.msg
	p.Fill(c(mb.addr), n(mb.stride()), c(0), c(0), n(mb.w), n(mb.h), c(0))
	p.Glyphs(c(f.font), c(mb.addr), n(mb.stride()), n(mb.px), n(mb.baseline), c(2), c(15), c(state+standbyMsg), c(f.msgLen))
	p.Draw(c(mb.addr), n(mb.stride()), c(0), c(0), n(mb.x), n(mb.y), n(mb.w), n(mb.h))
	p.Call(drawBatteries)
	p.Call(drawClock)
	p.Bind(end)
	p.End()

	battery, peerMsg := p.Label(), p.Label()
	p.Bind(battery)
	p.Call(sendBattery)
	p.Bind(peerMsg) // and PEER
	p.Beq(offline, c(0), end)
	p.Call(drawBatteries)
	p.End()

	sync := p.Label()
	p.Bind(sync) // USER0: the host's time
	p.Mov(minute, mailMin)
	p.Mov(next, mailNext)
	p.Beq(offline, c(0), end)
	p.Call(drawClock)
	p.End()

	p.On(scouterkit.TriggerStart, start)
	p.On(scouterkit.TriggerCase, caseCheck)
	p.On(scouterkit.TriggerTimer, linkCheck)
	p.On(scouterkit.TriggerBattery, battery)
	p.On(scouterkit.TriggerUser0, sync)
	if peerLink {
		p.On(scouterkit.TriggerPeer, peerMsg)
	}
	p.Changes(state, standbyState)
	p.Changes(f.msg.addr, uint32(f.msg.bytes()))
	p.Changes(f.batt.addr, uint32(f.batt.bytes()))
	p.Changes(f.clock.addr, uint32(f.clock.bytes()))
	return p
}
