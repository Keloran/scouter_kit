package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"

	"scouterkit"
	"scouterkit/examples/internal/demo"
)

// Stream mode (-levels 2): the PC encodes each frame's changed rows as runs
// of black and white. For every frame it writes the record into a buffer in
// each arm's arena and the frame's due time (in that arm's clock), then
// CALLs stored code that composes the record into a canvas, sleeps until the
// due time and draws the canvas: host code, so the programs' run-time cap of
// 30 ms in 100 ms does not apply, and packets queue in the arm's window while
// it sleeps, so the link stays busy.
//
// A record: u32 length (bytes, a multiple of 4), u32 frame index, then codes:
// a byte b >= 0x80 skips b - 0x7F unchanged rows; b < 0x80 replaces the next
// row by b runs, one byte each, from x = 0, black first and alternating (a
// run over 255 goes as 255, 0, the rest).

const (
	sDue = iota
	sFrame
	sX
	sY
	stateWords
)

type layout struct {
	base, state, canvas, record, end uint32
	w, h, stride                     int
}

func newLayout(base uint32, n, w, h int) (layout, error) {
	l := layout{base: base, w: w, h: h, stride: (w + 1) / 2}
	l.state = base + 1024
	l.canvas = l.state + 4*stateWords
	l.record = (l.canvas + uint32(l.stride*h) + 3) &^ 3
	l.end = l.record + uint32(8+h*128+3)&^3
	if l.end > base+uint32(n) {
		return l, fmt.Errorf("no room for a %dx%d canvas and its record (%d bytes)", w, h, n)
	}
	return l, nil
}

func (l layout) size() int { return int(l.end - l.base) }

func (l layout) word(i int) scouterkit.Word { return scouterkit.Word(l.state/4 + uint32(i)) }

// lateUS: a frame composed later than this after its due time is not drawn
// (the next one shows it), so both eyes only show frames on time.
const lateUS = 30000

// frameCode is the stored code: compose the record, sleep until sDue, draw,
// NOTIFY (frame, composing | drawing << 16 in 8 µs units). A real sleep (1 ms,
// so that it is still ahead when it runs) after about 800 jumps starts the
// budget of 1000 jumps again.
func frameCode(l layout) (*scouterkit.Program, uint32) {
	p := scouterkit.NewProgramAt(l.base, 24)
	c := p.Const
	r := scouterkit.R
	due, frame, px, py := l.word(sDue), l.word(sFrame), l.word(sX), l.word(sY)
	canvas, stride, now := c(l.canvas), c(uint32(l.stride)), c(uint32(scouterkit.PropTimeUS))
	rows, changed, runs, rowDone, composed := p.Label(), p.Label(), p.Label(), p.Label(), p.Label()
	at := p.Here()
	p.SysRead(r(13), now)
	p.Mov(r(1), c(l.record))
	p.Load(32, r(3), r(1), c(0))
	p.Add(r(4), r(1), c(8))
	p.Add(r(5), r(1), r(3))
	p.Mov(r(6), c(0))
	p.Mov(r(12), c(0))
	p.Bind(rows)
	p.BgeU(r(4), r(5), composed)
	p.Load(8, r(7), r(4), c(0))
	p.Add(r(4), r(4), c(1))
	p.BltU(r(7), c(0x80), changed)
	p.Sub(r(7), r(7), c(0x7F))
	p.Add(r(6), r(6), r(7))
	p.Jmp(rows)
	p.Bind(changed)
	p.Add(r(14), r(7), c(4))
	p.Mov(r(8), c(0))
	p.Mov(r(9), c(0))
	p.Bind(runs)
	p.Beq(r(7), c(0), rowDone)
	p.Load(8, r(10), r(4), c(0))
	p.Add(r(4), r(4), c(1))
	p.Fill(canvas, stride, r(8), r(6), r(10), c(1), r(9))
	p.Add(r(8), r(8), r(10))
	p.Sub(r(9), c(15), r(9))
	p.Sub(r(7), r(7), c(1))
	p.Jmp(runs)
	p.Bind(rowDone)
	p.Add(r(6), r(6), c(1))
	p.Add(r(12), r(12), r(14))
	p.BltU(r(12), c(800), rows)
	p.Mov(r(12), c(0))
	p.SysRead(r(11), now)
	p.Add(r(11), r(11), c(1000))
	p.SleepUntil(r(11))
	p.Jmp(rows)
	late, drew := p.Label(), p.Label()
	p.Bind(composed)
	p.SysRead(r(11), now)
	p.Sub(r(13), r(11), r(13))
	p.Shr(r(13), r(13), c(3))
	p.SleepUntil(due)
	p.SysRead(r(11), now)
	p.Sub(r(10), r(11), due)
	p.BltS(c(lateUS), r(10), late)
	p.Draw(canvas, stride, c(0), c(0), px, py, c(uint32(l.w)), c(uint32(l.h)))
	p.SysRead(r(10), now)
	p.Sub(r(10), r(10), r(11))
	p.Shr(r(10), r(10), c(3))
	p.Shl(r(10), r(10), c(16))
	p.Or(r(13), r(13), r(10))
	p.Jmp(drew)
	p.Bind(late)
	p.Or(r(13), r(13), c(1<<31))
	p.Bind(drew)
	p.Notify(frame, r(13))
	p.Ret()
	p.Changes(l.canvas, uint32(l.stride*l.h))
	return p, at
}

// encoder turns frames into records against the last frame it encoded.
type encoder struct {
	w, h      int
	threshold uint8
	prev      []bool
	first     bool
}

func newEncoder(w, h, threshold int) *encoder {
	return &encoder{w: w, h: h, threshold: uint8(threshold), prev: make([]bool, w*h), first: true}
}

func (e *encoder) encode(frame []byte, k uint32) []byte {
	out := make([]byte, 8, 8+2*e.h)
	skip := 0
	flush := func() {
		for skip > 0 {
			n := min(skip, 128)
			out = append(out, byte(0x7F+n))
			skip -= n
		}
	}
	row := make([]bool, e.w)
	for y := 0; y < e.h; y++ {
		same := !e.first
		for x := 0; x < e.w; x++ {
			row[x] = frame[y*e.w+x] >= e.threshold
			if same && row[x] != e.prev[y*e.w+x] {
				same = false
			}
		}
		if same {
			skip++
			continue
		}
		flush()
		runs := runsOf(row)
		for len(runs) > 0x7F {
			runs = merge(runs)
		}
		out = append(out, byte(len(runs)))
		out = append(out, runs...)
		x, white := 0, false
		for _, n := range runs {
			for i := 0; i < int(n); i++ {
				e.prev[y*e.w+x] = white
				x++
			}
			white = !white
		}
	}
	e.first = false
	for len(out)%4 != 0 {
		out = append(out, 0x80)
	}
	binary.LittleEndian.PutUint32(out[0:], uint32(len(out)))
	binary.LittleEndian.PutUint32(out[4:], k)
	return out
}

// runsOf is a row as runs from x = 0, black first; a run over 255 goes as
// 255, 0, the rest.
func runsOf(row []bool) []byte {
	var runs []byte
	white := false
	for x := 0; x < len(row); {
		n := 0
		for x < len(row) && row[x] == white {
			x++
			n++
		}
		for n > 255 {
			runs = append(runs, 255, 0)
			n -= 255
		}
		runs = append(runs, byte(n))
		white = !white
	}
	return runs
}

// merge joins the shortest inner run with its two neighbours (one colour),
// for a row with more runs than a code can carry.
func merge(runs []byte) []byte {
	i := 1
	for j := 1; j < len(runs)-1; j++ {
		if int(runs[j-1])+int(runs[j])+int(runs[j+1]) <= 255 && (runs[j] < runs[i] || int(runs[i-1])+int(runs[i])+int(runs[i+1]) > 255) {
			i = j
		}
	}
	m := int(runs[i-1]) + int(runs[i]) + int(runs[i+1])
	if m > 255 {
		return runs[:0x7F]
	}
	return append(append(append([]byte(nil), runs[:i-1]...), byte(m)), runs[i+2:]...)
}

func streamPlay(cfg scouterkit.Config, arms []scouterkit.Arm, c config, w, h, x, y int, r io.Reader) error {
	probe, err := newLayout(0, 1<<30, w, h)
	if err != nil {
		return err
	}
	base, err := scouterkit.Alloc(probe.size())
	if err != nil {
		return err
	}
	defer scouterkit.Free(base)
	l, err := newLayout(base, probe.size(), w, h)
	if err != nil {
		return err
	}
	code, at := frameCode(l)
	if err := code.Err(); err != nil {
		return err
	}
	period := time.Duration(float64(time.Second) / c.fps)
	for _, arm := range arms {
		ax, ay := demo.PanelAt(cfg, arm, x, y)
		arm.WriteArena(l.canvas, make([]byte, l.stride*h))
		arm.WriteArena(l.state+4*sX, demo.Words(uint32(int32(ax)), uint32(int32(ay))))
		if err := arm.Load(code); err != nil {
			return err
		}
	}

	enc := newEncoder(w, h, c.threshold)
	frame := make([]byte, w*h)
	drawn, notified, dropped := map[scouterkit.Arm]int{}, map[scouterkit.Arm]int{}, map[scouterkit.Arm]int{}
	maxLate, compose, drawT := map[scouterkit.Arm]time.Duration{}, map[scouterkit.Arm]time.Duration{}, map[scouterkit.Arm]time.Duration{}
	composeSum, prevUS, cycleSum := map[scouterkit.Arm]time.Duration{}, map[scouterkit.Arm]uint32{}, map[scouterkit.Arm]time.Duration{}
	packets0, resent0 := map[scouterkit.Arm]uint32{}, map[scouterkit.Arm]uint32{}
	for _, arm := range arms {
		packets0[arm], resent0[arm] = arm.State().Packets, arm.State().Resent
	}
	devDue := map[scouterkit.Arm]map[uint32]uint32{}
	for _, arm := range arms {
		devDue[arm] = map[uint32]uint32{}
	}
	var audio *exec.Cmd
	t0 := time.Now().Add(c.sendLead + 300*time.Millisecond)
	sentFrames, skipped, bytes, last := 0, 0, 0, uint32(0)
	stop := demo.Interrupted()

	events := func() error {
		for _, arm := range arms {
			for {
				e, ok := arm.NextEvent(0)
				if !ok {
					break
				}
				switch e.Type {
				case scouterkit.EventNotify:
					notified[arm]++
					if e.B&(1<<31) != 0 {
						dropped[arm]++
						e.B &^= 1 << 31
					} else {
						drawn[arm]++
					}
					compose[arm] = max(compose[arm], time.Duration(e.B&0xFFFF)*8*time.Microsecond)
					composeSum[arm] += time.Duration(e.B&0xFFFF) * 8 * time.Microsecond
					if prevUS[arm] != 0 {
						cycleSum[arm] += time.Duration(int32(e.DeviceUS-prevUS[arm])) * time.Microsecond
					}
					prevUS[arm] = e.DeviceUS
					drawT[arm] = max(drawT[arm], time.Duration(e.B>>16)*8*time.Microsecond)
					if d, ok := devDue[arm][e.A]; ok {
						maxLate[arm] = max(maxLate[arm], time.Duration(int32(e.DeviceUS-d))*time.Microsecond)
						delete(devDue[arm], e.A)
					}
				case scouterkit.EventHalt, scouterkit.EventError:
					return fmt.Errorf("%s arm: event %d, code %d at %#x", arm, e.Type, e.Code, e.Address)
				}
			}
		}
		return nil
	}
	queued := func() int {
		q := 0
		for _, arm := range arms {
			q = max(q, arm.State().Queued)
		}
		return q
	}

	interrupted := false
	for k := uint32(0); ; k++ {
		if _, err := io.ReadFull(r, frame); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				break
			}
			return err
		}
		select {
		case <-stop:
			interrupted = true
		default:
		}
		if interrupted {
			break
		}
		if err := events(); err != nil {
			return err
		}
		due := t0.Add(time.Duration(k) * period)
		sleepUntil(due.Add(-c.sendLead))
		for {
			busy := queued() >= 24
			for _, arm := range arms {
				busy = busy || sentFrames-notified[arm] >= 4
			}
			if !busy {
				break
			}
			time.Sleep(2 * time.Millisecond)
			if err := events(); err != nil {
				return err
			}
		}
		if c.audio && audio == nil && time.Now().After(t0.Add(c.audioDelay)) {
			audio = startAudio(c)
		}
		if k > 0 && time.Until(due) < c.sendLead/2 {
			skipped++
			continue
		}
		rec := enc.encode(frame, k)
		for _, arm := range arms {
			d := arm.DeviceTime(time.Until(due))
			devDue[arm][k] = d
			arm.WriteArena(l.record, rec)
			arm.WriteArena(l.state+4*sDue, demo.Words(d, k))
			arm.Call(at)
		}
		sentFrames, bytes, last = sentFrames+1, bytes+len(rec), k
	}
	deadline := time.Now().Add(10 * time.Second)
	for !interrupted && time.Now().Before(deadline) {
		if err := events(); err != nil {
			return err
		}
		all := true
		for _, arm := range arms {
			all = all && notified[arm] == sentFrames
		}
		if all {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	elapsed := time.Since(t0)
	fmt.Printf("%d frames shown, %d skipped, %.1f KB per frame, in %v (%.1f frames/s)", sentFrames, skipped,
		float64(bytes)/float64(max(1, sentFrames))/1024, elapsed.Round(time.Millisecond), float64(sentFrames)/elapsed.Seconds())
	for _, arm := range arms {
		fmt.Printf("; %s drew %d (%d late, not drawn), at most %v late, composing up to %v, drawing up to %v", arm, drawn[arm], dropped[arm],
			maxLate[arm].Round(time.Millisecond), compose[arm].Round(time.Millisecond), drawT[arm].Round(time.Millisecond))
	}
	fmt.Printf(" (last frame %d)\n", last)
	for _, arm := range arms {
		n := time.Duration(max(1, notified[arm]))
		st := arm.State()
		fmt.Printf("%s arm: composing %v and a frame every %v on average; %.0f packets/s, %d resent\n", arm,
			(composeSum[arm] / n).Round(100*time.Microsecond), (cycleSum[arm] / max(1, n-1)).Round(time.Millisecond),
			float64(st.Packets-packets0[arm])/elapsed.Seconds(), st.Resent-resent0[arm])
	}
	if audio != nil {
		if interrupted {
			audio.Process.Kill()
		}
		audio.Wait()
	}

	failed := false
	if c.check && !interrupted {
		img := make([]byte, w*h)
		for i, on := range enc.prev {
			if on {
				img[i] = 15
			}
		}
		for _, arm := range arms {
			want := demo.NewPanel()
			ax, ay := demo.PanelAt(cfg, arm, x, y)
			want.Paste(&demo.Image{W: w, H: h, Pix: img}, ax, ay)
			if err := demo.CheckPanel(arm, want, 0, scouterkit.PanelHeight); err != nil {
				fmt.Println(err)
				failed = true
			} else {
				fmt.Printf("%s panel: as expected\n", arm)
			}
		}
	}
	for _, arm := range arms {
		arm.SetPanel(false)
	}
	if failed {
		return errors.New("a panel did not read back as expected")
	}
	return nil
}
