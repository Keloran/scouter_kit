// Command teleprompter shows a script on both eyes and scrolls it as the
// speaker reads it. Speech comes from the glasses' microphone (the right
// arm; on only while the command runs, its indicator lit) or from an audio
// file (-audio); whisper.cpp recognises it on the PC, conditioned on the
// script before the utterance, and the last words recognised are aligned to
// the script around the position. The line being read sits at the reading
// row (a mark at the left edge), what was read is dim. A program on each
// arm holds the script's lines and draws the steps the host plans: a scroll
// of the area with one BLIT and the lines that came in or changed, shown on
// both eyes at the same time. TouchBar: a tap on the right arm moves on a
// line, on the left back a line; a double tap pauses and resumes the
// following. Nothing is stored, and with the microphone the terminal shows
// only the position and counts.
//
//	go run -tags whisper ./examples/teleprompter -model ggml-small-q5_1.bin [flags] SCRIPT.txt
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"sync"
	"syscall"
	"time"

	"scouterkit"
	"scouterkit/examples/internal/demo"
	"scouterkit/examples/internal/speech"
)

type options struct {
	speech.Flags
	path, font             string
	scale, w, h, rows, dim int
	check                  int
	mark                   float64
	frame, stay            time.Duration
	prompt                 bool
	rec                    speech.Recognizer // tests: instead of -model
	ring                   int               // tests: at most this many lines in the ring
}

// result is what a run did.
type result struct {
	t0    int64     // NowUS when the input started
	moves []move    // the position's changes
	lat   []float64 // s from the newest audio of a move to both eyes reaching it
	late  []float64 // ms each DRAW began after its time, per arm
	steps int
	end   int // the position at the end
}

type move struct {
	at  int64 // NowUS
	pos int
}

func main() {
	var f demo.Flags
	f.Register()
	var o options
	o.Flags.Register(400 * time.Millisecond)
	flag.StringVar(&o.font, "font", "", "font blob made by font_converter.py (default: the built-in Departure Mono)")
	flag.IntVar(&o.scale, "scale", 0, "enlarge a pixel font this many times (0: 2 for the built-in font, else 1)")
	flag.IntVar(&o.w, "width", 540, "text area width (540: the columns both eyes see)")
	flag.IntVar(&o.h, "height", 240, "text area height")
	flag.Float64Var(&o.mark, "mark", 0.3, "the reading row, as a fraction of the height")
	flag.IntVar(&o.rows, "rows", 16, "scroll steps of at most this many rows (a move eases in: a third of the way each step)")
	flag.DurationVar(&o.frame, "frame", 125*time.Millisecond, "time between scroll steps")
	flag.IntVar(&o.dim, "dim", 5, "the level of what was read (1...15)")
	flag.BoolVar(&o.prompt, "prompt", true, "give the recogniser the script before each utterance")
	flag.IntVar(&o.check, "check", 0, "no speech: step through this many words, reading both panels back after each step")
	flag.DurationVar(&o.stay, "for", 0, "stop after this long (0: at Ctrl-C, or at the end of -audio)")
	demo.Usage("teleprompter -model FILE [-vad FILE] [-lang en] [-threads 8] [-step 400ms] [-prompt=false] [-denoise=false] [-rate 32|16] [-audio FILE [-pace=false] [-codec 32|16]] [-font FILE.skf] [-scale N] [-width 540] [-height 240] [-mark 0.3] [-rows N] [-frame 120ms] [-dim 5] [-check N] [-for D] SCRIPT.txt")
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	o.path = flag.Arg(0)
	if _, err := run(f, o); err != nil {
		fmt.Fprintln(os.Stderr, "teleprompter:", err)
		os.Exit(1)
	}
}

// prompter is the script on the arms.
type prompter struct {
	g     geometry
	l     layout
	sc    script
	font  *scouterkit.Font
	exp   *demo.Font
	arms  []scouterkit.Arm
	pos   map[scouterkit.Arm][2]int
	slot  []int // the line each ring slot holds (-1: none)
	rows  int
	frame time.Duration
	p     planner
	want  view // what they are to show
	audio int64
	seq   uint32
	fly   *flight
	last  time.Time // the last step's time
	steps int
	late  []float64
}

// flight is the step under way.
type flight struct {
	seq   uint32
	v     view
	due   map[scouterkit.Arm]uint32
	left  map[scouterkit.Arm]bool
	sent  time.Time
	audio int64
}

func newPrompter(cfg scouterkit.Config, arms []scouterkit.Arm, text string, o options) (*prompter, error) {
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
	font, err := scouterkit.LoadFont(blob, charset(text), scale)
	if err != nil {
		return nil, err
	}
	g := geometry{w: o.w &^ 1, h: o.h, lineH: max(font.LineSpacing, font.Ascent+font.Descent), ascent: font.Ascent,
		dim: o.dim}
	vw, vh := scouterkit.Size()
	switch {
	case g.w < 100 || g.w > vw-2:
		return nil, fmt.Errorf("-width %d: 100...%d", o.w, vw-2)
	case g.h < 2*g.lineH || g.h > min(vh, (maxLines-2)*g.lineH):
		return nil, fmt.Errorf("-height %d: %d...%d", o.h, 2*g.lineH, min(vh, (maxLines-2)*g.lineH))
	case o.mark < 0 || o.mark > 0.8:
		return nil, fmt.Errorf("-mark %v: 0...0.8", o.mark)
	case o.dim < 1 || o.dim > 15:
		return nil, fmt.Errorf("-dim %d: 1...15", o.dim)
	case o.rows < 1 || o.rows > g.lineH:
		return nil, fmt.Errorf("-rows %d: 1...%d", o.rows, g.lineH)
	}
	g.mark = int(o.mark * float64(g.h))
	sc := layoutScript(text, font, g.w-2*margin, recordBytes-4)
	if len(sc.words) == 0 {
		return nil, errors.New("no words in the script")
	}
	g.total = len(sc.lines)
	budget := scouterkit.FreeMemory() - codeBytes - 4*stateWords - g.bw()/2*g.rows() - 64
	g.ring = min(g.total, budget/recordBytes)
	if o.ring > 0 {
		g.ring = min(g.ring, o.ring)
	}
	if g.ring < min(g.total, 3*g.h/g.lineH+8) {
		return nil, fmt.Errorf("no room for the ring (%d bytes free)", budget)
	}
	base, err := scouterkit.Alloc(int(newLayout(0, g).end))
	if err != nil {
		return nil, fmt.Errorf("arena: %w", err)
	}
	pr := &prompter{g: g, l: newLayout(base, g), sc: sc, font: font, arms: arms, pos: map[scouterkit.Arm][2]int{},
		slot: make([]int, g.ring), rows: o.rows, frame: o.frame, p: planner{g: g}}
	for i := range pr.slot {
		pr.slot[i] = -1
	}
	if pr.exp, err = demo.FontOf(arms[0], font); err != nil {
		return nil, err
	}
	pr.want = sc.view(pr.exp, g, 0)
	pr.refill(pr.want)
	for _, arm := range arms {
		x, y := demo.PanelAt(cfg, arm, (vw-g.w)/2, (vh-g.h)/2)
		pr.pos[arm] = [2]int{x, y}
		p := program(pr.l, g, font.Address(), x, y)
		if p.Here() > pr.l.state {
			return nil, fmt.Errorf("program of %d bytes overlaps its state", p.Here()-base)
		}
		if err := arm.Load(p); err != nil {
			return nil, err
		}
	}
	for _, arm := range arms {
		n := uint32(min(g.ring, g.total) * recordBytes)
		got, want, err := arm.ArenaCRC(pr.l.ring, n)
		if err != nil {
			return nil, fmt.Errorf("%s arm: ring read-back: %w", arm, err)
		}
		if got != want {
			return nil, fmt.Errorf("%s arm: the ring read back %08x, sent %08x", arm, got, want)
		}
	}
	for _, arm := range arms {
		if err := arm.Start(pr.l.base); err != nil {
			return nil, err
		}
	}
	return pr, nil
}

func (pr *prompter) splitBytes(v view) uint32 {
	if v.cur >= len(pr.sc.lines) {
		return 0
	}
	return uint32(len(demo.Varints(pr.sc.lines[v.cur].glyphs[:v.split])))
}

// refill puts the lines around view v's current line in the ring: a quarter
// of it before, the rest after.
func (pr *prompter) refill(v view) {
	lo := max(0, min(v.cur-pr.g.ring/4, pr.g.total-pr.g.ring))
	for k := lo; k < min(pr.g.total, lo+pr.g.ring); {
		if pr.slot[k%pr.g.ring] == k {
			k++
			continue
		}
		var b []byte
		first := k
		for ; k < min(pr.g.total, lo+pr.g.ring) && pr.slot[k%pr.g.ring] != k && (k == first || k%pr.g.ring != 0); k++ {
			b = append(b, record(pr.sc.lines[k].glyphs)...)
			pr.slot[k%pr.g.ring] = k
		}
		for _, arm := range pr.arms {
			arm.WriteArena(pr.l.ring+uint32(first%pr.g.ring*recordBytes), b)
		}
	}
}

// moveTo makes script word pos what the arms are to show; audio is the
// newest audio of the move (NowUS; 0: none).
func (pr *prompter) moveTo(pos int, audio int64) {
	if v := pr.sc.view(pr.exp, pr.g, pos); v != pr.want {
		pr.want, pr.audio = v, audio
	}
}

// work is the arm time a step is expected to take, from the costs measured on
// the glasses (g1v0 0.5.40): GLYPHS about 12 ms a line, a BLIT of the bitmap 8 ms.
func (pr *prompter) work(s step) time.Duration {
	ms := 3 + 12*len(s.lines)
	if s.dy != 0 || s.full {
		ms += 10
	}
	if ms > 25 {
		ms = ms * 10 / 3 // the run-time cap: 30 ms of every 100 ms
	}
	return time.Duration(ms) * time.Millisecond
}

// next sends the step toward the wanted view, unless one is under way or
// nothing changes; it reports whether it sent one.
func (pr *prompter) next() bool {
	if pr.fly != nil || pr.p.valid && pr.p.drawn == pr.want {
		return false
	}
	b := pr.want
	if pr.p.valid {
		b.pos = pr.p.drawn.pos + moveBy(pr.want.pos-pr.p.drawn.pos, pr.rows)
	}
	s := pr.p.plan(b)
	if !s.full && s.dy == 0 && len(s.lines) == 0 {
		return false
	}
	pr.refill(b)
	pr.seq++
	at := time.Now().Add(demo.Lead(pr.arms) + pr.work(s))
	if t := pr.last.Add(pr.frame); s.dy != 0 && at.Before(t) {
		at = t
	}
	pr.last = at
	f := &flight{seq: pr.seq, v: b, due: map[scouterkit.Arm]uint32{}, left: map[scouterkit.Arm]bool{}, sent: time.Now()}
	if b == pr.want {
		f.audio, pr.audio = pr.audio, 0
	}
	words := []uint32{pr.seq, 0, uint32(int32(b.pos)), uint32(b.cur), pr.splitBytes(b), uint32(b.splitX), uint32(int32(s.dy)),
		0, uint32(s.d0), uint32(s.d1), uint32(len(s.lines))}
	if s.full {
		words[wFull] = 1
	}
	for _, k := range s.lines {
		words = append(words, uint32(k))
	}
	for _, arm := range pr.arms {
		f.due[arm] = arm.DeviceTime(time.Until(at))
		words[wDue] = f.due[arm]
		arm.WriteArena(pr.l.state, demo.Words(words...))
		arm.Raise(scouterkit.TriggerUser0)
		f.left[arm] = true
	}
	pr.fly, pr.steps = f, pr.steps+1
	return true
}

// shown takes an arm's NOTIFY; once both arms have shown the step it returns
// it (nil before), and the bitmaps hold its view.
func (pr *prompter) shown(arm scouterkit.Arm, e scouterkit.Event) (*flight, uint32) {
	f := pr.fly
	if f == nil || e.A != f.seq || !f.left[arm] {
		return nil, 0
	}
	at := demo.Before24(e, e.B&0xFFFFFF)
	pr.late = append(pr.late, float64(int32(at-f.due[arm]))/1000)
	delete(f.left, arm)
	if len(f.left) > 0 {
		return nil, at
	}
	pr.fly = nil
	return f, at
}

// stuck gives up a step whose NOTIFYs did not come: the next one redraws
// everything.
func (pr *prompter) stuck() bool {
	if pr.fly == nil || time.Since(pr.fly.sent) < 3*time.Second {
		return false
	}
	pr.fly, pr.p.valid = nil, false
	return true
}

// expected is the arm's panel with the area showing the view.
func (pr *prompter) expected(arm scouterkit.Arm, v view) *demo.Image {
	m := demo.NewPanel()
	m.Paste(page(pr.exp, pr.g, pr.sc.lines, v), pr.pos[arm][0], pr.pos[arm][1])
	return m
}

func (pr *prompter) checkPanels(v view) error {
	for _, arm := range pr.arms {
		y0 := max(0, pr.pos[arm][1])
		rows := min(scouterkit.PanelHeight, pr.pos[arm][1]+pr.g.h) - y0
		if err := demo.CheckPanel(arm, pr.expected(arm, v), y0, rows); err != nil {
			return err
		}
	}
	return nil
}

// steps goes word by word through the first n words, reading both panels
// back after every step.
func (pr *prompter) checkSteps(n int) (int, error) {
	checked := 0
	for w := 0; w <= min(n, len(pr.sc.words)); w++ {
		pr.moveTo(w, 0)
		for pr.next() {
			for _, arm := range pr.arms {
				e, err := demo.WaitNotify(arm, 5*time.Second)
				if err != nil {
					return checked, err
				}
				pr.shown(arm, e)
			}
			if pr.fly != nil {
				return checked, fmt.Errorf("word %d: step %d not shown on both arms", w, pr.seq)
			}
			if err := pr.checkPanels(pr.p.drawn); err != nil {
				return checked, fmt.Errorf("word %d, step %d: %w", w, pr.seq, err)
			}
			checked++
		}
	}
	return checked, nil
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
	text, err := os.ReadFile(o.path)
	if err != nil {
		return res, err
	}
	var recog speech.Recognition
	if o.check == 0 {
		var closeRec func()
		if recog, closeRec, err = o.Recognition(o.rec); err != nil {
			return res, err
		}
		defer closeRec()
	}
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
	pr, err := newPrompter(cfg, arms, string(text), o)
	if err != nil {
		return res, err
	}
	defer demo.Stop(arms)
	fmt.Printf("%d words in %d lines (all %d in the ring: %v)\n", len(pr.sc.words), len(pr.sc.lines), pr.g.ring,
		pr.g.ring == pr.g.total)
	if o.check > 0 {
		n, err := pr.checkSteps(o.check)
		if err != nil {
			return res, err
		}
		fmt.Printf("%d steps over the first %d words, each as expected on both panels; %s\n", n, o.check, lateness(pr.late))
		demo.Status(arms)
		res.late, res.steps = pr.late, pr.steps
		return res, nil
	}
	for _, arm := range arms {
		if _, err := arm.ReportInputs(1 << scouterkit.TriggerTouch); err != nil {
			return res, fmt.Errorf("%s arm: INPUT: %w", arm, err)
		}
	}
	pr.next()
	frames, stopInput, err := o.Input()
	if err != nil {
		return res, err
	}
	res.t0 = scouterkit.NowUS()
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

	tr := newTracker(pr.sc.words)
	var mu sync.Mutex
	prompts := map[int]string{}
	if as, ok := recog.(speech.AgreeStream); ok {
		as.Seg.MaxSpeech = 500 // 5 s: the tail is what counts, and shorter decodes come sooner
	}
	if as, ok := recog.(speech.AgreeStream); ok && o.prompt {
		as.Prompt = func(utt int) string {
			mu.Lock()
			defer mu.Unlock()
			if p, ok := prompts[utt]; ok {
				return p
			}
			prompts[utt] = pr.sc.before(tr.pos, 40)
			return prompts[utt]
		}
	}
	texts := make(chan speech.Text, 16)
	errc := make(chan error, 1)
	go func() { errc <- recog.Run(frames, texts) }()

	var skew demo.Skew
	paused := false
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var end <-chan time.Time
	if o.stay > 0 {
		end = time.After(o.stay)
	}
	done := ctx.Done()
	links := reconnects(arms)
	moveTo := func(pos int, audio int64) {
		mu.Lock()
		tr.pos = pos
		mu.Unlock()
		res.moves = append(res.moves, move{scouterkit.NowUS(), pos})
		pr.moveTo(pos, audio)
		pr.next()
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
			mu.Lock()
			moved := !paused && tr.hear(speech.Words(t.Stable+" "+t.Tail))
			pos := tr.pos
			mu.Unlock()
			if moved {
				moveTo(pos, t.Arrived)
			}
		case ae := <-events:
			switch e := ae.e; e.Type {
			case scouterkit.EventHalt, scouterkit.EventError, scouterkit.EventReset:
				return res, fmt.Errorf("%s arm: event %d (code %d, op 0x%02x, detail 0x%x)", ae.arm, e.Type, e.Code, e.Op, e.Detail)
			case scouterkit.EventInput:
				if e.Trigger != int(scouterkit.TriggerTouch) {
					continue
				}
				switch {
				case e.A == 2:
					paused = !paused
					fmt.Println("following paused:", paused)
				case e.A == 1:
					k, _ := pr.sc.at(tr.pos)
					k = pr.lineStep(k, ae.arm == scouterkit.Right)
					moveTo(pr.sc.lines[k].first, 0)
				}
			case scouterkit.EventNotify:
				seq := e.A
				fl, at := pr.shown(ae.arm, e)
				if at != 0 {
					skew.At(ae.arm, seq, at)
				}
				if fl != nil {
					if fl.audio != 0 {
						res.lat = append(res.lat, float64(demo.HostUS(ae.arm, at)-fl.audio)/1e6)
					}
					pr.next()
				}
			}
		case <-tick.C:
			if pr.stuck() {
				fmt.Println("a step was not shown on both arms within 3 s: redrawing")
				pr.next()
			}
			if n := reconnects(arms); n != links {
				links, pr.p.valid = n, false // the library redraws a panel from its own copy after a reconnect
				pr.next()
			}
			if o.Mic() {
				printStatus(recog, tr.pos, len(pr.sc.words), paused)
			}
		}
	}
	if err := <-errc; err != nil {
		return res, err
	}
	res.end, res.late, res.steps = tr.pos, pr.late, pr.steps
	demo.Status(arms)
	fmt.Printf("at word %d of %d; %d moves, %d steps; %s\n", tr.pos, len(pr.sc.words), len(res.moves), pr.steps, recog.Summary())
	if lat := append([]float64(nil), res.lat...); len(lat) > 0 {
		sort.Float64s(lat)
		fmt.Printf("from the newest audio of a move to both eyes showing it: median %.2f s, max %.2f s\n",
			lat[len(lat)/2], lat[len(lat)-1])
	}
	fmt.Printf("%s; %s\n", lateness(pr.late), skew.String())
	if o.Mic() {
		if s := scouterkit.Right.MicState(); s.Stopped != scouterkit.MicStoppedByHost {
			return res, fmt.Errorf("the microphone stopped: %s", s.Stopped)
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
		return "no step shown"
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

// lineStep is the next (or previous) line with words after line k.
func (pr *prompter) lineStep(k int, forward bool) int {
	d := 1
	if !forward {
		d = -1
	}
	for i := k + d; i >= 0 && i < len(pr.sc.lines); i += d {
		if len(pr.sc.lines[i].words) > 0 {
			return i
		}
	}
	return min(k, len(pr.sc.lines)-1)
}

func printStatus(r speech.Recognition, pos, words int, paused bool) {
	m := scouterkit.Right.MicState()
	p := ""
	if paused {
		p = " (paused)"
	}
	q := "speech"
	if r.Quiet() > time.Second {
		q = fmt.Sprintf("quiet %.0f s", r.Quiet().Seconds())
	}
	fmt.Printf("word %d of %d%s; mic %v: %d frames, %d gaps; %s\n", pos, words, p, m.On, m.Frames, m.Gaps, q)
}
