// Command captions shows live captions on both eyes. Speech comes from the
// glasses' microphone (the right arm; on only while the command runs, its
// indicator lit) or from an audio file (-audio). A streaming recogniser
// (-live, sherpa-onnx) gives the words as they are spoken; without it
// whisper.cpp decodes each utterance again and again and a word is shown as
// stable once two decodes agree on it. Stable words never change on the
// band; the words that still may (the tail) are dimmer. A program on each
// arm draws the band of the last -lines lines, only what changed: a scroll
// by whole lines with one BLIT and the lines that are new, shown on both eyes
// at the same time. After -clear with no speech and no new word the band
// goes blank, and 30 s later the panel goes off. Nothing is stored: with the
// microphone the terminal shows only counts and levels, never the text.
//
//	go run -tags sherpa ./examples/captions -live DIR [flags]
//	go run -tags whisper ./examples/captions -model ggml-small-q5_1.bin [flags]
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"math/bits"
	"os"
	"os/signal"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"scouterkit"
	"scouterkit/examples/internal/demo"
	"scouterkit/examples/internal/speech"
)

type options struct {
	speech.Flags
	font                     string
	scale, lines, w, y, tail int
	clear, stay              time.Duration
	check                    bool
	ref                      string
	rec                      speech.Recognizer // tests: instead of -model
}

// result is what a run showed.
type result struct {
	final   string    // the stable texts
	updates uint32    // caption updates sent to the arms
	blanks  []float64 // s from the start when the host blanked the band
	lat     []float64 // s from the newest audio of an update to both eyes showing it
	late    []float64 // ms each DRAW began after its time, per arm
	wer     float64
	mic     scouterkit.MicState // at the end
}

func main() {
	var f demo.Flags
	f.Register()
	var o options
	o.Flags.Register(500 * time.Millisecond)
	flag.StringVar(&o.font, "font", "", "font blob made by font_converter.py (default: the built-in Departure Mono)")
	flag.IntVar(&o.scale, "scale", 0, "enlarge a pixel font this many times (0: 2 for the built-in font, else 1)")
	flag.IntVar(&o.lines, "lines", 3, "caption lines")
	flag.IntVar(&o.w, "width", 540, "band width (540: the columns both eyes see)")
	flag.IntVar(&o.y, "y", -1, "the band's top row on the display (-1: 60 px above the bottom)")
	flag.IntVar(&o.tail, "tail", 8, "the level of words that may still change (1...15)")
	flag.DurationVar(&o.clear, "clear", 8*time.Second, "the band goes blank after this long with no speech and no new word")
	flag.DurationVar(&o.stay, "for", 0, "stop after this long (0: at Ctrl-C, or at the end of -audio)")
	flag.BoolVar(&o.check, "check", false, "read both panels back after every update")
	flag.StringVar(&o.ref, "ref", "", "with -audio: a text file of what is said, for the word error rate")
	demo.Usage("captions {-live DIR | -model FILE [-vad FILE] [-lang en] [-step 500ms]} [-threads 8] [-denoise=false] [-rate 32|16] [-audio FILE [-pace=false] [-codec 32|16] [-ref FILE]] [-lines 3] [-tail 8] [-font FILE.skf] [-scale N] [-width 540] [-y N] [-clear 8s] [-check] [-for D]")
	flag.Parse()
	if flag.NArg() != 0 {
		flag.Usage()
		os.Exit(2)
	}
	if _, err := run(f, o); err != nil {
		fmt.Fprintln(os.Stderr, "captions:", err)
		os.Exit(1)
	}
}

// display is the caption band on the arms.
type display struct {
	g     geometry
	l     layout
	font  *scouterkit.Font
	exp   *demo.Font
	arms  []scouterkit.Arm
	pos   map[scouterkit.Arm][2]int
	sent  map[scouterkit.Arm][][]byte // the records as last written
	drawn []line                      // the band's lines in the arms' bitmaps, once valid
	valid bool
	shown bool // the band is on, not blank
	want  []line
	blank bool // the band is to go blank
	audio int64
	seq   uint32
	fly   *flight
	late  []float64
}

// flight is the update under way.
type flight struct {
	seq   uint32
	lines []line
	blank bool
	due   map[scouterkit.Arm]uint32
	left  map[scouterkit.Arm]bool
	sent  time.Time
	audio int64
}

func newDisplay(cfg scouterkit.Config, arms []scouterkit.Arm, o options) (*display, error) {
	blob, scale := scouterkit.DepartureMono(), 2
	if o.font != "" {
		b, err := os.ReadFile(o.font)
		if err != nil {
			return nil, err
		}
		blob, scale = b, 1
	}
	if o.scale > 0 {
		scale = o.scale
	}
	if o.lines < 1 || o.lines > 8 {
		return nil, fmt.Errorf("-lines %d: 1...8", o.lines)
	}
	if o.tail < 1 || o.tail > 15 {
		return nil, fmt.Errorf("-tail %d: 1...15", o.tail)
	}
	font, err := scouterkit.LoadFont(blob, "", scale)
	if err != nil {
		return nil, err
	}
	g := geometry{w: o.w &^ 1, lines: o.lines, lineH: max(font.LineSpacing, font.Ascent+font.Descent), ascent: font.Ascent,
		tail: o.tail, clear: uint32(max(o.clear, time.Minute).Microseconds())}
	vw, vh := scouterkit.Size()
	if g.w < 100 || g.w > vw-2 {
		return nil, fmt.Errorf("-width %d: 100...%d", o.w, vw-2)
	}
	y := o.y
	if y < 0 {
		y = vh - g.h() - 60
	}
	if y < 20 || y+g.h() > vh {
		return nil, fmt.Errorf("a band of %d rows at row %d does not fit below the microphone's indicator (rows 4-19) on the display (%d rows)", g.h(), y, vh)
	}
	base, err := scouterkit.Alloc(int(newLayout(0, g).end))
	if err != nil {
		return nil, fmt.Errorf("arena: %w", err)
	}
	d := &display{g: g, l: newLayout(base, g), font: font, arms: arms, pos: map[scouterkit.Arm][2]int{},
		sent: map[scouterkit.Arm][][]byte{}, drawn: make([]line, g.lines), want: make([]line, g.lines)}
	if d.exp, err = demo.FontOf(arms[0], font); err != nil {
		return nil, err
	}
	for _, arm := range arms {
		px, py := demo.PanelAt(cfg, arm, (vw-g.w)/2, y)
		d.pos[arm] = [2]int{px, py}
		p := program(d.l, g, font.Address(), px, py)
		if p.Here() > d.l.state {
			return nil, fmt.Errorf("program of %d bytes overlaps its state", p.Here()-base)
		}
		arm.WriteArena(d.l.state, make([]byte, 4*stateWords))
		d.sent[arm] = make([][]byte, g.lines)
		if err := arm.Load(p); err != nil {
			return nil, err
		}
		if err := arm.Start(d.l.base); err != nil {
			return nil, err
		}
	}
	return d, nil
}

// set makes lines, bottom-aligned, what the band is to show; audio is the
// newest audio in them (NowUS).
func (d *display) set(lines []line, audio int64) {
	want := make([]line, d.g.lines)
	copy(want[d.g.lines-len(lines):], lines)
	if d.blank || !slices.Equal(want, d.want) {
		d.want, d.audio, d.blank = want, audio, false
	}
}

// off makes the band go blank.
func (d *display) off() { d.blank, d.want = true, make([]line, d.g.lines) }

// work is the arm time an update is expected to take (GLYPHS about 6 ms a
// line on g1v0 0.5.42, 9 on 0.5.40; a BLIT of the band 3 ms).
func work(u update) time.Duration {
	ms := 3 + 12*bits.OnesCount(uint(u.mask))
	if u.scroll > 0 {
		ms += 3
	}
	if ms > 25 {
		ms = ms * 10 / 3 // the run-time cap: 30 ms of every 100 ms
	}
	return time.Duration(ms) * time.Millisecond
}

// next sends the update toward the wanted lines (or the blank) unless one is
// under way or nothing changes; it reports whether it sent one.
func (d *display) next() bool {
	if d.fly != nil {
		return false
	}
	if d.blank {
		if !d.shown {
			return false
		}
		return d.send(update{}, true)
	}
	if d.valid && d.shown && slices.Equal(d.drawn, d.want) {
		return false
	}
	return d.send(d.g.plan(d.drawn, d.want, d.valid), false)
}

func (d *display) send(u update, blank bool) bool {
	d.seq++
	at := time.Now().Add(demo.Lead(d.arms) + work(u))
	f := &flight{seq: d.seq, lines: d.want, blank: blank, due: map[scouterkit.Arm]uint32{},
		left: map[scouterkit.Arm]bool{}, sent: time.Now(), audio: d.audio}
	d.audio = 0
	for _, arm := range d.arms {
		for k := 0; k < d.g.lines && !blank; k++ {
			if u.mask&(1<<k) == 0 {
				continue
			}
			gl := glyphLine(d.font, d.exp, d.want[k])
			r := record(gl.glyphs, gl.split, gl.splitX)
			if string(r) != string(d.sent[arm][k]) {
				arm.WriteArena(d.l.records+uint32(k*recordBytes), r)
				d.sent[arm][k] = r
			}
		}
		f.due[arm] = arm.DeviceTime(time.Until(at))
		off := uint32(0)
		if blank {
			off = 1
		}
		arm.WriteArena(d.l.state, demo.Words(d.seq, f.due[arm], uint32(u.scroll), uint32(u.mask), uint32(u.d0), uint32(u.d1), off))
		arm.Raise(scouterkit.TriggerUser0)
		f.left[arm] = true
	}
	d.fly = f
	return true
}

// shown takes an arm's NOTIFY; once both arms have shown the update (or
// gone blank) it returns it (nil before).
func (d *display) shownBy(arm scouterkit.Arm, e scouterkit.Event) (*flight, uint32) {
	f := d.fly
	if f == nil || e.A != f.seq || !f.left[arm] || (e.B>>24 == blanked) != f.blank {
		return nil, 0
	}
	at := uint32(0)
	if !f.blank {
		at = demo.Before24(e, e.B&0xFFFFFF)
		d.late = append(d.late, float64(int32(at-f.due[arm]))/1000)
	}
	delete(f.left, arm)
	if len(f.left) > 0 {
		return nil, at
	}
	d.fly = nil
	if f.blank {
		d.shown = false
	} else {
		d.drawn, d.valid, d.shown = f.lines, true, true
	}
	return f, at
}

// stuck gives up an update whose NOTIFYs did not come: the next one draws
// the whole band.
func (d *display) stuck() bool {
	if d.fly == nil || time.Since(d.fly.sent) < 3*time.Second {
		return false
	}
	d.fly, d.valid = nil, false
	return true
}

// check reads both panels back against the band's lines.
func (d *display) check() error {
	lines := make([]drawnLine, d.g.lines)
	for k, l := range d.drawn {
		lines[k] = glyphLine(d.font, d.exp, l)
	}
	for _, arm := range d.arms {
		want := demo.NewPanel()
		if d.shown {
			want.Paste(band(d.exp, d.g, lines), d.pos[arm][0], d.pos[arm][1])
		}
		if err := demo.CheckPanel(arm, want, d.pos[arm][1], d.g.h()); err != nil && d.shown {
			return fmt.Errorf("update %d: %w", d.seq, err)
		}
	}
	return nil
}

// sync sends updates and waits for each on both arms, reading them back
// (-check).
func (d *display) sync() error {
	for d.next() {
		for _, arm := range d.arms {
			for d.fly != nil && d.fly.left[arm] {
				e, err := demo.WaitNotify(arm, 5*time.Second)
				if err != nil {
					return err
				}
				d.shownBy(arm, e)
			}
		}
		if err := d.check(); err != nil {
			return err
		}
	}
	return nil
}

type armEvent struct {
	arm scouterkit.Arm
	e   scouterkit.Event
}

func run(f demo.Flags, o options) (result, error) {
	var res result
	if err := o.Check(); err != nil {
		return res, err
	}
	if o.ref != "" && o.Mic() {
		return res, errors.New("-ref needs -audio")
	}
	recog, closeRec, err := o.Recognition(o.rec)
	if err != nil {
		return res, err
	}
	defer closeRec()
	signal.Ignore(syscall.SIGPIPE)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	cfg, arms, err := demo.Connect(f)
	if err != nil {
		return res, err
	}
	defer scouterkit.Deinit()
	if len(arms) == 0 {
		return res, errors.New("no arm")
	}
	d, err := newDisplay(cfg, arms, o)
	if err != nil {
		return res, err
	}
	defer demo.Stop(arms)
	frames, stopInput, err := o.Input()
	if err != nil {
		return res, err
	}
	inputOn := true
	stopIn := func() {
		if inputOn {
			inputOn = false
			stopInput()
		}
	}
	defer stopIn()
	if o.Mic() {
		fmt.Println("microphone on (right arm, its indicator lit); Ctrl-C ends")
	}
	quit := make(chan struct{})
	defer close(quit)
	events := make(chan armEvent, 16)
	for _, arm := range arms {
		if o.check {
			break // the check reads the NOTIFYs itself
		}
		go func(arm scouterkit.Arm) {
			for {
				select {
				case <-quit:
					return
				default:
				}
				if e, ok := arm.NextEvent(200 * time.Millisecond); ok {
					select {
					case events <- armEvent{arm, e}:
					case <-quit:
						return
					}
				}
			}
		}(arm)
	}
	texts := make(chan speech.Text, 16)
	errc := make(chan error, 1)
	go func() { errc <- recog.Run(frames, texts) }()

	var c captions
	var final strings.Builder
	var skew demo.Skew
	t0 := time.Now()
	changed := t0
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	var end <-chan time.Time
	if o.stay > 0 {
		end = time.After(o.stay)
	}
	done := ctx.Done()
	status := time.Now()
	links := reconnects(arms)
	update := func() error {
		if o.check {
			return d.sync()
		}
		d.next()
		return nil
	}
	for texts != nil {
		select {
		case <-done:
			done = nil
			stopIn()
		case <-end:
			stopIn()
		case t, ok := <-texts:
			if !ok {
				texts = nil
				continue
			}
			if t.Final && t.Stable != "" {
				final.WriteString(t.Stable + " ")
				if !o.Mic() {
					fmt.Printf("%6.1f s  %s\n", time.Since(t0).Seconds(), t.Stable)
				}
			}
			c.add(t)
			changed = time.Now()
			d.set(c.lines(d.font, d.g.w-2*margin, d.g.lines), t.Arrived)
			if err := update(); err != nil {
				return res, err
			}
		case ae := <-events:
			switch e := ae.e; e.Type {
			case scouterkit.EventHalt, scouterkit.EventError, scouterkit.EventReset:
				return res, fmt.Errorf("%s arm: event %d (code %d, op 0x%02x, detail 0x%x)", ae.arm, e.Type, e.Code, e.Op, e.Detail)
			case scouterkit.EventNotify:
				seq := e.A
				fl, at := d.shownBy(ae.arm, e)
				if at != 0 {
					skew.At(ae.arm, seq, at)
				}
				if fl != nil {
					if fl.audio != 0 && !fl.blank {
						res.lat = append(res.lat, float64(demo.HostUS(ae.arm, at)-fl.audio)/1e6)
					}
					d.next()
				}
			}
		case <-tick.C:
			if d.stuck() {
				fmt.Println("an update was not shown on both arms within 3 s: drawing the whole band")
				d.next()
			}
			if n := reconnects(arms); n != links {
				links, d.valid = n, false // the library redraws a panel from its own copy after a reconnect
				d.next()
			}
			if d.shown && !c.empty() && time.Since(changed) >= o.clear && recog.Quiet() >= o.clear {
				c.reset()
				d.off()
				res.blanks = append(res.blanks, time.Since(t0).Seconds())
				if err := update(); err != nil {
					return res, err
				}
			}
			if o.Mic() && time.Since(status) >= time.Second {
				status = time.Now()
				printStatus(recog, d.seq)
			}
		}
	}
	if err := <-errc; err != nil {
		return res, err
	}
	res.final, res.updates, res.late = final.String(), d.seq, d.late
	demo.Status(arms)
	fmt.Printf("%s; %d caption updates, %d blanks after quiet\n", recog.Summary(), d.seq, len(res.blanks))
	if lat := append([]float64(nil), res.lat...); len(lat) > 0 {
		sort.Float64s(lat)
		fmt.Printf("from the newest audio in a text to both eyes showing it: median %.2f s, 90 %% %.2f s, max %.2f s\n",
			lat[len(lat)/2], lat[len(lat)*9/10], lat[len(lat)-1])
	}
	fmt.Printf("%s; %s\n", lateness(d.late), skew.String())
	if o.ref != "" {
		b, err := os.ReadFile(o.ref)
		if err != nil {
			return res, err
		}
		e, n := speech.WER(speech.Words(string(b)), speech.Words(final.String()))
		res.wer = float64(e) / float64(max(1, n))
		fmt.Printf("word error rate %.1f %% (%d of %d words)\n", 100*res.wer, e, n)
	}
	if o.Mic() {
		res.mic = scouterkit.Right.MicState()
		if res.mic.Stopped != scouterkit.MicStoppedByHost {
			return res, fmt.Errorf("the microphone stopped: %s", res.mic.Stopped)
		}
		fmt.Println("microphone off")
	}
	return res, nil
}

// reconnects counts the arms' reconnections.
func reconnects(arms []scouterkit.Arm) uint32 {
	n := uint32(0)
	for _, arm := range arms {
		n += arm.State().Reconnects
	}
	return n
}

// lateness sums up how long after their time the DRAWs began.
func lateness(late []float64) string {
	if len(late) == 0 {
		return "no update shown"
	}
	s := append([]float64(nil), late...)
	sort.Float64s(s)
	n := 0
	for _, v := range s {
		if v > 5 {
			n++
		}
	}
	return fmt.Sprintf("DRAWs began %.1f ms after their time (median), %.1f ms at most; %d of %d more than 5 ms late",
		s[len(s)/2], s[len(s)-1], n, len(s))
}

func printStatus(r speech.Recognition, updates uint32) {
	m := scouterkit.Right.MicState()
	q := "speech"
	if r.Quiet() > time.Second {
		q = fmt.Sprintf("quiet %.0f s", r.Quiet().Seconds())
	}
	fmt.Printf("mic %v: %d frames, %d gaps, %d bad, %d dropped; %s; %d caption updates\n",
		m.On, m.Frames, m.Gaps, m.Bad, m.Dropped, q, updates)
}
