package main

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"scouterkit"
	"scouterkit/examples/internal/demo"
)

// The arms keep the last notifications in a ring of slots, each a title and
// two body lines of glyph varints (u8 byte count first).
const (
	slots      = 8
	entryLines = 3
	listShown  = 4 // entries in the list, two lines each
	lineBytes  = 64
	poolWords  = 96
	codeBytes  = 3072
	stale      = 2_000_000 // µs: a popup this late shows only in the list
)

// View states, NOTIFYd as (state, head) after each change.
const (
	stHidden    = iota // panel off, or in standby with -standby
	stPopup            // the newest notification, until hideAt
	stList             // the head is up: the last notifications
	stHostList         // shown by the host (USER1) until hideAt
	stHold             // the head went down: the list stays until hideAt
	stDismissed        // the list timed out with the head up: hidden until it goes down
)

// State words, from layout.state. The host writes wHead...wTimeout.
const (
	wState   = iota
	wHideAt  // device time
	wHead    // number of the newest notification (1, 2, ...): slot wHead % slots
	wCount   // notifications in the ring
	wShowAt  // device time the popup shows
	wTimeout // µs the popup stays
	stateWords
)

type layout struct{ base, state, levels, icons, text, band uint32 }

func (l layout) word(i int) scouterkit.Word { return scouterkit.Word(l.state/4 + uint32(i)) }

// face is the views on one arm: the popup at panel (x, y), the list's
// entries below each other from there, slotH rows each; the text from textX
// (right of the icon, if any).
type face struct {
	font                   uint32
	w, x, y                int
	pad, baseline, spacing int
	textX                  int
	popupH, slotH          int
	angle, hysteresis      int
	hold, listFor          uint32 // µs
	hidePanel              uint32 // PANEL while hidden: 0 off, 2 standby
}

func (f face) stride() int { return (f.w + 1) / 2 }

// program shows notifications from the ring. USER0 (the host wrote a new
// one) pops it up at wShowAt for wTimeout, or redraws the list while it is
// shown; TILT shows the list while the head is up (above angle) and hides
// it hold after it goes down; the list also hides after listFor, and comes
// back only once the head has been down. USER1 shows the list for the host,
// USER2 hides everything. TIMER ends a popup, the list or the hold.
func program(l layout, f face) *scouterkit.Program {
	p := scouterkit.NewProgramAt(l.base, poolWords)
	p.PanelPolicy(scouterkit.NoLabel)
	c := p.Const
	r := scouterkit.R
	n := func(v int) scouterkit.Word { return c(uint32(int32(v))) }
	prop := func(id scouterkit.Property) scouterkit.Word { return c(uint32(id)) }
	w := l.word
	state, hideAt, head, count, showAt, timeout := w(wState), w(wHideAt), w(wHead), w(wCount), w(wShowAt), w(wTimeout)
	stride, band := n(f.stride()), c(l.band)
	lo, hi, timer := prop(scouterkit.PropPitchLo), prop(scouterkit.PropPitchHi), prop(scouterkit.PropTimer)
	now := func(d scouterkit.Word) { p.SysRead(d, prop(scouterkit.PropTimeUS)) }
	report := func() { p.Notify(state, head) }

	// entry draws r2 lines of slot r1 and its icon (if any) into the band and
	// shows r4 rows of it at panel row r3; r5 = 1 waits for wShowAt first.
	entry, line, skip, drawn, at, noIcon := p.Label(), p.Label(), p.Label(), p.Label(), p.Label(), p.Label()
	p.Bind(entry)
	p.Fill(band, stride, c(0), c(0), n(f.w), r(4), c(0))
	if f.textX > f.pad {
		p.Beq(r(2), c(0), noIcon)
		p.Load(32, r(12), c(l.icons), r(1))
		p.Beq(r(12), c(0), noIcon)
		p.Sub(r(13), r(4), n(iconSize))
		p.Sar(r(13), r(13), c(1))
		p.Blit(r(12), n(iconStride), c(0), c(0), band, stride, n(f.pad), r(13), n(iconSize), n(iconSize), c(0), c(15))
		p.Bind(noIcon)
	}
	p.Mov(r(6), c(0))
	p.Bind(line)
	p.BgeU(r(6), r(2), drawn)
	p.Mul(r(7), r(1), c(entryLines))
	p.Add(r(7), r(7), r(6))
	p.Mul(r(7), r(7), c(lineBytes))
	p.Add(r(7), r(7), c(l.text))
	p.Load(8, r(8), r(7), c(0))
	p.Beq(r(8), c(0), skip)
	p.Add(r(7), r(7), c(1))
	p.Load(8, r(9), c(l.levels), r(6))
	p.Mul(r(10), r(6), n(f.spacing))
	p.Add(r(10), r(10), n(f.baseline))
	p.Glyphs(c(f.font), band, stride, n(f.textX), r(10), c(2), r(9), r(7), r(8))
	p.Bind(skip)
	p.Add(r(6), r(6), c(1))
	p.Jmp(line)
	p.Bind(drawn)
	p.Beq(r(5), c(0), at)
	p.SleepUntil(showAt)
	p.Bind(at)
	p.Draw(band, stride, c(0), c(0), n(f.x), r(3), n(f.w), r(4))
	p.Ret()

	popup := p.Label()
	p.Bind(popup)
	p.ModU(r(1), head, c(slots))
	p.Mov(r(2), c(entryLines))
	p.Mov(r(3), n(f.y))
	p.Mov(r(4), n(f.popupH))
	p.Mov(r(5), c(1))
	p.Call(entry)
	p.Ret()

	list, next, blank := p.Label(), p.Label(), p.Label()
	p.Bind(list)
	p.Mov(r(11), c(0))
	p.Bind(next)
	p.Mul(r(3), r(11), n(f.slotH))
	p.Add(r(3), r(3), n(f.y))
	p.Mov(r(4), n(f.slotH))
	p.Mov(r(5), c(0))
	p.Mov(r(2), c(0))
	p.BgeU(r(11), count, blank)
	p.Sub(r(1), head, r(11))
	p.ModU(r(1), r(1), c(slots))
	p.Mov(r(2), c(2))
	p.Bind(blank)
	p.Call(entry)
	p.Add(r(11), r(11), c(1))
	p.Bne(r(11), c(listShown), next)
	p.Ret()

	down := func() { // the TILT range while the list is not up: fires when the head comes up
		p.SysWrite(lo, n(-90))
		p.SysWrite(hi, n(f.angle))
	}
	up := func() { // while it is up: fires when the head goes down
		p.SysWrite(lo, n(f.angle-f.hysteresis))
		p.SysWrite(hi, c(90))
	}
	arm := func(d uint32) {
		now(r(1))
		p.Add(hideAt, r(1), c(d))
		p.SysWrite(timer, hideAt)
	}

	hide, showList := p.Label(), p.Label()
	p.Bind(hide)
	p.SysWrite(prop(scouterkit.PropPanel), c(f.hidePanel))
	p.Mov(state, c(stHidden))
	down()
	report()
	p.Ret()
	p.Bind(showList)
	p.Call(list)
	p.Mov(state, c(stList))
	up()
	arm(f.listFor)
	report()
	p.Ret()

	start, done := p.Label(), p.Label()
	p.Bind(start)
	p.Call(hide)
	p.Bind(done)
	p.End()

	fresh, relist, kept := p.Label(), p.Label(), p.Label()
	p.Bind(fresh)
	now(r(1))
	p.Sub(r(2), r(1), showAt)
	p.BltS(c(stale), r(2), done)
	p.Beq(state, c(stList), relist)
	p.Beq(state, c(stHostList), relist)
	p.Beq(state, c(stHold), relist)
	p.Call(popup)
	p.Mov(state, c(stPopup))
	down()
	p.Add(hideAt, showAt, timeout)
	p.SysWrite(timer, hideAt)
	report()
	p.End()
	p.Bind(relist)
	p.Call(list)
	p.Beq(state, c(stHold), kept)
	arm(f.listFor)
	p.Bind(kept)
	report()
	p.End()

	tilt, fromList, fromHold, fromDismissed, stillUp, stillDown := p.Label(), p.Label(), p.Label(), p.Label(),
		p.Label(), p.Label()
	p.Bind(tilt)
	p.SysRead(r(0), prop(scouterkit.PropPitch))
	p.Beq(r(0), c(scouterkit.Unknown), done)
	p.Beq(state, c(stList), fromList)
	p.Beq(state, c(stHold), fromHold)
	p.Beq(state, c(stDismissed), fromDismissed)
	p.Beq(state, c(stHostList), done)
	p.BltS(r(0), n(f.angle), stillDown)
	p.Call(showList)
	p.End()
	p.Bind(stillDown)
	down()
	p.End()
	p.Bind(fromList)
	p.BgeS(r(0), n(f.angle-f.hysteresis), stillUp)
	if f.hold == 0 {
		p.Call(hide)
		p.End()
	} else {
		p.Mov(state, c(stHold))
		down()
		arm(f.hold)
		report()
		p.End()
	}
	p.Bind(stillUp)
	up()
	p.End()
	p.Bind(fromHold)
	p.BltS(r(0), n(f.angle), stillDown)
	p.Mov(state, c(stList))
	up()
	arm(f.listFor)
	report()
	p.End()
	p.Bind(fromDismissed)
	p.BgeS(r(0), n(f.angle-f.hysteresis), stillUp)
	p.Call(hide)
	p.End()

	tick, early, dismiss := p.Label(), p.Label(), p.Label()
	p.Bind(tick)
	now(r(1))
	p.Sub(r(2), r(1), hideAt)
	p.BltS(r(2), c(0), early)
	p.Beq(state, c(stList), dismiss)
	p.Beq(state, c(stHidden), done)
	p.Beq(state, c(stDismissed), done)
	p.Call(hide)
	p.End()
	p.Bind(dismiss)
	p.SysWrite(prop(scouterkit.PropPanel), c(f.hidePanel))
	p.Mov(state, c(stDismissed))
	report()
	p.End()
	p.Bind(early)
	p.Beq(state, c(stHidden), done)
	p.Beq(state, c(stDismissed), done)
	p.SysWrite(timer, hideAt)
	p.End()

	hostList, hostHide := p.Label(), p.Label()
	p.Bind(hostList)
	p.Call(list)
	p.Mov(state, c(stHostList))
	p.SysWrite(lo, n(-90))
	p.SysWrite(hi, c(90))
	arm(f.listFor)
	report()
	p.End()
	p.Bind(hostHide)
	p.Call(hide)
	p.End()

	p.On(scouterkit.TriggerStart, start)
	p.On(scouterkit.TriggerUser0, fresh)
	p.On(scouterkit.TriggerTilt, tilt)
	p.On(scouterkit.TriggerTimer, tick)
	p.On(scouterkit.TriggerUser1, hostList)
	p.On(scouterkit.TriggerUser2, hostHide)
	p.Changes(l.state, 4*wHead)
	p.Changes(l.band, uint32(f.stride()*(f.popupH+f.spacing)))
	return p
}

// glasses is the notification program on the arms and what the host knows
// of their ring.
type glasses struct {
	lib   *scouterkit.Font
	cfg   scouterkit.Config
	l     layout
	faces map[scouterkit.Arm]face
	font  *demo.Font
	arms  []scouterkit.Arm
	ring  [slots][entryLines][]uint32
	head  uint32
	count uint32
	ids   [slots]uint32 // the notification's ID in each slot (0: none)
	hide  uint32        // PANEL while hidden
	icons bool
	icon  [slots][]byte     // each slot's icon bitmap (nil: none)
	at    map[string]uint32 // where the icons loaded so far are
}

type viewConfig struct {
	angle, hysteresis int
	hold, listFor     time.Duration
	glyphs            string // characters to load ("": all of the font)
	blob              []byte
	scale             int
	standby           bool // hide in panel standby instead of off
	icons             bool // the app icons beside the text
}

var levels = [entryLines]uint8{15, 9, 9}

func newGlasses(cfg scouterkit.Config, arms []scouterkit.Arm, v viewConfig) (*glasses, error) {
	if len(arms) == 0 {
		return nil, errors.New("no arm")
	}
	blob, scale := v.blob, v.scale
	if blob == nil {
		blob = scouterkit.DepartureMono()
	}
	if scale == 0 {
		scale = 2
	}
	f, err := scouterkit.LoadFont(blob, v.glyphs, scale)
	if err != nil {
		return nil, err
	}
	g := &glasses{lib: f, cfg: cfg, faces: map[scouterkit.Arm]face{}, arms: arms, icons: v.icons,
		at: map[string]uint32{}}
	if v.standby {
		g.hide = panelStandby
	}
	if g.font, err = demo.FontOf(arms[0], f); err != nil {
		return nil, err
	}
	vw, _ := scouterkit.Size()
	base := face{font: f.Address(), w: vw - 2*abs(cfg.DX), pad: 8, spacing: f.LineSpacing, angle: v.angle,
		hysteresis: v.hysteresis, hold: uint32(v.hold.Microseconds()), listFor: uint32(v.listFor.Microseconds()),
		hidePanel: g.hide}
	base.w |= 1
	base.textX = base.pad
	if v.icons {
		base.textX += iconSize + iconGap
	}
	base.baseline = base.pad + (f.LineSpacing-f.Ascent-f.Descent)/2 + f.Ascent
	base.popupH = entryLines*f.LineSpacing + 2*base.pad
	base.slotH = 2*f.LineSpacing + base.pad
	bandBytes := uint32(base.stride() * (base.popupH + f.LineSpacing))
	textBytes := uint32(slots * entryLines * lineBytes)
	at, err := scouterkit.Alloc(int(codeBytes + 4*stateWords + 4 + 4*slots + textBytes + bandBytes))
	if err != nil {
		return nil, err
	}
	g.l = layout{base: at, state: at + codeBytes}
	g.l.levels = g.l.state + 4*stateWords
	g.l.icons = g.l.levels + 4
	g.l.text = g.l.icons + 4*slots
	g.l.band = g.l.text + textBytes
	for _, arm := range arms {
		fa := base
		fa.x, fa.y = demo.PanelAt(cfg, arm, abs(cfg.DX), 30)
		g.faces[arm] = fa
		p := program(g.l, fa)
		if p.Here() > g.l.state {
			return nil, fmt.Errorf("program of %d bytes overlaps its state", p.Here()-at)
		}
		arm.WriteArena(g.l.state, make([]byte, 4*stateWords))
		arm.WriteArena(g.l.levels, levels[:])
		arm.WriteArena(g.l.icons, make([]byte, 4*slots))
		arm.WriteArena(g.l.text, make([]byte, textBytes))
		if err := arm.Load(p); err != nil {
			return nil, err
		}
	}
	return g, nil
}

func abs(v int) int { return max(v, -v) }

func (g *glasses) start() error {
	for _, arm := range g.arms {
		if err := arm.Start(g.l.base); err != nil {
			return err
		}
	}
	return nil
}

func (g *glasses) glyphs(s string) []uint32 { return g.lib.GlyphIndices(s) }

// width is the advance of the glyphs, kerning included.
func (g *glasses) width(gl []uint32) int {
	img := demo.NewImage(1, 1)
	return g.font.Draw(img, 0, 0, gl, 0)
}

// fit returns the glyphs of the longest start of s that fits the line,
// with "..." when cut, and the rest of s.
func (g *glasses) fit(s string, room int, last bool) ([]uint32, string) {
	s = strings.TrimSpace(s)
	limit := func(gl []uint32) bool {
		return g.width(gl) <= room && len(demo.Varints(gl)) < lineBytes
	}
	if gl := g.glyphs(s); limit(gl) {
		return gl, ""
	}
	cut := len(s)
	for cut > 0 {
		_, size := utf8.DecodeLastRuneInString(s[:cut])
		cut -= size
		if last {
			if gl := g.glyphs(strings.TrimSpace(s[:cut]) + "..."); limit(gl) {
				return gl, ""
			}
			continue
		}
		if i := strings.LastIndexByte(s[:cut], ' '); i > 0 {
			cut = i
		}
		if gl := g.glyphs(s[:cut]); limit(gl) {
			return gl, s[cut:]
		}
	}
	return nil, s
}

// lines lays a notification out: "HH:MM app: summary" and the body in two
// lines.
func (g *glasses) lines(n *note) [entryLines][]uint32 {
	f := g.faces[g.arms[0]]
	room := f.w - f.pad - f.textX
	title := n.Summary
	if n.App != "" && n.App != n.Summary {
		title = n.App + ": " + title
	}
	var out [entryLines][]uint32
	out[0], _ = g.fit(n.At.Format("15:04")+" "+title, room, true)
	rest := strings.Join(strings.Fields(n.Body), " ")
	for i := 1; i < entryLines && rest != ""; i++ {
		out[i], rest = g.fit(rest, room, i == entryLines-1)
	}
	return out
}

func record(gl []uint32) []byte {
	v := demo.Varints(gl)
	b := make([]byte, lineBytes)
	b[0] = byte(len(v))
	copy(b[1:], v)
	return b
}

// push puts a notification in the ring: a new slot, or in place for the one
// it replaces if that is still there. With popup it shows at lead from now
// for d. It returns the slot.
func (g *glasses) push(n *note, popup bool, lead, d time.Duration) int {
	slot := -1
	if n.Replaces != 0 {
		for i, id := range g.ids {
			if id == n.Replaces && g.count > 0 {
				slot = i
			}
		}
	}
	if slot < 0 {
		g.head++
		g.count = min(slots, g.count+1)
		slot = int(g.head % slots)
	}
	g.ring[slot] = g.lines(n)
	g.ids[slot] = n.ID
	if n.ID == 0 {
		g.ids[slot] = n.Replaces
	}
	var text []byte
	for _, l := range g.ring[slot] {
		text = append(text, record(l)...)
	}
	g.icon[slot] = nil
	var icon uint32
	if g.icons {
		name := iconFor(n)
		var fresh bool
		if icon, fresh = g.iconAt(name); icon != 0 {
			g.icon[slot] = iconData(name)
		}
		if fresh {
			lead += iconLead
		}
	}
	for _, arm := range g.arms {
		arm.WriteArena(g.l.text+uint32(slot*entryLines*lineBytes), text)
		arm.WriteArena(g.l.icons+4*uint32(slot), demo.Words(icon))
		arm.WriteArena(g.l.state+4*wHead, demo.Words(g.head, g.count, arm.DeviceTime(lead), uint32(d.Microseconds())))
		if popup && uint32(slot) == g.head%slots {
			arm.Raise(scouterkit.TriggerUser0)
		}
	}
	return slot
}

// iconLead is how much later a popup shows when its icon (1250 bytes) goes
// to the arms with it.
const iconLead = 100 * time.Millisecond

// iconAt is where the icon is on the arms (0: nowhere), and whether it went
// there just now: each icon is loaded once.
func (g *glasses) iconAt(name string) (uint32, bool) {
	if at, ok := g.at[name]; ok {
		return at, false
	}
	data := iconData(name)
	if data == nil {
		return 0, false
	}
	at, err := scouterkit.Alloc(len(data))
	if err != nil {
		return 0, false
	}
	for _, arm := range g.arms {
		arm.WriteArena(at, data)
	}
	g.at[name] = at
	return at, true
}

// slotOf is the ring slot that holds the notification with this ID.
func (g *glasses) slotOf(id uint32) (int, bool) {
	for i, x := range g.ids {
		if x == id && id != 0 {
			return i, true
		}
	}
	return 0, false
}

// entryImage is the band as the program draws lines of a slot.
func (g *glasses) entryImage(f face, slot, nlines, rows int) *demo.Image {
	img := demo.NewImage(f.w, rows)
	if b := g.icon[slot]; b != nil && nlines > 0 && f.textX > f.pad {
		y0 := (rows - iconSize) / 2
		for y := 0; y < iconSize; y++ {
			for x := 0; x < iconSize; x++ {
				v := b[y*iconStride+x/2]
				if x%2 == 0 {
					v >>= 4
				}
				img.Set(f.pad+x, y0+y, v&15)
			}
		}
	}
	for i := 0; i < nlines; i++ {
		if gl := g.ring[slot][i]; len(gl) > 0 {
			g.font.Draw(img, f.textX, f.baseline+i*f.spacing, gl, levels[i])
		}
	}
	return img
}

// popupImage and listImage are the arm's panel showing the popup of the
// newest notification or the list.
func (g *glasses) popupImage(arm scouterkit.Arm) *demo.Image {
	f := g.faces[arm]
	panel := demo.NewPanel()
	panel.Paste(g.entryImage(f, int(g.head%slots), entryLines, f.popupH), f.x, f.y)
	return panel
}

func (g *glasses) listImage(arm scouterkit.Arm) *demo.Image {
	f := g.faces[arm]
	panel := demo.NewPanel()
	for i := 0; i < listShown; i++ {
		lines := 0
		slot := 0
		if uint32(i) < g.count {
			lines, slot = 2, int((g.head-uint32(i))%slots)
		}
		panel.Paste(g.entryImage(f, slot, lines, f.slotH), f.x, f.y+i*f.slotH)
	}
	return panel
}

func (g *glasses) checkPopup(arm scouterkit.Arm) error {
	f := g.faces[arm]
	return demo.CheckPanel(arm, g.popupImage(arm), f.y, f.popupH)
}

func (g *glasses) checkList(arm scouterkit.Arm) error {
	f := g.faces[arm]
	return demo.CheckPanel(arm, g.listImage(arm), f.y, listShown*f.slotH)
}

// wait returns the arm's next state NOTIFY for notification head (0: any)
// in one of the states.
func wait(arm scouterkit.Arm, head uint32, states ...uint32) (scouterkit.Event, error) {
	for {
		e, err := demo.WaitNotify(arm, 10*time.Second)
		if err != nil {
			return e, err
		}
		if head != 0 && e.B != head {
			continue
		}
		for _, s := range states {
			if e.A == s {
				return e, nil
			}
		}
	}
}

// panelStandby is PANEL = 2: deep power-down, about 1 mA on the arms
// (docs/power.md §14), and the next popup's DRAW wakes it in about 4 ms
// (VM.md §2.5) instead of about 75 ms from off.
const panelStandby = 2

// hidden checks that an arm's panel is as the program leaves it while hidden.
func (g *glasses) hidden(arm scouterkit.Arm) error {
	v, err := arm.Props(scouterkit.PropPanel)
	if err != nil {
		return err
	}
	if v[0] != g.hide {
		return fmt.Errorf("%s arm: PANEL %d while hidden, not %d", arm, v[0], g.hide)
	}
	return nil
}

func (g *glasses) hiddenAs() string {
	if g.hide == panelStandby {
		return "in standby"
	}
	return "off"
}
