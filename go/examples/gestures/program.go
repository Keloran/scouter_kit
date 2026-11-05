package main

import "scouterkit"

// Detector words (cleared by reset), from layout.det.
const (
	wS     = iota // samples taken
	wState        // 0 rest, 1 first dip, 2 after a nod, 3 second dip
	wRef          // reference pitch, 1/16 degree
	wLow
	wPeak
	wS0  // where the dip started
	wRet // where the nod came half way back
	wQ   // samples since the last motion
	detWords
)

// The rest of the detector's memory, in words after the detector words:
// the last 4 pitches, the last 4 acceleration samples, the motion ring.
const (
	histAt = detWords
	accAt  = histAt + 4
	ringAt = accAt + 12
)

// App words, from layout.app.
const (
	wGesture = iota // the last gesture, for USER1
	wShowAt         // device time USER1 shows the page at
	wRecord         // 1: NOTIFY every sample
	wPage
	wShown
	wHideAt
	wDue // the next sample's device time
	appWords
)

// NOTIFY a values besides the gestures (b: the page).
const (
	notePage    = 0x10
	noteHidden  = 0x11
	noteStarted = 0x20
	recordMark  = 0xA5 << 24 // in b: a recorded sample
)

const (
	poolWords   = 96
	codeBytes   = 2048
	replayBatch = 27 // samples per host-code packet (the library sends at most 234 bytes): WRITE 5 + 4 + 8 x 27, CALL 4
	periodUS    = 20000
)

type layout struct {
	base, det, app, levels, replay, text uint32
	guard                                uint32
}

func (l layout) det32(i int) scouterkit.Word { return scouterkit.Word(l.det/4 + uint32(i)) }
func (l layout) app32(i int) scouterkit.Word { return scouterkit.Word(l.app/4 + uint32(i)) }
func (l layout) detBytes() uint32            { return 4 * (ringAt + l.guard) }

// entries are the routines host code calls (CALL): reset clears the
// detector, replay runs step over the batch at layout.replay (u32 count,
// then i16 pitch, ax, ay, az per sample) and NOTIFYs what it detects.
type entries struct{ reset, replay uint32 }

// pager is the list on one arm: pages of lines (u8 byte count, then glyph
// varints, lineBytes each) drawn into a band shown at panel (x, y).
type pager struct {
	font                    uint32
	band                    uint32
	w, h, x, y              int
	pages, lines, lineBytes int
	pad, baseline, spacing  int
	hold                    uint32 // µs shown after a gesture
	rate                    int    // IMU_RATE while it runs
}

func (g pager) stride() int { return g.w / 2 }

// program is the nod detector and the list it drives. START clears the
// detector, sets IMU_RATE and the 20 ms TIMER; each TIMER reads PITCH and
// ACC_X/Y/Z, runs step, and hides the list once its time is up. step
// NOTIFYs each gesture (gesture, sample); with no host connected it hands it
// to the app at once (RAISE USER1), else the host hands it to both arms for
// a common wShowAt. USER1 is the app: a nod shows the list or turns to the
// next page, a double nod to the one before, a shake (once there is a
// detector for it) hides it. replay feeds step the batch at l.replay from
// host code, for tests.
func program(l layout, pr params, g pager) (*scouterkit.Program, entries) {
	p := scouterkit.NewProgramAt(l.base, poolWords)
	p.PanelPolicy(scouterkit.NoLabel)
	c := p.Const
	r := scouterkit.R
	n := func(v int) scouterkit.Word { return c(uint32(int32(v))) }
	prop := func(id scouterkit.Property) scouterkit.Word { return c(uint32(id)) }
	d, a := l.det32, l.app32
	s, state, ref, low, peak, s0, ret, q := d(wS), d(wState), d(wRef), d(wLow), d(wPeak), d(wS0), d(wRet), d(wQ)
	hist, acc, ring := c(l.det+4*histAt), c(l.det+4*accAt), c(l.det+4*ringAt)
	gesture, showAt, record, page, shown, hideAt, due := a(wGesture), a(wShowAt), a(wRecord), a(wPage), a(wShown),
		a(wHideAt), a(wDue)

	step, unknown, still, below, onset, abort, done := p.Label(), p.Label(), p.Label(), p.Label(), p.Label(),
		p.Label(), p.Label()
	s1, s1a, s1b, s2, s2a, s2b, s3, s3a, deep := p.Label(), p.Label(), p.Label(), p.Label(), p.Label(), p.Label(),
		p.Label(), p.Label(), p.Label()
	nod, double, report := p.Label(), p.Label(), p.Label()
	p.Bind(step)
	p.Add(s, s, c(1))
	p.Beq(r(0), c(scouterkit.Unknown), unknown)
	p.ModU(r(4), s, c(4))
	p.Store(32, hist, r(4), r(0))
	p.Load(32, r(5), hist, c(0))
	for i := 1; i < 4; i++ {
		p.Load(32, r(6), hist, c(uint32(i)))
		p.Add(r(5), r(5), r(6))
	}
	p.Shl(r(5), r(5), c(2))
	p.Mul(r(7), r(4), c(3))
	for i := 0; i < 3; i++ {
		p.Add(r(8), r(7), c(uint32(i)))
		p.Load(32, r(9), acc, r(8))
		p.Sub(r(9), r(1+i), r(9))
		p.Mul(r(9), r(9), r(9))
		if i == 0 {
			p.Mov(r(6), r(9))
		} else {
			p.Add(r(6), r(6), r(9))
		}
		p.Store(32, acc, r(8), r(1+i))
	}
	p.Add(q, q, c(1))
	p.BgeU(c(pr.motion2), r(6), still)
	p.Mov(q, c(0))
	p.Bind(still)
	p.ModU(r(8), s, c(pr.guard))
	p.Load(32, r(9), ring, r(8))
	p.Store(32, ring, r(8), q)
	p.Beq(state, c(1), s1)
	p.Beq(state, c(2), s2)
	p.Beq(state, c(3), s3)
	p.Sub(r(10), ref, n(int(pr.start)))
	p.BltS(r(5), r(10), below)
	p.BltU(q, c(pr.guard), done)
	p.Sub(r(10), r(5), ref)
	p.Sar(r(10), r(10), c(3))
	p.Add(ref, ref, r(10))
	p.Ret()
	p.Bind(below)
	p.BgeU(r(9), c(pr.quiet), onset)
	p.Mov(ref, r(5))
	p.Ret()
	p.Bind(onset)
	p.Mov(state, c(1))
	p.Mov(s0, s)
	p.Mov(low, r(5))
	p.Ret()

	p.Bind(s1)
	p.BgeS(r(5), low, s1a)
	p.Mov(low, r(5))
	p.Bind(s1a)
	p.Sub(r(10), s, s0)
	p.BltU(c(pr.maxLen), r(10), abort)
	p.Sub(r(11), ref, low)
	p.BltS(r(11), n(int(pr.depth)), done)
	p.Sub(r(7), r(5), low)
	p.Shl(r(7), r(7), c(1))
	p.BltS(r(7), r(11), done)
	p.BltU(r(10), c(pr.minLen), abort)
	p.Mov(state, c(2))
	p.Mov(ret, s)
	p.Mov(peak, ref)
	p.BgeS(r(5), ref, s1b)
	p.Mov(peak, r(5))
	p.Bind(s1b)
	p.Ret()
	p.Bind(abort)
	p.Mov(state, c(0))
	p.Mov(ref, r(5))
	p.Ret()

	p.Bind(s2)
	p.Mov(r(10), r(5))
	p.BltS(r(5), ref, s2a)
	p.Mov(r(10), ref)
	p.Bind(s2a)
	p.BgeS(peak, r(10), s2b)
	p.Mov(peak, r(10))
	p.Bind(s2b)
	p.Sub(r(11), s, ret)
	p.BltU(c(pr.window), r(11), nod)
	p.Sub(r(11), peak, n(int(pr.start)))
	p.BgeS(r(5), r(11), done)
	p.Mov(state, c(3))
	p.Mov(s0, s)
	p.Mov(low, r(5))
	p.Ret()

	p.Bind(s3)
	p.BgeS(r(5), low, s3a)
	p.Mov(low, r(5))
	p.Bind(s3a)
	p.Sub(r(10), s, s0)
	p.Sub(r(11), peak, low)
	p.BgeS(r(11), n(int(pr.depth2)), deep)
	p.Sub(r(7), s, ret)
	p.BltU(c(pr.window), r(7), nod)
	p.Ret()
	p.Bind(deep)
	p.Sub(r(7), r(5), low)
	p.Shl(r(7), r(7), c(1))
	p.BgeS(r(7), r(11), double)
	p.BltU(c(pr.maxLen2), r(10), nod)
	p.Ret()
	p.Bind(double)
	p.Mov(r(11), c(gestureDouble))
	p.Jmp(report)
	p.Bind(nod)
	p.Mov(r(11), c(gestureNod))
	p.Bind(report)
	p.Mov(state, c(0))
	p.Mov(ref, r(5))
	p.Notify(r(11), s)
	p.SysRead(r(10), prop(scouterkit.PropLink))
	p.Bne(r(10), c(0), done)
	p.Mov(gesture, r(11))
	p.SysRead(showAt, prop(scouterkit.PropTimeUS))
	p.Raise(c(uint32(scouterkit.TriggerUser1)))
	p.Bind(done)
	p.Ret()
	p.Bind(unknown)
	p.Mov(q, c(0))
	p.Mov(state, c(0))
	p.Ret()

	var e entries
	reset, zero := p.Label(), p.Label()
	e.reset = p.Here()
	p.Bind(reset)
	p.Mov(r(1), c(0))
	p.Bind(zero)
	p.Store(32, c(l.det), r(1), c(0))
	p.Add(r(1), r(1), c(1))
	p.Bne(r(1), c(l.detBytes()/4), zero)
	p.Ret()

	next, last := p.Label(), p.Label()
	e.replay = p.Here()
	p.Mov(r(12), c(0))
	p.Load(32, r(13), c(l.replay), c(0))
	p.Bind(next)
	p.Beq(r(12), r(13), last)
	p.Mul(r(7), r(12), c(4))
	for i := 0; i < 4; i++ {
		if i > 0 {
			p.Add(r(7), r(7), c(1))
		}
		p.Load(16, r(i), c(l.replay+4), r(7))
		p.Shl(r(i), r(i), c(16))
		p.Sar(r(i), r(i), c(16))
	}
	p.Call(step)
	p.Add(r(12), r(12), c(1))
	p.Jmp(next)
	p.Bind(last)
	p.Ret()

	draw, line, skip, hide := p.Label(), p.Label(), p.Label(), p.Label()
	stride := n(g.stride())
	p.Bind(draw)
	p.Fill(c(g.band), stride, c(0), c(0), n(g.w), n(g.h), c(0))
	p.Mov(r(1), c(0))
	p.Mul(r(2), page, n(g.lines))
	p.Bind(line)
	p.Add(r(3), r(2), r(1))
	p.Mul(r(3), r(3), n(g.lineBytes))
	p.Add(r(3), r(3), c(l.text))
	p.Load(8, r(4), r(3), c(0))
	p.Beq(r(4), c(0), skip)
	p.Add(r(3), r(3), c(1))
	p.Load(8, r(5), c(l.levels), r(1))
	p.Mul(r(6), r(1), n(g.spacing))
	p.Add(r(6), r(6), n(g.baseline))
	p.Glyphs(c(g.font), c(g.band), stride, n(g.pad), r(6), c(2), r(5), r(3), r(4))
	p.Bind(skip)
	p.Add(r(1), r(1), c(1))
	p.Bne(r(1), n(g.lines), line)
	p.SleepUntil(showAt)
	p.Draw(c(g.band), stride, c(0), c(0), n(g.x), n(g.y), n(g.w), n(g.h))
	p.Ret()
	p.Bind(hide)
	p.SysWrite(prop(scouterkit.PropPanel), c(0))
	p.Mov(shown, c(0))
	p.Notify(c(noteHidden), page)
	p.Ret()

	app, prev, show, quit := p.Label(), p.Label(), p.Label(), p.Label()
	p.Bind(app)
	p.Beq(gesture, c(gestureShake), quit)
	p.Beq(shown, c(0), show)
	p.Beq(gesture, c(gestureDouble), prev)
	p.Add(page, page, c(1))
	p.ModU(page, page, n(g.pages))
	p.Jmp(show)
	p.Bind(prev)
	p.Add(page, page, n(g.pages-1))
	p.ModU(page, page, n(g.pages))
	p.Bind(show)
	p.Call(draw)
	p.Mov(shown, c(1))
	p.SysRead(r(1), prop(scouterkit.PropTimeUS))
	p.Add(hideAt, r(1), c(g.hold))
	p.Notify(c(notePage), page)
	p.End()
	p.Bind(quit)
	p.Call(hide)
	p.End()

	start := p.Label()
	p.Bind(start)
	p.Call(reset)
	p.Mov(page, c(0))
	p.Mov(shown, c(0))
	p.SysWrite(prop(scouterkit.PropPanel), c(0))
	p.SysWrite(prop(scouterkit.PropIMURate), n(g.rate))
	p.SysRead(r(1), prop(scouterkit.PropTimeUS))
	p.Add(due, r(1), c(periodUS))
	p.SysWrite(prop(scouterkit.PropTimer), due)
	p.Notify(c(noteStarted), c(0))
	p.End()

	tick, plain, kept, armed := p.Label(), p.Label(), p.Label(), p.Label()
	p.Bind(tick)
	p.SysRead(r(0), prop(scouterkit.PropPitch))
	p.SysRead(r(1), prop(scouterkit.PropAccX))
	p.SysRead(r(2), prop(scouterkit.PropAccY))
	p.SysRead(r(3), prop(scouterkit.PropAccZ))
	p.Call(step)
	// A shake detector (GYR_Y once GYRO_RATE is driven, M8) goes here, reporting gestureShake as step does.
	p.Beq(record, c(0), plain)
	p.And(r(4), r(1), c(0xFFFF))
	p.Shl(r(5), r(2), c(16))
	p.Or(r(4), r(4), r(5))
	p.And(r(5), r(3), c(0xFFFF))
	p.And(r(6), r(0), c(0xFF))
	p.Shl(r(6), r(6), c(16))
	p.Or(r(5), r(5), r(6))
	p.Or(r(5), r(5), c(recordMark))
	p.Notify(r(4), r(5))
	p.Bind(plain)
	p.SysRead(r(6), prop(scouterkit.PropTimeUS))
	p.Beq(shown, c(0), kept)
	p.Sub(r(7), r(6), hideAt)
	p.BltS(r(7), c(0), kept)
	p.Call(hide)
	p.Bind(kept)
	p.Add(due, due, c(periodUS))
	p.Sub(r(7), due, r(6))
	p.BgeS(r(7), c(0), armed)
	p.Add(due, r(6), c(periodUS))
	p.Bind(armed)
	p.SysWrite(prop(scouterkit.PropTimer), due)
	p.End()

	p.On(scouterkit.TriggerStart, start)
	p.On(scouterkit.TriggerTimer, tick)
	p.On(scouterkit.TriggerUser1, app)
	p.Changes(l.det, l.detBytes())
	p.Changes(l.app, 4*appWords)
	p.Changes(g.band, uint32(g.stride()*g.h*2))
	return p, e
}
