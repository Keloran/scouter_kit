//go:build emulator

package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"scouterkit"
	"scouterkit/examples/internal/demo"
)

func sessionOptions() options {
	return options{dx: -10, dy: -8, fontScale: 1.5, refresh: 5 * time.Minute, aiRefresh: 30 * time.Second}
}

// fakeClient answers every API call: projects for /api/projects, a summary otherwise.
func fakeClient(t *testing.T, projects string) *client {
	t.Helper()
	c, err := newClient("https://example.com", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	c.http.Transport = systemTestTransport(func(r *http.Request) (*http.Response, error) {
		body := `{"projects":2,"customers":2,"paid_customers":1}`
		if r.URL.Path == "/api/projects" {
			body = projects
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	return c
}

func TestHostReleaseAcquireRepeatedly(t *testing.T) {
	host := &glassesHost{o: sessionOptions()}
	var screens []*screen
	for i := 1; i <= 3; i++ {
		if err := host.Acquire(context.Background()); err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
		st := host.Status()
		if st.State != hostActive || st.Generation != uint64(i) || st.Arms != 2 {
			t.Fatalf("acquire %d: status %+v", i, st)
		}
		if err := host.Acquire(context.Background()); err != nil || host.Status().Generation != uint64(i) {
			t.Fatalf("second Acquire on an active session: %v, %+v", err, host.Status())
		}
		s := host.current()
		for _, old := range screens {
			if old == s.screen || old.bitmap == s.screen.bitmap || old.font == s.screen.font {
				t.Fatal("a handle from an earlier session was reused")
			}
		}
		screens = append(screens, s.screen)
		s.screen.draw(page{title: "Session", lines: []string{"one"}, status: "Fetched 12:00"}, 0, 1, false)
		host.Release(nil)
		if st := host.Status(); st.State != hostReleased || st.Err != nil || host.current() != nil {
			t.Fatalf("release %d: status %+v", i, st)
		}
		// Nothing reaches the glasses once released.
		if _, err := scouterkit.Left.Props(scouterkit.PropPanel); err == nil {
			t.Fatal("an arm answered after Release")
		}
	}
	host.Release(nil) // twice is harmless
}

func TestHostAcquireCancelled(t *testing.T) {
	host := &glassesHost{o: sessionOptions()}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := host.Acquire(ctx); err != context.Canceled {
		t.Fatalf("cancelled Acquire: %v", err)
	}
	if st := host.Status(); st.State != hostReleased || st.Generation != 0 || st.Err == nil {
		t.Fatalf("status after a cancelled Acquire: %+v", st)
	}
	// Nothing was left initialized: a new Acquire works.
	if err := host.Acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	host.Release(nil)
}

func TestHostOneArm(t *testing.T) {
	host := &glassesHost{f: demo.Flags{Right: "none"}, o: sessionOptions()}
	if err := host.Acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := host.Status(); st.Arms != 1 || host.current().arms[0] != scouterkit.Left {
		t.Fatalf("one-arm session: %+v", st)
	}
	host.Release(nil)
	if err := host.Acquire(context.Background()); err != nil || host.Status().Arms != 1 {
		t.Fatalf("one-arm reacquire: %v %+v", err, host.Status())
	}
	host.Release(nil)
}

// The page survives a new session; the new session is painted in full.
func TestAttachKeepsPageAndRepaints(t *testing.T) {
	host := &glassesHost{o: sessionOptions()}
	if err := host.Acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer host.Release(nil)
	data := fakeClient(t, `[{"id":"one","slug":"one","name":"One"},{"id":"two","slug":"two","name":"Two"}]`).fetch(context.Background(), nil)
	d := &dashState{nav: newNavigation([]page{{id: "loading", title: "Loading"}}, nil, nil, true), sites: &data}
	d.attach(host.current())
	pages := d.nav.views[viewSites].pages
	if len(pages) < 2 {
		t.Fatalf("want several Sites pages, got %d", len(pages))
	}
	d.nav.move(1)
	want := d.nav.current().pages[d.nav.current().index].id

	host.Release(nil)
	d.s = nil
	if err := host.Acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	d.attach(host.current())
	v := d.nav.current()
	if got := v.pages[v.index].id; got != want {
		t.Fatalf("page after reacquire: %q, want %q", got, want)
	}
	if d.s.screen.status != &d.music {
		t.Fatal("the new screen does not show the dashboard's music line")
	}
	panel := scouterkit.Left.EmulatedPanel()
	lit := 0
	for _, p := range panel.Pix {
		if p != 0 {
			lit++
		}
	}
	if lit == 0 {
		t.Fatal("the new session was not painted")
	}
}

func TestDashboardReleaseResume(t *testing.T) {
	c := fakeClient(t, `[{"id":"one","slug":"one","name":"Site"}]`)
	o := sessionOptions()
	host := &glassesHost{o: o}
	control := make(chan hostCommand)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- dashboard(ctx, host, o, c, control) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("dashboard: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("dashboard did not stop")
		}
		if host.Status().State != hostReleased {
			t.Error("glasses still held after the dashboard stopped")
		}
	}()
	command := func(release bool) {
		t.Helper()
		reply := make(chan error, 1)
		select {
		case control <- hostCommand{release: release, done: reply}:
		case <-time.After(10 * time.Second):
			t.Fatal("dashboard did not take the command")
		}
		if err := <-reply; err != nil {
			t.Fatalf("command (release %t): %v", release, err)
		}
	}
	waitPanel := func(want uint32) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			p, err := scouterkit.Right.Props(scouterkit.PropPanel)
			if err == nil && len(p) == 1 && p[0] == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("panel did not reach power state %d", want)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	for host.Status().Generation == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	waitPanel(1)
	for i := 2; i <= 3; i++ {
		command(true)
		if st := host.Status(); st.State != hostReleased || st.Err != nil {
			t.Fatalf("after release: %+v", st)
		}
		// Data keeps arriving while released; nothing is sent and the loop runs on.
		time.Sleep(2200 * time.Millisecond)
		command(false)
		if st := host.Status(); st.State != hostActive || st.Generation != uint64(i) {
			t.Fatalf("after resume: %+v", st)
		}
		waitPanel(1)
		// Fresh input subscriptions: a case event in the new session turns the dashboard off.
		scouterkit.Right.SetEmulatedProperty(scouterkit.PropInCase, 1)
		waitPanel(0)
		scouterkit.Right.SetEmulatedProperty(scouterkit.PropInCase, 0)
		waitPanel(1)
	}
}

// Every arm unreachable for 5 checks: the dashboard releases the glasses,
// records why, and acquires them again instead of exiting.
func TestDashboardRecoversFromLostArms(t *testing.T) {
	defer func(every, first time.Duration) { hostLinkCheck, reconnectFirst = every, first }(hostLinkCheck, reconnectFirst)
	hostLinkCheck, reconnectFirst = 100*time.Millisecond, 100*time.Millisecond
	o := sessionOptions()
	host := &glassesHost{o: o}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- dashboard(ctx, host, o, fakeClient(t, `[]`), nil) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("dashboard: %v", err)
		}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for host.Status().Generation == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no session")
		}
		time.Sleep(10 * time.Millisecond)
	}
	scouterkit.Left.EmulateDisconnect(5 * time.Second)
	scouterkit.Right.EmulateDisconnect(5 * time.Second)
	sawLoss := false
	for host.Status().Generation < 2 {
		if st := host.Status(); st.State == hostReleased && st.Err != nil {
			sawLoss = true
		}
		select {
		case err := <-done:
			t.Fatalf("dashboard exited: %v", err)
		default:
		}
		if time.Now().After(deadline.Add(10 * time.Second)) {
			t.Fatalf("not reacquired: %+v", host.Status())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !sawLoss {
		t.Error("the loss was not recorded in the status")
	}
	if st := host.Status(); st.State != hostActive || st.Err != nil {
		t.Fatalf("after recovery: %+v", st)
	}
}
