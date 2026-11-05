// Command dashboard puts a home screen on the glasses that shows when the
// head tilts up, as stock's dashboard does: the clock in the middle, and
// each arm's battery in the strip only its own eye sees. It runs entirely as
// a VM program on the arms (the TILT trigger, a minute timer, the battery
// trigger) with the panels off while it is hidden; the host sets the time
// and leaves.
//
//	go run ./examples/dashboard [flags]
package main

import (
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"scouterkit"
	"scouterkit/examples/internal/demo"
)

type config struct {
	angle, hysteresis int
	hold, stay, power time.Duration
	show, check, stop bool
}

func main() {
	var f demo.Flags
	f.Register()
	var c config
	flag.IntVar(&c.angle, "angle", 20, "head-up angle, degrees: the dashboard shows above it")
	flag.IntVar(&c.hysteresis, "hysteresis", 5, "degrees below the angle at which it hides again")
	flag.DurationVar(&c.hold, "hold", time.Second, "how long it stays after the head goes down")
	flag.BoolVar(&c.show, "show", false, "show it now and keep it shown (the head does not hide it; the next run does)")
	flag.BoolVar(&c.check, "check", false, "show and hide it once, reading both panels back")
	flag.DurationVar(&c.power, "power", 0, "measure the battery current hidden, then shown, this long each (at least 66s)")
	flag.DurationVar(&c.stay, "for", 0, "stay connected this long, printing when it shows and hides")
	flag.BoolVar(&c.stop, "stop", false, "stop the program at the end (default: it keeps running with the host away)")
	demo.Usage("dashboard [flags]")
	flag.Parse()
	if flag.NArg() != 0 {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(f, c); err != nil {
		fmt.Fprintln(os.Stderr, "dashboard:", err)
		os.Exit(1)
	}
}

// board is the dashboard loaded on the arms.
type board struct {
	l                   layout
	faces               map[scouterkit.Arm]face
	clockFont, battFont *demo.Font
	arms                []scouterkit.Arm
}

func load(cfg scouterkit.Config, arms []scouterkit.Arm, c config) (*board, error) {
	clockFont, err := scouterkit.LoadFont(scouterkit.DepartureMono(), "0123456789:", 4)
	if err != nil {
		return nil, err
	}
	battFont, err := scouterkit.LoadFont(scouterkit.DepartureMono(), "0123456789%LR-", 2)
	if err != nil {
		return nil, err
	}
	clock := band{w: (clockFont.Measure("00:00").Width + 9) &^ 1, h: clockFont.Ascent + clockFont.Descent + 8,
		px: 4, baseline: 4 + clockFont.Ascent}
	batt := band{w: (battFont.Measure("L 100%").Width + 7) &^ 1, h: battFont.Ascent + battFont.Descent + 6,
		px: 3, baseline: 3 + battFont.Ascent}
	base, err := scouterkit.Alloc(codeBytes + stateBytes + clock.bytes() + batt.bytes())
	if err != nil {
		return nil, err
	}
	l := layout{base: base, state: base + codeBytes}
	clock.addr = l.state + stateBytes
	batt.addr = clock.addr + uint32(clock.bytes())
	b := &board{l: l, faces: map[scouterkit.Arm]face{}, arms: arms}
	if b.clockFont, err = demo.FontOf(arms[0], clockFont); err != nil {
		return nil, err
	}
	if b.battFont, err = demo.FontOf(arms[0], battFont); err != nil {
		return nil, err
	}
	vw, _ := scouterkit.Size()
	cx, cy := (vw-clock.w)/2, 24
	by := cy + (clock.h-batt.h)/2
	g := func(f *scouterkit.Font, s string) uint32 { return f.GlyphIndices(s)[0] }
	for _, arm := range arms {
		f := face{clock: clock, battery: batt, clockFont: clockFont.Address(), battFont: battFont.Address(),
			colon: g(clockFont, ":"), space: g(battFont, " "), dash: g(battFont, "-"), letter: g(battFont, "L"),
			angle: c.angle, hysteresis: c.hysteresis, hold: uint32(c.hold.Microseconds())}
		bx := 6
		if arm == scouterkit.Right {
			bx, f.letter = vw-6-batt.w, g(battFont, "R")
		}
		f.clock.x, f.clock.y = demo.PanelAt(cfg, arm, cx, cy)
		f.battery.x, f.battery.y = demo.PanelAt(cfg, arm, bx, by)
		b.faces[arm] = f
		p := program(l, f)
		if p.Here() > l.state {
			return nil, fmt.Errorf("program of %d bytes overlaps its state", p.Here()-base)
		}
		arm.WriteArena(l.state+clockText, []byte{0, 0, byte(f.colon), 0, 0})
		arm.WriteArena(l.state+batteryText+6, []byte{byte(g(battFont, "%"))})
		setTime(arm, l)
		if err := arm.Load(p); err != nil {
			return nil, err
		}
	}
	return b, nil
}

// setTime writes the minute of the day and the next minute's start, in the
// arm's clock, to the program's mailbox.
func setTime(arm scouterkit.Arm, l layout) {
	now := time.Now()
	next := now.Truncate(time.Minute).Add(time.Minute)
	arm.WriteArena(l.state+4*wMailMin, demo.Words(uint32(now.Hour()*60+now.Minute()), arm.DeviceTime(time.Until(next))))
}

func (b *board) start() error {
	for _, arm := range b.arms {
		if err := arm.Start(b.l.base); err != nil {
			return err
		}
	}
	for _, arm := range b.arms {
		if _, err := b.wait(arm, 0); err != nil {
			return err
		}
	}
	return nil
}

// wait waits for the program to report that it is hidden (0), shown (1),
// hiding (2) or shown by the host (3), and returns the pitch it read.
func (b *board) wait(arm scouterkit.Arm, shown uint32) (int32, error) {
	for {
		e, err := demo.WaitNotify(arm, 5*time.Second)
		if err != nil {
			return 0, err
		}
		if e.A == shown {
			return int32(e.B), nil
		}
	}
}

// expected is the arm's panel with the dashboard shown at minute m and
// battery pct.
func (b *board) expected(arm scouterkit.Arm, m int, pct uint32) *demo.Image {
	f := b.faces[arm]
	panel := demo.NewPanel()
	clock := demo.NewImage(f.clock.w, f.clock.h)
	b.clockFont.Draw(clock, f.clock.px, f.clock.baseline, []uint32{uint32(m / 600), uint32(m / 60 % 10), f.colon,
		uint32(m % 60 / 10), uint32(m % 10)}, 15)
	panel.Paste(clock, f.clock.x, f.clock.y)
	text := []uint32{f.letter, f.space}
	if pct > 100 {
		text = append(text, f.dash)
	} else {
		for _, d := range fmt.Sprint(pct) {
			text = append(text, uint32(d-'0'))
		}
	}
	batt := demo.NewImage(f.battery.w, f.battery.h)
	b.battFont.Draw(batt, f.battery.px, f.battery.baseline, append(text, b.percent(arm)), 15)
	panel.Paste(batt, f.battery.x, f.battery.y)
	return panel
}

func (b *board) percent(arm scouterkit.Arm) uint32 {
	v := arm.Mirror(b.l.state+batteryText+6, 1)
	return uint32(v[0])
}

// checkShown reads the arm's minute and battery and compares its panel rows
// with the dashboard drawn here.
func (b *board) checkShown(arm scouterkit.Arm) error {
	st, err := arm.ReadArena(b.l.state+4*wMinute, 4)
	if err != nil {
		return err
	}
	v, err := arm.Props(scouterkit.PropBattPct)
	if err != nil {
		return err
	}
	f := b.faces[arm]
	y0, y1 := min(f.clock.y, f.battery.y), max(f.clock.y+f.clock.h, f.battery.y+f.battery.h)
	return demo.CheckPanel(arm, b.expected(arm, int(binary.LittleEndian.Uint32(st)), v[0]), y0, y1-y0)
}

func panelOff(arm scouterkit.Arm) error {
	v, err := arm.Props(scouterkit.PropPanel)
	if err != nil {
		return err
	}
	if v[0] != 0 {
		return errors.New(arm.String() + " arm: the panel is on")
	}
	return nil
}

// check shows the dashboard (USER1), reads both panels back, and hides it
// (USER2): panels off.
func (b *board) check() error {
	for _, arm := range b.arms {
		if err := panelOff(arm); err != nil {
			return err
		}
		arm.Raise(scouterkit.TriggerUser1)
	}
	for _, arm := range b.arms {
		if _, err := b.wait(arm, 3); err != nil {
			return err
		}
		if err := b.checkShown(arm); err != nil {
			return err
		}
		arm.Raise(scouterkit.TriggerUser2)
	}
	for _, arm := range b.arms {
		if _, err := b.wait(arm, 0); err != nil {
			return err
		}
		if err := panelOff(arm); err != nil {
			return err
		}
	}
	return nil
}

func run(f demo.Flags, c config) error {
	cfg, arms, err := demo.Connect(f)
	if err != nil {
		return err
	}
	defer scouterkit.Deinit()
	b, err := load(cfg, arms, c)
	if err != nil {
		return err
	}
	if err := b.start(); err != nil {
		return err
	}
	for _, arm := range arms {
		v, err := arm.Props(scouterkit.PropPitch, scouterkit.PropRoll)
		if err != nil {
			return err
		}
		fmt.Printf("%s arm: running, hidden (pitch %s, roll %s): it shows above %d°\n", arm, demo.Degrees(v[0]),
			demo.Degrees(v[1]), c.angle)
	}
	if c.check {
		if err := b.check(); err != nil {
			return err
		}
		fmt.Println("check: shown on both panels as drawn here (clock and battery), then hidden with the panels off")
	}
	if c.power > 0 {
		stop := demo.Interrupted()
		hidden := demo.PrintCurrent("hidden", arms, c.power, stop)
		for _, arm := range arms {
			arm.Raise(scouterkit.TriggerUser1)
		}
		shown := demo.PrintCurrent("shown", arms, c.power, stop)
		for i := range arms {
			if len(hidden[i].MA) > 0 && len(shown[i].MA) > 0 {
				fmt.Printf("  %s arm: shown costs %+.1f mA over hidden\n", arms[i], shown[i].Mean()-hidden[i].Mean())
			}
		}
		if !c.show {
			for _, arm := range arms {
				arm.Raise(scouterkit.TriggerUser2)
			}
		}
	}
	if c.show {
		for _, arm := range arms {
			arm.Raise(scouterkit.TriggerUser1)
		}
	}
	if c.stay > 0 {
		b.watch(c.stay)
	}
	if err := demo.Halted(arms); err != nil {
		return err
	}
	if c.stop {
		demo.Stop(arms)
		fmt.Println("program stopped, panels off")
		return nil
	}
	fmt.Println("the dashboard keeps running on the arms")
	return nil
}

// watch prints the dashboard's changes for d.
func (b *board) watch(d time.Duration) {
	stop := demo.Interrupted()
	end := time.After(d)
	names := []string{"hidden", "shown", "hiding", "shown by the host"}
	for {
		select {
		case <-stop:
			return
		case <-end:
			return
		default:
		}
		for _, arm := range b.arms {
			if e, ok := arm.NextEvent(50 * time.Millisecond); ok && e.Type == scouterkit.EventNotify && e.A < 4 {
				fmt.Printf("%s %s arm: %s (pitch %s)\n", time.Now().Format("15:04:05.000"), arm, names[e.A], demo.Degrees(e.B))
			}
		}
	}
}
