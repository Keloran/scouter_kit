// Command vmclock runs a clock as a program on both arms (VM.md §9.2, made
// with the program builder): each arm redraws HH:MM every minute by itself,
// in the arm's own clock. With -away the host drops both links for that long;
// then it reconnects, reads the clock back from both arms (its digits and the
// panel rows) and corrects it, timing the redraw.
//
//	go run ./examples/vmclock [-away 10m] [-stop] [-left ADDR -right ADDR] [-adapter hci1]
package main

import (
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"hash/crc32"
	"os"
	"time"

	"scouterkit"
)

const (
	bandW, bandH = 160, 32
	stride       = bandW / 2
	bandX, bandY = 290, 8 // on the virtual display
)

// The arena layout, from base: table, pool, code; then the program's data.
type layout struct {
	base, text, mailbox, state, band uint32
}

func newLayout(base uint32) layout {
	return layout{base: base, text: base + 768, mailbox: base + 776, state: base + 784, band: base + 1024}
}

func main() {
	away := flag.Duration("away", 0, "drop the links for this long, then check and correct the clock")
	stop := flag.Bool("stop", false, "stop the programs and switch the panels off at the end")
	left := flag.String("left", "", `left arm address (empty: the bonded glasses, "none": no left arm)`)
	right := flag.String("right", "", `right arm address (empty: the bonded glasses, "none": no right arm)`)
	adapter := flag.String("adapter", "", `BlueZ adapter, "hci1" or its address (empty: the first powered one)`)
	logs := flag.Bool("log", false, "the library logs to stderr")
	flag.Parse()
	cfg := scouterkit.DefaultConfig()
	cfg.Left, cfg.Right, cfg.Adapter, cfg.Log = *left, *right, *adapter, *logs
	if err := run(*away, *stop, cfg); err != nil {
		fmt.Fprintln(os.Stderr, "vmclock:", err)
		os.Exit(1)
	}
}

// panelAt is where an eye shows virtual display pixel (x, y).
func panelAt(cfg scouterkit.Config, arm scouterkit.Arm, x, y int) (int, int) {
	if arm == scouterkit.Left {
		return x - max(0, -cfg.DX), y + max(0, -cfg.DY)
	}
	return x - max(0, cfg.DX), y + max(0, cfg.DY)
}

// program is VM.md §9.2 at this layout, with this eye's DRAW position and a
// NOTIFY [M], [N] after each redraw.
func program(l layout, font uint32, x, y int) *scouterkit.Program {
	p := scouterkit.NewProgramAt(l.base, 48)
	p.PanelPolicy(scouterkit.NoLabel)
	c := p.Const
	m, n := scouterkit.Word(l.state/4), scouterkit.Word(l.state/4+1)
	mNew, nNew := scouterkit.Word(l.mailbox/4), scouterkit.Word(l.mailbox/4+1)
	r := scouterkit.R
	sync, tick, show := p.Label(), p.Label(), p.Label()
	p.Bind(sync)
	p.Mov(m, mNew)
	p.Mov(n, nNew)
	p.Jmp(show)
	p.Bind(tick)
	p.Add(m, m, c(1))
	p.ModU(m, m, c(1440))
	p.Add(n, n, c(60000000))
	p.Bind(show)
	p.DivU(r(4), m, c(60))
	p.ModU(r(5), m, c(60))
	p.DivU(r(6), r(4), c(10))
	p.Store(8, c(l.text), c(0), r(6))
	p.ModU(r(6), r(4), c(10))
	p.Store(8, c(l.text), c(1), r(6))
	p.DivU(r(6), r(5), c(10))
	p.Store(8, c(l.text), c(3), r(6))
	p.ModU(r(6), r(5), c(10))
	p.Store(8, c(l.text), c(4), r(6))
	p.Fill(c(l.band), c(stride), c(0), c(0), c(bandW), c(bandH), c(0))
	p.Glyphs(c(font), c(l.band), c(stride), c(4), c(24), c(2), c(15), c(l.text), c(5))
	p.Draw(c(l.band), c(stride), c(0), c(0), c(uint32(x)), c(uint32(y)), c(bandW), c(bandH))
	p.SysWrite(c(uint32(scouterkit.PropTimer)), n)
	p.Notify(m, n)
	p.End()
	p.On(scouterkit.TriggerStart, sync)
	p.On(scouterkit.TriggerTimer, tick)
	p.On(scouterkit.TriggerUser0, sync)
	p.Changes(l.text, 24)
	p.Changes(l.band, stride*bandH)
	return p
}

// setTime writes the minute of the day and the next minute's start, in the
// arm's clock, to the program's mailbox.
func setTime(arm scouterkit.Arm, l layout) {
	now := time.Now()
	next := now.Truncate(time.Minute).Add(time.Minute)
	b := make([]byte, 8)
	binary.LittleEndian.PutUint32(b, uint32(now.Hour()*60+now.Minute()))
	binary.LittleEndian.PutUint32(b[4:], arm.DeviceTime(time.Until(next)))
	arm.WriteArena(l.mailbox, b)
}

// waitRedraw waits for the program's NOTIFY after a redraw.
func waitRedraw(arm scouterkit.Arm, timeout time.Duration) (scouterkit.Event, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		e, ok := arm.NextEvent(time.Until(deadline))
		if ok && e.Type == scouterkit.EventNotify && e.Trigger != 0xFF {
			return e, nil
		}
		if ok && e.Type == scouterkit.EventHalt {
			return e, fmt.Errorf("%s arm: the program halted: %+v", arm, e)
		}
	}
	return scouterkit.Event{}, fmt.Errorf("%s arm: no redraw within %v", arm, timeout)
}

// check reads the clock back from the arm: its digits, its state, and the
// band's panel rows against the text drawn here. Close to a minute's start
// (the arms redraw then), it waits until just after it.
func check(arm scouterkit.Arm, l layout, font *scouterkit.Font, x, y int) error {
	now := time.Now()
	if next := now.Truncate(time.Minute).Add(time.Minute).Sub(now); next < 2*time.Second {
		time.Sleep(next + 500*time.Millisecond)
	}
	digits, err := arm.ReadArena(l.text, 5)
	if err != nil {
		return err
	}
	state, err := arm.ReadArena(l.state, 8)
	if err != nil {
		return err
	}
	minute := int(binary.LittleEndian.Uint32(state))
	text := fmt.Sprintf("%d%d:%d%d", digits[0], digits[1], digits[3], digits[4])
	if want := fmt.Sprintf("%02d:%02d", minute/60, minute%60); text != want {
		return fmt.Errorf("%s arm: the text says %s, the state %s", arm, text, want)
	}
	now = time.Now()
	ok := false
	for _, t := range []time.Time{now, now.Add(-3 * time.Second), now.Add(3 * time.Second)} {
		ok = ok || t.Hour()*60+t.Minute() == minute
	}
	if !ok {
		return fmt.Errorf("%s arm shows %s at %s", arm, text, now.Format("15:04:05"))
	}
	band, err := scouterkit.CreateBitmap(bandW, bandH)
	if err != nil {
		return err
	}
	defer band.Unload()
	font.DrawLine(band, text, 4, 24, scouterkit.AlignLeft, 255)
	img := band.Image()
	rows := make([]byte, bandH*scouterkit.PanelWidth/2)
	for r := 0; r < bandH; r++ {
		for c := 0; c < bandW; c++ {
			level := img.GrayAt(c, r).Y / 17
			px := x + c
			if px%2 == 0 {
				level <<= 4
			}
			rows[r*scouterkit.PanelWidth/2+px/2] |= level
		}
	}
	got, _, err := arm.PanelCRC(y, bandH)
	if err != nil {
		return fmt.Errorf("%s arm: panel read-back: %w", arm, err)
	}
	if want := crc32.ChecksumIEEE(rows); got != want {
		return fmt.Errorf("%s arm: the panel rows %d...%d read back %08x, %s drawn here is %08x", arm, y, y+bandH-1,
			got, text, want)
	}
	s, err := arm.VMStatus()
	if err != nil || s.State != 1 {
		return fmt.Errorf("%s arm: program state %+v, %v", arm, s, err)
	}
	fmt.Printf("%s arm: %s, panel rows as drawn here, %d tasks, timer armed %v\n", arm, text, s.Tasks, s.TimerArmed)
	return nil
}

func run(away time.Duration, stop bool, cfg scouterkit.Config) error {
	if err := scouterkit.Init(cfg); err != nil {
		return err
	}
	defer scouterkit.Deinit()
	font, err := scouterkit.LoadFont(scouterkit.DepartureMono(), "0123456789:", 2)
	if err != nil {
		return err
	}
	base, err := scouterkit.Alloc(4096)
	if err != nil {
		return err
	}
	l := newLayout(base)
	colon := font.GlyphIndices(":")[0]
	var arms []scouterkit.Arm
	pos := map[scouterkit.Arm][2]int{}
	for _, arm := range []scouterkit.Arm{scouterkit.Left, scouterkit.Right} {
		if !arm.State().Present {
			continue
		}
		arms = append(arms, arm)
		x, y := panelAt(cfg, arm, bandX, bandY)
		pos[arm] = [2]int{x, y}
		p := program(l, font.Address(), x, y)
		arm.WriteArena(l.text, []byte{0, 0, byte(colon), 0, 0})
		setTime(arm, l)
		if err := arm.Load(p); err != nil {
			return err
		}
		if err := arm.Start(l.base); err != nil {
			return err
		}
		fmt.Printf("%s arm: %d bytes of code at 0x%05x, drawn at panel (%d, %d)\n", arm, len(p.Code()),
			l.base+64+48*4, x, y)
	}
	for _, arm := range arms {
		if _, err := waitRedraw(arm, 5*time.Second); err != nil {
			return err
		}
		if err := check(arm, l, font, pos[arm][0], pos[arm][1]); err != nil {
			return err
		}
	}
	if away > 0 {
		fmt.Printf("dropping the links for %v (until %s)\n", away, time.Now().Add(away).Format("15:04:05"))
		for _, arm := range arms {
			arm.Disconnect(away)
		}
		time.Sleep(away)
		deadline := time.Now().Add(2 * time.Minute)
		for _, arm := range arms {
			for !arm.State().Connected && time.Now().Before(deadline) {
				time.Sleep(100 * time.Millisecond)
			}
			s := arm.State()
			if !s.Connected {
				return errors.New(arm.String() + " arm: not connected again")
			}
			if s.EpochChanges != 0 {
				return errors.New(arm.String() + " arm: it rebooted while the host was away")
			}
		}
		fmt.Println("connected again")
		for _, arm := range arms {
			if err := check(arm, l, font, pos[arm][0], pos[arm][1]); err != nil {
				return err
			}
		}
		for _, arm := range arms {
			for {
				if _, ok := arm.NextEvent(0); !ok {
					break
				}
			}
			setTime(arm, l)
			sent := scouterkit.NowUS()
			arm.Raise(scouterkit.TriggerUser0)
			e, err := waitRedraw(arm, 5*time.Second)
			if err != nil {
				return err
			}
			fmt.Printf("%s arm: corrected, redrawn %.0f ms after the request\n", arm, float64(e.HostUS-sent)/1000)
			if err := check(arm, l, font, pos[arm][0], pos[arm][1]); err != nil {
				return err
			}
		}
	}
	if stop {
		for _, arm := range arms {
			if err := arm.Stop(); err != nil {
				return err
			}
			arm.SetPanel(false)
		}
		fmt.Println("programs stopped, panels off")
	} else {
		fmt.Println("the clock keeps running on the arms")
	}
	return nil
}
