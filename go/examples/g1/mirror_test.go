//go:build emulator

package main

import (
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"scouterkit"
	"scouterkit/examples/internal/demo"
)

func testConfig(t *testing.T, bus string) mirrorConfig {
	return mirrorConfig{bus: bus, popup: 5 * time.Second, longest: 15 * time.Second, quietFrom: -1, quietTo: -1, probe: time.Second,
		socket: filepath.Join(t.TempDir(), "g1.sock"),
		view:   viewConfig{angle: 20, hysteresis: 5, hold: 300 * time.Millisecond, listFor: time.Second, icons: true}}
}

func start(t *testing.T, c mirrorConfig) *mirror {
	t.Helper()
	cfg := scouterkit.DefaultConfig()
	if err := scouterkit.Init(cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(scouterkit.Deinit)
	arms := demo.Present()
	for _, arm := range arms {
		arm.SetEmulatedProperty(scouterkit.PropPitch, 0)
	}
	m, err := newMirror(cfg, arms, c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.close)
	return m
}

// save writes the emulated panels as PNGs when G1_PNG names a directory.
func save(t *testing.T, name string) {
	dir := os.Getenv("G1_PNG")
	if dir == "" {
		return
	}
	for _, arm := range demo.Present() {
		f, err := os.Create(filepath.Join(dir, name+"-"+arm.String()+".png"))
		if err != nil {
			t.Fatal(err)
		}
		png.Encode(f, arm.EmulatedPanel())
		f.Close()
	}
}

// Serving on a private bus: the mirror's own check (notify-send popups read
// back, the list, hidden, a popup that expires), with the default hold-back.
func TestServeCheck(t *testing.T) {
	c := testConfig(t, privateBus(t))
	c.serve = true
	c.holdback = 500 * time.Millisecond
	m := start(t, c)
	if err := m.check(); err != nil {
		t.Fatal(err)
	}
}

func pitch(m *mirror, deg int32) {
	for _, arm := range m.g.arms {
		arm.SetEmulatedProperty(scouterkit.PropPitch, uint32(deg))
	}
}

func expect(t *testing.T, m *mirror, head uint32, states ...uint32) {
	t.Helper()
	if _, err := m.shown(head, time.Now(), states...); err != nil {
		t.Fatal(err)
	}
}

// Watching another daemon: notify-send's notifications pop up; one that
// replaces an earlier one (notify-send -r) takes its slot; the head up
// shows the list, down hides it after the hold; the list also hides after
// listFor with the head up and comes back only after it went down; a
// notification the desktop closes leaves the glasses too.
func TestMonitorTilt(t *testing.T) {
	bus := privateBus(t)
	desktop := make(chan event, 64)
	d, err := serve(bus, false, false, 30*time.Second, desktop)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	m := start(t, testConfig(t, bus))
	send := func(args ...string) uint32 {
		t.Helper()
		if err := m.notifySend(args...); err != nil {
			t.Fatal(err)
		}
		head, _, err := m.nextNote()
		if err != nil {
			t.Fatal(err)
		}
		return head
	}
	first := send("-a", "Mail", "-t", "10000", "Two new messages", "From: Ana, Ben")
	expect(t, m, first, stPopup)
	save(t, "popup")
	for _, arm := range m.g.arms {
		if err := m.g.checkPopup(arm); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		send("-a", "build", "-t", "10000", "Step "+strconv.Itoa(i), "done")
	}
	select {
	case e := <-m.events:
		m.handle(e)
	case <-time.After(time.Second):
	}
	head := m.g.head
	if m.g.count != 4 {
		t.Fatalf("%d notifications in the ring", m.g.count)
	}
	replaced := send("-a", "build", "-r", "4", "-t", "10000", "Step 2, updated", "all done")
	if replaced != head || m.g.count != 4 {
		t.Fatalf("the replacement went to %d (count %d), not in place of %d", replaced, m.g.count, head)
	}
	expect(t, m, head, stPopup)
	pitch(m, 30)
	expect(t, m, head, stList)
	save(t, "list")
	for _, arm := range m.g.arms {
		if err := m.g.checkList(arm); err != nil {
			t.Fatal(err)
		}
	}
	pitch(m, 10)
	expect(t, m, head, stHold)
	expect(t, m, head, stHidden)
	pitch(m, 25)
	expect(t, m, head, stList)
	expect(t, m, head, stDismissed)
	for _, arm := range m.g.arms {
		if err := m.g.hidden(arm); err != nil {
			t.Fatal(err)
		}
	}
	pitch(m, 0)
	expect(t, m, head, stHidden)
	pitch(m, 40)
	expect(t, m, head, stList)
	pitch(m, 0)
	expect(t, m, head, stHold)
	expect(t, m, head, stHidden)

	last := send("-a", "chat", "-t", "10000", "Ping", "are you there?")
	expect(t, m, last, stPopup)
	for {
		e := <-desktop
		if e.note != nil && e.note.Summary == "Ping" {
			d.closed(e.note.ID, 2)
			break
		}
	}
	for closed := false; !closed; {
		select {
		case e := <-m.events:
			m.handle(e)
			closed = e.closed != 0
		case <-time.After(5 * time.Second):
			t.Fatal("no NotificationClosed")
		}
	}
	expect(t, m, last, stHidden)
}

// g1 notify through the mirror's socket: shown, latency reported, read
// back; a request while no mirror listens says so.
func TestNotifySocket(t *testing.T) {
	c := testConfig(t, privateBus(t))
	c.serve = true
	m := start(t, c)
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- m.loop(stop) }()
	r, err := viaMirror(c.socket, request{Summary: "Hello", Body: "from g1 notify", TimeoutMS: 2000})
	if err != nil || !r.Shown {
		t.Fatalf("%+v, %v", r, err)
	}
	t.Logf("shown %.0f ms after the mirror got it", r.MS)
	close(stop)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for _, arm := range m.g.arms {
		if err := m.g.checkPopup(arm); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := viaMirror(filepath.Join(t.TempDir(), "none.sock"), request{Summary: "x"}); err != errNoMirror {
		t.Fatalf("no mirror: %v", err)
	}
}

// The layout: the title line with the time and app, the body over two
// lines, the second cut with dots; long words break.
func TestLines(t *testing.T) {
	m := start(t, testConfig(t, privateBus(t)))
	at := time.Date(2026, 10, 27, 9, 5, 0, 0, time.Local)
	n := &note{App: "Mail", Summary: "Re: plans", At: at, Body: strings.Repeat("word ", 30) +
		"Supercalifragilisticexpialidociousandmore"}
	l := m.g.lines(n)
	room := m.g.faces[m.g.arms[0]].w - 2*m.g.faces[m.g.arms[0]].pad
	for i, gl := range l {
		if w := m.g.width(gl); w > room || len(gl) == 0 {
			t.Errorf("line %d: %d glyphs, %d px of %d", i, len(gl), w, room)
		}
	}
	if got := m.g.glyphs("09:05 Mail: Re: plans"); !same(l[0], got) {
		t.Errorf("title %v, want %v", l[0], got)
	}
	dots := m.g.glyphs("...")
	if tail := l[2][len(l[2])-3:]; !same(tail, dots) {
		t.Errorf("the last line does not end in dots")
	}
	n.Body = "short"
	if l := m.g.lines(n); len(l[2]) != 0 || !same(l[1], m.g.glyphs("short")) {
		t.Errorf("short body: %v", l)
	}
}

func same(a, b []uint32) bool {
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

// New notifications while the list is up redraw it (newest on top), during
// the hold too; one while the list is dismissed with the head still up pops
// up and, the head being up, brings the list back.
func TestNewWhileShown(t *testing.T) {
	c := testConfig(t, privateBus(t))
	c.serve = true
	c.view.hold, c.view.listFor = 600*time.Millisecond, 1500*time.Millisecond
	m := start(t, c)
	push := func(summary string) uint32 {
		t.Helper()
		if err := m.notifySend("-a", "t", "-t", "10000", summary, "body of "+summary); err != nil {
			t.Fatal(err)
		}
		head, _, err := m.nextNote()
		if err != nil {
			t.Fatal(err)
		}
		return head
	}
	h := push("one")
	expect(t, m, h, stPopup)
	pitch(m, 30)
	expect(t, m, h, stList)
	h = push("two")
	expect(t, m, h, stList)
	for _, arm := range m.g.arms {
		if err := m.g.checkList(arm); err != nil {
			t.Fatal(err)
		}
	}
	pitch(m, 5)
	expect(t, m, h, stHold)
	h = push("three")
	expect(t, m, h, stHold)
	for _, arm := range m.g.arms {
		if err := m.g.checkList(arm); err != nil {
			t.Fatal(err)
		}
	}
	expect(t, m, h, stHidden)
	pitch(m, 30)
	expect(t, m, h, stList)
	expect(t, m, h, stDismissed)
	h = push("four")
	expect(t, m, h, stPopup)
	expect(t, m, h, stList)
	for _, arm := range m.g.arms {
		if err := m.g.checkList(arm); err != nil {
			t.Fatal(err)
		}
	}
	if err := demo.Halted(m.g.arms); err != nil {
		t.Fatal(err)
	}
}

// app calls Notify and CloseNotification on the bus, as a desktop app does.
type app struct {
	t *testing.T
	b *bus
}

func newApp(t *testing.T, address string) *app {
	b, err := dialBus(address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	return &app{t, b}
}

func (a *app) notify(name string, replaces uint32, summary, body string) uint32 {
	a.t.Helper()
	m, err := a.b.call(notifyName, notifyPath, notifyName, "Notify", "susssasa{sv}i", name, replaces, "",
		summary, body, []any{}, []any{}, int32(10000))
	if err != nil {
		a.t.Fatal(err)
	}
	id, _ := m.arg(0).(uint32)
	return id
}

func (a *app) close(id uint32) {
	a.t.Helper()
	if _, err := a.b.call(notifyName, notifyPath, notifyName, "CloseNotification", "u", id); err != nil {
		a.t.Fatal(err)
	}
}

// pump handles the mirror's events and hold-backs for d.
func pump(m *mirror, d time.Duration) {
	end := time.After(d)
	for {
		select {
		case e := <-m.events:
			m.handle(e)
		case now := <-m.due():
			m.release(now)
		case <-end:
			return
		}
	}
}

// The hold-back, serving and watching another daemon: a notification shows
// 500 ms after it arrived; one closed or replaced within that time never
// shows (the replacement does, as a new one); one closed later hides.
func TestHoldBack(t *testing.T) {
	for _, serving := range []bool{true, false} {
		t.Run(map[bool]string{true: "serve", false: "monitor"}[serving], func(t *testing.T) {
			bus := privateBus(t)
			c := testConfig(t, bus)
			c.holdback = 500 * time.Millisecond
			c.serve = serving
			if !serving {
				d, err := serve(bus, false, false, 30*time.Second, make(chan event, 64))
				if err != nil {
					t.Fatal(err)
				}
				defer d.Close()
			}
			m := start(t, c)
			a := newApp(t, bus)
			id := a.notify("Thunderbird", 0, "First", "shown after the hold-back")
			head, at, err := m.nextNote()
			if err != nil {
				t.Fatal(err)
			}
			if d := time.Since(at); d < c.holdback {
				t.Fatalf("pushed %v after it arrived", d)
			}
			expect(t, m, head, stPopup)
			save(t, "icon-popup")
			for _, arm := range m.g.arms {
				if err := m.g.checkPopup(arm); err != nil {
					t.Fatal(err)
				}
			}
			gone := a.notify("Signal", 0, "Gone", "closed at once")
			a.close(gone)
			first := a.notify("make", 0, "Step 1", "replaced at once")
			a.notify("make", first, "Step 2", "the one shown")
			next, _, err := m.nextNote()
			if err != nil {
				t.Fatal(err)
			}
			pump(m, 800*time.Millisecond)
			if next != head+1 || m.g.head != next || m.g.count != 2 {
				t.Fatalf("head %d (was %d), %d in the ring: a withdrawn one showed", m.g.head, head, m.g.count)
			}
			if body := m.g.ring[next%slots][1]; !same(body, m.g.glyphs("the one shown")) {
				t.Fatalf("the replacement is not the one shown")
			}
			expect(t, m, next, stPopup)
			for _, arm := range m.g.arms {
				if err := m.g.checkPopup(arm); err != nil {
					t.Fatal(err)
				}
			}
			a.close(id)
			pump(m, 300*time.Millisecond)
			if _, ok := m.g.slotOf(id); !ok {
				t.Fatalf("the first one left the ring")
			}
			if err := demo.Halted(m.g.arms); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// followGone handles the mirror's events until its notification daemon
// changes, and follows it.
func followGone(t *testing.T, m *mirror) {
	t.Helper()
	for {
		select {
		case e := <-m.events:
			if e.gone {
				if err := m.follow(); err != nil {
					t.Fatal(err)
				}
				return
			}
			m.handle(e)
		case <-time.After(5 * time.Second):
			t.Fatal("the mirror did not see its daemon change")
		}
	}
}

func popupOf(t *testing.T, m *mirror, args ...string) {
	t.Helper()
	if err := m.notifySend(args...); err != nil {
		t.Fatal(err)
	}
	head, _, err := m.nextNote()
	if err != nil {
		t.Fatal(err)
	}
	expect(t, m, head, stPopup)
	for _, arm := range m.g.arms {
		if err := m.g.checkPopup(arm); err != nil {
			t.Fatal(err)
		}
	}
}

// As on a desktop without its graphical session: the bus can only try to
// start a notification daemon that never comes (a Notify would hang and
// reach no monitor), so g1 mirror is the daemon, and notify-send works.
func TestNoDaemon(t *testing.T) {
	m := start(t, testConfig(t, busWith(t, "/bin/sleep 5")))
	popupOf(t, m, "-a", "Thunderbird", "Test", "Hello")
}

// The daemon g1 mirror watches leaves: g1 mirror becomes the daemon. One
// that takes the name over then is watched again.
func TestDaemonChanges(t *testing.T) {
	bus := privateBus(t)
	desktop, err := serve(bus, false, false, 30*time.Second, make(chan event, 64))
	if err != nil {
		t.Fatal(err)
	}
	m := start(t, testConfig(t, bus))
	popupOf(t, m, "-a", "Mail", "Watched", "the desktop's daemon has it")
	desktop.Close()
	followGone(t, m)
	popupOf(t, m, "-a", "Mail", "Served", "g1 mirror has it")
	again, err := serve(bus, true, false, 30*time.Second, make(chan event, 64))
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	followGone(t, m)
	popupOf(t, m, "-a", "Mail", "Watched again", "the new daemon has it")
}
