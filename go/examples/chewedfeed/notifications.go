package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"html"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

const notificationDuration = 5 * time.Second

type notification struct {
	App, Summary, Body string
}

var (
	notificationTags   = regexp.MustCompile(`<[^>]*>`)
	notificationBreaks = regexp.MustCompile(`(?i)<br\s*/?>|</p\s*>`)
)

func notificationText(s string) string {
	runes := []rune(s)
	if len(runes) > 8192 {
		s = string(runes[:8192]) + "..."
	}
	s = notificationBreaks.ReplaceAllString(s, "\n")
	s = html.UnescapeString(notificationTags.ReplaceAllString(s, ""))
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		lines = append(lines, value(line))
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// busctl performs the same passive BecomeMonitor subscription as the g1 mirror
// example. Its structured output avoids adding a second D-Bus wire codec here.
func decodeNotification(line []byte) (notification, bool) {
	var m struct {
		Type, Interface, Member, Path string
		Payload                       struct {
			Type string
			Data []json.RawMessage
		}
	}
	if json.Unmarshal(line, &m) != nil || m.Type != "method_call" ||
		m.Interface != "org.freedesktop.Notifications" || m.Member != "Notify" ||
		m.Path != "/org/freedesktop/Notifications" || m.Payload.Type != "susssasa{sv}i" || len(m.Payload.Data) != 8 {
		return notification{}, false
	}
	var n notification
	for i, dest := range map[int]*string{0: &n.App, 3: &n.Summary, 4: &n.Body} {
		if json.Unmarshal(m.Payload.Data[i], dest) != nil {
			return notification{}, false
		}
		*dest = notificationText(*dest)
	}
	n.App = value(n.App)
	return n, n.Summary != "" || n.Body != ""
}

func startNotifications(ctx context.Context, address string) (<-chan notification, <-chan error, error) {
	args := []string{"--json=short", "--no-pager",
		"--match=type='method_call',interface='org.freedesktop.Notifications',member='Notify'"}
	if address == "" {
		args = append(args, "--user")
	} else {
		args = append(args, "--address="+address)
	}
	args = append(args, "monitor")
	cmd := exec.CommandContext(ctx, "busctl", args...)
	cmd.Stderr = io.Discard
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, errors.New("cannot start busctl; desktop notification monitoring unavailable")
	}
	notes := make(chan notification, 8)
	errorsOut := make(chan error, 1)
	go func() {
		defer close(notes)
		defer close(errorsOut)
		defer func() {
			cmd.Process.Kill()
			cmd.Wait()
		}()
		scanner := bufio.NewScanner(stdout)
		// Image hints can make a Notify much larger than its displayed text.
		scanner.Buffer(make([]byte, 4096), 16<<20)
		for scanner.Scan() {
			n, ok := decodeNotification(scanner.Bytes())
			if !ok {
				continue
			}
			select {
			case notes <- n:
			case <-ctx.Done():
				return
			}
		}
		if ctx.Err() == nil {
			errorsOut <- errors.New("desktop notification monitor stopped; check session-bus access and monitoring permissions")
		}
	}()
	return notes, errorsOut, nil
}

type notificationPopup struct {
	note  notification
	until time.Time
}

func (p *notificationPopup) active() bool { return !p.until.IsZero() }

func (p *notificationPopup) show(n notification, now time.Time) {
	p.note, p.until = n, now.Add(notificationDuration)
}

func (p *notificationPopup) expire(now time.Time) bool {
	if p.active() && !now.Before(p.until) {
		*p = notificationPopup{}
		return true
	}
	return false
}

func notificationLines(n notification, width, rows int, measure func(string) int) []string {
	var lines []string
	text := n.Summary
	if n.Body != "" {
		if text != "" {
			text += "\n"
		}
		text += n.Body
	}
	for _, paragraph := range strings.Split(text, "\n") {
		lines = append(lines, wrap(paragraph, width, measure)...)
		if len(lines) > rows {
			break
		}
	}
	if len(lines) > rows {
		lines = lines[:rows]
		lines[rows-1] = fitLine(lines[rows-1]+"...", width, measure)
	}
	return lines
}
