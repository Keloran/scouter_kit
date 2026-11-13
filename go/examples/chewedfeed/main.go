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
// Without the host (link lost, released, or after it exits) the arms show "Not
// connected" with their batteries and the time, kept up to date by a program on
// each arm. When every arm stays unreachable the dashboard releases the glasses
// and keeps trying to connect again; its data and page stay as they were.
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
	"sync"
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
	ownership               ownershipSettings
	managed                 bool // coordinator owns acquisition and reconnect policy
	snapshots               *snapshotBridge
	snapshotLocal           snapshotOverrides
}

var stages = log.New(os.Stderr, "chewedfeed: ", log.Ltime|log.Lmicroseconds)

func progress(format string, args ...any) { stages.Printf(format, args...) }

func main() {
	var f demo.Flags
	f.Register()
	var o options
	o.ownership.register(flag.CommandLine)
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
	o.snapshotLocal = snapshotOverrides{rotate: explicit["rotate"], project: explicit["project"]}
	var err error
	o, err = configureDisplay(o, explicit)
	if err == nil {
		configDir, _ := os.UserConfigDir()
		o.ownership, err = configureOwnership(withUserPeerEndpoint(o.ownership, explicit, configDir), explicit)
	}
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
	if o.calibrate && !o.preview {
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
	if o.ownership.Enabled {
		return coordinatedDisplay(ctx, f, o, c)
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
	status         *musicStatus // the dashboard's; outlives the screen
}

const (
	textInset       = 4
	footerRaiseRows = 2
	footerLift      = 80
	lowBatteryAt    = 20
	popupLines      = 3 // a notification popup: the app, then at most this many lines (all of it in the Notifications view)
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
	icons := &screen{footerFont: footer}
	fetchW, _ := icons.iconSize("download")
	fetchW += footer.Measure(" 00:00").Width + 2*textInset // the Sites fetch time under the mode icon
	batteryW, _ := icons.iconSize("computer")
	warnW, _ := icons.iconSize("warning")
	// case and computer rows: "100%", or a low "20%" with its warning icon
	batteryW += max(footer.Measure(" 100%").Width, footer.Measure(" 20% ").Width+warnW) + 2*textInset
	statusW := max(90, w/7, fetchW, batteryW)
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
		rows: max(1, rows), status: &musicStatus{}}, nil
}

func (s *screen) measure(text string) int { return s.font.Measure(text).Width }

// textW is what content text may use: textInset clear of both the left edge and the status divider.
func (s *screen) textW() int { return max(1, s.contentW-2*textInset) }

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
		text = fitLine(text, s.textW(), s.measure)
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
		for i, text := range notificationLines(*popup, s.textW(), min(popupLines, s.rows+s.bodyStart-1), s.measure) {
			line(text, i+1, 255)
		}
	}
	s.drawStatusPanel(paused, p.status)
	s.drawNavigationRow(index, total)
	scouterkit.Draw(s.bitmap, s.x, s.y)
	scouterkit.Flush()
}

func display(ctx context.Context, f demo.Flags, o options, c *client) error {
	return dashboard(ctx, &glassesHost{f: f, o: o}, o, c, nil)
}

// hostCommand asks the dashboard to release the glasses (release) or acquire
// them again; the outcome goes to done. The release is complete (workers
// joined, Deinit returned) before done receives.
type hostCommand struct {
	release      bool
	done         chan<- error
	ctx          context.Context
	cancellation *scouterkit.Cancellation
}

// Link checks, and reconnects after an unexpected loss (handover contract:
// retry 5 s, doubling to 60 s). Variables for the tests.
var (
	hostLinkCheck  = 10 * time.Second
	reconnectFirst = 5 * time.Second
	reconnectMax   = 60 * time.Second
)

// dashState is the dashboard without the glasses: its data, navigation,
// notification history and music line survive sessions. s is the current
// session (nil while released); pages are formatted for its screen.
type dashState struct {
	nav         navigation
	history     notificationHistory
	notesOff    []page        // the listener is unavailable or disabled: shown instead of the history
	sites       *snapshot     // the last API data; nil until the first fetch
	system      *systemUpdate // the last sample; nil until the first one
	music       musicStatus
	popup       notificationPopup
	s           *glassesSession
	snapshots   *snapshotBridge
	restorePage *uiSnapshot
}

func (d *dashState) restoreSnapshotPage() {
	if d.restorePage == nil || d.s == nil || d.restorePage.View == "sites" && d.sites == nil {
		return
	}
	d.restorePage.selectPage(&d.nav)
	d.restorePage = nil
}

// attach formats every view for a new session's screen and paints it in full
// (the session's compositor starts empty).
func (d *dashState) attach(s *glassesSession) {
	d.s = s
	scr := s.screen
	scr.status = &d.music
	if d.sites != nil {
		d.nav.replace(viewSites, makePages(*d.sites, scr.textW(), scr.rows, scr.measure))
	}
	if d.system != nil {
		d.nav.replace(viewSystem, systemPages(*d.system, scr.rows))
	}
	d.formatNotes()
	d.restoreSnapshotPage()
	d.nav.inCase = s.cases.any()
	d.nav.level = false // no head angle yet: dashboard on
	if d.nav.visible() {
		d.render()
	} else {
		scr.hide(s.arms)
	}
}

func (d *dashState) formatNotes() {
	if d.notesOff != nil {
		d.nav.replace(viewNotifications, d.notesOff)
	} else if d.s != nil {
		scr := d.s.screen
		d.nav.replace(viewNotifications, d.history.pages(scr.textW(), scr.rows, scr.measure))
	}
}

func (d *dashState) render() {
	if d.s == nil || !(d.nav.visible() || d.popup.active() && d.nav.popupVisible()) {
		return
	}
	var note *notification
	if d.popup.active() {
		note = &d.popup.note
	}
	d.s.screen.renderView(&d.nav, note)
}

// hide blanks the glasses, if there is a session.
func (d *dashState) hide() {
	if d.s != nil {
		d.s.screen.hide(d.s.arms)
	}
}

// dashboard runs until ctx ends. The data workers run for its whole life; the
// glasses session can be released and acquired again (control, or after every
// arm stayed unreachable) without losing the data or the selected page.
func dashboard(ctx context.Context, host *glassesHost, o options, c *client, control <-chan hostCommand) error {
	started := time.Now()
	if o.managed {
		progress("Waiting for glasses ownership")
	} else {
		progress("Looking for glasses and connecting over Bluetooth")
	}
	if !o.managed {
		if err := host.Acquire(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("connecting to glasses: %w", err)
		}
	}
	defer func() {
		if o.managed {
			progress("Stopping dashboard and releasing glasses")
		} else {
			progress("Stopping dashboard; the glasses keep their standby screen")
		}
		host.Release(nil)
		progress("Stopped")
	}()
	s := host.current()
	if s != nil {
		progress("Glasses connected: %d arms (%s)", len(s.arms), time.Since(started).Round(time.Millisecond))
		for _, arm := range s.arms {
			progress("%s arm ready: firmware %s", arm, arm.State().Firmware)
		}
	}

	loading := []page{{id: "loading", title: "Chewedfeed", lines: []string{"Loading dashboard..."}, status: "Connecting to API"}}
	ai := aiUsagePages(o.aiUsageDir, nil, time.Now())
	system := []page{{id: "system/cpu", title: "System monitor", lines: []string{"Sampling laptop activity..."}, status: "Starting"}}
	d := &dashState{nav: newNavigation(loading, ai, system, o.rotate == 0), snapshots: o.snapshots}
	d.music.setTitle(currentMusicTitle(ctx), time.Now())
	rotation := o.rotate
	rotationSetting := o.rotate
	selection := apiSelection{project: c.project}
	publishSnapshot := func() { o.snapshots.publish(exportSnapshot(&d.nav, selection.project, rotationSetting, 0)) }
	publishSnapshot()
	if rotation == 0 {
		rotation = 12 * time.Second
	}

	workerCtx, stopWorker := context.WithCancel(ctx)
	var workers sync.WaitGroup
	defer func() { stopWorker(); workers.Wait() }()
	var notes <-chan notification
	var noteErrors <-chan error
	if o.notifications {
		var err error
		notes, noteErrors, err = startNotifications(workerCtx, o.notificationBus)
		if err != nil {
			progress("Desktop notifications unavailable: %v", err)
			d.notesOff = []page{{id: "notifications/empty", title: "Notifications", lines: []string{"Notification listener unavailable"}, status: "Unavailable"}}
		} else {
			progress("Desktop notification listener started (5-second popups)")
		}
	} else {
		d.notesOff = []page{{id: "notifications/empty", title: "Notifications", lines: []string{"Notification listening disabled"}, status: "Disabled"}}
	}
	var batteryUpdates <-chan batteryStatus
	if s != nil {
		progress("Sending loading screen to glasses")
		d.attach(s)
		host.updateReadiness()
		batteryUpdates = s.batteries
	}
	progress("Started: Sites, AI, Notifications and Stats screens; system refresh every 2s, API refresh every %s, page rotation every %s (paused: %t)", o.refresh, rotation, d.nav.paused)

	updates := make(chan apiUpdate, 1)
	projects := make(chan apiSelection, 1)
	musicUpdates := make(chan string, 1)
	workers.Add(3)
	go func() {
		defer workers.Done()
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
	statusPath := glassesStatusPath()
	if statusPath != "" {
		defer os.Remove(statusPath)
	}
	systemUpdates := monitorSystem(workerCtx, "/proc")
	defer func() {
		stopWorker()
		if systemUpdates != nil {
			for range systemUpdates {
			}
		}
		if notes != nil {
			for range notes {
			}
		}
	}()
	lastSystemError := ""
	aiUpdates := make(chan []page, 1)
	go func() {
		defer workers.Done()
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
	go func(initial apiSelection) {
		defer workers.Done()
		watchDashboardAPI(workerCtx, c, o.refresh, initial, projects, updates)
	}(selection)
	progress("Single right/left: next/previous page. Double right/left: next/previous screen (Sites, AI, Notifications, Stats). Triple right: pause. Ctrl-C exits.")
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	next := time.Now().Add(rotation)
	var snapshotUpdates <-chan struct{}
	if o.snapshots != nil {
		snapshotUpdates = o.snapshots.notify
	}
	changeProject := func(project string) {
		if project == selection.project {
			return
		}
		selection = apiSelection{project: project, generation: selection.generation + 1}
		d.sites = nil
		d.nav.replace(viewSites, loading)
		selectDashboardProject(projects, selection)
	}
	applySnapshot := func() bool {
		s, ok := o.snapshots.take()
		if !ok {
			return false
		}
		setting, project := s.apply(&d.nav, o.snapshotLocal, rotationSetting, selection.project)
		changeProject(project)
		rotationSetting, rotation = setting, setting
		if rotation == 0 {
			rotation = 12 * time.Second
		}
		next = time.Now().Add(rotation)
		d.restorePage = &s
		d.restoreSnapshotPage()
		publishSnapshot()
		return true
	}
	lastWarnings := ""
	lastClockMinute := time.Now().Format(musicStatusFormat)
	// ponytail: 5 checks per arm; all arms missing 5 in a row releases the session.
	linkCheck := time.NewTicker(hostLinkCheck)
	defer linkCheck.Stop()
	angleTick := time.NewTicker(500 * time.Millisecond)
	defer angleTick.Stop()
	lastAngle := ""
	var retryAt time.Time // reconnect after an unexpected loss; zero: not trying
	retryDelay := reconnectFirst
	release := func(lost error) {
		d.s, batteryUpdates = nil, nil
		host.Release(lost)
	}
	acquire := func(acquireCtx context.Context, token *scouterkit.Cancellation) error {
		applySnapshot() // before setup/attach and the first complete frame
		var err error
		if token == nil {
			err = host.Acquire(acquireCtx)
		} else {
			err = host.acquireWithCancellation(acquireCtx, token)
		}
		if err != nil {
			return err
		}
		if o.managed && !host.bothConnected() {
			host.Release(errors.New("both arms are required for handover"))
			return errors.New("both arms are required for handover")
		}
		if d.s == nil {
			d.attach(host.current())
			if o.managed {
				scouterkit.Flush() // wait for the fresh full frame to be shown
			}
			host.updateReadiness()
			batteryUpdates = d.s.batteries
			next = time.Now().Add(rotation)
		}
		return nil
	}
	for {
		redraw := false
		select {
		case cmd := <-control:
			var err error
			if cmd.release {
				progress("Releasing the glasses")
				release(nil)
			} else {
				progress("Acquiring the glasses")
				acquireCtx := cmd.ctx
				if acquireCtx == nil {
					acquireCtx = ctx
				}
				err = acquire(acquireCtx, cmd.cancellation)
			}
			retryAt, retryDelay = time.Time{}, reconnectFirst
			publishSnapshot()
			cmd.done <- err
			continue
		case <-snapshotUpdates:
			redraw = applySnapshot()
		case <-angleTick.C:
			if d.s == nil || len(d.s.arms) == 0 {
				continue
			}
			// Both arms report the same head angles (VM.md §2).
			v, err := d.s.arms[0].Props(scouterkit.PropPitch, scouterkit.PropRoll)
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
			if level == d.nav.level {
				continue
			}
			wasVisible := d.nav.visible()
			d.nav.level = level
			progress("Head level: %t (dashboard shown: %t)", level, d.nav.visible())
			if wasVisible && !d.nav.visible() && !d.popup.active() {
				d.hide()
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
			d.history.add(n, time.Now())
			d.formatNotes()
			if d.nav.selected == viewNotifications {
				redraw = true
			}
			if d.s != nil && (d.nav.visible() || o.alwaysNotes && d.nav.popupVisible()) {
				d.popup.show(n, time.Now())
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
			d.nav.replace(viewAI, ai)
			if d.nav.selected == viewAI {
				redraw = true
			}
		case title := <-musicUpdates:
			redraw = d.music.setTitle(title, time.Now())
		case batteries, ok := <-batteryUpdates:
			if !ok {
				batteryUpdates = nil
				continue
			}
			scr := d.s.screen
			if c := batteries.casePct; c != nil && (scr.batteries.casePct == nil || *c != *scr.batteries.casePct) {
				progress("Case battery: %d%%", *c)
			}
			scr.batteries = batteries
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
			d.system = &data
			if d.s != nil {
				d.nav.replace(viewSystem, systemPages(data, d.s.screen.rows))
			}
			if d.nav.selected == viewSystem {
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
		case update := <-updates:
			if update.selection != selection {
				continue
			}
			data := update.data
			if o.snapshots != nil && !o.snapshotLocal.project && selection.project != "" && missingSnapshotProject(data) {
				changeProject("")
				publishSnapshot()
				continue
			}
			d.sites = &data
			if d.s != nil {
				scr := d.s.screen
				d.nav.replace(viewSites, makePages(data, scr.textW(), scr.rows, scr.measure))
				d.restoreSnapshotPage()
				progress("Sites ready: %d pages; page %d selected", len(d.nav.views[0].pages), d.nav.views[0].index+1)
			}
			if d.nav.selected == viewSites {
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
			if o.managed {
				host.updateReadiness()
				continue
			}
			if d.s == nil {
				continue
			}
			allDown := true
			for _, arm := range d.s.arms {
				if arm.State().Connected {
					d.s.missed[arm] = 0
					allDown = false
					continue
				}
				d.s.missed[arm]++
				progress("%s arm not connected (%d/5)", arm, d.s.missed[arm])
				allDown = allDown && d.s.missed[arm] >= 5
			}
			if allDown && len(d.s.arms) > 0 {
				lost := errors.New("all arms not connected for 5 checks")
				progress("%v; releasing the glasses, reconnecting in %s", lost, reconnectFirst)
				release(lost)
				retryAt, retryDelay = time.Now().Add(reconnectFirst), reconnectFirst
			}
			continue
		case <-tick.C:
			if o.managed {
				host.updateReadiness()
			}
			now := time.Now()
			if d.s == nil && !retryAt.IsZero() && !now.Before(retryAt) {
				progress("Reconnecting to the glasses")
				if err := acquire(ctx, nil); err != nil {
					if ctx.Err() != nil {
						continue
					}
					retryDelay = min(2*retryDelay, reconnectMax)
					retryAt = time.Now().Add(retryDelay)
					progress("Reconnect failed: %v; next try in %s", err, retryDelay)
				} else {
					retryAt, retryDelay = time.Time{}, reconnectFirst
					progress("Glasses reconnected: %d arms", len(d.s.arms))
				}
				continue
			}
			if d.s != nil {
				if err := d.events(&redraw, &next, rotation); err != nil {
					return err
				}
			}
			if minute := now.Format(musicStatusFormat); minute != lastClockMinute {
				lastClockMinute = minute
				redraw = true
			}
			if d.s != nil {
				scr := d.s.screen
				statusWidth := scr.w - scr.footerSplit() - 2*textInset
				if d.music.advance(now, statusWidth, func(text string) int { return scr.footerFont.Measure(text).Width }) {
					redraw = true
				}
			}
			if d.popup.expire(now) {
				next = now.Add(rotation)
				redraw = true
				if !d.nav.visible() && d.nav.popupVisible() {
					d.hide() // head level: back to off
				}
				progress("Notification popup ended; dashboard restored")
			}
			if !d.nav.paused && d.nav.visible() && !d.popup.active() && now.After(next) {
				d.nav.move(1)
				d.snapshots.edited()
				d.restorePage = nil
				next = time.Now().Add(rotation)
				redraw = true
			}
		}
		if redraw {
			d.render()
		}
		publishSnapshot()
	}
}

// events handles the session's arm events: link back, resets, case changes
// and touches. An arm's program error or halt is fatal.
func (d *dashState) events(redraw *bool, next *time.Time, rotation time.Duration) error {
	s := d.s
	for _, arm := range s.arms {
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
				syncStandbyClock(arm, s.standby)
				s.screen.hide(s.arms) // flushes black, so the next render sends every pixel again
				*redraw = true
				continue
			}
			if e.Type == scouterkit.EventReset {
				progress("%s arm session reset; redrawing dashboard", arm)
				*redraw = true
			}
			if inCase, ok := caseEvent(e); ok {
				if s.cases[arm] == inCase {
					continue
				}
				wasVisible := d.nav.visible()
				s.cases[arm] = inCase
				d.nav.inCase = s.cases.any()
				progress("%s arm in case: %t", arm, inCase)
				if !d.nav.visible() && wasVisible {
					s.screen.hide(s.arms)
					d.popup = notificationPopup{}
				}
				*next = time.Now().Add(rotation)
				*redraw = true
				continue
			}
			if e.Type != scouterkit.EventInput || e.Trigger != int(scouterkit.TriggerTouch) {
				continue
			}
			if !d.nav.touch(e.A, arm == scouterkit.Left) {
				continue
			}
			d.snapshots.edited()
			d.restorePage = nil
			v := d.nav.current()
			if e.A == 1 || e.A == 2 {
				d.popup = notificationPopup{}
			}
			switch e.A {
			case 1:
				progress("%s arm tap: %s page %d/%d", arm, v.name, v.index+1, len(v.pages))
			case 2:
				progress("Screen switched to %s, page %d/%d", v.name, v.index+1, len(v.pages))
			case 3:
				progress("Page rotation paused: %t", d.nav.paused)
			}
			*next = time.Now().Add(rotation)
			*redraw = true
		}
	}
	return nil
}
