package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// record runs the command on a pseudo-terminal, types the keys (each after
// the output went quiet) and returns all the output; status queries are
// answered as term does.
func record(t *testing.T, cols, rows int, keys []string, name string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Env = []string{"TERM=xterm-256color", "HOME=" + t.TempDir(), "PATH=/usr/bin:/bin", "LANG=C.UTF-8"}
	master, err := startPTY(cmd, cols, rows)
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	var mu sync.Mutex
	var out bytes.Buffer
	v := newVT(cols, rows)
	v.reply = func(b []byte) { master.Write(b) }
	got := make(chan struct{}, 1)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			mu.Lock()
			out.Write(buf[:n])
			v.Write(buf[:n])
			mu.Unlock()
			select {
			case got <- struct{}{}:
			default:
			}
			if err != nil {
				return
			}
		}
	}()
	quiet := func() {
		for {
			select {
			case <-got:
			case <-time.After(400 * time.Millisecond):
				return
			}
		}
	}
	// btop samples for about 1.5 s before its first frame, then redraws every second.
	for settle := time.After(2 * time.Second); settle != nil; {
		select {
		case <-got:
		case <-settle:
			settle = nil
		}
	}
	quiet()
	for _, k := range keys {
		master.Write([]byte(k))
		quiet()
	}
	cmd.Process.Kill()
	cmd.Wait()
	mu.Lock()
	defer mu.Unlock()
	return append([]byte(nil), out.Bytes()...)
}

// tmuxScreen is the screen tmux shows after the output.
func tmuxScreen(t *testing.T, out []byte, cols, rows int) []string {
	t.Helper()
	dir := t.TempDir()
	file, sock := filepath.Join(dir, "out"), filepath.Join(dir, "sock")
	if err := os.WriteFile(file, out, 0o600); err != nil {
		t.Fatal(err)
	}
	tmux := func(args ...string) string {
		b, err := exec.Command("tmux", append([]string{"-S", sock, "-f", "/dev/null"}, args...)...).Output()
		if err != nil {
			t.Fatalf("tmux %v: %v", args, err)
		}
		return string(b)
	}
	tmux("new-session", "-d", "-x", fmt.Sprint(cols), "-y", fmt.Sprint(rows),
		"cat "+file+"; tmux -S "+sock+" wait-for -S shown; sleep 60")
	defer exec.Command("tmux", "-S", sock, "kill-server").Run()
	tmux("wait-for", "shown")
	lines := strings.Split(strings.TrimRight(tmux("capture-pane", "-p"), "\n"), "\n")
	for len(lines) < rows {
		lines = append(lines, "")
	}
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	return lines
}

// Real programs' output, read by term's VT and by tmux: the same screens.
func TestAgainstTmux(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil || testing.Short() {
		t.Skip("tmux not installed, or -short")
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "text.txt")
	var b strings.Builder
	for i := 1; i <= 200; i++ {
		fmt.Fprintf(&b, "line %d: %s\n", i, strings.Repeat("abc def ", i%12))
	}
	os.WriteFile(file, []byte(b.String()), 0o600)
	cases := []struct {
		name string
		keys []string
		args []string
	}{
		{"shell", []string{"printf 'a\\tb\\033[1;31mred\\033[m\\n'; seq 1 50\r", "printf '\\033[5;10r\\033[10;1Hx\\n\\n\\033[r'\r",
			"echo done\r"}, []string{"sh"}},
		{"less", []string{" ", "G", "/line 150\r", "g"}, []string{"less", file}},
		{"vi", []string{"ihello world\x1b", ":set nu\r", "yy5p", "gg", "dd"}, []string{"vi", filepath.Join(dir, "v.txt")}},
		{"nano", []string{"first line\rsecond\r", "\x0b", "\x1b[A\x1b[A"}, []string{"nano", filepath.Join(dir, "n.txt")}},
		{"whiptail", nil, []string{"whiptail", "--title", "A box", "--yesno", "Lines in a box?", "10", "40"}},
		{"top", nil, []string{"top", "-d", "0.5"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := exec.LookPath(c.args[0]); err != nil {
				t.Skip(c.args[0] + " not installed")
			}
			for _, size := range [][2]int{{77, 33}, {38, 16}} {
				out := record(t, size[0], size[1], c.keys, c.args[0], c.args[1:]...)
				v := newVT(size[0], size[1])
				v.Write(out)
				got, want := text(v), tmuxScreen(t, out, size[0], size[1])
				for i := range want {
					if got[i] != want[i] {
						t.Errorf("%d x %d, row %d:\n got %q\nwant %q", size[0], size[1], i, got[i], want[i])
					}
				}
			}
		})
	}
}
