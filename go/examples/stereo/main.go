// Command stereo places UI elements at chosen apparent distances by giving
// each eye its own horizontal offset (disparity): a HUD line far away, a
// label on the virtual display's own plane, a menu at arm's length and a
// word that slides toward you and back. Disparity 0 is infinity, DX (100) is
// the virtual display's plane (about 0.8 m for the user), and it grows as
// 1/distance. A VM program on each arm draws the elements and moves the
// slider on a timer, both arms on the same schedule.
//
//	go run ./examples/stereo [flags]
package main

import (
	"errors"
	"flag"
	"fmt"
	"hash/crc32"
	"image"
	"math"
	"os"
	"time"

	"scouterkit"
	"scouterkit/examples/internal/demo"
)

// maxDisparity is where fusion breaks for most people (docs/ideas.md B1).
const maxDisparity = 150

type config struct {
	ref, hud, label, menu, near, far float64
	fps                              int
	cycle, stay, power               time.Duration
	cycles                           int
	check, leave                     bool
}

func main() {
	var f demo.Flags
	f.Register()
	var c config
	flag.Float64Var(&c.ref, "ref", 0.8, "distance in metres that disparity DX (the virtual display's plane) stands for")
	flag.Float64Var(&c.hud, "hud", 2, "HUD line distance, metres (0: infinity)")
	flag.Float64Var(&c.label, "label", 0.8, "label distance, metres")
	flag.Float64Var(&c.menu, "menu", 0.6, "menu distance, metres")
	flag.Float64Var(&c.near, "near", 0.6, "nearest point of the slider, metres")
	flag.Float64Var(&c.far, "far", 0, "farthest point of the slider, metres (0: infinity)")
	flag.DurationVar(&c.cycle, "cycle", 4*time.Second, "time for the slider to come near and go back")
	flag.IntVar(&c.fps, "fps", 25, "slider frames per second")
	flag.IntVar(&c.cycles, "cycles", 0, "cycles before the arms switch the panels off by themselves (0: until the host stops them)")
	flag.BoolVar(&c.check, "check", false, "read both panels back and measure each element's offset per eye, then run")
	flag.DurationVar(&c.stay, "for", 0, "how long the host stays (0: until Ctrl-C)")
	flag.BoolVar(&c.leave, "leave", false, "exit with the slider moving (needs -cycles)")
	flag.DurationVar(&c.power, "power", 0, "measure the battery current while it runs, this long (at least 66s)")
	demo.Usage("stereo [flags]")
	flag.Parse()
	if flag.NArg() != 0 {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(f, c); err != nil {
		fmt.Fprintln(os.Stderr, "stereo:", err)
		os.Exit(1)
	}
}

// depth maps distances to disparities: k / z pixels, k = DX x ref.
type depth struct {
	cfg scouterkit.Config
	k   float64
}

func (g depth) disparity(z float64) int {
	if z == 0 {
		return 0
	}
	return int(math.Round(g.k / z))
}

func (g depth) distance(d int) string {
	if d <= 0 {
		return "infinity"
	}
	return fmt.Sprintf("%.2f m", g.k/float64(d))
}

// panelX is where the arm shows virtual column x of something at
// disparity d: the left eye d - DX to the right of the virtual display's
// mapping, split evenly between the eyes so it stays straight ahead.
func (g depth) panelX(arm scouterkit.Arm, x, d int) int {
	px, _ := demo.PanelAt(g.cfg, arm, x, 0)
	e := d - g.cfg.DX
	if arm == scouterkit.Left {
		return px + e - e>>1
	}
	return px - e>>1
}

func (g depth) panelY(arm scouterkit.Arm, y int) int {
	_, py := demo.PanelAt(g.cfg, arm, 0, y)
	return py
}

// element is a bitmap at a distance; x, y is its top-left corner in the
// virtual display.
type element struct {
	name  string
	z     float64
	x, y  int
	bm    *scouterkit.Bitmap
	image *demo.Image
}

func (e element) disparity(g depth) int { return g.disparity(e.z) }

type scene struct {
	g      depth
	still  []element
	slider element
	margin int    // black columns each side of the slider's text
	ds     []int  // the slider's disparity per frame
	period uint32 // µs per frame
	l      layout
	arms   []scouterkit.Arm
	frames map[scouterkit.Arm][]int32
	t0     int64 // host time (NowUS) of the first frame
}

const pad = 6

// text renders lines into a new bitmap with a frame and one highlighted
// line (hi; -1: none).
func text(f *scouterkit.Font, lines []string, hi, margin int, framed bool) (*scouterkit.Bitmap, error) {
	w := 0
	for _, s := range lines {
		w = max(w, f.Measure(s).Width)
	}
	w += 2*pad + 2*margin
	w |= 1
	h := len(lines)*f.LineSpacing + 2*pad
	bm, err := scouterkit.CreateBitmap(w, h)
	if err != nil {
		return nil, err
	}
	if framed {
		for _, r := range []image.Rectangle{image.Rect(0, 0, w, 2), image.Rect(0, h-2, w, h),
			image.Rect(0, 0, 2, h), image.Rect(w-2, 0, w, h)} {
			bm.Fill(r, 8*17)
		}
	}
	for i, s := range lines {
		top := pad + i*f.LineSpacing
		if i == hi {
			bm.Fill(image.Rect(margin+pad/2, top, w-margin-pad/2, top+f.LineSpacing), 3*17)
		}
		f.DrawLine(bm, s, margin+pad, top+(f.LineSpacing-f.Ascent-f.Descent)/2+f.Ascent, scouterkit.AlignLeft, 255)
	}
	return bm, nil
}

func newScene(cfg scouterkit.Config, arms []scouterkit.Arm, c config) (*scene, error) {
	g := depth{cfg: cfg, k: float64(cfg.DX) * c.ref}
	for _, z := range []float64{c.hud, c.label, c.menu, c.near, c.far} {
		if z < 0 || g.disparity(z) > maxDisparity {
			return nil, fmt.Errorf("%.2f m: disparity %d px, fusion breaks above %d (nearest: %.2f m)", z,
				g.disparity(z), maxDisparity, g.k/maxDisparity)
		}
	}
	if c.fps < 1 || c.fps > 50 || c.cycle < 200*time.Millisecond {
		return nil, errors.New("-fps 1...50 and -cycle of at least 200ms")
	}
	font, err := scouterkit.LoadFont(scouterkit.DepartureMono(), "", 2)
	if err != nil {
		return nil, err
	}
	s := &scene{g: g, arms: arms, period: uint32(1000000 / c.fps), frames: map[scouterkit.Arm][]int32{}}
	n := max(2, int(c.cycle.Microseconds()/int64(s.period)))
	dNear, dFar := g.disparity(c.near), g.disparity(c.far)
	step := 0
	for k := 0; k < n; k++ {
		d := dFar + int(math.Round(float64(dNear-dFar)*(1-math.Cos(2*math.Pi*float64(k)/float64(n)))/2))
		if k > 0 {
			step = max(step, abs(d-s.ds[k-1]))
		}
		s.ds = append(s.ds, d)
	}
	s.margin = step/2 + 2
	vw, _ := scouterkit.Size()
	centre := func(w int) int { return max(0, cfg.DX) + (vw-2*abs(cfg.DX)-w)/2 }
	add := func(name string, z float64, y int, lines []string, hi int, framed bool) error {
		bm, err := text(font, lines, hi, 0, framed)
		if err != nil {
			return err
		}
		e := element{name: name, z: z, y: y, bm: bm, image: demo.FromPanel(bm.Image())}
		e.x = centre(e.image.W)
		s.still = append(s.still, e)
		return nil
	}
	if err := add("HUD", c.hud, 24, []string{"HUD  " + g.distance(g.disparity(c.hud))}, -1, false); err != nil {
		return nil, err
	}
	if err := add("label", c.label, 92, []string{"label  " + g.distance(g.disparity(c.label))}, -1, false); err != nil {
		return nil, err
	}
	menu := []string{"Menu  " + g.distance(g.disparity(c.menu)), "> Resume", "  Library", "  Settings"}
	if err := add("menu", c.menu, 160, menu, 1, true); err != nil {
		return nil, err
	}
	bm, err := text(font, []string{"<< DEPTH >>"}, -1, s.margin, false)
	if err != nil {
		return nil, err
	}
	s.slider = element{name: "slider", y: 372, bm: bm, image: demo.FromPanel(bm.Image())}
	s.slider.x = centre(s.slider.image.W)
	return s, nil
}

func abs(v int) int { return max(v, -v) }

// at is where the arm shows the element at disparity d.
func (s *scene) at(arm scouterkit.Arm, e element, d int) (int, int) {
	return s.g.panelX(arm, e.x, d), s.g.panelY(arm, e.y)
}

func (s *scene) placedOn(arm scouterkit.Arm, e element, d int) placed {
	addr, stride := e.bm.Address()
	x, y := s.at(arm, e, d)
	return placed{addr: addr, stride: stride, w: e.image.W, h: e.image.H, x: x, y: y}
}

func (s *scene) load(c config) error {
	frames := 4 * len(s.ds)
	base, err := scouterkit.Alloc(4096 + 4*stateWords + frames)
	if err != nil {
		return err
	}
	s.l = layout{base: base, state: base + 4096, table: base + 4096 + 4*stateWords}
	for _, arm := range s.arms {
		var still []placed
		for _, e := range s.still {
			still = append(still, s.placedOn(arm, e, e.disparity(s.g)))
		}
		sl := s.placedOn(arm, s.slider, s.ds[0])
		sl.period = s.period
		for _, d := range s.ds {
			x, _ := s.at(arm, s.slider, d)
			sl.frames = append(sl.frames, int32(x))
		}
		s.frames[arm] = sl.frames
		p := program(s.l, still, sl)
		if p.Here() > s.l.state {
			return fmt.Errorf("program of %d bytes overlaps its state", p.Here()-base)
		}
		table := make([]uint32, len(sl.frames))
		for i, x := range sl.frames {
			table[i] = uint32(x)
		}
		arm.WriteArena(s.l.table, demo.Words(table...))
		arm.WriteArena(s.l.state, demo.Words(1))
		arm.WriteArena(s.l.state+4*wCycles, demo.Words(uint32(c.cycles)))
		if err := arm.Load(p); err != nil {
			return err
		}
	}
	return nil
}

// start starts the programs and waits until both show the still elements.
func (s *scene) start() error {
	for _, arm := range s.arms {
		if err := arm.Start(s.l.base); err != nil {
			return err
		}
	}
	for _, arm := range s.arms {
		for {
			e, err := demo.WaitNotify(arm, 5*time.Second)
			if err != nil {
				return err
			}
			if e.A == stillDrawn {
				break
			}
		}
	}
	return nil
}

// locate finds the element's image on the arm's panel within r pixels of
// (x, y), by the panel's read-back CRC of the rows it can be in.
func locate(arm scouterkit.Arm, img *demo.Image, x, y, r int) (int, int, error) {
	y0, y1 := max(0, y-r), min(scouterkit.PanelHeight, y+img.H+r)
	got, _, err := arm.PanelCRC(y0, y1-y0)
	if err != nil {
		return 0, 0, fmt.Errorf("%s arm: panel read-back: %w", arm, err)
	}
	for dy := -r; dy <= r; dy++ {
		for dx := -r; dx <= r; dx++ {
			rows := demo.NewImage(scouterkit.PanelWidth, y1-y0)
			rows.Paste(img, x+dx, y+dy-y0)
			if crc32.ChecksumIEEE(rows.Rows(0, rows.H)) == got {
				return x + dx, y + dy, nil
			}
		}
	}
	return 0, 0, fmt.Errorf("%s arm: rows %d...%d (crc %08x) hold no copy of the element within %d px of (%d, %d)",
		arm, y0, y1-1, got, r, x, y)
}

// measure reads where both eyes show the element and checks that the
// offsets are those of disparity d (and DY).
func (s *scene) measure(e element, d int) (string, error) {
	xs := map[scouterkit.Arm]int{}
	ys := map[scouterkit.Arm]int{}
	for _, arm := range s.arms {
		x, y := s.at(arm, e, d)
		gx, gy, err := locate(arm, e.image, x, y, 6)
		if err != nil {
			return "", fmt.Errorf("%s: %w", e.name, err)
		}
		if gx != x || gy != y {
			return "", fmt.Errorf("%s: %s arm shows it at (%d, %d), expected (%d, %d)", e.name, arm, gx, gy, x, y)
		}
		xs[arm], ys[arm] = gx, gy
	}
	if len(s.arms) < 2 {
		return fmt.Sprintf("%-7s %s arm at x %d (one arm: no disparity)", e.name, s.arms[0], xs[s.arms[0]]), nil
	}
	got := xs[scouterkit.Left] - xs[scouterkit.Right]
	dy := ys[scouterkit.Right] - ys[scouterkit.Left]
	if got != d || dy != s.g.cfg.DY {
		return "", fmt.Errorf("%s: disparity %d px, dy %d, expected %d, %d", e.name, got, dy, d, s.g.cfg.DY)
	}
	return fmt.Sprintf("%-7s left x %3d, right x %3d: disparity %3d px = %s (dy %d)", e.name, xs[scouterkit.Left],
		xs[scouterkit.Right], got, s.g.distance(got), dy), nil
}

// check measures every still element and the slider at four frames (USER3).
func (s *scene) check() error {
	for _, e := range s.still {
		line, err := s.measure(e, e.disparity(s.g))
		if err != nil {
			return err
		}
		fmt.Println("check:", line)
	}
	n := len(s.ds)
	for _, k := range []int{0, n / 4, n / 2, 3 * n / 4} {
		if err := s.step(k); err != nil {
			return err
		}
		line, err := s.measure(s.slider, s.ds[k])
		if err != nil {
			return fmt.Errorf("frame %d: %w", k, err)
		}
		fmt.Printf("check: frame %3d %s\n", k, line)
	}
	return nil
}

func (s *scene) step(k int) error {
	for _, arm := range s.arms {
		arm.WriteArena(s.l.state+4*wStep, demo.Words(uint32(k)))
		arm.Raise(scouterkit.TriggerUser3)
	}
	for _, arm := range s.arms {
		for {
			e, err := demo.WaitNotify(arm, 5*time.Second)
			if err != nil {
				return err
			}
			if e.B == uint32(k) {
				break
			}
		}
	}
	return nil
}

// animate starts the slider on both arms at the same time, lead from now.
func (s *scene) animate(lead time.Duration) {
	s.t0 = scouterkit.NowUS() + lead.Microseconds()
	for _, arm := range s.arms {
		arm.WriteArena(s.l.state, demo.Words(0))
		arm.WriteArena(s.l.state+4*wStart, demo.Words(arm.DeviceTime(lead)))
		arm.Raise(scouterkit.TriggerUser0)
	}
}

// reanchor gives both arms the start of a cycle at least a second ahead, in
// each one's clock as estimated now, so drift and better clock estimates do
// not part the eyes.
func (s *scene) reanchor() {
	cycle := int64(len(s.ds)) * int64(s.period)
	now := scouterkit.NowUS()
	k := (now + 1000000 - s.t0 + cycle - 1) / cycle
	at := time.Duration(s.t0+k*cycle-now) * time.Microsecond
	for _, arm := range s.arms {
		arm.WriteArena(s.l.state+4*wAnchor, demo.Words(arm.DeviceTime(at)))
	}
}

// watch follows the frames until d (0: Ctrl-C), printing the eyes' skew.
func (s *scene) watch(d time.Duration, stop <-chan struct{}) (*demo.Skew, error) {
	var skew demo.Skew
	var end <-chan time.Time
	if d > 0 {
		end = time.After(d)
	}
	report := time.NewTicker(10 * time.Second)
	defer report.Stop()
	for {
		select {
		case <-stop:
			return &skew, nil
		case <-end:
			return &skew, nil
		case <-report.C:
			fmt.Println(skew.String())
			s.reanchor()
		default:
		}
		for _, arm := range s.arms {
			e, ok := arm.NextEvent(20 * time.Millisecond)
			if !ok {
				continue
			}
			switch e.Type {
			case scouterkit.EventNotify:
				skew.Add(arm, e.A, e)
			case scouterkit.EventHalt, scouterkit.EventReset:
				return &skew, fmt.Errorf("%s arm: the program stopped (event %d)", arm, e.Type)
			}
		}
	}
}

func run(f demo.Flags, c config) error {
	if c.leave && c.cycles == 0 {
		return errors.New("-leave needs -cycles: the panels would stay on for good")
	}
	cfg, arms, err := demo.Connect(f)
	if err != nil {
		return err
	}
	defer scouterkit.Deinit()
	s, err := newScene(cfg, arms, c)
	if err != nil {
		return err
	}
	if err := s.load(c); err != nil {
		return err
	}
	if err := s.start(); err != nil {
		return err
	}
	fmt.Printf("slider: %d frames of %d ms, disparity %d...%d px (%s to %s), %d px margin\n", len(s.ds),
		s.period/1000, s.ds[0], s.ds[len(s.ds)/2], s.g.distance(s.ds[0]), s.g.distance(s.ds[len(s.ds)/2]), s.margin)
	if c.check {
		if err := s.check(); err != nil {
			return err
		}
		fmt.Println("check: every element sits at its depth's offset on both panels")
	}
	s.animate(500 * time.Millisecond)
	stop := demo.Interrupted()
	if c.power > 0 {
		demo.PrintCurrent("slider moving", arms, c.power, stop)
	}
	if c.leave {
		for _, arm := range arms {
			if _, err := demo.WaitNotify(arm, 5*time.Second); err != nil {
				return err
			}
		}
		fmt.Printf("left running: the arms stop after %d cycles\n", c.cycles)
		return demo.Halted(arms)
	}
	skew, err := s.watch(c.stay, stop)
	fmt.Println(skew.String())
	if err == nil {
		err = demo.Halted(arms)
	}
	demo.Status(arms)
	demo.Stop(arms)
	fmt.Println("program stopped, panels off")
	return err
}
