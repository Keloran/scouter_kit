package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	"scouterkit"
	"scouterkit/examples/internal/demo"
)

type mirrorConfig struct {
	bus            string
	serve, replace bool
	view           viewConfig
	popup, longest time.Duration
	holdback       time.Duration
	probe          time.Duration // how long to wait for an activatable server to start
	apps, skip     []string
	minUrgency     byte
	quietFrom      int // minute of the day, -1: no quiet hours
	quietTo        int
	socket         string
	check, idle    bool // idle: let the links go to their idle profile before the first popup
	stay, power    time.Duration
}

// request is a `g1 notify` over the socket.
type request struct {
	App, Summary, Body string
	Icon               string `json:",omitempty"`
	TimeoutMS          int    `json:"timeout_ms"`
	reply              chan<- reply
}

type reply struct {
	Shown bool    `json:"shown"`
	MS    float64 `json:"ms,omitempty"`
	Error string  `json:"error,omitempty"`
}

type mirror struct {
	c        mirrorConfig
	g        *glasses
	events   chan event
	requests chan request
	slotOf   map[*note]int
	state    map[scouterkit.Arm][2]uint32 // the last (state, head) each arm NOTIFYd
	waiting  []waiter
	recent   map[string]time.Time
	closers  []func()
	source   func() // closes the bus connection that events come from
	pushed   string // the link when the last notification was pushed
	held     []held // waiting out the hold-back, oldest first
}

// held is a notification waiting out the hold-back: shown at due unless it
// is closed or replaced before.
type held struct {
	n     *note
	popup bool
	due   time.Time
}

// waiter is a socket request until every arm shows its notification.
type waiter struct {
	head  uint32
	since time.Time
	seen  map[scouterkit.Arm]bool
	reply chan<- reply
}

func socketPath() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return d + "/scouterkit-g1.sock"
	}
	return fmt.Sprintf("/tmp/scouterkit-g1-%d.sock", os.Getuid())
}

func mirrorMain(args []string) error {
	fs := flag.NewFlagSet("mirror", flag.ExitOnError)
	var f demo.Flags
	fs.StringVar(&f.Left, "left", "", `left arm address (empty: the bonded glasses, "none": no left arm)`)
	fs.StringVar(&f.Right, "right", "", `right arm address (empty: the bonded glasses, "none": no right arm)`)
	fs.StringVar(&f.Adapter, "adapter", "", `BlueZ adapter, "hci1" or its address (empty: the first powered one)`)
	fs.BoolVar(&f.Log, "log", false, "the library logs to stderr")
	var c mirrorConfig
	fs.StringVar(&c.bus, "bus", "", "D-Bus address (default: the session bus)")
	fs.BoolVar(&c.serve, "serve", false, "be the bus's notification daemon instead of watching it (default: only when none runs)")
	fs.BoolVar(&c.replace, "replace", false, "with -serve: take over from a daemon that allows it")
	fs.IntVar(&c.view.angle, "angle", 20, "head-up angle, degrees: the list of recent notifications shows above it")
	fs.IntVar(&c.view.hysteresis, "hysteresis", 5, "degrees below the angle at which it hides again")
	fs.DurationVar(&c.view.hold, "hold", time.Second, "how long the list stays after the head goes down")
	fs.DurationVar(&c.view.listFor, "list-for", 15*time.Second, "the list hides after this long even with the head up")
	fs.BoolVar(&c.view.standby, "standby", false, "keep the panels in standby while hidden: popups light up about 70 ms sooner, for about 1 mA more")
	fs.DurationVar(&c.popup, "popup", 5*time.Second, "how long a notification shows when its sender leaves it to the server")
	fs.DurationVar(&c.longest, "longest", 15*time.Second, "longest popup (also for notifications that never expire)")
	fs.DurationVar(&c.holdback, "holdback", 500*time.Millisecond, "hold each new notification back this long: one closed or replaced meanwhile never shows (0: at once)")
	fs.BoolVar(&c.view.icons, "icons", true, "show the app's icon (Pebble's notification icons) beside the text")
	apps := fs.String("apps", "", "comma-separated apps to show (default: all)")
	skip := fs.String("skip", "", "comma-separated apps never to show")
	urgency := fs.String("urgency", "low", "lowest urgency shown: low, normal or critical")
	quiet := fs.String("quiet", "", "quiet hours, HH:MM-HH:MM: notifications go to the list without a popup (critical ones still pop up)")
	font := fs.String("font", "", "font blob from font_converter.py (default: the built-in Departure Mono)")
	fs.IntVar(&c.view.scale, "scale", 0, "enlarge a pixel font this many times (0: 2 for the built-in font, else 1)")
	fs.StringVar(&c.socket, "socket", socketPath(), "Unix socket for `g1 notify`")
	fs.BoolVar(&c.check, "check", false, "send test notifications with notify-send on the bus and through the socket, read both panels back, then go on")
	fs.DurationVar(&c.power, "power", 0, "measure the battery current with nothing shown, this long (at least 66s)")
	fs.DurationVar(&c.stay, "for", 0, "run this long (0: until Ctrl-C)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: g1 mirror [flags]\n\nShows the desktop's notifications on the glasses: a popup, and the last ones when the head tilts up.")
		fs.PrintDefaults()
	}
	fs.Parse(args)
	if fs.NArg() != 0 {
		fs.Usage()
		os.Exit(2)
	}
	c.idle = c.check
	c.probe = 3 * time.Second
	if *apps != "" {
		c.apps = strings.Split(strings.ToLower(*apps), ",")
	}
	if *skip != "" {
		c.skip = strings.Split(strings.ToLower(*skip), ",")
	}
	u := map[string]byte{"low": 0, "normal": 1, "critical": 2}
	level, ok := u[*urgency]
	if !ok {
		return fmt.Errorf("-urgency %q: low, normal or critical", *urgency)
	}
	c.minUrgency = level
	c.quietFrom, c.quietTo = -1, -1
	if *quiet != "" {
		var h1, m1, h2, m2 int
		if _, err := fmt.Sscanf(*quiet, "%d:%d-%d:%d", &h1, &m1, &h2, &m2); err != nil {
			return fmt.Errorf("-quiet %q: HH:MM-HH:MM", *quiet)
		}
		c.quietFrom, c.quietTo = h1*60+m1, h2*60+m2
	}
	if *font != "" {
		b, err := os.ReadFile(*font)
		if err != nil {
			return err
		}
		c.view.blob = b
		if c.view.scale == 0 {
			c.view.scale = 1
		}
	}
	return runMirror(f, c)
}

func runMirror(f demo.Flags, c mirrorConfig) error {
	cfg, arms, err := demo.Connect(f)
	if err != nil {
		return err
	}
	defer scouterkit.Deinit()
	m, err := newMirror(cfg, arms, c)
	if err != nil {
		return err
	}
	defer m.close()
	if c.check {
		if err := m.check(); err != nil {
			return err
		}
	}
	stop := demo.Interrupted()
	if c.power > 0 {
		demo.PrintCurrent("nothing shown", arms, c.power, stop)
	}
	return m.loop(stop)
}

func newMirror(cfg scouterkit.Config, arms []scouterkit.Arm, c mirrorConfig) (*mirror, error) {
	g, err := newGlasses(cfg, arms, c.view)
	if err != nil {
		return nil, err
	}
	if err := g.start(); err != nil {
		return nil, err
	}
	for _, arm := range arms {
		if _, err := wait(arm, 0, stHidden); err != nil {
			return nil, err
		}
	}
	m := &mirror{c: c, g: g, requests: make(chan request), slotOf: map[*note]int{},
		state: map[scouterkit.Arm][2]uint32{}, recent: map[string]time.Time{}}
	if err := m.follow(); err != nil {
		return nil, err
	}
	if c.socket != "" {
		if err := m.listen(); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// follow takes the notifications from the bus: by watching its notification
// daemon, or as the daemon itself with -serve or when none runs (a desktop
// daemon that asks may take over again).
func (m *mirror) follow() error {
	if m.source != nil {
		old := m.events
		m.source()
		for range old { // until its reader ends
		}
	}
	m.events = make(chan event, 64)
	if !m.c.serve {
		_, err := daemon(m.c.bus, m.c.probe)
		if err == nil {
			b, err := monitor(m.c.bus, m.events)
			if err != nil {
				return fmt.Errorf("watching the notifications on the session bus: %w (-serve makes g1 mirror the daemon)", err)
			}
			m.source = func() { b.Close() }
			fmt.Println("monitoring the bus's notifications (the desktop's daemon goes on as before)")
			return nil
		}
		if !errors.Is(err, errNoDaemon) {
			return fmt.Errorf("the session bus: %w", err)
		}
		fmt.Printf("%v: g1 mirror is the daemon now; notifications show only on the glasses\n", err)
	}
	s, err := serve(m.c.bus, m.c.replace, !m.c.serve, m.c.popup, m.events)
	if err != nil {
		return err
	}
	m.source = func() { s.Close() }
	if m.c.serve {
		fmt.Println("serving org.freedesktop.Notifications")
	}
	return nil
}

func (m *mirror) close() {
	if m.source != nil {
		m.source()
	}
	for _, f := range m.closers {
		f()
	}
	m.closers = nil
	if m.c.socket != "" {
		os.Remove(m.c.socket)
	}
}

// listen serves `g1 notify` on the Unix socket: one JSON request a line.
func (m *mirror) listen() error {
	if c, err := net.Dial("unix", m.c.socket); err == nil {
		c.Close()
		return errors.New(m.c.socket + ": another mirror is running")
	}
	os.Remove(m.c.socket)
	l, err := net.Listen("unix", m.c.socket)
	if err != nil {
		return err
	}
	m.closers = append(m.closers, func() { l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				var r request
				if err := json.NewDecoder(conn).Decode(&r); err != nil {
					json.NewEncoder(conn).Encode(reply{Error: err.Error()})
					return
				}
				ch := make(chan reply, 1)
				r.reply = ch
				m.requests <- r
				json.NewEncoder(conn).Encode(<-ch)
			}()
		}
	}()
	return nil
}

// duration is how long a notification pops up.
func (m *mirror) duration(n *note) time.Duration {
	switch {
	case n.Timeout < 0:
		return min(m.c.popup, m.c.longest)
	case n.Timeout == 0:
		return m.c.longest
	}
	return min(max(n.Timeout, time.Second), m.c.longest)
}

func listed(list []string, app string) bool {
	for _, a := range list {
		if strings.TrimSpace(a) == strings.ToLower(app) {
			return true
		}
	}
	return false
}

// wanted says whether to keep a notification, and whether to pop it up.
func (m *mirror) wanted(n *note) (keep, popup bool) {
	if n.Urgency < m.c.minUrgency || listed(m.c.skip, n.App) || (m.c.apps != nil && !listed(m.c.apps, n.App)) {
		return false, false
	}
	key := n.App + "\x00" + n.Summary + "\x00" + n.Body
	if t, ok := m.recent[key]; ok && n.At.Sub(t) < 5*time.Second && n.Replaces == 0 {
		return false, false
	}
	m.recent[key] = n.At
	for k, t := range m.recent {
		if n.At.Sub(t) > time.Minute {
			delete(m.recent, k)
		}
	}
	if m.c.quietFrom >= 0 && n.Urgency < 2 {
		t := n.At.Hour()*60 + n.At.Minute()
		in := t >= m.c.quietFrom && t < m.c.quietTo
		if m.c.quietFrom > m.c.quietTo {
			in = t >= m.c.quietFrom || t < m.c.quietTo
		}
		if in {
			return true, false
		}
	}
	return true, true
}

// handle takes a source event; it returns the head of a notification it
// pushed (0: none).
func (m *mirror) handle(e event) uint32 {
	switch {
	case e.note != nil && e.id != 0:
		if m.holding(e.note) {
			e.note.ID = e.id
			return 0
		}
		if slot, ok := m.slotOf[e.note]; ok {
			delete(m.slotOf, e.note)
			if m.g.ids[slot] == 0 {
				m.g.ids[slot] = e.id
			}
		}
	case e.note != nil:
		keep, popup := m.wanted(e.note)
		if !keep {
			return 0
		}
		if m.c.holdback <= 0 {
			return m.show(e.note, popup)
		}
		m.withdraw(e.note.Replaces)
		m.held = append(m.held, held{e.note, popup, e.note.At.Add(m.c.holdback)})
		if m.g.icons {
			m.g.iconAt(iconFor(e.note)) // on the arms by the end of the hold-back
		}
	case e.closed != 0:
		if m.withdraw(e.closed) {
			return 0
		}
		slot, ok := m.g.slotOf(e.closed)
		if !ok || uint32(slot) != m.g.head%slots {
			return 0
		}
		for _, arm := range m.g.arms {
			if s := m.state[arm]; s[0] == stPopup && s[1] == m.g.head {
				arm.Raise(scouterkit.TriggerUser2)
			}
		}
	}
	return 0
}

// show pushes a notification to the arms and returns its head.
func (m *mirror) show(n *note, popup bool) uint32 {
	lead := demo.Lead(m.g.arms)
	m.pushed = fmt.Sprintf("lead %v", lead.Round(time.Millisecond))
	for _, arm := range m.g.arms {
		s := arm.State()
		m.pushed += fmt.Sprintf(", %s %v ±%v", arm, s.Interval, s.ClockError.Round(time.Millisecond))
	}
	slot := m.g.push(n, popup, lead, m.duration(n))
	if n.ID == 0 && n.Replaces == 0 {
		m.slotOf[n] = slot
	}
	for o := range m.slotOf {
		if time.Since(o.At) > 10*time.Second {
			delete(m.slotOf, o)
		}
	}
	fmt.Printf("%s %s: %s\n", n.At.Format("15:04:05"), n.App, n.Summary)
	return m.g.head
}

func (m *mirror) holding(n *note) bool {
	for _, h := range m.held {
		if h.n == n {
			return true
		}
	}
	return false
}

// withdraw drops the held notification with this ID: it never shows.
func (m *mirror) withdraw(id uint32) bool {
	for i, h := range m.held {
		if id != 0 && h.n.ID == id {
			m.held = append(m.held[:i], m.held[i+1:]...)
			fmt.Printf("%s %s: %s (withdrawn within the hold-back)\n", h.n.At.Format("15:04:05"), h.n.App, h.n.Summary)
			return true
		}
	}
	return false
}

// release shows the held notifications whose hold-back is over and returns
// the last one (nil: none).
func (m *mirror) release(now time.Time) *note {
	var last *note
	for len(m.held) > 0 && !m.held[0].due.After(now) {
		h := m.held[0]
		m.held = m.held[1:]
		m.show(h.n, h.popup)
		last = h.n
	}
	return last
}

// due fires when the oldest held notification is due (nil: none held).
func (m *mirror) due() <-chan time.Time {
	if len(m.held) == 0 {
		return nil
	}
	return time.After(time.Until(m.held[0].due))
}

// wallUS is a device time of the arm on the wall clock (Unix µs).
func wallUS(arm scouterkit.Arm, device uint32) int64 {
	return demo.HostUS(arm, device) - scouterkit.NowUS() + time.Now().UnixMicro()
}

type armEvent struct {
	arm scouterkit.Arm
	e   scouterkit.Event
}

// loop runs the mirror until stop, the end of -for or the bus closing.
func (m *mirror) loop(stop <-chan struct{}) error {
	arms := make(chan armEvent, 64)
	quit := make(chan struct{})
	defer close(quit)
	for _, arm := range m.g.arms {
		go func(arm scouterkit.Arm) {
			for {
				select {
				case <-quit:
					return
				default:
				}
				if e, ok := arm.NextEvent(100 * time.Millisecond); ok {
					arms <- armEvent{arm, e}
				}
			}
		}(arm)
	}
	var end <-chan time.Time
	if m.c.stay > 0 {
		end = time.After(m.c.stay)
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return nil
		case <-end:
			return nil
		case e, ok := <-m.events:
			if !ok {
				return errors.New("the D-Bus connection closed")
			}
			if e.gone {
				fmt.Println("the bus's notification daemon changed")
				if err := m.follow(); err != nil {
					return err
				}
				continue
			}
			m.handle(e)
		case now := <-m.due():
			m.release(now)
		case r := <-m.requests:
			n := &note{App: r.App, Summary: r.Summary, Body: r.Body, Icon: r.Icon, Urgency: 1, At: time.Now(), Timeout: -1}
			if r.TimeoutMS > 0 {
				n.Timeout = time.Duration(r.TimeoutMS) * time.Millisecond
			}
			m.g.push(n, true, demo.Lead(m.g.arms), m.duration(n))
			m.waiting = append(m.waiting, waiter{head: m.g.head, since: n.At, seen: map[scouterkit.Arm]bool{},
				reply: r.reply})
		case ae := <-arms:
			if err := m.armEvent(ae); err != nil {
				return err
			}
		case <-tick.C:
			for i := 0; i < len(m.waiting); i++ {
				if w := m.waiting[i]; time.Since(w.since) > 5*time.Second {
					w.reply <- reply{Error: "sent, but not shown on every arm within 5 s"}
					m.waiting = append(m.waiting[:i], m.waiting[i+1:]...)
					i--
				}
			}
		}
	}
}

func (m *mirror) armEvent(ae armEvent) error {
	arm, e := ae.arm, ae.e
	switch e.Type {
	case scouterkit.EventReset, scouterkit.EventHalt:
		fmt.Printf("%s arm: program stopped (event %d, code %d): starting it again\n", arm, e.Type, e.Code)
		return arm.Start(m.g.l.base)
	case scouterkit.EventNotify:
		m.state[arm] = [2]uint32{e.A, e.B}
		if e.A == stHidden || e.A == stDismissed {
			return nil
		}
		for i := 0; i < len(m.waiting); i++ {
			w := &m.waiting[i]
			if e.B != w.head {
				continue
			}
			w.seen[arm] = true
			if len(w.seen) == len(m.g.arms) {
				w.reply <- reply{Shown: true, MS: float64(wallUS(arm, e.DeviceUS)-w.since.UnixMicro()) / 1000}
				m.waiting = append(m.waiting[:i], m.waiting[i+1:]...)
				i--
			}
		}
	}
	return nil
}

// nextNote handles source events until a notification is pushed and
// returns its head.
func (m *mirror) nextNote() (uint32, time.Time, error) {
	deadline := time.After(10 * time.Second)
	for {
		select {
		case e, ok := <-m.events:
			if !ok {
				return 0, time.Time{}, errors.New("the D-Bus connection closed")
			}
			if e.gone {
				if err := m.follow(); err != nil {
					return 0, time.Time{}, err
				}
				continue
			}
			if head := m.handle(e); head != 0 {
				return head, e.note.At, nil
			}
		case now := <-m.due():
			if n := m.release(now); n != nil {
				return m.g.head, n.At, nil
			}
		case <-deadline:
			return 0, time.Time{}, errors.New("no notification arrived from the bus within 10 s")
		}
	}
}

func (m *mirror) notifySend(args ...string) error {
	cmd := exec.Command("notify-send", args...)
	if m.c.bus != "" {
		cmd.Env = append(os.Environ(), "DBUS_SESSION_BUS_ADDRESS="+m.c.bus)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("notify-send: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// shown waits until every arm NOTIFYs that it shows head in one of the
// states and returns the latest arm's delay after at.
func (m *mirror) shown(head uint32, at time.Time, states ...uint32) (time.Duration, error) {
	var worst time.Duration
	for _, arm := range m.g.arms {
		e, err := wait(arm, head, states...)
		if err != nil {
			return 0, err
		}
		m.state[arm] = [2]uint32{e.A, e.B}
		worst = max(worst, time.Duration(wallUS(arm, e.DeviceUS)-at.UnixMicro())*time.Microsecond)
	}
	return worst, nil
}

// check sends notify-send notifications on the bus and reads both panels
// back: two popups (one with a long body and characters outside ASCII), the
// list (as the host shows it), hidden, and a popup that expires by itself.
func (m *mirror) check() error {
	popup := func(args ...string) (uint32, error) {
		if err := m.notifySend(args...); err != nil {
			return 0, err
		}
		head, at, err := m.nextNote()
		if err != nil {
			return 0, err
		}
		d, err := m.shown(head, at, stPopup)
		if err != nil {
			return 0, err
		}
		for _, arm := range m.g.arms {
			if err := m.g.checkPopup(arm); err != nil {
				return 0, err
			}
		}
		fmt.Printf("check: %q shown on both panels %.0f ms after the bus delivered it (held back %v, pushed with %s), read back as drawn here\n",
			args[len(args)-2], d.Seconds()*1000, m.c.holdback, m.pushed)
		return head, nil
	}
	if m.c.idle {
		deadline := time.Now().Add(20 * time.Second)
		for !m.idle() && time.Now().Before(deadline) {
			time.Sleep(200 * time.Millisecond)
		}
		for _, arm := range m.g.arms {
			s := arm.State()
			fmt.Printf("check: %s arm link: interval %v, idle profile asked %v\n", arm, s.Interval, s.Idle)
		}
	}
	if _, err := popup("-a", "Thunderbird", "Test", "Hello"); err != nil {
		return err
	}
	if _, err := popup("-a", "g1 check", "Build finished", "make: all 42 targets are up to date"); err != nil {
		return err
	}
	if _, err := popup("-a", "Mail", "Café € 12,50 — paid", "A body long enough to need wrapping over two lines, "+
		"so the second one ends in dots because it cannot hold all of this text…"); err != nil {
		return err
	}
	for _, arm := range m.g.arms {
		arm.Raise(scouterkit.TriggerUser1)
	}
	if _, err := m.shown(m.g.head, time.Now(), stHostList); err != nil {
		return err
	}
	for _, arm := range m.g.arms {
		if err := m.g.checkList(arm); err != nil {
			return err
		}
		arm.Raise(scouterkit.TriggerUser2)
	}
	for _, arm := range m.g.arms {
		if _, err := wait(arm, 0, stHidden); err != nil {
			return err
		}
		if err := m.g.hidden(arm); err != nil {
			return err
		}
	}
	fmt.Println("check: the list of both read back on both panels; hidden with the panels", m.g.hiddenAs())
	head, err := popup("-a", "g1 check", "-t", "1500", "Short one", "gone in 1.5 s")
	if err != nil {
		return err
	}
	t0 := time.Now()
	for _, arm := range m.g.arms {
		if _, err := wait(arm, head, stHidden); err != nil {
			return err
		}
		if err := m.g.hidden(arm); err != nil {
			return err
		}
	}
	fmt.Printf("check: it hid by itself %.1f s later, panels %s\n", time.Since(t0).Seconds(), m.g.hiddenAs())
	stop, done := make(chan struct{}), make(chan error, 1)
	go func() { done <- m.loop(stop) }()
	r, err := viaMirror(m.c.socket, request{Summary: "g1 notify", Body: "through the socket", TimeoutMS: 5000})
	close(stop)
	if err := <-done; err != nil {
		return err
	}
	if err != nil {
		return fmt.Errorf("g1 notify through the socket: %w", err)
	}
	for _, arm := range m.g.arms {
		if err := m.g.checkPopup(arm); err != nil {
			return err
		}
	}
	fmt.Printf("check: g1 notify through the socket shown %.0f ms after the mirror got it, read back\n", r.MS)
	demo.Status(m.g.arms)
	return nil
}

// idle says whether every link has gone to its idle profile.
func (m *mirror) idle() bool {
	for _, arm := range m.g.arms {
		if s := arm.State(); !s.Idle || s.Interval < 40*time.Millisecond {
			return false
		}
	}
	return true
}
