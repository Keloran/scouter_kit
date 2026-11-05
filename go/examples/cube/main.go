// Command cube draws a wireframe cube on both eyes that turns against the
// head's pitch and roll, so it seems to stay put as the head tilts. A VM
// program on each arm reads its accelerometer's PITCH and ROLL, rotates the
// cube's corners in Q14 fixed point with a sine table in the arena, and
// draws the 12 edges with LINE, 20 times a second, with the host away.
//
//	go run ./examples/cube [flags]
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
	size, fps, spin, frames, rate, check int
	master                               string
	stay, power                          time.Duration
	leave                                bool
}

func main() {
	var f demo.Flags
	f.Register()
	var c config
	flag.IntVar(&c.size, "size", 150, "bitmap size, pixels (the cube is a fifth of it)")
	flag.IntVar(&c.fps, "fps", 15, "frames per second (lowered if the arms cannot keep up)")
	flag.IntVar(&c.spin, "spin", 2, "degrees per frame about the vertical axis (0: only the head turns it)")
	flag.IntVar(&c.frames, "frames", 0, "the arms stop after this many frames, panels off (0: never)")
	flag.IntVar(&c.rate, "rate", 52, "accelerometer rate while it runs, Hz (13, 26, 52 or 104)")
	flag.StringVar(&c.master, "master", "", `"left" or "right": both eyes use that arm's angles, forwarded by the host`)
	flag.IntVar(&c.check, "check", 0, "first draw this many frames one by one, reading both panels back")
	flag.DurationVar(&c.stay, "for", 0, "how long the host stays connected (0: until Ctrl-C or the last frame)")
	flag.BoolVar(&c.leave, "leave", false, "exit with the cube still turning (with -frames it stops by itself)")
	flag.DurationVar(&c.power, "power", 0, "measure the battery current while it runs, for this long (at least 66s)")
	demo.Usage("cube [flags]")
	flag.Parse()
	if flag.NArg() != 0 {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(f, c); err != nil {
		fmt.Fprintln(os.Stderr, "cube:", err)
		os.Exit(1)
	}
}

// cube is the program loaded on the arms.
type cube struct {
	g       geometry
	l       layout
	arms    []scouterkit.Arm
	pos     map[scouterkit.Arm][2]int
	master  scouterkit.Arm
	slave   scouterkit.Arm
	forward bool
}

func load(cfg scouterkit.Config, arms []scouterkit.Arm, c config) (*cube, error) {
	g, err := newGeometry(c.size)
	if err != nil {
		return nil, err
	}
	g.spin = (c.spin%360 + 360) % 360
	fps := max(1, c.fps)
	if limit := int(0.25e6 / g.cost()); fps > limit {
		fmt.Printf("%d fps would take more than the arms give a program (30 %%): %d fps\n", fps, limit)
		fps = limit
	}
	g.period = uint32(1e6 / fps)
	g.rate = c.rate
	base, err := scouterkit.Alloc(int(newLayout(0, g).end))
	if err != nil {
		return nil, err
	}
	cb := &cube{g: g, l: newLayout(base, g), arms: arms, pos: map[scouterkit.Arm][2]int{}}
	switch c.master {
	case "":
	case "left", "right":
		if len(arms) != 2 {
			return nil, errors.New("-master needs both arms")
		}
		cb.forward = true
		cb.master, cb.slave = scouterkit.Left, scouterkit.Right
		if c.master == "right" {
			cb.master, cb.slave = scouterkit.Right, scouterkit.Left
		}
	default:
		return nil, fmt.Errorf("-master %q: left or right", c.master)
	}
	vw, vh := scouterkit.Size()
	for _, arm := range arms {
		x, y := demo.PanelAt(cfg, arm, (vw-g.size)/2, (vh-g.size)/2)
		cb.pos[arm] = [2]int{x, y}
		p := program(cb.l, g, x, y)
		if p.Here() > cb.l.state {
			return nil, fmt.Errorf("program of %d bytes overlaps its state", p.Here()-base)
		}
		st := make([]uint32, stateWords)
		st[wLast], st[wLastBottom] = uint32(c.frames), uint32(g.size-1)
		if c.check > 0 {
			st[wMode] = 1
		}
		if cb.forward && arm == cb.slave {
			st[wMaster] = 1
		}
		arm.WriteArena(cb.l.state, demo.Words(st...))
		arm.WriteArena(cb.l.sines, tables(cb.l, g))
		if err := arm.Load(p); err != nil {
			return nil, err
		}
	}
	return cb, nil
}

// forwardAngles passes the master arm's angles to the other one.
func (cb *cube) forwardAngles() {
	if !cb.forward {
		return
	}
	if v, err := cb.master.Props(scouterkit.PropPitch, scouterkit.PropRoll); err == nil {
		cb.slave.WriteArena(cb.l.state+4*wMailPitch, demo.Words(v[0], v[1]))
	}
}

// last reads the angles and the spin the arm drew its last frame with.
func (cb *cube) last(arm scouterkit.Arm) (pitch, roll, yaw int32, frames uint32, err error) {
	b, err := arm.ReadArena(cb.l.state, 4*(wRoll+1))
	if err != nil {
		return 0, 0, 0, 0, err
	}
	get := func(i int) int32 { return int32(binary.LittleEndian.Uint32(b[4*i:])) }
	return get(wPitch), get(wRoll), get(wYaw), uint32(get(wFrames)), nil
}

func (cb *cube) expected(arm scouterkit.Arm, pitch, roll, yaw int32) *demo.Image {
	m := demo.NewPanel()
	m.Paste(cb.g.render(pitch, roll, yaw), cb.pos[arm][0], cb.pos[arm][1])
	return m
}

func (cb *cube) check(arm scouterkit.Arm, pitch, roll, yaw int32) error {
	y0 := max(0, cb.pos[arm][1])
	return demo.CheckPanel(arm, cb.expected(arm, pitch, roll, yaw), y0, min(scouterkit.PanelHeight, cb.pos[arm][1]+cb.g.size)-y0)
}

// steps draws n frames in step mode and checks both panels after each.
func (cb *cube) steps(n int) error {
	for _, arm := range cb.arms {
		if err := arm.Start(cb.l.base); err != nil {
			return err
		}
	}
	for k := 1; k <= n; k++ {
		cb.forwardAngles()
		for _, arm := range cb.arms {
			arm.Raise(scouterkit.TriggerUser3)
		}
		for _, arm := range cb.arms {
			e, err := demo.WaitNotify(arm, 5*time.Second)
			if err != nil {
				return err
			}
			pitch, roll, yaw, frames, err := cb.last(arm)
			if err != nil {
				return err
			}
			if e.A != uint32(k) || frames != uint32(k) || yaw != int32(k*cb.g.spin%360) {
				return fmt.Errorf("%s arm: frame %d: notified %d, yaw %d", arm, k, e.A, yaw)
			}
			if err := cb.check(arm, pitch, roll, yaw); err != nil {
				return fmt.Errorf("frame %d (pitch %d°, roll %d°, spin %d°): %w", k, pitch, roll, yaw, err)
			}
		}
	}
	return nil
}

// play starts the timed frames at host time h0.
func (cb *cube) play(h0 int64, started bool) error {
	for _, arm := range cb.arms {
		due := arm.DeviceTime(time.Duration(h0-scouterkit.NowUS()) * time.Microsecond)
		if started {
			arm.WriteArena(cb.l.state+4*wDue, demo.Words(due, 0))
			arm.Raise(scouterkit.TriggerUser0)
			continue
		}
		arm.WriteArena(cb.l.state+4*wDue, demo.Words(due))
		if err := arm.Start(cb.l.base); err != nil {
			return err
		}
	}
	return nil
}

// checkStopped compares a stopped arm's panel with its last frame (or the
// one before, if it stopped between its angles and its DRAW).
func (cb *cube) checkStopped(arm scouterkit.Arm) error {
	pitch, roll, yaw, frames, err := cb.last(arm)
	if err != nil {
		return err
	}
	err = cb.check(arm, pitch, roll, yaw)
	if err != nil && cb.check(arm, pitch, roll, (yaw-int32(cb.g.spin)+360)%360) == nil {
		err = nil
	}
	if err == nil {
		fmt.Printf("%s arm: stopped after %d frames, panel as drawn here (pitch %d°, roll %d°, spin %d°)\n", arm, frames,
			pitch, roll, yaw)
	}
	return err
}

func run(f demo.Flags, c config) error {
	cfg, arms, err := demo.Connect(f)
	if err != nil {
		return err
	}
	defer scouterkit.Deinit()
	cb, err := load(cfg, arms, c)
	if err != nil {
		return err
	}
	if c.check > 0 {
		if err := cb.steps(c.check); err != nil {
			return err
		}
		fmt.Printf("step mode: %d frames as expected on both panels\n", c.check)
		err = cb.play(scouterkit.NowUS()+300000, true)
	} else {
		err = cb.play(scouterkit.NowUS()+300000, false)
	}
	if err != nil {
		return err
	}
	if err := cb.watch(c); err != nil {
		return err
	}
	demo.Status(arms)
	if c.check > 0 {
		for _, arm := range arms {
			if err := arm.Stop(); err != nil {
				return err
			}
			if v, _ := arm.Props(scouterkit.PropPanel); len(v) == 1 && v[0] == 0 {
				fmt.Printf("%s arm: panel off after the last frame\n", arm)
				continue
			}
			if err := cb.checkStopped(arm); err != nil {
				return err
			}
		}
	}
	if c.leave {
		fmt.Println("the cube keeps turning on the arms")
		return nil
	}
	demo.Stop(arms)
	for _, arm := range arms {
		arm.SysWrite(scouterkit.PropIMURate, 13)
	}
	return nil
}

// watch forwards the angles with -master, measures with -power, and waits
// for -for, the last frame or Ctrl-C.
func (cb *cube) watch(c config) error {
	interrupted := demo.Interrupted()
	quit := make(chan struct{})
	defer close(quit)
	stay := c.stay
	if c.power > 0 {
		stay = max(stay, c.power+time.Second)
	}
	if stay == 0 && c.frames == 0 && (c.check > 0 || c.leave) {
		stay = 10 * time.Second
	}
	if stay == 0 && c.frames > 0 {
		stay = time.Duration(c.frames)*time.Duration(cb.g.period)*time.Microsecond + time.Second
	}
	end := time.After(stay)
	if stay == 0 {
		end = nil
	}
	var power chan []demo.Current
	if c.power > 0 {
		power = make(chan []demo.Current, 1)
		go func() { power <- demo.PrintCurrent("turning", cb.arms, c.power, quit) }()
	}
	tick := time.NewTicker(time.Duration(cb.g.period) * time.Microsecond)
	defer tick.Stop()
	for {
		select {
		case <-interrupted:
			return nil
		case <-end:
			if power != nil {
				<-power
			}
			return nil
		case <-tick.C:
			cb.forwardAngles()
		}
		for _, arm := range cb.arms {
			if e, ok := arm.NextEvent(0); ok {
				switch e.Type {
				case scouterkit.EventHalt:
					return fmt.Errorf("%s arm: the program halted: code %d, op 0x%02x at 0x%05x", arm, e.Code, e.Op, e.Address)
				case scouterkit.EventReset:
					return errors.New(arm.String() + " arm: rebooted, the cube is gone")
				}
			}
		}
	}
}
