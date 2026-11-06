package main

import (
	"bufio"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

const notificationJSON = `{"type":"method_call","path":"/org/freedesktop/Notifications","interface":"org.freedesktop.Notifications","member":"Notify","payload":{"type":"susssasa{sv}i","data":["Demo",0,"","<b>Build</b> &amp; test","Line one<br/>Line two&#27;",[],{},5000]}}`

func TestNotificationParsing(t *testing.T) {
	n, ok := decodeNotification([]byte(notificationJSON))
	if !ok || n.App != "Demo" || n.Summary != "Build & test" || n.Body != "Line one\nLine two" {
		t.Fatalf("notification was not sanitized: %#v, %t", n, ok)
	}
	for _, invalid := range []string{
		`not JSON`,
		strings.Replace(notificationJSON, `"method_call"`, `"signal"`, 1),
		strings.Replace(notificationJSON, `"member":"Notify"`, `"member":"Other"`, 1),
		strings.Replace(notificationJSON, `"Demo"`, `42`, 1),
		strings.Replace(notificationJSON, `"susssasa{sv}i"`, `"s"`, 1),
	} {
		if _, ok := decodeNotification([]byte(invalid)); ok {
			t.Fatalf("accepted invalid or unrelated notification: %s", invalid)
		}
	}
}

func TestNotificationPopupFiveSecondsAndReplacement(t *testing.T) {
	now := time.Unix(100, 0)
	var p notificationPopup
	p.show(notification{Summary: "First"}, now)
	if p.expire(now.Add(5*time.Second-time.Nanosecond)) || !p.active() {
		t.Fatal("popup expired before five seconds")
	}
	p.show(notification{Summary: "Second"}, now.Add(4*time.Second))
	if p.expire(now.Add(5*time.Second)) || p.note.Summary != "Second" {
		t.Fatal("replacement did not receive its own five seconds")
	}
	if !p.expire(now.Add(9*time.Second)) || p.active() || p.note.Summary != "" {
		t.Fatal("popup was not dismissed and cleared at its deadline")
	}
}

func TestNotificationTextUsesAvailableRows(t *testing.T) {
	n := notification{Summary: "Build finished", Body: strings.Repeat("Detailed body text 世界 ", 100)}
	lines := notificationLines(n, 25, 11, utf8.RuneCountInString)
	if len(lines) != 11 || lines[0] != "Build finished" || !strings.HasSuffix(lines[len(lines)-1], "...") {
		t.Fatalf("popup did not fill its space or indicate omitted text: %#v", lines)
	}
	for _, line := range lines {
		if utf8.RuneCountInString(line) > 25 || !utf8.ValidString(line) {
			t.Fatalf("notification overflow or damaged UTF-8: %q", line)
		}
	}
}

func notificationTestBus(t *testing.T) string {
	t.Helper()
	for _, tool := range []string{"dbus-daemon", "busctl"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " is not installed")
		}
	}
	dir := t.TempDir()
	address := "unix:path=" + filepath.Join(dir, "bus")
	config := `<busconfig><type>session</type><listen>` + address + `</listen>
<policy context="default"><allow send_destination="*" eavesdrop="true"/><allow eavesdrop="true"/><allow own="*"/></policy></busconfig>`
	path := filepath.Join(dir, "bus.conf")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("dbus-daemon", "--config-file="+path, "--nofork", "--nopidfile", "--print-address=1")
	cmd.Stderr = io.Discard
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	if _, err := bufio.NewReader(stdout).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	return address
}

func TestDesktopNotificationMonitorOnPrivateBus(t *testing.T) {
	address := notificationTestBus(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	notes, failures, err := startNotifications(ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case n, ok := <-notes:
			if !ok {
				t.Fatal("monitor closed without delivering the test notification")
			}
			if n.App != "Demo" || n.Summary != "Test summary" || n.Body != "Test body" {
				t.Fatalf("unexpected notification: %#v", n)
			}
			cancel()
			stopped := time.NewTimer(time.Second)
			defer stopped.Stop()
			for {
				select {
				case _, ok := <-notes:
					if !ok {
						return
					}
				case <-stopped.C:
					t.Fatal("monitor did not stop on cancellation")
				}
			}
		case failure := <-failures:
			t.Fatalf("monitor failed: %v", failure)
		case <-ctx.Done():
			t.Fatal("monitor did not receive the synthetic notification")
		case <-tick.C:
			// No daemon is activated on this bus: this synthetic call is only observed.
			cmd := exec.CommandContext(ctx, "busctl", "--address="+address, "--expect-reply=no", "--", "call",
				"org.freedesktop.Notifications", "/org/freedesktop/Notifications", "org.freedesktop.Notifications", "Notify",
				"susssasa{sv}i", "Demo", "0", "", "Test summary", "Test body", "0", "0", "5000")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("synthetic notification: %v: %s", err, out)
			}
		}
	}
}
