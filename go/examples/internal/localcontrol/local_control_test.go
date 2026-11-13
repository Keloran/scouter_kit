//go:build linux

package localcontrol

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fake is a controller whose release takes releaseFor and stays until resume.
type fake struct {
	mu         sync.Mutex
	released   bool
	pinned     bool
	releaseFor time.Duration
	releases   int
}

func (f *fake) Status() Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	owner := "this"
	if f.released {
		owner = "none"
	}
	return Status{Role: "laptop", Owner: owner, Phase: map[bool]string{false: "active", true: "idle"}[f.released],
		Eligible: !f.released, Released: f.released, Pinned: f.pinned, Left: !f.released, Right: !f.released}
}

func (f *fake) Release(ctx context.Context) (bool, error) {
	done := make(chan struct{})
	go func() { // the release goes on even if the request's time runs out
		time.Sleep(f.releaseFor)
		f.mu.Lock()
		f.released = true
		f.releases++
		f.mu.Unlock()
		close(done)
	}()
	select {
	case <-done:
		return true, nil
	case <-ctx.Done():
		return false, nil
	}
}

func (f *fake) Resume(ctx context.Context) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.released = false
	return true, nil
}

func (f *fake) Pin(ctx context.Context, on bool) (bool, error) {
	return false, errors.New("pin is a phone preference")
}

func start(t *testing.T, f *fake) (*Server, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "run", "control.sock")
	s, err := Listen(path, f)
	if err != nil {
		t.Fatal(err)
	}
	go s.Serve()
	t.Cleanup(func() { s.Close() })
	return s, path
}

func do(t *testing.T, path string, req Request) Response {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	resp, err := Do(ctx, path, req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := Listen(filepath.Join(dir, "control.sock"), &fake{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for path, want := range map[string]os.FileMode{dir: 0o700, s.Path(): 0o600, s.Path() + ".lock": 0o600} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != want {
			t.Errorf("%s: mode %o, want %o", filepath.Base(path), fi.Mode().Perm(), want)
		}
	}
	// A symlink in place of the directory is refused.
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Listen(filepath.Join(link, "control.sock"), &fake{}); err == nil {
		t.Error("listened through a symlinked directory")
	}
}

func TestStatusReleaseResume(t *testing.T) {
	f := &fake{}
	_, path := start(t, f)
	if r := do(t, path, Request{Command: CmdStatus}); r.Result != ResultDone || r.Status == nil || r.Status.Owner != "this" || !r.Status.Left {
		t.Fatalf("status: %+v", r)
	}
	r := do(t, path, Request{Command: CmdRelease})
	if r.Result != ResultDone || !r.Status.Released || r.Status.Owner != "none" {
		t.Fatalf("release: %+v %+v", r, r.Status)
	}
	// Released stays released until resume.
	if r := do(t, path, Request{Command: CmdStatus}); !r.Status.Released {
		t.Fatal("not released any more without a resume")
	}
	if r := do(t, path, Request{Command: CmdResume}); r.Result != ResultDone || r.Status.Released {
		t.Fatalf("resume: %+v", r)
	}
	if r := do(t, path, Request{Command: CmdPin, Pin: true}); r.Result != ResultRefused || r.Error == "" {
		t.Fatalf("pin on a laptop: %+v", r)
	}
}

func TestLongReleaseIsPending(t *testing.T) {
	f := &fake{releaseFor: 300 * time.Millisecond}
	s, path := start(t, f)
	s.timeout = 50 * time.Millisecond
	r := do(t, path, Request{Command: CmdRelease})
	if r.Result != ResultPending || r.Status.Released {
		t.Fatalf("a release still in progress was reported as %+v", r)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !do(t, path, Request{Command: CmdStatus}).Status.Released {
		if time.Now().After(deadline) {
			t.Fatal("the pending release never completed")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestCancelledClient(t *testing.T) {
	f := &fake{releaseFor: 300 * time.Millisecond}
	_, path := start(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := Do(ctx, path, Request{Command: CmdRelease}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancelled client: %v", err)
	}
	// The server finished the release and still answers.
	time.Sleep(400 * time.Millisecond)
	if r := do(t, path, Request{Command: CmdStatus}); !r.Status.Released {
		t.Fatalf("after a cancelled client: %+v", r.Status)
	}
}

func TestConcurrentClients(t *testing.T) {
	f := &fake{releaseFor: 20 * time.Millisecond}
	_, path := start(t, f)
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := Request{Command: []string{CmdStatus, CmdRelease, CmdResume}[i%3]}
			r, err := Do(context.Background(), path, req)
			if err == nil && r.Result != ResultDone {
				err = errors.New(r.Result + " " + r.Error)
			}
			if err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func raw(t *testing.T, path, send string, closeWrite bool) string {
	t.Helper()
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	c.Write([]byte(send))
	if closeWrite {
		c.(*net.UnixConn).CloseWrite()
	}
	b := make([]byte, 4096)
	n, _ := c.Read(b)
	return string(b[:n])
}

func TestMalformedRequests(t *testing.T) {
	f := &fake{}
	_, path := start(t, f)
	for name, tc := range map[string]struct {
		send       string
		closeWrite bool
		want       string
	}{
		"garbage":         {"not json\n", false, "malformed request"},
		"unknown field":   {`{"command":"status","token":"x"}` + "\n", false, "malformed request"},
		"unknown command": {`{"command":"reboot"}` + "\n", false, "unknown command"},
		"too long":        {`{"command":"status","pad":"` + strings.Repeat("a", 2000) + `"}` + "\n", false, "request too long"},
		"no newline":      {`{"command":"release"}`, true, "incomplete request"},
	} {
		got := raw(t, path, tc.send, tc.closeWrite)
		if !strings.Contains(got, `"result":"error"`) || !strings.Contains(got, tc.want) {
			t.Errorf("%s: %q", name, got)
		}
	}
	// A silent client is cut off after the read timeout, and nothing changed.
	start := time.Now()
	if got := raw(t, path, "", false); !strings.Contains(got, "incomplete request") || time.Since(start) > 4*time.Second {
		t.Errorf("silent client: %q after %s", got, time.Since(start))
	}
	if f.releases != 0 {
		t.Fatal("a malformed request released the glasses")
	}
}

func TestSecondInstanceAndStaleSocket(t *testing.T) {
	f := &fake{}
	s, path := start(t, f)
	if _, err := Listen(path, &fake{}); !errors.Is(err, ErrRunning) {
		t.Fatalf("second instance: %v", err)
	}
	// The first one's socket was left alone.
	if r := do(t, path, Request{Command: CmdStatus}); r.Result != ResultDone {
		t.Fatalf("first instance after the second tried: %+v", r)
	}
	s.Close()
	s.Close() // idempotent
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket left after Close: %v", err)
	}

	// A dead process's socket (its lock gone with it) is replaced.
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	ln.SetUnlinkOnClose(false)
	ln.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatal("no stale socket to recover from")
	}
	s2, err := Listen(path, f)
	if err != nil {
		t.Fatalf("stale socket: %v", err)
	}
	go s2.Serve()
	defer s2.Close()
	if r := do(t, path, Request{Command: CmdStatus}); r.Result != ResultDone {
		t.Fatalf("after stale recovery: %+v", r)
	}
}

func TestNotRunning(t *testing.T) {
	_, err := Do(context.Background(), filepath.Join(t.TempDir(), "control.sock"), Request{Command: CmdStatus})
	if err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("no server: %v", err)
	}
}
