// Command gestures detects head nods on the glasses and pages through a
// list with them: a nod shows the list or turns to the next page, a double
// nod to the page before, and the list hides a few seconds after the last
// one. The detector is a VM program on each arm that reads PITCH and the
// accelerometer every 20 ms (IMU_RATE 52) and NOTIFYs each gesture to the
// host. While the host is connected it decides, so both eyes turn the same
// page at the same time; with the host away each arm pages by itself. A
// shake needs the gyroscope and has its gesture code and handler waiting.
//
//	go run ./examples/gestures [flags]
//	go run ./examples/gestures -replay FILE.csv   (a -record file, no glasses)
package main

import (
	"bufio"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"scouterkit"
	"scouterkit/examples/internal/demo"
)

type config struct {
	th                       thresholds
	list, record, replayFile string
	lines                    int
	rate                     int
	hold, idle, stay, power  time.Duration
	check, leave             bool
}

func main() {
	var f demo.Flags
	f.Register()
	c := config{th: defaultThresholds()}
	flag.Float64Var(&c.th.depth, "depth", c.th.depth, "a nod dips at least this much, degrees")
	flag.Float64Var(&c.th.depth2, "depth2", c.th.depth2, "the second dip of a double nod, degrees")
	flag.Float64Var(&c.th.start, "start", c.th.start, "a dip starts this far below the head's rest pitch, degrees")
	flag.IntVar(&c.th.motion, "motion", c.th.motion, "mg of change over 80 ms that counts as moving")
	flag.DurationVar(&c.th.quiet, "quiet", c.th.quiet, "the head must be still this long before a nod")
	flag.DurationVar(&c.th.guard, "guard", c.th.guard, "...up to this long before the dip shows")
	flag.DurationVar(&c.th.min, "min", c.th.min, "shortest nod")
	flag.DurationVar(&c.th.max, "max", c.th.max, "longest nod")
	flag.DurationVar(&c.th.second, "second", c.th.second, "longest second dip of a double nod")
	flag.DurationVar(&c.th.double, "double", c.th.double, "a second dip within this long makes a double nod (a nod is reported after it)")
	flag.StringVar(&c.list, "list", "", "text file, one item per line (default: a built-in list)")
	flag.IntVar(&c.lines, "lines", 5, "lines per page, the title included")
	flag.IntVar(&c.rate, "rate", 52, "IMU_RATE while it runs: 52 or 104 Hz")
	flag.DurationVar(&c.hold, "hold", 6*time.Second, "the list hides this long after the last gesture")
	flag.BoolVar(&c.check, "check", false, "replay synthetic traces on the arms, page by host gestures with read-back, then watch idle")
	flag.DurationVar(&c.idle, "idle", time.Minute, "with -check: how long to watch for false gestures")
	flag.StringVar(&c.record, "record", "", "write every sample to this CSV file (device µs, pitch, ax, ay, az)")
	flag.StringVar(&c.replayFile, "replay", "", "run the detector over a -record file here (no glasses) and print what it finds")
	flag.DurationVar(&c.stay, "for", 0, "how long the host stays (0: until Ctrl-C)")
	flag.BoolVar(&c.leave, "leave", false, "exit with the detector running (nods page with the host away)")
	flag.DurationVar(&c.power, "power", 0, "measure the battery current while it runs, this long (at least 66s)")
	demo.Usage("gestures [flags]")
	flag.Parse()
	if flag.NArg() != 0 {
		flag.Usage()
		os.Exit(2)
	}
	var err error
	if c.replayFile != "" {
		err = replayFile(c)
	} else {
		err = run(f, c)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "gestures:", err)
		os.Exit(1)
	}
}

var builtin = []string{"Preheat the oven, 200 C", "Butter a 24 cm tin", "Whisk 3 eggs, 150 g sugar",
	"Fold in 150 g flour", "Add 50 g melted butter", "Pour into the tin", "Bake 25 minutes",
	"Knife comes out clean?", "Cool 10 minutes", "Turn out onto a rack", "Dust with sugar", "Enjoy"}

// loaded is the detector and the list on the arms.
type loaded struct {
	l       layout
	pr      params
	e       entries
	g       map[scouterkit.Arm]pager
	font    *demo.Font
	text    [][]uint32 // glyphs per line, page by page
	levels  []uint8
	arms    []scouterkit.Arm
	relayed time.Time
}

const lineBytes = 64

func load(cfg scouterkit.Config, arms []scouterkit.Arm, c config) (*loaded, error) {
	if c.rate != 52 && c.rate != 104 {
		return nil, errors.New("-rate 52 or 104: the detector reads every 20 ms")
	}
	if c.lines < 2 {
		return nil, errors.New("-lines: at least 2")
	}
	items := builtin
	if c.list != "" {
		b, err := os.ReadFile(c.list)
		if err != nil {
			return nil, err
		}
		items = nil
		for _, s := range strings.Split(string(b), "\n") {
			if s = strings.TrimSpace(s); s != "" {
				items = append(items, s)
			}
		}
		if len(items) == 0 {
			return nil, errors.New(c.list + ": no items")
		}
	}
	font, err := scouterkit.LoadFont(scouterkit.DepartureMono(), charset(items)+"List 0123456789/.", 2)
	if err != nil {
		return nil, err
	}
	vw, _ := scouterkit.Size()
	g := pager{font: font.Address(), w: vw - 2*abs(cfg.DX), pad: 8, lines: c.lines, lineBytes: lineBytes,
		spacing: font.LineSpacing, hold: uint32(c.hold.Microseconds()), rate: c.rate}
	g.h = c.lines*font.LineSpacing + 2*g.pad
	g.baseline = g.pad + (font.LineSpacing-font.Ascent-font.Descent)/2 + font.Ascent
	per := c.lines - 1
	g.pages = (len(items) + per - 1) / per
	if g.pages > 255 {
		return nil, errors.New("too many pages")
	}
	ld := &loaded{pr: c.th.params(), g: map[scouterkit.Arm]pager{}, arms: arms}
	for pg := 0; pg < g.pages; pg++ {
		ld.text = append(ld.text, font.GlyphIndices(fmt.Sprintf("List %d/%d", pg+1, g.pages)))
		for i := pg * per; i < (pg+1)*per; i++ {
			s := ""
			if i < len(items) {
				s = fit(font, items[i], g.w-2*g.pad)
			}
			ld.text = append(ld.text, font.GlyphIndices(s))
		}
	}
	ld.levels = make([]uint8, c.lines)
	for i := range ld.levels {
		ld.levels[i] = 15
	}
	ld.levels[0] = 9
	if ld.pr.guard > 64 {
		return nil, errors.New("-guard: at most 1.28s")
	}
	l := layout{guard: ld.pr.guard}
	bandBytes := uint32(g.stride() * g.h * 2)
	textBytes := uint32(len(ld.text) * lineBytes)
	size := codeBytes + l.detBytes() + 4*appWords + 64 + 256 + textBytes + bandBytes
	base, err := scouterkit.Alloc(int(size))
	if err != nil {
		return nil, err
	}
	l.base, l.det = base, base+codeBytes
	l.app = l.det + l.detBytes()
	l.levels = l.app + 4*appWords
	l.replay = l.levels + 64
	l.text = l.replay + 256
	g.band = l.text + textBytes
	ld.l = l
	if ld.font, err = demo.FontOf(arms[0], font); err != nil {
		return nil, err
	}
	var text []byte
	for _, line := range ld.text {
		v := demo.Varints(line)
		rec := make([]byte, lineBytes)
		rec[0] = byte(len(v))
		copy(rec[1:], v)
		text = append(text, rec...)
	}
	for _, arm := range arms {
		ga := g
		ga.x, ga.y = demo.PanelAt(cfg, arm, abs(cfg.DX), 110)
		ld.g[arm] = ga
		p, e := program(l, ld.pr, ga)
		if p.Here() > l.det {
			return nil, fmt.Errorf("program of %d bytes overlaps its state", p.Here()-base)
		}
		ld.e = e
		arm.WriteArena(l.levels, ld.levels)
		arm.WriteArena(l.text, text)
		arm.WriteArena(l.app, demo.Words(make([]uint32, appWords)...))
		if c.record != "" {
			arm.WriteArena(l.app+4*wRecord, demo.Words(1))
		}
		if err := arm.Load(p); err != nil {
			return nil, err
		}
	}
	return ld, nil
}

func abs(v int) int { return max(v, -v) }

func charset(items []string) string {
	seen := map[rune]bool{}
	var b strings.Builder
	for _, s := range items {
		for _, r := range s {
			if !seen[r] && r >= ' ' {
				seen[r] = true
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

// fit cuts s to width pixels, ending in "..." if it was cut.
func fit(f *scouterkit.Font, s string, width int) string {
	if f.Measure(s).Width <= width {
		return s
	}
	r := []rune(s)
	for len(r) > 0 && f.Measure(string(r)+"...").Width > width {
		r = r[:len(r)-1]
	}
	return string(r) + "..."
}

// expected is the arm's panel showing page pg.
func (ld *loaded) expected(arm scouterkit.Arm, pg int) *demo.Image {
	g := ld.g[arm]
	band := demo.NewImage(g.w, g.h)
	for i := 0; i < g.lines; i++ {
		ld.font.Draw(band, g.pad, g.baseline+i*g.spacing, ld.text[pg*g.lines+i], ld.levels[i])
	}
	panel := demo.NewPanel()
	panel.Paste(band, g.x, g.y)
	return panel
}

func (ld *loaded) checkPage(arm scouterkit.Arm, pg int) error {
	g := ld.g[arm]
	return demo.CheckPanel(arm, ld.expected(arm, pg), g.y, g.h)
}

// replay runs the detector over the samples on the arm through host code
// (the program must not be running: host code then owns r0...r14) and
// returns what it detected, at 0-based sample indices.
func (ld *loaded) replay(arm scouterkit.Arm, samples []sample) ([]detection, error) {
	demo.Drain(arm)
	arm.Call(ld.e.reset)
	for i := 0; i < len(samples); i += replayBatch {
		batch := samples[i:min(len(samples), i+replayBatch)]
		b := binary.LittleEndian.AppendUint32(nil, uint32(len(batch)))
		for _, x := range batch {
			for _, v := range []int32{x.pitch, x.ax, x.ay, x.az} {
				b = binary.LittleEndian.AppendUint16(b, uint16(v))
			}
		}
		h := scouterkit.NewProgram(0, 0, 0, 0)
		h.Write(ld.l.replay, b)
		h.Call(h.At(ld.e.replay))
		if err := h.Err(); err != nil {
			return nil, err
		}
		arm.Run(h.Code())
	}
	arm.Notify(0xFE, uint32(len(samples)))
	var out []detection
	for {
		e, err := demo.WaitNotify(arm, 30*time.Second)
		if err != nil {
			return nil, err
		}
		switch {
		case e.Trigger == 0xFF && e.A == 0xFE:
			return out, nil
		case e.Trigger == 0xFF && (e.A == gestureNod || e.A == gestureDouble):
			out = append(out, detection{int(e.A), int(e.B) - 1})
		}
	}
}

func sameDetections(a, b []detection) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// checkReplay replays synthetic traces on every arm and compares the
// detections with the Go model's.
func (ld *loaded) checkReplay() error {
	traces := []trace{nodTrace(1, gestureNod, 3), nodTrace(1001, gestureDouble, 3), walkingTrace(2001, 60),
		stillTrace(3001, 30, true)}
	for _, tr := range traces {
		want := detect(ld.pr, tr.samples)
		for _, arm := range ld.arms {
			start := time.Now()
			got, err := ld.replay(arm, tr.samples)
			if err != nil {
				return err
			}
			if !sameDetections(got, want) {
				return fmt.Errorf("%s arm, %s: detected %v, the model %v", arm, tr.name, got, want)
			}
			fmt.Printf("check: %s arm replayed %s (%.0f s of samples in %.1f s): %v, as the model\n", arm, tr.name,
				float64(len(tr.samples))*samplePeriod, time.Since(start).Seconds(), got)
		}
	}
	return nil
}

func (ld *loaded) start() error {
	for _, arm := range ld.arms {
		demo.Drain(arm)
		if err := arm.Start(ld.l.base); err != nil {
			return err
		}
	}
	for _, arm := range ld.arms {
		if _, err := ld.wait(arm, noteStarted); err != nil {
			return err
		}
	}
	return nil
}

// wait returns the arm's next NOTIFY with a = what.
func (ld *loaded) wait(arm scouterkit.Arm, what uint32) (scouterkit.Event, error) {
	for {
		e, err := demo.WaitNotify(arm, 10*time.Second)
		if err != nil || e.A == what {
			return e, err
		}
	}
}

// send hands every arm's app a gesture, to show at one time on both eyes.
func (ld *loaded) send(kind uint32) {
	lead := demo.Lead(ld.arms)
	for _, arm := range ld.arms {
		arm.WriteArena(ld.l.app+4*wGesture, demo.Words(kind, arm.DeviceTime(lead)))
		arm.Raise(scouterkit.TriggerUser1)
	}
}

// relay acts on an arm's gesture while the host is connected (the arms then
// leave paging to it, so both eyes turn the same page): the first report
// counts, the other arm's report of the same gesture is dropped.
func (ld *loaded) relay(e scouterkit.Event) {
	if time.Since(ld.relayed) < 600*time.Millisecond {
		return
	}
	ld.relayed = time.Now()
	ld.send(e.A)
}

// checkPaging shows the list by host gestures: nod (page 1), nod (2),
// double nod (1), each read back, then waits for it to hide.
func (ld *loaded) checkPaging() error {
	pages := ld.g[ld.arms[0]].pages
	for _, step := range []struct {
		kind uint32
		page int
	}{{gestureNod, 0}, {gestureNod, 1 % pages}, {gestureDouble, 0}} {
		ld.send(step.kind)
		for _, arm := range ld.arms {
			e, err := ld.wait(arm, notePage)
			if err != nil {
				return err
			}
			if int(e.B) != step.page {
				return fmt.Errorf("%s arm: page %d, expected %d", arm, e.B, step.page)
			}
			if err := ld.checkPage(arm, step.page); err != nil {
				return err
			}
		}
	}
	for _, arm := range ld.arms {
		if _, err := ld.wait(arm, noteHidden); err != nil {
			return err
		}
		v, err := arm.Props(scouterkit.PropPanel)
		if err != nil {
			return err
		}
		if v[0] != 0 {
			return fmt.Errorf("%s arm: the panel is on after the list hid", arm)
		}
	}
	return nil
}

// checkIdle watches for d: no gesture may come, and the arms must sample
// at 50 Hz with the pitch known.
func (ld *loaded) checkIdle(d time.Duration) error {
	count := func(arm scouterkit.Arm) (uint32, error) {
		b, err := arm.ReadArena(ld.l.det+4*wS, 4)
		if err != nil {
			return 0, err
		}
		return binary.LittleEndian.Uint32(b), nil
	}
	from := map[scouterkit.Arm]uint32{}
	for _, arm := range ld.arms {
		n, err := count(arm)
		if err != nil {
			return err
		}
		from[arm] = n
	}
	t0 := time.Now()
	gestures := 0
	for time.Since(t0) < d {
		for _, arm := range ld.arms {
			e, ok := arm.NextEvent(50 * time.Millisecond)
			switch {
			case !ok:
			case e.Type == scouterkit.EventHalt || e.Type == scouterkit.EventReset:
				return fmt.Errorf("%s arm: the program stopped (event %d)", arm, e.Type)
			case e.Type == scouterkit.EventNotify && (e.A == gestureNod || e.A == gestureDouble):
				gestures++
				fmt.Printf("check: %s arm: gesture %d at sample %d\n", arm, e.A, e.B)
			}
		}
	}
	for _, arm := range ld.arms {
		n, err := count(arm)
		if err != nil {
			return err
		}
		rate := float64(n-from[arm]) / time.Since(t0).Seconds()
		v, err := arm.Props(scouterkit.PropPitch, scouterkit.PropIMURate)
		if err != nil {
			return err
		}
		fmt.Printf("check: %s arm idle %v: %.1f samples/s, pitch %s, IMU_RATE %d\n", arm, d.Round(time.Second), rate,
			demo.Degrees(v[0]), v[1])
		if rate < 45 || rate > 51 || v[0] == scouterkit.Unknown {
			return fmt.Errorf("%s arm: %.1f samples/s, pitch %s", arm, rate, demo.Degrees(v[0]))
		}
	}
	if gestures > 0 {
		return fmt.Errorf("%d gestures while idle", gestures)
	}
	return nil
}

func run(f demo.Flags, c config) error {
	cfg, arms, err := demo.Connect(f)
	if err != nil {
		return err
	}
	defer scouterkit.Deinit()
	ld, err := load(cfg, arms, c)
	if err != nil {
		return err
	}
	if c.check {
		if err := ld.checkReplay(); err != nil {
			return err
		}
	}
	if err := ld.start(); err != nil {
		return err
	}
	if c.check {
		if err := ld.checkPaging(); err != nil {
			return err
		}
		fmt.Println("check: nod, nod, double nod paged 1, 2, 1, read back on both panels; hidden with the panels off")
		if err := ld.checkIdle(c.idle); err != nil {
			return err
		}
		fmt.Println("check: no gesture while idle")
	}
	stop := demo.Interrupted()
	if c.power > 0 {
		demo.PrintCurrent("detector idle", arms, c.power, stop)
	}
	if c.leave {
		fmt.Println("left running: nod to see the list")
		return demo.Halted(arms)
	}
	if !c.check || c.stay > 0 {
		if err := ld.watch(c, stop); err != nil {
			return err
		}
	}
	if err := demo.Halted(arms); err != nil {
		return err
	}
	demo.Status(arms)
	demo.Stop(arms)
	for _, arm := range arms {
		arm.SysWrite(scouterkit.PropIMURate, 13)
	}
	fmt.Println("program stopped, panels off, IMU_RATE 13")
	return nil
}

// watch prints the gestures and pages until c.stay (0: Ctrl-C), and
// writes recorded samples.
func (ld *loaded) watch(c config, stop <-chan struct{}) error {
	var rec *bufio.Writer
	if c.record != "" {
		f, err := os.Create(c.record)
		if err != nil {
			return err
		}
		defer f.Close()
		rec = bufio.NewWriter(f)
		defer rec.Flush()
		fmt.Fprintln(rec, "arm,device_us,pitch,ax,ay,az")
	}
	fmt.Println("nod to show the list or turn the page, double nod to go back")
	var end <-chan time.Time
	if c.stay > 0 {
		end = time.After(c.stay)
	}
	names := map[uint32]string{gestureNod: "nod", gestureDouble: "double nod", gestureShake: "shake"}
	for {
		select {
		case <-stop:
			return nil
		case <-end:
			return nil
		default:
		}
		for _, arm := range ld.arms {
			e, ok := arm.NextEvent(10 * time.Millisecond)
			if !ok {
				continue
			}
			now := time.Now().Format("15:04:05.000")
			switch {
			case e.Type == scouterkit.EventHalt || e.Type == scouterkit.EventReset:
				return fmt.Errorf("%s arm: the program stopped (event %d)", arm, e.Type)
			case e.Type != scouterkit.EventNotify:
			case e.B>>24 == recordMark>>24:
				if rec != nil {
					fmt.Fprintf(rec, "%s,%d,%d,%d,%d,%d\n", arm, e.DeviceUS, int8(e.B>>16), int16(e.A), int16(e.A>>16), int16(e.B))
				}
			case names[e.A] != "":
				fmt.Printf("%s %s arm: %s\n", now, arm, names[e.A])
				ld.relay(e)
			case e.A == notePage:
				fmt.Printf("%s %s arm: page %d\n", now, arm, e.B+1)
			case e.A == noteHidden:
				fmt.Printf("%s %s arm: hidden\n", now, arm)
			}
		}
	}
}

// readRecord reads a -record file: each arm's samples, and the arms in the
// order they first appear.
func readRecord(path string) (map[string][]sample, []string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	per := map[string][]sample{}
	var order []string
	for i, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		f := strings.Split(line, ",")
		if i == 0 || len(f) != 6 {
			continue
		}
		var v [4]int32
		for k := range v {
			x, err := strconv.Atoi(f[2+k])
			if err != nil {
				return nil, nil, fmt.Errorf("%s line %d: %v", path, i+1, err)
			}
			v[k] = int32(x)
		}
		if per[f[0]] == nil {
			order = append(order, f[0])
		}
		per[f[0]] = append(per[f[0]], sample{pitch: v[0], ax: v[1], ay: v[2], az: v[3]})
	}
	return per, order, nil
}

// replayFile runs the detector over a -record file, per arm.
func replayFile(c config) error {
	per, order, err := readRecord(c.replayFile)
	if err != nil {
		return err
	}
	for _, arm := range order {
		s := per[arm]
		got := detect(c.th.params(), s)
		fmt.Printf("%s arm: %d samples (%.0f s), %d gestures\n", arm, len(s), float64(len(s))*samplePeriod, len(got))
		for _, d := range got {
			fmt.Printf("  %s at %.2f s\n", map[int]string{gestureNod: "nod", gestureDouble: "double nod"}[d.kind],
				float64(d.at)*samplePeriod)
		}
	}
	return nil
}
