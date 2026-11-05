// Command term shows a terminal on the glasses. It runs a command (default:
// the shell) on a pseudo-terminal of the size the glasses show, passes it
// through to this terminal as before (keys in, screen out), and mirrors the
// screen on both eyes with its own VT100/xterm emulator.
//
// A program on each arm keeps the screen as a ring of rows: the host sends
// only the rows that changed, each as runs of glyph indices with their
// levels (colours, bold, faint, reverse video and underline become levels),
// and for a scroll only the rows that came in. The program renders them
// into its bitmap and draws them at a common time on both eyes.
//
//	term [flags] [COMMAND [ARGS...]]
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"scouterkit"
	"scouterkit/examples/internal/demo"
)

type config struct {
	cols, rows, scale int
	most              [2]int // with cols or rows 0: at most this many (this terminal's size)
	wide              bool   // the whole virtual display, not only the part both eyes see
	font              []byte
	leave             bool
}

func main() {
	var f demo.Flags
	f.Register()
	var c config
	flag.IntVar(&c.cols, "cols", 0, "columns (0: as many as fit the part both eyes see, or with -wide the whole display)")
	flag.BoolVar(&c.wide, "wide", false, "use the whole 740-px virtual display: each eye shows its 640 px of it, the outer columns only one eye sees")
	flag.IntVar(&c.rows, "rows", 0, "rows (0: as many as fit)")
	flag.IntVar(&c.scale, "scale", 1, "enlarge the font's pixels this many times")
	font := flag.String("font", "", "monospace font blob from font_converter.py (default: Departure Mono Nerd Font Mono)")
	check := flag.Bool("check", false, "show test screens instead of a command and read both panels back")
	flag.BoolVar(&c.leave, "leave", false, "leave the last screen on the glasses")
	demo.Usage("term [flags] [COMMAND [ARGS...]]")
	flag.Parse()
	if *font != "" {
		b, err := os.ReadFile(*font)
		if err != nil {
			fmt.Fprintln(os.Stderr, "term:", err)
			os.Exit(1)
		}
		c.font = b
	}
	if !*check && isTerminal(os.Stdin.Fd()) {
		if cols, rows, ok := termSize(os.Stdout.Fd()); ok {
			c.most = [2]int{cols, rows}
		}
	}
	if err := run(f, c, *check, flag.Args()); err != nil {
		fmt.Fprintln(os.Stderr, "term:", err)
		os.Exit(1)
	}
}

func run(f demo.Flags, c config, check bool, args []string) error {
	cfg, arms, err := demo.Connect(f)
	if err != nil {
		return err
	}
	defer scouterkit.Deinit()
	t, err := newTerm(cfg, arms, c)
	if err != nil {
		return err
	}
	defer t.close()
	if check {
		err = t.check()
	} else {
		if len(args) == 0 {
			args = []string{os.Getenv("SHELL")}
			if args[0] == "" {
				args[0] = "/bin/sh"
			}
		}
		fmt.Printf("%s in a %d x %d terminal on the glasses\n", args[0], t.g.cols, t.g.rows)
		if t.fit != [2]int{t.g.cols, t.g.rows} {
			fmt.Printf("(the size of this terminal: make it %d x %d or larger and start again for the whole area)\n",
				t.fit[0], t.fit[1])
		}
		err = t.run(args)
		t.summary()
	}
	if !c.leave {
		demo.Stop(arms)
	}
	return err
}

type armEvent struct {
	arm scouterkit.Arm
	e   scouterkit.Event
}

type term struct {
	arms    []scouterkit.Arm
	g       geometry
	fit     [2]int // the size that would fit, without this terminal's limit
	l       layout
	lib     *scouterkit.Font
	font    *demo.Font
	blob    []byte // the whole font; lib holds the part on the arms
	scale   int
	has     map[rune]bool // in the blob
	loaded  map[rune]bool // on the arms
	want    map[rune]bool // on the screen, in the blob, not on the arms yet
	never   map[rune]bool // did not fit
	pos     map[scouterkit.Arm][2]int
	glyphs  map[rune]uint32
	space   uint32
	unknown uint32
	scr     screen
	frame   uint32
	events  chan armEvent
	quit    chan struct{}
	shown   []time.Duration // per frame: from sending to the last arm's NOTIFY
	spread  []time.Duration // per frame: between the arms' NOTIFYs

	mu      sync.Mutex
	vt      *vt
	changed chan struct{}
}

func newTerm(cfg scouterkit.Config, arms []scouterkit.Arm, c config) (*term, error) {
	if len(arms) == 0 {
		return nil, errors.New("no arm")
	}
	blob := c.font
	if blob == nil {
		blob = nerdFont
	}
	has := codepoints(blob)
	loaded := baseSet(has)
	lib, err := scouterkit.LoadFont(blob, chars(loaded), max(c.scale, 1))
	if err != nil {
		return nil, err
	}
	df, err := demo.FontOf(arms[0], lib)
	if err != nil {
		return nil, err
	}
	vw, vh := scouterkit.Size()
	width := vw - 2*max(cfg.DX, -cfg.DX)
	if c.wide {
		width = vw
	}
	g := geometry{cw: lib.Measure("M").Width, ch: lib.LineSpacing, ascent: lib.Ascent}
	g.cols, g.rows = c.cols, c.rows
	fit := [2]int{c.cols, c.rows}
	if g.cols <= 0 {
		fit[0] = width / g.cw
		g.cols = fit[0]
		if c.most[0] > 0 {
			g.cols = min(g.cols, c.most[0])
		}
	}
	if g.rows <= 0 {
		fit[1] = vh / g.ch
		g.rows = fit[1]
		if c.most[1] > 0 {
			g.rows = min(g.rows, c.most[1])
		}
	}
	if g.cols > 255 || g.rows > 255 || g.cols*g.cw > vw || g.rows*g.ch > vh {
		return nil, fmt.Errorf("%d x %d cells of %d x %d px do not fit the %d x %d display", g.cols, g.rows, g.cw, g.ch, vw, vh)
	}
	g.x = (vw - g.w()) / 2
	g.y = (vh - g.rows*g.ch) / 2
	t := &term{arms: arms, g: g, fit: fit, lib: lib, font: df, pos: map[scouterkit.Arm][2]int{}, glyphs: map[rune]uint32{},
		blob: blob, scale: max(c.scale, 1), has: has, loaded: loaded, want: map[rune]bool{}, never: map[rune]bool{},
		events: make(chan armEvent, 64), quit: make(chan struct{}), vt: newVT(g.cols, g.rows),
		changed: make(chan struct{}, 1)}
	t.scr = screen{slots: make([]string, g.rows), fresh: true}
	t.space = lib.GlyphIndices(" ")[0]
	t.unknown = t.space
	if q := lib.GlyphIndices("?"); len(q) == 1 {
		t.unknown = q[0]
	}
	base, err := scouterkit.Alloc(int(newLayout(0, g).end))
	if err != nil {
		return nil, fmt.Errorf("%d x %d cells: %w", g.cols, g.rows, err)
	}
	t.l = newLayout(base, g)
	for _, arm := range arms {
		px, py := demo.PanelAt(cfg, arm, g.x, g.y)
		t.pos[arm] = [2]int{px, py}
		p := program(t.l, g, px, py)
		if p.Here() > t.l.state {
			return nil, fmt.Errorf("program of %d bytes overlaps its state", p.Here()-t.l.base)
		}
		arm.WriteArena(t.l.state, make([]byte, 4*stateWords+g.rows))
		if err := arm.Load(p); err != nil {
			return nil, err
		}
		if err := arm.Start(t.l.base); err != nil {
			return nil, err
		}
		go func(arm scouterkit.Arm) {
			for {
				select {
				case <-t.quit:
					return
				default:
				}
				if e, ok := arm.NextEvent(100 * time.Millisecond); ok {
					t.events <- armEvent{arm, e}
				}
			}
		}(arm)
	}
	return t, nil
}

func (t *term) close() { close(t.quit) }

// similar stands in for symbols that TUIs use and Departure Mono has not.
var similar = map[rune]rune{
	'⏺': '•', '●': '•', '◉': '•', '○': 'o', '◦': 'o', '◯': 'o', '⎿': '└', '✻': '*', '✱': '*', '✳': '*', '✶': '*',
	'✢': '*', '✽': '*', '✔': '√', '✓': '√', '✅': '√', '✘': '×', '✗': '×', '❌': '×', '▶': '►', '▸': '►', '⏵': '►',
	'◀': '◁', '⏴': '◁', '◆': '♦', '◇': '♦', '⋯': '…', '⚠': '!', '☐': '□', '▢': '□', '☑': '■', '☒': '■', '⏹': '■',
	'⭐': '★', '⇒': '→', '⇐': '←', '⏎': '↵',
}

// glyph is the glyph of a character, one cell wide (a similar one or "?"
// for one the font has not, a space for the right half of a wide one).
func (t *term) glyph(r rune) uint32 {
	if g, ok := t.glyphs[r]; ok {
		return g
	}
	g := t.unknown
	if r == 0 || r == ' ' {
		g = t.space
	} else if !t.loaded[r] && t.has[r] && !t.never[r] {
		t.want[r] = true
		return g
	} else if gl := t.lib.GlyphIndices(string(r)); len(gl) == 1 &&
		t.font.Draw(demo.NewImage(1, 1), 0, 0, gl, 0) == t.g.cw {
		g = gl[0]
	} else if s, ok := similar[r]; ok {
		g = t.glyph(s)
	}
	t.glyphs[r] = g
	return g
}

// grow puts the wanted characters on the arms too: the font again with them
// (the glyph indices change, so the next frame redraws everything), or, if
// that does not fit the arena, as it was, and those characters show as "?".
func (t *term) grow() error {
	more := map[rune]bool{}
	for r := range t.loaded {
		more[r] = true
	}
	for r := range t.want {
		more[r] = true
	}
	t.lib.Unload()
	lib, err := scouterkit.LoadFont(t.blob, chars(more), t.scale)
	if err != nil {
		for r := range t.want {
			t.never[r] = true
		}
		more = t.loaded
		if lib, err = scouterkit.LoadFont(t.blob, chars(more), t.scale); err != nil {
			return err
		}
	}
	df, err := demo.FontOf(t.arms[0], lib)
	if err != nil {
		return err
	}
	t.lib, t.font, t.loaded, t.want, t.glyphs = lib, df, more, map[rune]bool{}, map[rune]uint32{}
	t.space = lib.GlyphIndices(" ")[0]
	if q := lib.GlyphIndices("?"); len(q) == 1 {
		t.unknown = q[0]
	}
	t.scr.fresh = true
	return nil
}

// rows is the screen as rows for the arms, and how far it scrolled up
// since the last call.
func (t *term) rows() ([]row, int) {
	t.mu.Lock()
	grid, scrolled := t.vt.take()
	cx, cy, on := t.vt.x, t.vt.y, t.vt.cursorOn
	t.mu.Unlock()
	rows := make([]row, len(grid))
	for y, cells := range grid {
		rows[y] = row{rec: encode(cells, t.glyph), cursor: -1}
		if on && y == cy {
			rows[y].cursor = cx
		}
	}
	return rows, scrolled
}

// update sends the arms what changed and waits until both show it.
func (t *term) update() error {
	rows, scrolled := t.rows()
	if len(t.want) > 0 {
		if err := t.grow(); err != nil {
			return err
		}
		rows, scrolled = t.rows()
	}
	f, ok := t.scr.next(rows, scrolled)
	if !ok {
		return nil
	}
	t.frame++
	bytes := len(f.dirty) + 4*stateWords
	for _, rec := range f.recs {
		bytes += len(rec)
	}
	lead := demo.Lead(t.arms) + time.Duration(bytes/200+1)*10*time.Millisecond +
		time.Duration(len(f.recs))*2*time.Millisecond
	full, cur := uint32(0), uint32(noCursor)
	if f.full {
		full = 1
	}
	if f.curSlot >= 0 {
		cur = uint32(f.curSlot)
	}
	sent := scouterkit.NowUS()
	for _, arm := range t.arms {
		for slot, rec := range f.recs {
			arm.WriteArena(t.l.ring+uint32(slot*recordBytes), rec)
		}
		arm.WriteArena(t.l.dirty, f.dirty)
		arm.WriteArena(t.l.state, demo.Words(uint32(f.top), full, cur, uint32(f.curX), arm.DeviceTime(lead), t.frame,
			t.lib.Address()))
		arm.Raise(scouterkit.TriggerUser0)
	}
	return t.wait(t.frame, sent)
}

// wait waits for each arm's NOTIFY of the frame; after a program stopped
// (an arm rebooted) it starts it again for a full redraw.
func (t *term) wait(frame uint32, sent int64) error {
	at := map[scouterkit.Arm]int64{}
	deadline := time.After(10 * time.Second)
	for len(at) < len(t.arms) {
		select {
		case ae := <-t.events:
			switch ae.e.Type {
			case scouterkit.EventNotify:
				if ae.e.A == frame {
					at[ae.arm] = demo.HostUS(ae.arm, ae.e.DeviceUS)
				}
			case scouterkit.EventReset, scouterkit.EventHalt:
				return t.restart(ae)
			}
		case <-deadline:
			t.scr.fresh = true
			return fmt.Errorf("frame %d not shown on every arm within 10 s", frame)
		}
	}
	lo, hi := int64(1<<62), int64(0)
	for _, us := range at {
		lo, hi = min(lo, us), max(hi, us)
	}
	t.shown = append(t.shown, time.Duration(hi-sent)*time.Microsecond)
	t.spread = append(t.spread, time.Duration(hi-lo)*time.Microsecond)
	return nil
}

// restart starts a stopped program again (the arm rebooted, or it halted)
// and has the next update redraw everything.
func (t *term) restart(ae armEvent) error {
	fmt.Fprintf(os.Stderr, "term: %s arm: program stopped (event %d, code %d): starting it again\r\n", ae.arm,
		ae.e.Type, ae.e.Code)
	ae.arm.WriteArena(t.l.state+4*wNext, make([]byte, 4))
	t.scr.fresh = true
	select {
	case t.changed <- struct{}{}:
	default:
	}
	return ae.arm.Start(t.l.base)
}

// expected is the arm's panel as the program draws the screen.
func (t *term) expected(arm scouterkit.Arm) *demo.Image {
	panel := demo.NewPanel()
	p := t.pos[arm]
	for y, r := range t.scr.rows {
		panel.Paste(r.draw(t.font, t.g), p[0], p[1]+y*t.g.ch)
	}
	return panel
}

func (t *term) checkPanels() error {
	for _, arm := range t.arms {
		if err := demo.CheckPanel(arm, t.expected(arm), t.pos[arm][1], t.g.rows*t.g.ch); err != nil {
			return err
		}
	}
	return nil
}

func (t *term) feed(s string) {
	t.mu.Lock()
	t.vt.Write([]byte(s))
	t.mu.Unlock()
}

// check shows test screens and reads both panels back after each: text in
// colours, bold, faint, reverse video and underline with line drawing and a
// wide character; a screenful of lines, then three at a time (the ring
// turns); the alternate screen and back.
func (t *term) check() error {
	type step struct{ name, text string }
	lines := func(from, n int) string {
		var b strings.Builder
		for i := from; i < from+n; i++ {
			fmt.Fprintf(&b, "\r\nline %d of the scroll test", i)
		}
		return b.String()
	}
	steps := []step{
		{"styles", "\x1b[2J\x1b[H\x1b[1mbold\x1b[0m \x1b[2mfaint\x1b[0m \x1b[7mreverse\x1b[0m \x1b[4munder\x1b[0m " +
			"\x1b[31mred\x1b[0m \x1b[42;30mgreen bg\x1b[0m \x1b[38;2;255;160;0mrgb\x1b[0m\r\n" +
			"\x1b(0lqqqqk\x1b(B box 日x café │─┌┐\r\n\x1b[5;10Hat 5,10"},
		{"a screenful", lines(1, t.g.rows)},
	}
	for i := 0; i < 4; i++ {
		steps = append(steps, step{fmt.Sprintf("scroll %d", i+1), lines(t.g.rows+1+3*i, 3)})
	}
	steps = append(steps, step{"alternate screen", "\x1b[?1049h\x1b[2J\x1b[Halternate\x1b[3;3H\x1b[44m  menu  \x1b[0m"},
		step{"back", "\x1b[?1049l"})
	for _, s := range steps {
		start := time.Now()
		t.feed(s.text)
		if err := t.update(); err != nil {
			return err
		}
		if err := t.checkPanels(); err != nil {
			return fmt.Errorf("%s: %w", s.name, err)
		}
		fmt.Printf("check: %s shown on both panels %.0f ms after the text came, read back as expected\n", s.name,
			time.Since(start).Seconds()*1000)
	}
	t.summary()
	demo.Status(t.arms)
	return demo.Halted(t.arms)
}

func (t *term) summary() {
	if len(t.shown) == 0 {
		return
	}
	s := append([]time.Duration(nil), t.shown...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	worst := time.Duration(0)
	for _, d := range t.spread {
		worst = max(worst, d)
	}
	fmt.Printf("%d frames: shown %v after sending (median, %v at most), eyes %v apart at most\n", len(s),
		s[len(s)/2].Round(time.Millisecond), s[len(s)-1].Round(time.Millisecond), worst.Round(time.Millisecond))
}

// run runs the command on a pseudo-terminal until it exits, mirroring its
// screen.
func (t *term) run(args []string) error {
	cmd := exec.Command(args[0], args[1:]...)
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "TERM=") && !strings.HasPrefix(e, "COLUMNS=") && !strings.HasPrefix(e, "LINES=") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env, "TERM=xterm-256color")
	master, err := startPTY(cmd, t.g.cols, t.g.rows)
	if err != nil {
		return err
	}
	defer master.Close()
	local := isTerminal(os.Stdin.Fd()) && isTerminal(os.Stdout.Fd())
	if local {
		if cols, rows, ok := termSize(os.Stdout.Fd()); ok && (cols < t.g.cols || rows < t.g.rows) {
			fmt.Fprintf(os.Stderr, "term: this terminal is %d x %d, smaller than the glasses' %d x %d\r\n", cols, rows,
				t.g.cols, t.g.rows)
		}
		if restore := makeRaw(os.Stdin.Fd()); restore != nil {
			defer restore()
		}
		go io.Copy(master, os.Stdin)
	} else {
		t.vt.reply = func(b []byte) { master.Write(b) }
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 16384)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				if local {
					os.Stdout.Write(buf[:n])
				}
				t.feed(string(buf[:n]))
				select {
				case t.changed <- struct{}{}:
				default:
				}
			}
			if err != nil {
				return
			}
		}
	}()
	exited := make(chan struct{})
	go func() {
		cmd.Wait()
		close(exited)
	}()
	stop := demo.Interrupted()
	for {
		select {
		case <-t.changed:
		case <-done:
			err := t.update()
			<-exited
			return err
		case <-exited: // what it left running may keep the terminal open
			select {
			case <-done:
			case <-time.After(200 * time.Millisecond):
			}
			return t.update()
		case <-stop:
			cmd.Process.Signal(syscall.SIGHUP)
			<-exited
			return nil
		case ae := <-t.events:
			if ae.e.Type != scouterkit.EventReset && ae.e.Type != scouterkit.EventHalt {
				continue
			}
			if err := t.restart(ae); err != nil {
				return err
			}
		}
		t.settle(done)
		if err := t.update(); err != nil {
			fmt.Fprintf(os.Stderr, "term: %v\r\n", err)
		}
	}
}

// settle waits for the output to pause (10 ms, at most 40 ms in all, or up
// to 300 ms while a synchronized update is open).
func (t *term) settle(done <-chan struct{}) {
	start := time.Now()
	for {
		t.mu.Lock()
		open := t.vt.sync
		t.mu.Unlock()
		limit, quiet := 40*time.Millisecond, 10*time.Millisecond
		if open {
			limit = 300 * time.Millisecond
		}
		left := limit - time.Since(start)
		if left <= 0 {
			return
		}
		if open {
			quiet = left
		}
		select {
		case <-t.changed:
		case <-time.After(min(quiet, left)):
			if !open {
				return
			}
		case <-done:
			return
		}
	}
}

func termSize(fd uintptr) (cols, rows int, ok bool) {
	var ws [4]uint16
	if ioctl(fd, syscall.TIOCGWINSZ, unsafe.Pointer(&ws)) != nil {
		return 0, 0, false
	}
	return int(ws[1]), int(ws[0]), true
}
