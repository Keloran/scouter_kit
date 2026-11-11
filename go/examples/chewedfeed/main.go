// Command chewedfeed is a read-only Chewedfeed dashboard for g1v0, one page per site.
//
//	go run ./examples/chewedfeed -preview
//	go run ./examples/chewedfeed [-token-file SESSION_FILE] [-project SLUG]
//
// The token file contains the JWT from Chewedfeed's session cookie, without
// the cookie name. Without it, public statistics, projects and uptime work.
// Keep the host running for refreshes and navigation. Right tap: next page;
// left tap: previous; double taps cycle Sites/AI/Notifications/Stats forward
// on the right and backward on the left; right triple tap: pause rotation;
// AI usage reads the existing local ai-usagebar cache.
// Desktop notifications temporarily replace the page for five seconds.
// Without the host (link lost, or after it exits) the arms show "Not connected"
// with their batteries and the time, kept up to date by a program on each arm.
// -preview prints one snapshot without initializing Bluetooth or the glasses.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
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
	notifications           bool
	notificationBus         string
	aiUsageDir              string
	aiRefresh               time.Duration
	calibrate               bool
	dx, dy                  int
	fontScale               float64
	configFile              string
	saveConfig              bool
	showAbove, showBelow    int
	alwaysNotes             bool
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
	flag.DurationVar(&o.refresh, "refresh", 5*time.Minute, "Chewedfeed API refresh interval (at least 30s)")
	flag.DurationVar(&o.rotate, "rotate", 12*time.Second, "time per page; 0 disables automatic rotation")
	flag.DurationVar(&o.stay, "for", 0, "stop after this duration; 0 runs until interrupted")
	flag.BoolVar(&o.preview, "preview", false, "print all pages once without connecting to glasses")
	flag.BoolVar(&o.details, "details", true, "include per-project uptime")
	flag.BoolVar(&o.notifications, "notifications", true, "show laptop notifications for five seconds, then restore the dashboard")
	flag.StringVar(&o.notificationBus, "notification-bus", "", "notification D-Bus address (empty: the laptop user session)")
	cacheDir, _ := os.UserCacheDir()
	if cacheDir != "" {
		cacheDir = filepath.Join(cacheDir, "ai-usagebar")
	}
	flag.StringVar(&o.aiUsageDir, "ai-usage-dir", cacheDir, "local ai-usagebar cache directory for Codex and Claude usage")
	flag.DurationVar(&o.aiRefresh, "ai-refresh", 30*time.Second, "local AI usage refresh interval (at least 1s)")
	geometry := scouterkit.DefaultConfig()
	flag.IntVar(&o.dx, "dx", geometry.DX, "horizontal separation of the eyes in pixels")
	flag.IntVar(&o.dy, "dy", geometry.DY, "vertical separation of the eyes in pixels")
	flag.BoolVar(&o.calibrate, "calibrate", false, "show a left-eye pixel ruler and right-eye crosshair instead of the dashboard")
	flag.StringVar(&o.configFile, "config", "chewedfeed.local.json", "local display settings file; overrides user config/config.json, then flags override both")
	flag.IntVar(&o.showAbove, "show-above", 9, "show the dashboard while head pitch is above this (degrees, + looking up)")
	flag.IntVar(&o.showBelow, "show-below", -30, "show the dashboard while head pitch is below this (degrees); between the two only notifications show")
	flag.BoolVar(&o.alwaysNotes, "always-show-notifications", true, "show notification popups whatever the head angle")
	flag.BoolVar(&o.saveConfig, "save-config", false, "save alignment and font scale, then exit without connecting")
	flag.Float64Var(&o.fontScale, "font-scale", 2, "text scale from 0.1 to 3, including fractional sizes such as 1.5")
	demo.Usage("chewedfeed [flags]")
	flag.Parse()
	if !f.Log {
		stages.SetOutput(io.Discard)
	}
	if flag.NArg() != 0 {
		flag.Usage()
		os.Exit(2)
	}
	explicit := make(map[string]bool)
	flag.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	var err error
	o, err = configureDisplay(o, explicit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "chewedfeed: %v\n", err)
		os.Exit(1)
	}
	if o.saveConfig {
		if err := saveDisplaySettings(o); err != nil {
			fmt.Fprintf(os.Stderr, "chewedfeed: saving display settings: %v\n", err)
			os.Exit(1)
		}
		progress("Saved display settings: dx=%d, dy=%d, font scale=%g", o.dx, o.dy, o.fontScale)
		return
	}
	if err := run(f, o); err != nil {
		fmt.Fprintf(os.Stderr, "chewedfeed: %v\n", err)
		os.Exit(1)
	}
}

func run(f demo.Flags, o options) error {
	if o.dx < -240 || o.dx > 240 || o.dy < -120 || o.dy > 120 {
		return errors.New("use -dx between -240 and 240, and -dy between -120 and 120")
	}
	if o.calibrate {
		return calibrate(f, o)
	}
	if o.refresh < 30*time.Second || o.rotate < 0 || (o.rotate > 0 && o.rotate < time.Second) || o.stay < 0 {
		return errors.New("use -refresh >= 30s, -rotate 0 or >= 1s, and -for >= 0")
	}
	if o.aiRefresh < time.Second {
		return errors.New("use -ai-refresh >= 1s")
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
		pages := makePages(data, 43, 10, utf8.RuneCountInString)
		progress("Preview ready: %d pages", len(pages))
		for i, p := range pages {
			fmt.Printf("\n[Sites %d/%d] %s\n%s\n%s\n", i+1, len(pages), p.title, strings.Join(p.lines, "\n"), p.status)
		}
		ai := aiUsagePages(o.aiUsageDir, nil, time.Now())
		for i, p := range ai {
			fmt.Printf("\n[AI %d/%d] %s\n%s\n%s\n", i+1, len(ai), p.title, strings.Join(p.lines, "\n"), p.status)
		}
		for i, p := range emptyNotificationHistory() {
			fmt.Printf("\n[Notifications %d/1] %s\n%s\n%s\n", i+1, p.title, strings.Join(p.lines, "\n"), p.status)
		}
		sample, sampleErr := readSystemSample("/proc", nil, time.Now())
		system := systemPages(systemUpdate{sample: sample, err: sampleErr}, 10)
		for i, p := range system {
			fmt.Printf("\n[Stats %d/%d] %s\n%s\n%s\n", i+1, len(system), p.title, strings.Join(p.lines, "\n"), p.status)
		}
		title := currentMusicTitle(ctx)
		fmt.Printf("\n[Status] %s\n", musicStatusLine(title, title != "", time.Now()))
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
	font           *scouterkit.Font
	footerFont     *scouterkit.Font
	footerBaseline int
	batteries      batteryStatus
	bodyStart      int
	bitmap         *scouterkit.Bitmap
	x, y           int
	w, h           int
	contentW       int
	statusX        int
	statusW        int
	rows           int
	status         musicStatus
}

const (
	textInset       = 4
	footerRaiseRows = 2
	footerLift      = 80
	lowBatteryAt    = 20
)

func newScreen(cfg scouterkit.Config) (*screen, error) {
	return newScreenScale(cfg, 2)
}

func newScreenScale(cfg scouterkit.Config, scale float64) (*screen, error) {
	progress("Loading display font onto glasses")
	f, err := loadDisplayFont("", scale)
	if err != nil {
		return nil, err
	}
	footer := f
	vw, vh := scouterkit.Size()
	w := min(600, vw-2*max(cfg.DX, -cfg.DX)-16)
	h := min(400, vh-40)
	fetchW, _ := (&screen{footerFont: footer}).iconSize("download")
	fetchW += footer.Measure(" 00:00").Width + 2*textInset // the Sites fetch time under the mode icon
	statusW := max(90, w/7, fetchW)
	contentW := w - statusW - 1
	progress("Allocating dashboard display (%dx%d)", w, h)
	b, err := scouterkit.CreateBitmap(w, h)
	if err != nil {
		return nil, err
	}
	footerBaseline := footer.Ascent + ((h/footer.LineSpacing)-1-footerRaiseRows)*footer.LineSpacing - footerLift
	// The old battery row is now a right-side status panel, so reserve one
	// footer row and let the page content use the space above it.
	bodyStart := 2
	if scale != 2 {
		bodyStart = 1
	}
	rows := (footerBaseline-footer.Ascent-8-f.Ascent-f.Descent)/f.LineSpacing - bodyStart + 1
	return &screen{font: f, footerFont: footer, footerBaseline: footerBaseline, bodyStart: bodyStart,
		bitmap: b, x: max(max(cfg.DX, -cfg.DX)+8, (vw-w)/2), y: (vh - h) / 2, w: w, h: h,
		contentW: contentW, statusX: contentW + 1, statusW: statusW,
		rows: max(1, rows)}, nil
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
	s.render(p, index, total, paused, nil)
}

func (s *screen) renderView(nav *navigation, popup *notification) {
	v := nav.current()
	s.render(v.pages[v.index], v.index, len(v.pages), nav.paused, popup)
}

func footerStatus(status string) string {
	if strings.HasPrefix(status, "STALE") {
		for _, marker := range []string{"Fetched ", "Cached ", "Sampled ", "Received "} {
			if i := strings.LastIndex(status, marker); i >= 0 {
				return "STALE " + status[i:]
			}
		}
		return "STALE"
	}
	return status
}

func (s *screen) render(p page, index, total int, paused bool, popup *notification) {
	s.bitmap.Fill(s.bitmap.Bounds(), 0)
	line := func(text string, n int, gray uint8) {
		text = fitLine(text, s.contentW-textInset, s.measure)
		s.font.DrawLine(s.bitmap, text, textInset, s.font.Ascent+n*s.font.LineSpacing, scouterkit.AlignLeft, gray)
	}
	if popup == nil {
		line(p.title, 0, 255)
		for i, text := range p.lines {
			if i >= s.rows {
				break
			}
			line(text, i+s.bodyStart, 255)
		}
	} else {
		app := popup.App
		if app == "" {
			app = "Notification"
		}
		line(app, 0, 255)
		for i, text := range notificationLines(*popup, s.contentW-textInset, s.rows+s.bodyStart-1, s.measure) {
			line(text, i+1, 255)
		}
	}
	s.drawStatusPanel(paused, p.status)
	s.drawNavigationRow(index, total)
	scouterkit.Draw(s.bitmap, s.x, s.y)
	scouterkit.Flush()
}

func display(ctx context.Context, f demo.Flags, o options, c *client) error {
	started := time.Now()
	progress("Looking for glasses and connecting over Bluetooth")
	cfg, arms, err := connectDisplay(f, o)
	if err != nil {
		return fmt.Errorf("connecting to glasses: %w", err)
	}
	defer func() {
		// The standby program runs on: it shows "Not connected" and the battery once the link is gone.
		progress("Stopping dashboard; the glasses keep their standby screen")
		scouterkit.Deinit()
		progress("Stopped")
	}()
	if ctx.Err() != nil {
		return nil
	}
	progress("Glasses connected: %d arms (%s)", len(arms), time.Since(started).Round(time.Millisecond))
	for _, arm := range arms {
		progress("%s arm ready: firmware %s", arm, arm.State().Firmware)
	}
	s, err := newScreenScale(cfg, o.fontScale)
	if err != nil {
		return err
	}
	standbyState, err := installStandby(cfg, s, o.fontScale, arms)
	if err != nil {
		return fmt.Errorf("standby program: %w", err)
	}
	for _, arm := range arms {
		progress("Enabling touch controls on %s arm", arm)
		mask, err := arm.ReportInputs(1<<scouterkit.TriggerTouch | 1<<scouterkit.TriggerCase)
		if err != nil {
			return err
		}
		if mask&(1<<scouterkit.TriggerTouch) == 0 {
			progress("%s arm: touch unavailable; use automatic rotation", arm)
		}
	}
	defer func() {
		for _, arm := range arms {
			arm.ReportInputs(0)
		}
	}()
	loading := []page{{id: "loading", title: "Chewedfeed", lines: []string{"Loading dashboard..."}, status: "Connecting to API"}}
	ai := aiUsagePages(o.aiUsageDir, nil, time.Now())
	system := []page{{id: "system/cpu", title: "System monitor", lines: []string{"Sampling laptop activity..."}, status: "Starting"}}
	s.status.setTitle(currentMusicTitle(ctx), time.Now())
	nav := newNavigation(loading, ai, system, o.rotate == 0)
	var history notificationHistory
	nav.replace(viewNotifications, history.pages(s.contentW-textInset, s.rows, s.measure))
	cases, err := initialCaseState(arms)
	if err != nil {
		return err
	}
	nav.inCase = cases.any()
	rotation := o.rotate
	if rotation == 0 {
		rotation = 12 * time.Second
	}
	progress("Sending loading screen to glasses")
	if nav.visible() {
		s.renderView(&nav, nil)
	} else {
		s.hide(arms)
	}
	progress("Started: Sites, AI, Notifications and Stats screens; system refresh every 2s, API refresh every %s, page rotation every %s (paused: %t)", o.refresh, rotation, nav.paused)
	updates := make(chan snapshot, 1)
	workerCtx, stopWorker := context.WithCancel(ctx)
	defer stopWorker()
	musicUpdates := make(chan string, 1)
	go func() {
		ticker := time.NewTicker(musicRefresh)
		defer ticker.Stop()
		for {
			select {
			case <-workerCtx.Done():
				return
			case <-ticker.C:
				title := currentMusicTitle(workerCtx)
				select {
				case musicUpdates <- title:
				case <-workerCtx.Done():
					return
				}
			}
		}
	}()
	batteryUpdates, stopBatteries := monitorBatteries(workerCtx, arms)
	defer stopBatteries()
	statusPath := glassesStatusPath()
	if statusPath != "" {
		defer os.Remove(statusPath)
	}
	systemUpdates := monitorSystem(workerCtx, "/proc")
	lastSystemError := ""
	aiUpdates := make(chan []page, 1)
	go func() {
		previous := ai
		ticker := time.NewTicker(o.aiRefresh)
		defer ticker.Stop()
		for {
			select {
			case <-workerCtx.Done():
				return
			case <-ticker.C:
				previous = aiUsagePages(o.aiUsageDir, previous, time.Now())
				select {
				case aiUpdates <- previous:
				case <-workerCtx.Done():
					return
				}
			}
		}
	}()
	var notes <-chan notification
	var noteErrors <-chan error
	var popup notificationPopup
	if o.notifications {
		notes, noteErrors, err = startNotifications(workerCtx, o.notificationBus)
		if err != nil {
			progress("Desktop notifications unavailable: %v", err)
			nav.replace(viewNotifications, []page{{id: "notifications/empty", title: "Notifications", lines: []string{"Notification listener unavailable"}, status: "Unavailable"}})
		} else {
			progress("Desktop notification listener started (5-second popups)")
		}
	} else {
		nav.replace(viewNotifications, []page{{id: "notifications/empty", title: "Notifications", lines: []string{"Notification listening disabled"}, status: "Disabled"}})
	}
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
	progress("Single right/left: next/previous page. Double right/left: next/previous screen (Sites, AI, Notifications, Stats). Triple right: pause. Ctrl-C exits.")
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	next := time.Now().Add(rotation)
	lastWarnings := ""
	lastClockMinute := time.Now().Format(musicStatusFormat)
	// ponytail: fixed 10s x 5 checks per arm; exits only when every arm has missed 5 in a row.
	linkCheck := time.NewTicker(10 * time.Second)
	defer linkCheck.Stop()
	missed := make(map[scouterkit.Arm]int)
	var angleTick <-chan time.Time
	if len(arms) > 0 {
		t := time.NewTicker(500 * time.Millisecond)
		defer t.Stop()
		angleTick = t.C
		// A TILT range narrower than ±90 makes the firmware stream every sample; without it,
		// PITCH read on demand stayed at its first value. Restored on exit (sensor settings
		// outlive the session).
		lo := int32(-89)
		arms[0].SysWrite(scouterkit.PropPitchLo, uint32(lo))
		defer arms[0].SysWrite(scouterkit.PropPitchLo, uint32(lo-1))
	}
	lastAngle := ""
	for {
		redraw := false
		select {
		case <-angleTick:
			// Both arms report the same head angles (VM.md §2).
			v, err := arms[0].Props(scouterkit.PropPitch, scouterkit.PropRoll)
			line := ""
			switch {
			case err != nil:
				line = fmt.Sprintf("Head angle unavailable: %v", err)
			case v[0] == scouterkit.Unknown || v[1] == scouterkit.Unknown:
				line = "Head angle unavailable: arm reports no accelerometer reading"
			default:
				line = fmt.Sprintf("Head angle: pitch %d° (+ looking up), roll %d° (+ right ear down)", int32(v[0]), int32(v[1]))
			}
			if line != lastAngle {
				lastAngle = line
				progress("%s", line)
			}
			// No reading: keep the dashboard on.
			level := err == nil && v[0] != scouterkit.Unknown && int32(v[0]) <= int32(o.showAbove) && int32(v[0]) >= int32(o.showBelow)
			if level == nav.level {
				continue
			}
			wasVisible := nav.visible()
			nav.level = level
			progress("Head level: %t (dashboard shown: %t)", level, nav.visible())
			if wasVisible && !nav.visible() && !popup.active() {
				s.hide(arms)
			}
			next = time.Now().Add(rotation)
			redraw = true
		case <-ctx.Done():
			progress("Shutdown requested: %v", ctx.Err())
			return nil
		case n, ok := <-notes:
			if !ok {
				notes = nil
				continue
			}
			history.add(n, time.Now())
			nav.replace(viewNotifications, history.pages(s.contentW-textInset, s.rows, s.measure))
			if nav.selected == viewNotifications {
				redraw = true
			}
			if nav.visible() || o.alwaysNotes && nav.popupVisible() {
				popup.show(n, time.Now())
				progress("Desktop notification popup (5s)")
				redraw = true
			}
		case err, ok := <-noteErrors:
			if !ok {
				noteErrors = nil
				continue
			}
			progress("Desktop notifications unavailable: %v", err)
		case ai := <-aiUpdates:
			nav.replace(viewAI, ai)
			if nav.selected == viewAI {
				redraw = true
			}
		case title := <-musicUpdates:
			redraw = s.status.setTitle(title, time.Now())
		case batteries, ok := <-batteryUpdates:
			if !ok {
				batteryUpdates = nil
				continue
			}
			s.batteries = batteries
			redraw = true
			if statusPath != "" {
				if err := writeGlassesStatus(statusPath, batteries, time.Now()); err != nil {
					progress("Glasses status file not written: %v", err)
				}
			}
		case data, ok := <-systemUpdates:
			if !ok {
				systemUpdates = nil
				continue
			}
			nav.replace(viewSystem, systemPages(data, s.rows))
			if nav.selected == viewSystem {
				redraw = true
			}
			message := ""
			if data.err != nil {
				message = data.err.Error()
			}
			if message != lastSystemError {
				if message != "" {
					progress("System monitor unavailable: %s", message)
				} else {
					progress("System monitor recovered")
				}
				lastSystemError = message
			}
		case data := <-updates:
			nav.replace(viewSites, makePages(data, s.contentW-textInset, s.rows, s.measure))
			progress("Sites ready: %d pages; page %d selected", len(nav.views[0].pages), nav.views[0].index+1)
			if nav.selected == viewSites {
				redraw = true
			}
			if w := warnings(data); w != lastWarnings {
				if w != "" {
					progress("API warnings:\n%s", w)
				} else if lastWarnings != "" {
					progress("All API sources recovered")
				}
				lastWarnings = w
			}
		case <-linkCheck.C:
			allDown := true
			for _, arm := range arms {
				if arm.State().Connected {
					missed[arm] = 0
					allDown = false
					continue
				}
				missed[arm]++
				progress("%s arm not connected (%d/5)", arm, missed[arm])
				allDown = allDown && missed[arm] >= 5
			}
			if allDown && len(arms) > 0 {
				return errors.New("all arms not connected for 5 checks; exiting")
			}
			continue
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
					if e.Type == scouterkit.EventNotify && e.A == linkNotice {
						// The program drew its offline face, which the library's mirror does not know.
						progress("%s arm link back; repainting", arm)
						syncStandbyClock(arm, standbyState)
						s.hide(arms) // flushes black, so the next render sends every pixel again
						redraw = true
						continue
					}
					if e.Type == scouterkit.EventReset {
						progress("%s arm session reset; redrawing dashboard", arm)
						redraw = true
					}
					if inCase, ok := caseEvent(e); ok {
						if cases[arm] == inCase {
							continue
						}
						wasVisible := nav.visible()
						cases[arm] = inCase
						nav.inCase = cases.any()
						progress("%s arm in case: %t", arm, inCase)
						if !nav.visible() && wasVisible {
							s.hide(arms)
							popup = notificationPopup{}
						}
						next = time.Now().Add(rotation)
						redraw = true
						continue
					}
					if e.Type != scouterkit.EventInput || e.Trigger != int(scouterkit.TriggerTouch) {
						continue
					}
					if !nav.touch(e.A, arm == scouterkit.Left) {
						continue
					}
					v := nav.current()
					if e.A == 1 || e.A == 2 {
						popup = notificationPopup{}
					}
					switch e.A {
					case 1:
						progress("%s arm tap: %s page %d/%d", arm, v.name, v.index+1, len(v.pages))
					case 2:
						progress("Screen switched to %s, page %d/%d", v.name, v.index+1, len(v.pages))
					case 3:
						progress("Page rotation paused: %t", nav.paused)
					}
					next = time.Now().Add(rotation)
					redraw = true
				}
			}
			now := time.Now()
			if minute := now.Format(musicStatusFormat); minute != lastClockMinute {
				lastClockMinute = minute
				redraw = true
			}
			statusWidth := s.w - s.footerSplit() - 2*textInset
			if s.status.advance(now, statusWidth, func(text string) int { return s.footerFont.Measure(text).Width }) {
				redraw = true
			}
			if popup.expire(now) {
				next = now.Add(rotation)
				redraw = true
				if !nav.visible() && nav.popupVisible() {
					s.hide(arms) // head level: back to off
				}
				progress("Notification popup ended; dashboard restored")
			}
			if !nav.paused && nav.visible() && !popup.active() && now.After(next) {
				nav.move(1)
				next = time.Now().Add(rotation)
				redraw = true
			}
		}
		if redraw && (nav.visible() || popup.active() && nav.popupVisible()) {
			var note *notification
			if popup.active() {
				note = &popup.note
			}
			s.renderView(&nav, note)
		}
	}
}
