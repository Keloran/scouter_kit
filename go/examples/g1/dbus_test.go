package main

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCodec(t *testing.T) {
	in := &message{Type: msgCall, Serial: 7, Path: "/a/b", Interface: "x.y", Member: "M", Destination: "z.w",
		Signature: "yisa{sv}(ub)asxdo", Body: []any{byte(3), int32(-5), "héllo",
			[]any{[]any{"k", variant{"y", byte(2)}}, []any{"t", variant{"as", []any{"p", "q"}}}},
			[]any{uint32(9), true}, []any{}, int64(-1 << 40), 0.5, objectPath("/o")}}
	r := &bus{r: bufio.NewReader(strings.NewReader(string(in.encode())))}
	got, err := r.read()
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != in.Path || got.Interface != in.Interface || got.Member != in.Member || got.Serial != 7 ||
		got.Destination != in.Destination || got.Signature != in.Signature {
		t.Fatalf("header %+v", got)
	}
	if !reflect.DeepEqual(got.Body, in.Body) {
		t.Fatalf("body %#v\nwant %#v", got.Body, in.Body)
	}
}

func TestPlain(t *testing.T) {
	if s := plain(`<b>Build</b> &amp; <a href="x">test</a> done`); s != "Build & test done" {
		t.Fatalf("%q", s)
	}
}

// privateBus runs a session bus of its own for the test, without the
// system's activatable services.
func privateBus(t *testing.T) string { return busWith(t, "") }

// busWith is a private bus on which org.freedesktop.Notifications is
// activatable and starts command ("": nothing is activatable).
func busWith(t *testing.T, command string) string {
	t.Helper()
	for _, tool := range []string{"dbus-daemon", "notify-send"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not installed")
		}
	}
	dir := t.TempDir()
	services := filepath.Join(dir, "services")
	if err := os.Mkdir(services, 0o700); err != nil {
		t.Fatal(err)
	}
	if command != "" {
		service := "[D-BUS Service]\nName=" + notifyName + "\nExec=" + command + "\n"
		if err := os.WriteFile(filepath.Join(services, "notifications.service"), []byte(service), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	addr := "unix:path=" + dir + "/bus"
	conf := `<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN"
 "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig><type>session</type><listen>` + addr + `</listen><servicedir>` + services + `</servicedir>
<policy context="default"><allow send_destination="*" eavesdrop="true"/><allow eavesdrop="true"/><allow own="*"/></policy>
</busconfig>`
	if err := os.WriteFile(filepath.Join(dir, "bus.conf"), []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("dbus-daemon", "--config-file="+filepath.Join(dir, "bus.conf"), "--nofork", "--nopidfile",
		"--print-address=1")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
	})
	if _, err := bufio.NewReader(out).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	return addr
}

func notifySend(t *testing.T, addr string, args ...string) {
	t.Helper()
	cmd := exec.Command("notify-send", args...)
	cmd.Env = append(os.Environ(), "DBUS_SESSION_BUS_ADDRESS="+addr)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("notify-send: %v: %s", err, out)
	}
}

func next(t *testing.T, ch <-chan event) event {
	t.Helper()
	select {
	case e, ok := <-ch:
		if !ok {
			t.Fatal("source closed")
		}
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("no event")
	}
	return event{}
}

// The server takes notify-send's notification, answers with its ID and
// closes it when its time is up; a second server is refused.
func TestServe(t *testing.T) {
	addr := privateBus(t)
	ch := make(chan event, 16)
	s, err := serve(addr, false, false, 5*time.Second, ch)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	notifySend(t, addr, "-a", "test app", "-u", "critical", "-t", "1500", "Title <b>x</b>", "Body &amp; more")
	e := next(t, ch)
	n := e.note
	if n == nil || n.App != "test app" || n.Summary != "Title x" || n.Body != "Body & more" || n.Urgency != 2 ||
		n.Timeout != 1500*time.Millisecond || n.ID != 1 {
		t.Fatalf("%+v", n)
	}
	if _, err := serve(addr, false, false, time.Second, make(chan event, 1)); err == nil {
		t.Fatal("a second server took the name")
	}
}

// The monitor sees notify-send's call to another daemon (here a server of
// ours), the ID that daemon gives it, and the close when it expires; the
// daemon still answers notify-send.
func TestMonitor(t *testing.T) {
	addr := privateBus(t)
	daemon := make(chan event, 16)
	s, err := serve(addr, false, false, time.Second, daemon)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ch := make(chan event, 16)
	b, err := monitor(addr, ch)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	notifySend(t, addr, "-a", "mail", "-t", "800", "New mail", "From: someone")
	e := next(t, ch)
	if e.note == nil || e.note.App != "mail" || e.note.Summary != "New mail" || e.note.Body != "From: someone" ||
		e.note.Timeout != 800*time.Millisecond {
		t.Fatalf("%+v", e.note)
	}
	if e := next(t, ch); e.note == nil || e.id != 1 {
		t.Fatalf("no ID event: %+v", e)
	}
	if e := next(t, ch); e.closed != 1 {
		t.Fatalf("no close: %+v", e)
	}
	if e := next(t, daemon); e.note == nil || e.note.ID != 1 {
		t.Fatalf("the daemon got %+v", e)
	}
}
