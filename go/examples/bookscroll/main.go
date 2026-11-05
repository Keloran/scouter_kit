// Command bookscroll scrolls a text file on both eyes at a set reading
// speed. The host lays the text out with a font from font_converter.py (or
// the built-in Departure Mono) and puts the lines, as glyph indices, in a
// ring in the arena; a VM program scrolls the page by itself on a timer and
// draws each line as it comes in, and the host refills the ring before the
// program needs the next lines.
//
//	go run ./examples/bookscroll [flags] FILE.txt
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
	path, font              string
	scale, wpm, step, w, h  int
	hold, stay, away, power time.Duration
	check, ring             int
	leave                   bool
	giveUp                  time.Duration // without the next line (0: a minute)
}

func main() {
	var f demo.Flags
	f.Register()
	var c config
	flag.StringVar(&c.font, "font", "", "font blob made by font_converter.py (default: the built-in Departure Mono)")
	flag.IntVar(&c.scale, "scale", 0, "enlarge a pixel font this many times (0: 2 for the built-in font, else 1)")
	flag.IntVar(&c.wpm, "wpm", 220, "reading speed, words per minute")
	flag.IntVar(&c.step, "step", 0, "rows per scroll step (0: a whole line; fewer is smoother and costs more)")
	flag.IntVar(&c.w, "width", 540, "text area width (540: the columns both eyes see)")
	flag.IntVar(&c.h, "height", 400, "text area height")
	flag.DurationVar(&c.hold, "hold", 3*time.Second, "how long the first page stays before the scrolling starts")
	flag.IntVar(&c.ring, "ring", 0, "lines the arms hold ahead (0: as many as fit; fewer uploads faster)")
	flag.IntVar(&c.check, "check", 0, "first scroll this many steps one by one, reading both panels back")
	flag.DurationVar(&c.stay, "for", 0, "how long the host stays connected (0: until Ctrl-C or the end of the text)")
	flag.BoolVar(&c.leave, "leave", false, "exit while the text scrolls on (the arms stop a minute after the ring runs out)")
	flag.DurationVar(&c.away, "away", 0, "drop the links for this long while scrolling, then check the arms")
	flag.DurationVar(&c.power, "power", 0, "measure the battery current while scrolling, for this long (at least 66s)")
	demo.Usage("bookscroll [flags] FILE.txt")
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	c.path = flag.Arg(0)
	if err := run(f, c); err != nil {
		fmt.Fprintln(os.Stderr, "bookscroll:", err)
		os.Exit(1)
	}
}

// reader is a book loaded on the arms.
type reader struct {
	g     geometry
	l     layout
	lines []line
	font  *demo.Font
	arms  []scouterkit.Arm
	pos   map[scouterkit.Arm][2]int
	avail map[scouterkit.Arm]int
}

func load(cfg scouterkit.Config, arms []scouterkit.Arm, text string, c config) (*reader, error) {
	blob, scale := scouterkit.DepartureMono(), 2
	if c.font != "" {
		b, err := os.ReadFile(c.font)
		if err != nil {
			return nil, err
		}
		blob, scale = b, 1
	}
	if c.scale > 0 {
		scale = c.scale
	}
	font, err := scouterkit.LoadFont(blob, charset(text), scale)
	if err != nil {
		return nil, err
	}
	lines := wrap(text, font, c.w-2*margin, recordBytes-4)
	if len(lines) == 0 {
		return nil, errors.New("no text")
	}
	count, filled := 0, 0
	for _, ln := range lines {
		count += ln.words
		if ln.words > 0 {
			filled++
		}
	}
	g := geometry{w: c.w &^ 1, h: c.h, lineH: font.LineSpacing, ascent: font.Ascent, step: c.step, total: len(lines),
		endHold: 30000000, maxWait: 60000000}
	if c.giveUp > 0 {
		g.maxWait = uint32(c.giveUp.Microseconds())
	}
	if g.step <= 0 || g.step > g.lineH {
		g.step = g.lineH
	}
	perLine := 60e6 * float64(count) / float64(max(1, filled)) / float64(c.wpm)
	g.period = uint32(perLine * float64(g.step) / float64(g.lineH))
	budget := scouterkit.FreeMemory() - codeBytes - 4*stateWords - g.w/2*(g.h+g.guard()) - 64
	g.ring = min(len(lines), budget/recordBytes)
	if c.ring > 0 {
		g.ring = min(g.ring, c.ring)
	}
	if g.ring < min(len(lines), g.h/g.lineH+8) {
		return nil, fmt.Errorf("no room for the ring (%d bytes free)", budget)
	}
	base, err := scouterkit.Alloc(int(newLayout(0, g).end))
	if err != nil {
		return nil, err
	}
	rd := &reader{g: g, l: newLayout(base, g), lines: lines, arms: arms, pos: map[scouterkit.Arm][2]int{},
		avail: map[scouterkit.Arm]int{}}
	if rd.font, err = demo.FontOf(arms[0], font); err != nil {
		return nil, err
	}
	vw, vh := scouterkit.Size()
	for _, arm := range arms {
		x, y := demo.PanelAt(cfg, arm, (vw-g.w)/2, (vh-g.h)/2)
		rd.pos[arm] = [2]int{x, y}
		p := program(rd.l, g, font.Address(), x, y)
		if p.Here() > rd.l.state {
			return nil, fmt.Errorf("program of %d bytes overlaps its state", p.Here()-base)
		}
		st := make([]uint32, stateWords)
		if c.check > 0 {
			st[wMode] = 1
		}
		arm.WriteArena(rd.l.state, demo.Words(st...))
		if err := arm.Load(p); err != nil {
			return nil, err
		}
		rd.refill(arm, 0)
	}
	t0 := time.Now()
	for _, arm := range arms {
		n := uint32(rd.avail[arm] * recordBytes)
		got, want, err := arm.ArenaCRC(rd.l.ring, n)
		if err != nil {
			return nil, fmt.Errorf("%s arm: ring read-back: %w", arm, err)
		}
		if got != want {
			return nil, fmt.Errorf("%s arm: the ring read back %08x, sent %08x", arm, got, want)
		}
	}
	fmt.Printf("ring uploaded and read back in %.1f s\n", time.Since(t0).Seconds())
	fmt.Printf("%d lines (%d words), %d in the ring, %.1f s per line, %d px per step every %.0f ms, %s for the whole text\n",
		len(lines), count, g.ring, perLine/1e6, g.step, float64(g.period)/1000,
		(time.Duration(float64(len(lines))*perLine) * time.Microsecond).Round(time.Second))
	return rd, nil
}

// refill puts every line the program may still need in the arm's ring:
// from the one two above the last line that came in.
func (rd *reader) refill(arm scouterkit.Arm, top int) {
	to := min(rd.g.total, max(0, top-2)+rd.g.ring)
	if to > rd.avail[arm] {
		fill(arm, rd.l, rd.g, rd.lines, rd.avail[arm], to)
		rd.avail[arm] = to
	}
}

// expected is the arm's panel with the area showing page row pos.
func (rd *reader) expected(arm scouterkit.Arm, pos int) *demo.Image {
	m := demo.NewPanel()
	m.Paste(page(rd.font, rd.g, rd.lines, pos), rd.pos[arm][0], rd.pos[arm][1])
	return m
}

func (rd *reader) check(arm scouterkit.Arm, pos int) error {
	y0 := max(0, rd.pos[arm][1])
	rows := min(scouterkit.PanelHeight, rd.pos[arm][1]+rd.g.h) - y0
	return demo.CheckPanel(arm, rd.expected(arm, pos), y0, rows)
}

func (rd *reader) state(arm scouterkit.Arm, w int) (uint32, error) {
	b, err := arm.ReadArena(rd.l.state+4*uint32(w), 4)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(b), nil
}

// steps starts the program in step mode and scrolls n steps, checking both
// panels after the first page and after every step.
func (rd *reader) steps(n int) error {
	for _, arm := range rd.arms {
		if err := arm.Start(rd.l.base); err != nil {
			return err
		}
	}
	for k := 0; k <= n; k++ {
		for _, arm := range rd.arms {
			if k > 0 {
				arm.Raise(scouterkit.TriggerUser3)
			}
		}
		for _, arm := range rd.arms {
			e, err := demo.WaitNotify(arm, 5*time.Second)
			if err != nil {
				return err
			}
			kind, pos := e.B>>24, int(e.B&0xFFFFFF)
			if k == 0 {
				pos = 0
			}
			if want := uint32(stepDone); k == 0 && kind != newLine || k > 0 && kind != want || pos != k*rd.g.step {
				return fmt.Errorf("%s arm: step %d: notified kind %d at page row %d", arm, k, kind, pos)
			}
			if err := rd.check(arm, pos); err != nil {
				return fmt.Errorf("step %d: %w", k, err)
			}
			rd.refill(arm, int(e.A))
		}
	}
	return nil
}

// play starts the timed scrolling: the next step at host time h0.
func (rd *reader) play(h0 int64, started bool) error {
	for _, arm := range rd.arms {
		due := arm.DeviceTime(time.Duration(h0-scouterkit.NowUS()) * time.Microsecond)
		if started {
			arm.WriteArena(rd.l.state+4*wDue, demo.Words(due, 0))
			arm.Raise(scouterkit.TriggerUser0)
			continue
		}
		arm.WriteArena(rd.l.state+4*wDue, demo.Words(due))
		if err := arm.Start(rd.l.base); err != nil {
			return err
		}
	}
	return nil
}

func run(f demo.Flags, c config) error {
	text, err := os.ReadFile(c.path)
	if err != nil {
		return err
	}
	cfg, arms, err := demo.Connect(f)
	if err != nil {
		return err
	}
	defer scouterkit.Deinit()
	t0 := time.Now()
	rd, err := load(cfg, arms, string(text), c)
	if err != nil {
		return err
	}
	if c.check > 0 {
		if err := rd.steps(c.check); err != nil {
			return err
		}
		fmt.Printf("step mode: the first page and %d steps as expected on both panels (%.1f s)\n", c.check,
			time.Since(t0).Seconds())
		err = rd.play(scouterkit.NowUS()+int64(c.hold/time.Microsecond), true)
	} else {
		err = rd.play(scouterkit.NowUS()+int64(c.hold/time.Microsecond)+300000, false)
	}
	if err != nil {
		return err
	}
	atEnd, err := rd.watch(c)
	demo.Status(arms)
	if err != nil {
		return err
	}
	if c.check > 0 && !atEnd {
		for _, arm := range arms {
			if err := arm.Stop(); err != nil {
				return err
			}
			pos, err := rd.state(arm, wShown)
			if err != nil {
				return err
			}
			if err := rd.check(arm, int(pos)); err != nil && rd.check(arm, int(pos)+rd.g.step) != nil {
				return err
			}
			fmt.Printf("%s arm: stopped at page row %d, panel as expected\n", arm, pos)
		}
	}
	if c.leave {
		fmt.Println("the text scrolls on")
		return nil
	}
	demo.Stop(arms)
	return nil
}

// watch refills the rings as the lines come in, until -for, the end of the
// text or Ctrl-C; with -away and -power on the way.
func (rd *reader) watch(c config) (bool, error) {
	interrupted := demo.Interrupted()
	quit := make(chan struct{})
	defer close(quit)
	stay := c.stay
	if c.power > 0 {
		stay = max(stay, c.power+time.Second)
	}
	if stay == 0 && (c.check > 0 || c.leave) {
		stay = 10 * time.Second
	}
	var end <-chan time.Time
	if stay > 0 {
		end = time.After(stay)
	}
	var power chan []demo.Current
	if c.power > 0 {
		power = make(chan []demo.Current, 1)
		go func() { power <- demo.PrintCurrent("scrolling", rd.arms, c.power, quit) }()
	}
	awayAt := time.Now().Add(c.hold + 5*time.Second)
	finished := map[scouterkit.Arm]bool{}
	shown := 0
	var skew demo.Skew
	defer func() { fmt.Println(skew.String()) }()
	started := map[scouterkit.Arm]bool{}
	for {
		select {
		case <-interrupted:
			return false, nil
		case <-end:
			if power != nil {
				<-power
			}
			return false, nil
		default:
		}
		for _, arm := range rd.arms {
			e, ok := arm.NextEvent(20 * time.Millisecond)
			if !ok {
				continue
			}
			switch e.Type {
			case scouterkit.EventHalt:
				return false, fmt.Errorf("%s arm: the program halted: code %d, op 0x%02x at 0x%05x", arm, e.Code, e.Op, e.Address)
			case scouterkit.EventReset:
				return false, errors.New(arm.String() + " arm: rebooted, the book is gone")
			case scouterkit.EventNotify:
				if e.Trigger == 0xFF {
					continue
				}
				switch e.B >> 24 {
				case newLine:
					if started[arm] || c.check > 0 {
						skew.At(arm, e.A, demo.Before24(e, e.B&0xFFFFFF))
					}
					started[arm] = true
				case waiting:
					fmt.Printf("%s arm: waiting for line %d\n", arm, e.A)
				case ended:
					finished[arm] = true
				}
				rd.refill(arm, int(e.A))
				if arm == rd.arms[0] && int(e.A)*10/rd.g.total > shown {
					shown = int(e.A) * 10 / rd.g.total
					fmt.Printf("line %d of %d\n", e.A, rd.g.total)
				}
			}
		}
		if len(finished) == len(rd.arms) {
			fmt.Println("the end of the text: the last page stays 30 s, then the panels go off")
			return true, nil
		}
		if c.away > 0 && time.Now().After(awayAt) {
			d := c.away
			c.away = 0
			if err := rd.goAway(d); err != nil {
				return false, err
			}
		}
	}
}

func (rd *reader) goAway(d time.Duration) error {
	if err := demo.Away(rd.arms, d); err != nil {
		return err
	}
	for _, arm := range rd.arms {
		top, err := rd.state(arm, wTop)
		if err != nil {
			return err
		}
		fmt.Printf("%s arm: back, at line %d, still running\n", arm, top)
		rd.refill(arm, int(top))
	}
	return nil
}
