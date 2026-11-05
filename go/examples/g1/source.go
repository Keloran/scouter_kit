package main

import (
	"errors"
	"fmt"
	"html"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// note is a desktop notification (org.freedesktop.Notifications.Notify).
type note struct {
	App, Summary, Body string
	Icon               string        // app_icon: an icon name or a file
	Desktop, Category  string        // the hints desktop-entry and category
	Urgency            byte          // 0 low, 1 normal, 2 critical
	Timeout            time.Duration // the sender's: below 0 the server's default, 0 never
	ID, Replaces       uint32        // the server's ID (0 not known yet), the one it replaces
	At                 time.Time     // when the bus delivered it to us
}

// event is what a source reports: a notification, the ID the server gave
// one (monitor mode), one closed on the desktop, or that the bus's
// notification server is gone (the one watched left, or one replaced g1).
type event struct {
	note   *note
	id     uint32
	closed uint32
	gone   bool
}

const (
	notifyName  = "org.freedesktop.Notifications"
	notifyPath  = "/org/freedesktop/Notifications"
	busName     = "org.freedesktop.DBus"
	busPath     = "/org/freedesktop/DBus"
	unknownCall = "org.freedesktop.DBus.Error.UnknownMethod"
)

var markup = regexp.MustCompile(`<[^>]*>`)

// plain drops the body markup a sender may use (<b>, <a href=...>, ...).
func plain(s string) string {
	return strings.TrimSpace(html.UnescapeString(markup.ReplaceAllString(s, "")))
}

// noteOf reads a Notify call: susssasa{sv}i.
func noteOf(m *message) (*note, error) {
	if m.Signature != "susssasa{sv}i" || len(m.Body) != 8 {
		return nil, fmt.Errorf("Notify with signature %q", m.Signature)
	}
	n := &note{App: m.Body[0].(string), Replaces: m.Body[1].(uint32), Icon: m.Body[2].(string),
		Summary: plain(m.Body[3].(string)), Body: plain(m.Body[4].(string)), Urgency: 1, At: time.Now()}
	for _, h := range m.Body[6].([]any) {
		kv := h.([]any)
		v := kv[1].(variant).Value
		switch k, _ := kv[0].(string); k {
		case "urgency":
			if u, ok := v.(byte); ok {
				n.Urgency = u
			}
		case "desktop-entry":
			n.Desktop, _ = v.(string)
		case "category":
			n.Category, _ = v.(string)
		}
	}
	if t := m.Body[7].(int32); t < 0 {
		n.Timeout = -1
	} else {
		n.Timeout = time.Duration(t) * time.Millisecond
	}
	return n, nil
}

// monitor watches the bus's Notify calls without taking part
// (org.freedesktop.DBus.Monitoring.BecomeMonitor): the desktop's own
// notification daemon goes on as before. It also follows the daemon's
// replies, for the IDs it gives (so a notification that replaces another
// is matched), and NotificationClosed. It returns once monitoring, then
// sends events until the bus closes.
func monitor(address string, out chan<- event) (*bus, error) {
	b, err := dialBus(address)
	if err != nil {
		return nil, err
	}
	rules := []any{
		"type='method_call',interface='" + notifyName + "',member='Notify'",
		"type='signal',interface='" + notifyName + "',member='NotificationClosed'",
		"type='signal',sender='" + busName + "',member='NameOwnerChanged',arg0='" + notifyName + "'",
	}
	if m, err := b.call(busName, busPath, busName, "GetNameOwner", "s", notifyName); err == nil {
		if owner, _ := m.arg(0).(string); owner != "" {
			rules = append(rules, "type='method_return',sender='"+owner+"'")
		}
	}
	if _, err := b.call(busName, busPath, busName+".Monitoring", "BecomeMonitor", "asu", rules, uint32(0)); err != nil {
		b.Close()
		return nil, err
	}
	go func() {
		defer close(out)
		type key struct {
			sender string
			serial uint32
		}
		pending := map[key]*note{}
		for {
			m, err := b.read()
			if err != nil {
				return
			}
			switch {
			case m.Type == msgCall && m.Member == "Notify":
				n, err := noteOf(m)
				if err != nil {
					continue
				}
				for k, p := range pending {
					if time.Since(p.At) > 10*time.Second {
						delete(pending, k)
					}
				}
				pending[key{m.Sender, m.Serial}] = n
				out <- event{note: n}
			case m.Type == msgReturn:
				k := key{m.Destination, m.ReplySerial}
				if n := pending[k]; n != nil {
					delete(pending, k)
					if id, ok := m.arg(0).(uint32); ok {
						out <- event{note: n, id: id}
					}
				}
			case m.Type == msgSignal && m.Member == "NotificationClosed":
				if id, ok := m.arg(0).(uint32); ok {
					out <- event{closed: id}
				}
			case m.Type == msgSignal && m.Member == "NameOwnerChanged" && m.arg(0) == notifyName && m.arg(2) == "":
				out <- event{gone: true}
			}
		}
	}()
	return b, nil
}

// server is the notification daemon of the bus (opt-in, for a desktop
// without one): it owns org.freedesktop.Notifications, shows nothing on
// the screen and hands each notification to the glasses.
type server struct {
	b       *bus
	mu      sync.Mutex
	next    uint32
	timers  map[uint32]*time.Timer
	timeout time.Duration
}

// serve becomes the bus's notification server. With replace it takes the
// name over from a server that allows that; with yield it lets a server that
// asks take it over (it then reports gone).
func serve(address string, replace, yield bool, timeout time.Duration, out chan<- event) (*server, error) {
	b, err := dialBus(address)
	if err != nil {
		return nil, err
	}
	flags := uint32(4)
	if replace {
		flags |= 2
	}
	if yield {
		flags |= 1
	}
	m, err := b.call(busName, busPath, busName, "RequestName", "su", notifyName, flags)
	if err != nil {
		b.Close()
		return nil, err
	}
	if code, _ := m.arg(0).(uint32); code != 1 {
		b.Close()
		return nil, fmt.Errorf("%s has another owner (a notification daemon runs: use the default monitor mode)", notifyName)
	}
	s := &server{b: b, timers: map[uint32]*time.Timer{}, timeout: timeout}
	go func() {
		defer close(out)
		for {
			m, err := b.read()
			if err != nil {
				return
			}
			if m.Type == msgSignal && m.Member == "NameLost" && m.arg(0) == notifyName {
				out <- event{gone: true}
			}
			if m.Type != msgCall {
				continue
			}
			if e, ok := s.handle(m); ok {
				out <- e
			}
		}
	}()
	return s, nil
}

func (s *server) handle(m *message) (event, bool) {
	switch m.Interface + "." + m.Member {
	case notifyName + ".Notify":
		n, err := noteOf(m)
		if err != nil {
			s.b.replyError(m, "org.freedesktop.DBus.Error.InvalidArgs", err.Error())
			return event{}, false
		}
		s.mu.Lock()
		n.ID = n.Replaces
		if n.ID == 0 {
			s.next++
			n.ID = s.next
		}
		s.mu.Unlock()
		s.b.reply(m, "u", n.ID)
		s.expire(n)
		return event{note: n}, true
	case notifyName + ".CloseNotification":
		id, _ := m.arg(0).(uint32)
		s.b.reply(m, "")
		s.closed(id, 3)
		return event{closed: id}, true
	case notifyName + ".GetCapabilities":
		s.b.reply(m, "as", []any{"body"})
	case notifyName + ".GetServerInformation":
		s.b.reply(m, "ssss", "scouterkit", "ScouterKit", "1", "1.2")
	case "org.freedesktop.DBus.Peer.Ping":
		s.b.reply(m, "")
	default:
		s.b.replyError(m, unknownCall, "no method "+m.Member)
	}
	return event{}, false
}

// expire sends NotificationClosed (reason 1) when the note's time is up.
func (s *server) expire(n *note) {
	d := n.Timeout
	if d < 0 {
		d = s.timeout
	}
	if d == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if t := s.timers[n.ID]; t != nil {
		t.Stop()
	}
	s.timers[n.ID] = time.AfterFunc(d, func() { s.closed(n.ID, 1) })
}

func (s *server) closed(id, reason uint32) {
	s.mu.Lock()
	t := s.timers[id]
	delete(s.timers, id)
	s.mu.Unlock()
	if t != nil || reason != 1 {
		if t != nil {
			t.Stop()
		}
		s.b.signal(notifyPath, notifyName, "NotificationClosed", "uu", id, reason)
	}
}

func (s *server) Close() error { return s.b.Close() }

var errNoDaemon = errors.New("no notification daemon runs on the bus")

// daemon returns the unique name of the bus's notification server. If none
// runs it asks the bus to start one and waits at most wait: an activation that
// fails may get no answer at all (the bus waits for the name up to 25 s or
// more, so a Notify meanwhile hangs and never arrives, also not for a monitor).
func daemon(address string, wait time.Duration) (string, error) {
	b, err := dialBus(address)
	if err != nil {
		return "", err
	}
	defer b.Close()
	owner := func() string {
		m, err := b.call(busName, busPath, busName, "GetNameOwner", "s", notifyName)
		if err != nil {
			return ""
		}
		name, _ := m.arg(0).(string)
		return name
	}
	if name := owner(); name != "" {
		return name, nil
	}
	b.conn.SetDeadline(time.Now().Add(wait))
	_, err = b.call(busName, busPath, busName, "StartServiceByName", "su", notifyName, uint32(0))
	switch {
	case errors.Is(err, os.ErrDeadlineExceeded):
		return "", fmt.Errorf("%w (none started within %v)", errNoDaemon, wait)
	case err != nil:
		return "", fmt.Errorf("%w (starting one: %v)", errNoDaemon, err)
	}
	b.conn.SetDeadline(time.Time{})
	if name := owner(); name != "" {
		return name, nil
	}
	return "", fmt.Errorf("%w (one started but left)", errNoDaemon)
}
