package main

import (
	"encoding/binary"

	"scouterkit"
	"scouterkit/examples/internal/demo"
)

// State words, from layout.state.
const (
	wSeq    = iota // the update, NOTIFYd when it is shown (host)
	wDue           // the device time to show it (host)
	wScroll        // lines the band scrolls up before the lines are drawn (host)
	wMask          // the band lines to draw from their records, bit k for line k (host)
	wD0            // rows d0...d1-1 of the band are shown (host)
	wD1
	wOff   // 1: blank the band now instead (host)
	wPhase // 0 idle, 1 prepared, 2 shown, 3 blank (panel in standby)
	wBlank // 1: the panel was blanked, so the next show DRAWs the whole band
	stateWords
)

// NOTIFY kinds, in the top byte of the second value.
const (
	shown = iota // with the low 24 bits of the device time its DRAW began
	blanked
)

const (
	recordBytes = 128 // u8 varint bytes, u8 bytes of the stable part, u16 pen x, u16 pen x of the tail, u16 0, the varints
	margin      = 6
	poolWords   = 64
	codeBytes   = 1280
	offAfter    = 30000000 // µs from blank to panel off
)

// geometry is the caption band.
type geometry struct {
	w, lines      int    // text width; lines shown
	lineH, ascent int    // a line's box and its baseline in it
	tail          int    // the level of words that may still change
	clear         uint32 // µs without an update, then the program blanks the band by itself
}

func (g geometry) h() int     { return g.lines * g.lineH }
func (g geometry) bw() int    { return g.w + 2 } // the bitmap: DRAW takes w + 1 columns, an odd width
func (g geometry) guard() int { return g.lineH }

type layout struct{ base, state, records, band, end uint32 }

func newLayout(base uint32, g geometry) layout {
	l := layout{base: base, state: base + codeBytes}
	l.records = l.state + 4*stateWords
	l.band = l.records + uint32(g.lines*recordBytes)
	l.end = l.band + uint32(g.bw()/2*(g.h()+g.guard()))
	return l
}

func (l layout) word(i int) scouterkit.Word { return scouterkit.Word(l.state/4 + uint32(i)) }

// program shows the caption band at panel (x, y), drawing only what changed.
// USER0 prepares an update in the bitmap: it scrolls the band up by wScroll
// lines with one BLIT and draws the lines of wMask from their records, each
// in its own box, the stable words at 15 and the tail at g.tail. The next
// TIMER, at wDue, DRAWs rows wD0...wD1-1 (the whole band after a blank) and
// NOTIFYs (wSeq, device time), so both eyes change at the same time. With
// wOff, USER0 blanks the band at once instead (the panel in standby, NOTIFY
// kind 1); so does the program itself g.clear after the last update, and 30
// s after a blank the panel goes off.
func program(l layout, g geometry, font uint32, x, y int) *scouterkit.Program {
	p := scouterkit.NewProgramAt(l.base, poolWords)
	p.PanelPolicy(scouterkit.NoLabel)
	c := p.Const
	r := scouterkit.R
	n := func(v int) scouterkit.Word { return c(uint32(int32(v))) }
	seq, due, scroll, maskW, d0, d1 := l.word(wSeq), l.word(wDue), l.word(wScroll), l.word(wMask), l.word(wD0), l.word(wD1)
	offW, phase, blankW := l.word(wOff), l.word(wPhase), l.word(wBlank)
	timer, panel, now := n(int(scouterkit.PropTimer)), n(int(scouterkit.PropPanel)), n(int(scouterkit.PropTimeUS))
	band, stride, bw, lineH := c(l.band), n(g.bw()/2), n(g.bw()), n(g.lineH)

	prepare, lines, blank := p.Label(), p.Label(), p.Label()
	p.Bind(prepare)
	p.Bne(offW, c(0), blank)
	p.Beq(scroll, c(0), lines)
	p.Mul(r(1), scroll, lineH)
	p.Sub(r(2), n(g.h()), r(1))
	p.Blit(band, stride, c(0), r(1), band, stride, c(0), c(0), bw, r(2), c(0), c(15))
	p.Fill(band, stride, c(0), r(2), bw, r(1), c(0))
	p.Bind(lines)
	for k := 0; k < g.lines; k++ {
		skip := p.Label()
		rec := l.records + uint32(k*recordBytes)
		p.And(r(1), maskW, n(1<<k))
		p.Beq(r(1), c(0), skip)
		p.Fill(band, stride, c(0), n(k*g.lineH), bw, lineH, c(0))
		p.Load(8, r(1), c(rec), c(0))
		p.Load(8, r(2), c(rec), c(1))
		p.Load(16, r(3), c(rec), c(1))
		p.Load(16, r(4), c(rec), c(2))
		p.Glyphs(c(font), band, stride, r(3), n(k*g.lineH+g.ascent), c(2), c(15), c(rec+8), r(2))
		p.Add(r(5), r(2), c(rec+8))
		p.Sub(r(1), r(1), r(2))
		p.Glyphs(c(font), band, stride, r(4), n(k*g.lineH+g.ascent), c(2), n(g.tail), r(5), r(1))
		p.Bind(skip)
	}
	p.Mov(phase, c(1))
	p.SysWrite(timer, due)
	p.End()

	tick, show, whole, draw, off := p.Label(), p.Label(), p.Label(), p.Label(), p.Label()
	p.Bind(tick)
	p.Beq(phase, c(1), show)
	p.Beq(phase, c(2), blank)
	p.Beq(phase, c(3), off)
	p.End()
	p.Bind(show)
	p.Mov(r(5), d0)
	p.Mov(r(6), d1)
	p.Bne(blankW, c(0), whole)
	p.Jmp(draw)
	p.Bind(whole)
	p.Mov(r(5), c(0))
	p.Mov(r(6), n(g.h()))
	p.Mov(blankW, c(0))
	p.Bind(draw)
	p.SysRead(r(3), now)
	p.Sub(r(7), r(6), r(5))
	p.Add(r(8), r(5), n(y))
	p.Draw(band, stride, c(0), r(5), n(x), r(8), n(g.w+1), r(7))
	p.Mov(phase, c(2))
	p.And(r(4), r(3), n(0xFFFFFF))
	p.Notify(seq, r(4))
	p.Add(r(3), r(3), n(int(g.clear)))
	p.SysWrite(timer, r(3))
	p.End()
	p.Bind(blank)
	p.SysWrite(panel, c(2))
	p.Mov(phase, c(3))
	p.Mov(blankW, c(1))
	p.Notify(seq, n(blanked<<24))
	p.SysRead(r(3), now)
	p.Add(r(3), r(3), n(offAfter))
	p.SysWrite(timer, r(3))
	p.End()
	p.Bind(off)
	p.SysWrite(panel, c(0))
	p.Mov(phase, c(0))
	p.End()

	p.On(scouterkit.TriggerUser0, prepare)
	p.On(scouterkit.TriggerTimer, tick)
	p.Changes(l.state+4*wPhase, 8)
	p.Changes(l.band, l.end-l.band)
	return p
}

// record is a band line for the program: its glyphs, the first split of
// them stable, the rest from pen x splitX on.
func record(glyphs []uint32, split, splitX int) []byte {
	b := make([]byte, recordBytes)
	v := demo.Varints(glyphs)
	b[0] = byte(len(v))
	b[1] = byte(len(demo.Varints(glyphs[:split])))
	binary.LittleEndian.PutUint16(b[2:], margin)
	binary.LittleEndian.PutUint16(b[4:], uint16(splitX))
	copy(b[8:], v)
	return b
}

// update is what the program does to go from the band's lines to new ones.
type update struct {
	scroll, mask int
	d0, d1       int
}

// plan finds the scroll that keeps the most lines (none if old is not
// drawn), and the lines to draw after it; the rows shown are the whole band
// after a scroll, else those of the lines drawn.
func (g geometry) plan(old, lines []line, drawn bool) update {
	u := update{}
	best := -1
	for s := 0; s < g.lines && drawn; s++ {
		kept := 0
		for k := 0; k+s < g.lines; k++ {
			if lines[k] == old[k+s] {
				kept++
			}
		}
		if kept > best {
			best, u.scroll = kept, s
		}
	}
	for k := 0; k < g.lines; k++ {
		if !drawn || k+u.scroll >= g.lines || lines[k] != old[k+u.scroll] {
			u.mask |= 1 << k
		}
	}
	if u.scroll > 0 || !drawn {
		u.d0, u.d1 = 0, g.h()
		return u
	}
	u.d0, u.d1 = g.h(), 0
	for k := 0; k < g.lines; k++ {
		if u.mask&(1<<k) != 0 {
			u.d0, u.d1 = min(u.d0, k*g.lineH), max(u.d1, (k+1)*g.lineH)
		}
	}
	return u
}

// drawnLine is a band line as glyphs: the first split stable, the rest
// from pen x splitX on.
type drawnLine struct {
	glyphs        []uint32
	split, splitX int
}

func glyphLine(f *scouterkit.Font, exp *demo.Font, l line) drawnLine {
	d := drawnLine{glyphs: f.GlyphIndices(l.text), splitX: margin}
	d.split = len(f.GlyphIndices(l.text[:l.tail]))
	d.splitX = margin + exp.Draw(demo.NewImage(1, 1), 0, 0, d.glyphs[:d.split], 15)
	return d
}

// band is the caption band as the program draws it.
func band(f *demo.Font, g geometry, lines []drawnLine) *demo.Image {
	m := demo.NewImage(g.w+1, g.h())
	for k, ln := range lines {
		f.Draw(m, margin, k*g.lineH+g.ascent, ln.glyphs[:ln.split], 15)
		f.Draw(m, ln.splitX, k*g.lineH+g.ascent, ln.glyphs[ln.split:], uint8(g.tail))
	}
	return m
}
