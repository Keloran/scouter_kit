// Command rsvp shows a text one word at a time (rapid serial visual
// presentation) on both eyes. Each word is three glyph runs around its
// optimal recognition point, the letter the eye fixes on, which stays at
// the same place; a VM program shows them on a timer at the set speed, from
// a ring of words in the arena that the host refills.
//
//	go run ./examples/rsvp [flags] FILE.txt
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
	scale, wpm, w, ring     int
	side                    int
	orp                     bool
	hold, stay, away, power time.Duration
	check                   int
	leave                   bool
	giveUp                  time.Duration // with the ring empty (0: a minute)
}

func main() {
	var f demo.Flags
	f.Register()
	var c config
	flag.IntVar(&c.wpm, "wpm", 300, "words per minute")
	flag.BoolVar(&c.orp, "orp", true, "align each word on its ORP letter, drawn brighter (false: centred words)")
	flag.IntVar(&c.side, "side", 7, "level 1...15 of the letters around the ORP letter (15)")
	flag.StringVar(&c.font, "font", "", "font blob made by font_converter.py (default: the built-in Departure Mono)")
	flag.IntVar(&c.scale, "scale", 0, "enlarge a pixel font this many times (0: 3 for the built-in font, else 1)")
	flag.IntVar(&c.w, "width", 540, "band width (540: the columns both eyes see)")
	flag.DurationVar(&c.hold, "hold", 2*time.Second, "time before the first word")
	flag.IntVar(&c.ring, "ring", 0, "words the arms hold ahead (0: as many as fit; fewer uploads faster)")
	flag.IntVar(&c.check, "check", 0, "first show this many words one by one, reading both panels back")
	flag.DurationVar(&c.stay, "for", 0, "how long the host stays connected (0: until Ctrl-C or the last word)")
	flag.BoolVar(&c.leave, "leave", false, "exit while the words go on (the arms stop a minute after the ring runs out)")
	flag.DurationVar(&c.away, "away", 0, "drop the links for this long while reading, then check the arms")
	flag.DurationVar(&c.power, "power", 0, "measure the battery current while reading, for this long (at least 66s)")
	demo.Usage("rsvp [flags] FILE.txt")
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	c.path = flag.Arg(0)
	if err := run(f, c); err != nil {
		fmt.Fprintln(os.Stderr, "rsvp:", err)
		os.Exit(1)
	}
}

// reader is a text loaded on the arms.
type reader struct {
	g     geometry
	l     layout
	words []word
	font  *demo.Font
	arms  []scouterkit.Arm
	pos   map[scouterkit.Arm][2]int
	avail map[scouterkit.Arm]int
}

func load(cfg scouterkit.Config, arms []scouterkit.Arm, text string, c config) (*reader, error) {
	blob, scale := scouterkit.DepartureMono(), 3
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
	seen := map[rune]bool{}
	var chars []rune
	for _, r := range text {
		if !seen[r] && r > ' ' {
			seen[r] = true
			chars = append(chars, r)
		}
	}
	font, err := scouterkit.LoadFont(blob, string(chars)+"-", scale)
	if err != nil {
		return nil, err
	}
	g := newGeometry(font, c.w)
	g.marks = c.orp
	g.maxWait = 60000000
	if c.giveUp > 0 {
		g.maxWait = uint32(c.giveUp.Microseconds())
	}
	if c.orp && c.side > 0 {
		g.side = uint8(min(15, c.side))
	}
	ws := prepare(font, text, c.wpm, g.focus, c.orp, textBytes)
	if len(ws) == 0 {
		return nil, errors.New("no words")
	}
	g.total = len(ws)
	budget := scouterkit.FreeMemory() - codeBytes - 4*stateWords - 2*int(g.bandBytes()) - 64
	g.ring = min(len(ws), budget/recordBytes)
	if c.ring > 0 {
		g.ring = min(g.ring, c.ring)
	}
	if g.ring < min(len(ws), 64) {
		return nil, fmt.Errorf("no room for the ring (%d bytes free)", budget)
	}
	base, err := scouterkit.Alloc(int(newLayout(0, g).end))
	if err != nil {
		return nil, err
	}
	rd := &reader{g: g, l: newLayout(base, g), words: ws, arms: arms, pos: map[scouterkit.Arm][2]int{},
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
	var total time.Duration
	for _, w := range ws {
		total += time.Duration(w.micro) * time.Microsecond
	}
	fmt.Printf("%d words, %d in the ring, %d wpm: %s for the whole text\n", len(ws), g.ring, c.wpm, total.Round(time.Second))
	return rd, nil
}

// refill puts the words from next on in the arm's ring.
func (rd *reader) refill(arm scouterkit.Arm, next int) {
	to := min(rd.g.total, next+rd.g.ring)
	if to > rd.avail[arm] {
		fill(arm, rd.l, rd.g, rd.words, rd.avail[arm], to)
		rd.avail[arm] = to
	}
}

func (rd *reader) check(arm scouterkit.Arm, i int) error {
	m := demo.NewPanel()
	m.Paste(bandImage(rd.font, rd.g, rd.words, i), rd.pos[arm][0], rd.pos[arm][1])
	y0 := max(0, rd.pos[arm][1])
	return demo.CheckPanel(arm, m, y0, min(scouterkit.PanelHeight, rd.pos[arm][1]+rd.g.h)-y0)
}

func (rd *reader) next(arm scouterkit.Arm) (int, error) {
	b, err := arm.ReadArena(rd.l.state+4*wNext, 4)
	if err != nil {
		return 0, err
	}
	return int(binary.LittleEndian.Uint32(b)), nil
}

// steps starts the program in step mode and shows n words, checking both
// panels with the empty band and after every word.
func (rd *reader) steps(n int) error {
	for _, arm := range rd.arms {
		if err := arm.Start(rd.l.base); err != nil {
			return err
		}
		if e, err := demo.WaitNotify(arm, 5*time.Second); err != nil || e.B != started<<24 {
			return fmt.Errorf("%s arm: START: %v (%x)", arm, err, e.B)
		}
		if err := rd.check(arm, -1); err != nil {
			return fmt.Errorf("the empty band: %w", err)
		}
	}
	for k := 0; k < n; k++ {
		for _, arm := range rd.arms {
			arm.Raise(scouterkit.TriggerUser3)
		}
		for _, arm := range rd.arms {
			e, err := demo.WaitNotify(arm, 5*time.Second)
			if err != nil {
				return err
			}
			if e.B != stepDone<<24 || int(e.A) != k+1 {
				return fmt.Errorf("%s arm: word %d: notified (%d, %x)", arm, k, e.A, e.B)
			}
			if err := rd.check(arm, k); err != nil {
				return fmt.Errorf("word %d %q: %w", k, rd.words[k].text, err)
			}
			rd.refill(arm, k+1)
		}
	}
	return nil
}

// play starts the timed words: the next one at host time h0.
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
	rd, err := load(cfg, arms, string(text), c)
	if err != nil {
		return err
	}
	if c.check > 0 {
		if err := rd.steps(c.check); err != nil {
			return err
		}
		fmt.Printf("step mode: the empty band and %d words as expected on both panels\n", c.check)
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
			next, err := rd.next(arm)
			if err != nil {
				return err
			}
			if err := rd.check(arm, next-1); err != nil && rd.check(arm, next-2) != nil {
				return err
			}
			fmt.Printf("%s arm: stopped before word %d, panel as expected\n", arm, next)
		}
	}
	if c.leave {
		fmt.Println("the words go on")
		return nil
	}
	demo.Stop(arms)
	return nil
}

// watch refills the rings and reports the pace, until -for, the last word
// or Ctrl-C; with -away and -power on the way.
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
		go func() { power <- demo.PrintCurrent("reading", rd.arms, c.power, quit) }()
	}
	awayAt := time.Now().Add(c.hold + 5*time.Second)
	finished := map[scouterkit.Arm]bool{}
	type mark struct {
		word int
		at   int64
	}
	marks := map[scouterkit.Arm]mark{}
	var skew demo.Skew
	defer func() { fmt.Println(skew.String()) }()
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
				return false, errors.New(arm.String() + " arm: rebooted, the text is gone")
			case scouterkit.EventNotify:
				if e.Trigger == 0xFF {
					continue
				}
				k := int(e.A)
				switch e.B >> 24 {
				case waiting:
					fmt.Printf("%s arm: waiting for word %d\n", arm, k)
				case ended:
					finished[arm] = true
				case progress:
					skew.Add(arm, e.A, e)
					at := demo.HostUS(arm, e.DeviceUS)
					if m, ok := marks[arm]; !ok {
						marks[arm] = mark{k, at}
					} else if arm == rd.arms[0] && (k-m.word)%320 == 0 {
						var want int64
						for i := m.word - 1; i < k-1; i++ {
							want += int64(rd.words[i].micro)
						}
						fmt.Printf("word %d of %d: %d words in %.3f s, the text's times add up to %.3f s\n", k,
							rd.g.total, k-m.word, float64(at-m.at)/1e6, float64(want)/1e6)
					}
				}
				rd.refill(arm, k)
			}
		}
		if len(finished) == len(rd.arms) {
			fmt.Println("the last word: the panels go off in 5 s")
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
		k, err := rd.next(arm)
		if err != nil {
			return err
		}
		fmt.Printf("%s arm: back, at word %d, still running\n", arm, k)
		rd.refill(arm, k)
	}
	return nil
}
