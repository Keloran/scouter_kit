package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"scouterkit"
	"scouterkit/examples/internal/demo"
)

var errNoMirror = errors.New("no mirror running")

// viaMirror hands the request to a running mirror.
func viaMirror(socket string, r request) (reply, error) {
	conn, err := net.DialTimeout("unix", socket, time.Second)
	if err != nil {
		return reply{}, errNoMirror
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	if err := json.NewEncoder(conn).Encode(r); err != nil {
		return reply{}, err
	}
	var out reply
	if err := json.NewDecoder(conn).Decode(&out); err != nil {
		return reply{}, err
	}
	if out.Error != "" {
		return out, errors.New(out.Error)
	}
	return out, nil
}

func notifyMain(args []string) error {
	fs := flag.NewFlagSet("notify", flag.ExitOnError)
	var f demo.Flags
	fs.StringVar(&f.Left, "left", "", `left arm address, without a mirror (empty: the bonded glasses, "none": no left arm)`)
	fs.StringVar(&f.Right, "right", "", `right arm address, without a mirror (empty: the bonded glasses, "none": no right arm)`)
	fs.StringVar(&f.Adapter, "adapter", "", `BlueZ adapter, without a mirror (empty: the first powered one)`)
	fs.BoolVar(&f.Log, "log", false, "without a mirror: the library logs to stderr")
	socket := fs.String("socket", socketPath(), "the mirror's socket")
	app := fs.String("app", "", "app name shown before the title")
	icon := fs.String("icon", "", "icon name, such as mail, chat, call, calendar, alarm, music, warning or done (default: from -app)")
	icons := fs.Bool("icons", true, "without a mirror: show the app's icon beside the text")
	timeout := fs.Duration("timeout", 5*time.Second, "how long it shows")
	direct := fs.Bool("direct", false, "connect to the glasses even if a mirror runs (it cannot then: one link per arm)")
	standby := fs.Bool("standby", false, "without a mirror: keep the panels in standby while hidden (as g1 mirror -standby)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: g1 notify [flags] TITLE [BODY]\n\nShows a message on the glasses now, through a running `g1 mirror` or, without one, by connecting\n(3-5 s) and leaving the message on the arms: it hides by itself and shows again while the head is up.")
		fs.PrintDefaults()
	}
	fs.Parse(args)
	if fs.NArg() < 1 || fs.NArg() > 2 {
		fs.Usage()
		os.Exit(2)
	}
	r := request{App: *app, Summary: fs.Arg(0), Body: fs.Arg(1), Icon: *icon, TimeoutMS: int(timeout.Milliseconds())}
	if !*direct {
		out, err := viaMirror(*socket, r)
		if err == nil {
			fmt.Printf("shown through the mirror, %.0f ms after it got the message\n", out.MS)
			return nil
		}
		if !errors.Is(err, errNoMirror) {
			return err
		}
	}
	return notifyDirect(f, r, *standby, *icons)
}

// notifyDirect connects, shows the message and leaves the program running.
func notifyDirect(f demo.Flags, r request, standby, icons bool) error {
	cfg, arms, err := demo.Connect(f)
	if err != nil {
		return err
	}
	defer scouterkit.Deinit()
	text := r.App + r.Summary + r.Body
	g, err := newGlasses(cfg, arms, viewConfig{angle: 20, hysteresis: 5, hold: time.Second, listFor: 15 * time.Second,
		glyphs: charset(text) + "0123456789:. ", standby: standby, icons: icons})
	if err != nil {
		return err
	}
	if err := g.start(); err != nil {
		return err
	}
	for _, arm := range arms {
		if _, err := wait(arm, 0, stHidden); err != nil {
			return err
		}
	}
	n := &note{App: r.App, Summary: r.Summary, Body: r.Body, Icon: r.Icon, Urgency: 1, At: time.Now(),
		Timeout: time.Duration(r.TimeoutMS) * time.Millisecond}
	g.push(n, true, demo.Lead(arms), n.Timeout)
	for _, arm := range arms {
		if _, err := wait(arm, g.head, stPopup); err != nil {
			return err
		}
	}
	fmt.Printf("shown on %d arm(s) %.0f ms after connecting; it hides by itself in %v\n", len(arms),
		float64(time.Since(n.At).Microseconds())/1000, n.Timeout)
	return nil
}

func charset(text string) string {
	seen := map[rune]bool{}
	var b strings.Builder
	for _, r := range text {
		if !seen[r] && r >= ' ' {
			seen[r] = true
			b.WriteRune(r)
		}
	}
	return b.String()
}
