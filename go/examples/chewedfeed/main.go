// Command chewedfeed is a read-only, paginated Chewedfeed dashboard for g1v0.
//
//	go run ./examples/chewedfeed -preview
//	go run ./examples/chewedfeed [-token-file SESSION_FILE] [-project SLUG]
//
// The token file contains the JWT from Chewedfeed's session cookie, without
// the cookie name. Without it, public statistics, projects and uptime work.
// Keep the host running for refreshes and navigation. Right tap: next page;
// left tap: previous; double tap: pause rotation; long press: hide/show.
// -preview prints one snapshot without initializing Bluetooth or the glasses.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"scouterkit"
	"scouterkit/examples/internal/demo"
)

type options struct {
	api, tokenFile, project string
	refresh, rotate, stay   time.Duration
	preview, details        bool
}

var stages = log.New(os.Stderr, "chewedfeed: ", log.Ltime|log.Lmicroseconds)

func progress(format string, args ...any) { stages.Printf(format, args...) }

func main() {
	var f demo.Flags
	f.Register()
	var o options
	flag.StringVar(&o.api, "api", "https://api.chewedfeed.com", "Chewedfeed API base URL")
	flag.StringVar(&o.tokenFile, "token-file", "", "file containing the session-cookie JWT for private views")
	flag.StringVar(&o.project, "project", "", "only this project slug (summary remains portfolio-wide)")
	flag.DurationVar(&o.refresh, "refresh", 2*time.Minute, "refresh interval (at least 30s)")
	flag.DurationVar(&o.rotate, "rotate", 12*time.Second, "time per page; 0 disables automatic rotation")
	flag.DurationVar(&o.stay, "for", 0, "stop after this duration; 0 runs until interrupted")
	flag.BoolVar(&o.preview, "preview", false, "print all pages once without connecting to glasses")
	flag.BoolVar(&o.details, "details", true, "include uptime and authenticated tickets, services and repos")
	demo.Usage("chewedfeed [flags]")
	flag.Parse()
	if flag.NArg() != 0 {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(f, o); err != nil {
		progress("Failed: %v", err)
		os.Exit(1)
	}
}

func run(f demo.Flags, o options) error {
	if o.refresh < 30*time.Second || o.rotate < 0 || (o.rotate > 0 && o.rotate < time.Second) || o.stay < 0 {
		return errors.New("use -refresh >= 30s, -rotate 0 or >= 1s, and -for >= 0")
	}
	var token string
	if o.tokenFile != "" {
		progress("Reading session token file")
		b, err := os.ReadFile(o.tokenFile)
		if err != nil {
			return errors.New("cannot read session token file")
		}
		token = strings.TrimSpace(string(b))
		if token == "" {
			return errors.New("session token file is empty")
		}
	}
	c, err := newClient(o.api, token, o.project, o.details)
	if err != nil {
		return err
	}
	if token == "" {
		progress("Starting with public API views")
	} else {
		progress("Starting with public and authenticated API views")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if o.stay > 0 {
		var stop context.CancelFunc
		ctx, stop = context.WithTimeout(ctx, o.stay)
		defer stop()
	}
	if o.preview {
		progress("Preview mode: glasses connection skipped")
		data := c.fetch(ctx, nil)
		pages := makePages(data, 44, 12, utf8.RuneCountInString)
		progress("Preview ready: %d pages", len(pages))
		for i, p := range pages {
			fmt.Printf("\n[%d] %s\n%s\n%s\n", i+1, p.title, strings.Join(p.lines, "\n"), p.status)
		}
		for _, r := range data {
			if !r.at.IsZero() {
				return nil
			}
		}
		return errors.New("no API data available")
	}
	return display(ctx, f, o, c)
}

type screen struct {
	font   *scouterkit.Font
	bitmap *scouterkit.Bitmap
	x, y   int
	w, h   int
	rows   int
}

const (
	textInset       = 4
	footerRaiseRows = 2
)

func newScreen(cfg scouterkit.Config) (*screen, error) {
	progress("Loading display font onto glasses")
	f, err := scouterkit.LoadFont(scouterkit.DepartureMono(), "", 2)
	if err != nil {
		return nil, err
	}
	vw, vh := scouterkit.Size()
	w, h := vw-2*max(cfg.DX, -cfg.DX)-16, vh-40
	progress("Allocating dashboard display (%dx%d)", w, h)
	b, err := scouterkit.CreateBitmap(w, h)
	if err != nil {
		return nil, err
	}
	return &screen{f, b, max(cfg.DX, -cfg.DX) + 8, 20, w, h, (h / f.LineSpacing) - 4 - footerRaiseRows}, nil
}

func (s *screen) measure(text string) int { return s.font.Measure(text).Width }

func (s *screen) hide(arms []scouterkit.Arm) {
	// Clear the compositor too, so showing the same page again emits its pixels.
	scouterkit.Clear()
	scouterkit.Flush()
	for _, arm := range arms {
		arm.SetPanel(false)
	}
}

func (s *screen) draw(p page, index, total int, paused bool) {
	s.bitmap.Fill(s.bitmap.Bounds(), 0)
	line := func(text string, n int, gray uint8) {
		text = fitLine(text, s.w-textInset, s.measure)
		s.font.DrawLine(s.bitmap, text, textInset, s.font.Ascent+n*s.font.LineSpacing, scouterkit.AlignLeft, gray)
	}
	line(p.title, 0, 255)
	for i, text := range p.lines {
		line(text, i+2, 255)
	}
	line(p.status, s.rows+2, 153)
	mode := "auto"
	if paused {
		mode = "paused"
	}
	line(fmt.Sprintf("%d/%d  %s  %s", index+1, total, mode, time.Now().Format("15:04")), s.rows+3, 153)
	scouterkit.Draw(s.bitmap, s.x, s.y)
	scouterkit.Flush()
}

func display(ctx context.Context, f demo.Flags, o options, c *client) error {
	started := time.Now()
	progress("Looking for glasses and connecting over Bluetooth")
	cfg, arms, err := demo.Connect(f)
	if err != nil {
		return fmt.Errorf("connecting to glasses: %w", err)
	}
	defer func() {
		progress("Stopping dashboard and switching panels off")
		demo.Stop(arms)
		scouterkit.Deinit()
		progress("Stopped")
	}()
	progress("Glasses connected: %d arms (%s)", len(arms), time.Since(started).Round(time.Millisecond))
	for _, arm := range arms {
		progress("%s arm ready: firmware %s", arm, arm.State().Firmware)
	}
	s, err := newScreen(cfg)
	if err != nil {
		return err
	}
	for _, arm := range arms {
		progress("Enabling touch controls on %s arm", arm)
		mask, err := arm.ReportInputs(1 << scouterkit.TriggerTouch)
		if err != nil {
			return err
		}
		if mask == 0 {
			progress("%s arm: touch unavailable; use automatic rotation", arm)
		}
	}
	defer func() {
		for _, arm := range arms {
			arm.ReportInputs(0)
		}
	}()
	pages := []page{{id: "loading", title: "Chewedfeed", lines: []string{"Loading dashboard..."}, status: "Connecting to API"}}
	index, paused, hidden := 0, o.rotate == 0, false
	rotation := o.rotate
	if rotation == 0 {
		rotation = 12 * time.Second
	}
	progress("Sending loading screen to glasses")
	s.draw(pages[0], 0, 1, paused)
	progress("Started: display queued; API refresh every %s, page rotation every %s (paused: %t)", o.refresh, rotation, paused)
	updates := make(chan snapshot, 1)
	workerCtx, stopWorker := context.WithCancel(ctx)
	defer stopWorker()
	go func() {
		var previous snapshot
		for {
			previous = c.fetch(workerCtx, previous)
			select {
			case updates <- previous:
			case <-workerCtx.Done():
				return
			}
			timer := time.NewTimer(o.refresh)
			select {
			case <-timer.C:
			case <-workerCtx.Done():
				timer.Stop()
				return
			}
		}
	}()
	fmt.Println("Right tap: next; left tap: previous; double tap: pause; long press: hide/show. Ctrl-C exits.")
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	next := time.Now().Add(rotation)
	lastWarnings := ""
	for {
		redraw := false
		select {
		case <-ctx.Done():
			progress("Shutdown requested: %v", ctx.Err())
			return nil
		case data := <-updates:
			id := pages[index].id
			pages = makePages(data, s.w-textInset, s.rows, s.measure)
			index = preservePage(pages, id)
			progress("Dashboard ready: %d pages; page %d selected", len(pages), index+1)
			redraw = true
			if w := warnings(data); w != lastWarnings {
				if w != "" {
					progress("API warnings:\n%s", w)
				} else if lastWarnings != "" {
					progress("All API sources recovered")
				}
				lastWarnings = w
			}
		case <-tick.C:
			for _, arm := range arms {
				for {
					e, ok := arm.NextEvent(0)
					if !ok {
						break
					}
					if e.Type == scouterkit.EventError || e.Type == scouterkit.EventHalt {
						return fmt.Errorf("%s arm: event %d, code %d", arm, e.Type, e.Code)
					}
					if e.Type == scouterkit.EventReset {
						progress("%s arm session reset; redrawing dashboard", arm)
						redraw = true
					}
					if e.Type != scouterkit.EventInput || e.Trigger != int(scouterkit.TriggerTouch) {
						continue
					}
					switch e.A {
					case 1:
						step := 1
						if arm == scouterkit.Left {
							step = -1
						}
						index = (index + step + len(pages)) % len(pages)
						progress("%s arm tap: page %d/%d", arm, index+1, len(pages))
					case 2:
						paused = !paused
						progress("Page rotation paused: %t", paused)
					case 4:
						hidden = !hidden
						progress("Display hidden: %t", hidden)
						if hidden {
							s.hide(arms)
						}
					default:
						continue
					}
					next = time.Now().Add(rotation)
					redraw = true
				}
			}
			if !paused && !hidden && time.Now().After(next) {
				index = (index + 1) % len(pages)
				next = time.Now().Add(rotation)
				redraw = true
			}
		}
		if redraw && !hidden {
			s.draw(pages[index], index, len(pages), paused)
		}
	}
}
