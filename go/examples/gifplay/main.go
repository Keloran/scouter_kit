// Command gifplay plays an animated GIF on both eyes from the arms' own
// memory: the frames are stored PackBits-packed in the arena as the changed
// rectangle against the previous frame, and a VM program unpacks and draws
// each one on a timer, at times the host gave each arm in its own clock, so
// the eyes stay in step with the host away. The frames are scaled until all
// of them fit in the arena.
//
//	go run ./examples/gifplay [flags] FILE.gif
package main

import (
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"time"

	"scouterkit"
	"scouterkit/examples/internal/demo"
	"scouterkit/examples/internal/picture"
)

type config struct {
	path         string
	loops        int
	stay, away   time.Duration
	power        time.Duration
	leave, check bool
	scale        float64
	maxKB        int
	opts         options
	save         string
}

func main() {
	var f demo.Flags
	f.Register()
	var c config
	flag.IntVar(&c.loops, "loops", 0, "the arms stop after this many loops, panels off (0: never)")
	flag.DurationVar(&c.stay, "for", 0, "how long the host stays connected (0: until Ctrl-C or the last loop)")
	flag.BoolVar(&c.leave, "leave", false, "exit with the animation still playing (the host steps back)")
	flag.DurationVar(&c.away, "away", 0, "drop the links for this long while playing, then check the arms")
	flag.BoolVar(&c.check, "check", false, "first step through every frame, reading both panels back")
	flag.DurationVar(&c.power, "power", 0, "measure the battery current while playing, for this long (at least 66s)")
	flag.Float64Var(&c.scale, "scale", 0, "scale factor (0: the largest at which every frame fits)")
	flag.IntVar(&c.maxKB, "max-kb", 0, "use at most this much of the arena, KB (0: all that is free; less uploads faster)")
	flag.Float64Var(&c.opts.gamma, "gamma", 1, "tone curve exponent before quantizing to the 16 levels")
	flag.BoolVar(&c.opts.dither, "dither", false, "Floyd–Steinberg inside each changed rectangle (packs worse)")
	flag.StringVar(&c.save, "save", "", "write the virtual display with the first frame to this PNG file")
	demo.Usage("gifplay [flags] FILE.gif")
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	c.path = flag.Arg(0)
	if err := run(f, c); err != nil {
		fmt.Fprintln(os.Stderr, "gifplay:", err)
		os.Exit(1)
	}
}

// player is an animation loaded on the arms.
type player struct {
	a     *anim
	l     layout
	arms  []scouterkit.Arm
	pos   map[scouterkit.Arm][2]int
	probe uint32
	draw  map[int]uint32 // µs per frame, from the probes: its DRAW...
	comp  map[int]uint32 // ...and composing it
}

func load(cfg scouterkit.Config, arms []scouterkit.Arm, src *source, c config) (*player, error) {
	budget := scouterkit.FreeMemory() - 1024 - 4*stateWords - 64
	if c.maxKB > 0 {
		budget = min(budget, 1024*c.maxKB-1024-4*stateWords)
	}
	w, h := scouterkit.Size()
	a, err := src.fit(budget, w, h, c.scale, c.opts)
	if err != nil {
		return nil, err
	}
	base, err := scouterkit.Alloc(int(newLayout(0, a).end))
	if err != nil {
		return nil, err
	}
	pl := &player{a: a, l: newLayout(base, a), arms: arms, pos: map[scouterkit.Arm][2]int{},
		draw: map[int]uint32{}, comp: map[int]uint32{}}
	vx, vy := (w-a.w)/2, (h-a.h)/2
	for _, arm := range arms {
		x, y := demo.PanelAt(cfg, arm, vx, vy)
		pl.pos[arm] = [2]int{x, y}
		p, probe := program(pl.l, a, x, y)
		pl.probe = probe
		if p.Here() > pl.l.state {
			return nil, fmt.Errorf("program of %d bytes overlaps its state", p.Here()-base)
		}
		if err := upload(arm, pl.l, a, p, state(0, c.check, c.loops)); err != nil {
			return nil, err
		}
	}
	return pl, nil
}

// verify compares the uploaded tables and frames with the library's copy.
func (pl *player) verify() error {
	for _, arm := range pl.arms {
		got, want, err := arm.ArenaCRC(pl.l.frames, pl.l.canvas-pl.l.frames)
		if err != nil {
			return fmt.Errorf("%s arm: arena read-back: %w", arm, err)
		}
		if got != want {
			return fmt.Errorf("%s arm: frames read back %08x, sent %08x", arm, got, want)
		}
	}
	return nil
}

// expected is the arm's panel showing frame i.
func (pl *player) expected(arm scouterkit.Arm, i int) *demo.Image {
	m := demo.NewPanel()
	m.Paste(pl.a.frames[i], pl.pos[arm][0], pl.pos[arm][1])
	return m
}

// rows are the panel rows the canvas covers on the arm.
func (pl *player) rows(arm scouterkit.Arm) (int, int) {
	y0 := max(0, pl.pos[arm][1])
	return y0, min(scouterkit.PanelHeight, pl.pos[arm][1]+pl.a.h) - y0
}

func (pl *player) start() error {
	for _, arm := range pl.arms {
		if err := arm.Start(pl.l.base); err != nil {
			return err
		}
	}
	return nil
}

// step shows the next frame on each arm from host code (CALL of the probe,
// which times it and the composing of the one after) and checks both
// panels: whole panels at the first and the last step, the canvas rows
// otherwise.
func (pl *player) step(k, last int) error {
	for _, arm := range pl.arms {
		arm.Call(pl.probe)
	}
	for _, arm := range pl.arms {
		e, err := demo.WaitNotify(arm, 5*time.Second)
		if err != nil {
			return err
		}
		i := k % len(pl.a.frames)
		if int(e.A) != i {
			return fmt.Errorf("%s arm: drew frame %d, expected %d", arm, e.A, i)
		}
		if k > 0 && k < last {
			pl.draw[i] = max(pl.draw[i], e.B&0xFFFF)
			pl.comp[(i+1)%len(pl.a.frames)] = max(pl.comp[(i+1)%len(pl.a.frames)], e.B>>16)
		}
		y0, rows := pl.rows(arm)
		if k == 0 || k == last {
			y0, rows = 0, scouterkit.PanelHeight
		}
		if err := demo.CheckPanel(arm, pl.expected(arm, i), y0, rows); err != nil {
			return fmt.Errorf("frame %d: %w", i, err)
		}
	}
	return nil
}

// play starts the timed playback on every arm: the next frame at host time
// h0 (in each arm's clock), loop counter 0.
func (pl *player) play(h0 int64) {
	for _, arm := range pl.arms {
		due := arm.DeviceTime(time.Duration(h0-scouterkit.NowUS()) * time.Microsecond)
		arm.WriteArena(pl.l.state+4*wDue, demo.Words(due, 0, 0, 0))
		arm.Raise(scouterkit.TriggerUser0)
	}
}

// timeline is the host's view of the playback: loop L (≥ 1) begins at
// first + (L-1)·period on the host clock.
type timeline struct{ first, period int64 }

func (t timeline) loopStart(loop uint32) int64 { return t.first + int64(loop-1)*t.period }

// anchor tells every arm when loop L begins, in its own clock.
func (pl *player) anchor(t timeline, loop uint32) {
	for _, arm := range pl.arms {
		at := arm.DeviceTime(time.Duration(t.loopStart(loop)-scouterkit.NowUS()) * time.Microsecond)
		arm.WriteArena(pl.l.state+4*wMailLoop, demo.Words(loop, at))
	}
}

func (pl *player) shown(arm scouterkit.Arm) (int, error) {
	b, err := arm.ReadArena(pl.l.state+4*wShown, 4)
	if err != nil {
		return 0, err
	}
	return int(binary.LittleEndian.Uint32(b)), nil
}

// checkStopped compares the stopped arm's panel with the frame it shows (or
// the one before, when it stopped just before that frame's DRAW).
func (pl *player) checkStopped(arm scouterkit.Arm) error {
	i, err := pl.shown(arm)
	if err != nil {
		return err
	}
	n := len(pl.a.frames)
	y0, rows := pl.rows(arm)
	err = demo.CheckPanel(arm, pl.expected(arm, i), y0, rows)
	if err != nil && demo.CheckPanel(arm, pl.expected(arm, (i+n-1)%n), y0, rows) == nil {
		err = nil
	}
	if err == nil {
		fmt.Printf("%s arm: stopped showing frame %d, panel as expected\n", arm, i)
	}
	return err
}

func run(f demo.Flags, c config) error {
	src, err := decode(c.path)
	if err != nil {
		return err
	}
	cfg, arms, err := demo.Connect(f)
	if err != nil {
		return err
	}
	defer scouterkit.Deinit()
	t0 := time.Now()
	pl, err := load(cfg, arms, src, c)
	if err != nil {
		return err
	}
	a := pl.a
	fmt.Printf("%d frames of %d (scale %.2f) at %dx%d, %.1f s per loop, %d KB in the arena per arm; "+
		"the arms busy up to %.0f%% of any 100 ms\n", len(a.frames), len(src.frames), a.scale, a.w, a.h,
		a.duration().Seconds(), (pl.l.end-pl.l.base)/1024, 100*a.load())
	if c.save != "" {
		w, h := scouterkit.Size()
		v := demo.NewImage(w, h)
		v.Paste(a.frames[0], (w-a.w)/2, (h-a.h)/2)
		if err := picture.SavePNG(c.save, v.Gray()); err != nil {
			return err
		}
	}
	if err := pl.verify(); err != nil {
		return err
	}
	fmt.Printf("uploaded and read back in %.1f s\n", time.Since(t0).Seconds())
	if c.check {
		if err := pl.start(); err != nil {
			return err
		}
		n := len(a.frames)
		for k := 0; k <= n; k++ {
			if err := pl.step(k, n); err != nil {
				return err
			}
		}
		fmt.Printf("step mode: frames 0...%d and the wrap to 0 drawn as expected on both panels\n", n-1)
		pl.report()
	}
	h0 := scouterkit.NowUS() + 500000
	next := 0
	if c.check {
		next = 1 % len(a.frames)
		pl.play(h0)
	} else {
		for _, arm := range arms {
			arm.WriteArena(pl.l.state+4*wDue, demo.Words(arm.DeviceTime(time.Duration(h0-scouterkit.NowUS())*time.Microsecond)))
		}
		if err := pl.start(); err != nil {
			return err
		}
	}
	rest := int64(0)
	for i := next; i < len(a.frames); i++ {
		rest += int64(a.delays[i])
	}
	tl := timeline{first: h0 + rest, period: a.duration().Microseconds()}
	finished, err := pl.watch(tl, c)
	demo.Status(arms)
	if err != nil {
		return err
	}
	if c.check {
		for _, arm := range arms {
			if err := arm.Stop(); err != nil {
				return err
			}
			if finished {
				if v, err := arm.Props(scouterkit.PropPanel); err != nil || v[0] != 0 {
					return fmt.Errorf("%s arm: panel %v after the last loop (%v)", arm, v, err)
				}
				fmt.Printf("%s arm: panel off after the last loop\n", arm)
			} else if err := pl.checkStopped(arm); err != nil {
				return err
			}
		}
	}
	if c.leave && !c.check {
		fmt.Println("the animation keeps playing on the arms")
		return nil
	}
	demo.Stop(arms)
	return nil
}

// watch follows the playback: each loop's start on both arms (the eyes'
// difference), a new anchor every minute, -away and -power, until -for, the
// last loop or Ctrl-C.
func (pl *player) watch(tl timeline, c config) (bool, error) {
	interrupted := demo.Interrupted()
	quit := make(chan struct{})
	defer close(quit)
	stay := c.stay
	if c.power > 0 {
		stay = max(stay, c.power+time.Second)
	}
	if stay == 0 && c.loops == 0 && (c.check || c.leave) {
		stay = 10 * time.Second
	}
	var end <-chan time.Time
	if stay > 0 {
		end = time.After(stay)
	}
	var power chan []demo.Current
	if c.power > 0 {
		power = make(chan []demo.Current, 1)
		go func() { power <- demo.PrintCurrent("playing", pl.arms, c.power, quit) }()
	}
	var skew demo.Skew
	var loop uint32
	anchored := time.Now()
	awayDone := c.away == 0
	finished := map[scouterkit.Arm]bool{}
	for {
		select {
		case <-interrupted:
			fmt.Println(skew.String())
			return false, nil
		case <-end:
			if power != nil {
				<-power
			}
			fmt.Println(skew.String())
			return false, nil
		default:
		}
		for _, arm := range pl.arms {
			e, ok := arm.NextEvent(20 * time.Millisecond)
			if !ok {
				continue
			}
			switch e.Type {
			case scouterkit.EventHalt:
				return false, fmt.Errorf("%s arm: the program halted: code %d, op 0x%02x at 0x%05x", arm, e.Code, e.Op, e.Address)
			case scouterkit.EventReset:
				return false, errors.New(arm.String() + " arm: rebooted, the animation is gone")
			case scouterkit.EventNotify:
				if e.Trigger == 0xFF {
					continue
				}
				loop = max(loop, e.A)
				if d, ok := skew.Add(arm, e.A, e); ok {
					fmt.Printf("loop %d: begun on both arms, %+.1f ms apart\n", e.A, d)
				}
				if c.loops > 0 && e.A == uint32(c.loops) {
					finished[arm] = true
				}
			}
		}
		if c.loops > 0 && len(finished) == len(pl.arms) {
			time.Sleep(time.Duration(pl.a.delays[len(pl.a.delays)-1])*time.Microsecond + 200*time.Millisecond)
			fmt.Println(skew.String())
			fmt.Printf("%d loops played, the arms switched their panels off\n", c.loops)
			return true, nil
		}
		if loop > 0 && time.Since(anchored) > time.Minute {
			ahead := uint32(max(2, math.Ceil(3e6/float64(tl.period))))
			pl.anchor(tl, loop+ahead)
			anchored = time.Now()
		}
		if !awayDone && loop >= 2 {
			awayDone = true
			if err := pl.goAway(c.away); err != nil {
				return false, err
			}
		}
	}
}

func (pl *player) goAway(d time.Duration) error {
	if err := demo.Away(pl.arms, d); err != nil {
		return err
	}
	for _, arm := range pl.arms {
		b, err := arm.ReadArena(pl.l.state+4*wLoop, 4)
		if err != nil {
			return err
		}
		fmt.Printf("%s arm: back, %d loops played so far, still running\n", arm, binary.LittleEndian.Uint32(b))
	}
	return nil
}

// report prints what each frame cost the slower arm: its share of the
// frame's time against the 30 ms per 100 ms the arms give a program.
func (pl *player) report() {
	var sum, top uint32
	share, count := 0.0, 0
	for i, d := range pl.a.deltas {
		t := pl.draw[i] + pl.comp[i]
		if i == 0 && len(pl.a.frames) > 1 {
			continue
		}
		count++
		raw, packed := 0, 0
		for _, c := range d.chunks {
			raw += d.rowBytes(pl.a.w) * c.h
			packed += len(c.packed)
		}
		fmt.Printf("frame %d: %dx%d at (%d, %d), %d chunks, %d bytes from %d packed: %d µs (model %.0f)\n", i, d.w,
			d.h, d.x, d.y, len(d.chunks), raw, packed, t, pl.a.cost(i))
		sum += t
		top = max(top, t)
		share = max(share, float64(t)/float64(pl.a.delays[i]))
	}
	fmt.Printf("frames take %.1f ms on average, %.1f ms at most: up to %.0f%% of a frame's time (programs get 30%%)\n",
		float64(sum)/float64(max(1, count))/1000, float64(top)/1000, 100*share)
}
