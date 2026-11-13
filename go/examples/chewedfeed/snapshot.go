package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"
	"unicode/utf8"
)

// uiSnapshot is the navigation state handed to the next owner of the glasses
// (tests/handover protocol.schema.json "snapshot", policy.json "snapshot"):
// the view, page and project, and rotation. Geometry, font scale, credentials,
// addresses, paths, notification contents and API data stay on each device.
type uiSnapshot struct {
	SchemaVersion  int     `json:"schema_version"`
	Revision       uint64  `json:"revision"`
	View           string  `json:"view"`
	PageID         string  `json:"page_id"`
	Project        *string `json:"project"`
	RotationPaused bool    `json:"rotation_paused"`
	RotationMs     int64   `json:"rotation_ms"`
}

const (
	snapshotVersion  = 1
	maxSnapshotBytes = 8192
	maxPageID        = 128
	maxRotationMs    = 3600000
	notificationPage = "notifications/empty" // notification history never leaves the device
)

var (
	snapshotViews = map[int]string{viewSites: "sites", viewAI: "ai", viewNotifications: "notifications", viewSystem: "stats"}
	snapshotPage  = regexp.MustCompile(`^[A-Za-z0-9_:/-]+$`)
	projectSlug   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
)

// exportSnapshot describes nav for the next owner; revision counts this host's
// navigation changes within its current peer session.
func exportSnapshot(nav *navigation, project string, rotate time.Duration, revision uint64) uiSnapshot {
	s := uiSnapshot{SchemaVersion: snapshotVersion, Revision: revision, View: snapshotViews[nav.selected],
		RotationPaused: nav.paused, RotationMs: min(rotate.Milliseconds(), maxRotationMs)}
	if s.RotationMs > 0 && s.RotationMs < 1000 {
		s.RotationMs = 1000
	}
	if projectSlug.MatchString(project) {
		s.Project = &project
	}
	v := nav.current()
	switch {
	case nav.selected == viewNotifications:
		s.PageID = notificationPage
	case v.index < len(v.pages) && validPageID(v.pages[v.index].id):
		s.PageID = v.pages[v.index].id
	case len(v.pages) > 0 && validPageID(v.pages[0].id):
		s.PageID = v.pages[0].id
	default:
		s.PageID = "summary"
	}
	return s
}

func validPageID(id string) bool {
	return utf8.RuneCountInString(id) <= maxPageID && snapshotPage.MatchString(id)
}

func (s uiSnapshot) validate() error {
	switch {
	case s.SchemaVersion != snapshotVersion:
		return fmt.Errorf("snapshot version %d", s.SchemaVersion)
	case !slices.Contains([]string{"sites", "ai", "notifications", "stats"}, s.View):
		return errors.New("unknown snapshot view")
	case !validPageID(s.PageID):
		return errors.New("invalid snapshot page")
	case s.View == "notifications" && s.PageID != notificationPage:
		return errors.New("notification history is local")
	case s.Project != nil && !projectSlug.MatchString(*s.Project):
		return errors.New("invalid snapshot project")
	case s.RotationMs < 0 || s.RotationMs > maxRotationMs || s.RotationMs > 0 && s.RotationMs < 1000:
		return errors.New("invalid snapshot rotation")
	case s.Revision > 1<<53-1:
		return errors.New("invalid snapshot revision")
	}
	return nil
}

func (s uiSnapshot) encode() ([]byte, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	return json.Marshal(s)
}

// decodeSnapshot accepts only a complete, valid snapshot with nothing else in it.
func decodeSnapshot(b []byte) (uiSnapshot, error) {
	var s uiSnapshot
	if len(b) > maxSnapshotBytes {
		return s, errors.New("snapshot too large")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		return s, errors.New("corrupted snapshot")
	}
	for _, name := range []string{"schema_version", "revision", "view", "page_id", "project", "rotation_paused", "rotation_ms"} {
		if _, ok := fields[name]; !ok {
			return s, fmt.Errorf("snapshot without %s", name)
		}
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields() // geometry, credentials, notifications... are not snapshot fields
	if err := dec.Decode(&s); err != nil {
		return uiSnapshot{}, errors.New("corrupted snapshot")
	}
	if err := s.validate(); err != nil {
		return uiSnapshot{}, err
	}
	return s, nil
}

// snapshotOverrides: settings given explicitly on this device win over a snapshot.
type snapshotOverrides struct {
	rotate, project bool
}

// apply selects the snapshot's view and page on nav. A page that no longer
// exists falls back to the view's first page; a view without pages to the
// first Sites page. It returns the rotation and project to use.
func (s uiSnapshot) apply(nav *navigation, local snapshotOverrides, rotate time.Duration, project string) (time.Duration, string) {
	s.selectPage(nav)
	nav.paused = s.RotationPaused
	if !local.rotate {
		rotate = time.Duration(s.RotationMs) * time.Millisecond
	}
	if rotate == 0 {
		nav.paused = true
	}
	if !local.project {
		project = ""
		if s.Project != nil {
			project = *s.Project
		}
	}
	return rotate, project
}

func (s uiSnapshot) selectPage(nav *navigation) {
	view := viewSites
	for v, name := range snapshotViews {
		if name == s.View {
			view = v
		}
	}
	if view >= len(nav.views) || len(nav.views[view].pages) == 0 {
		view = viewSites
	}
	nav.selected = view
	if v := &nav.views[view]; len(v.pages) > 0 {
		v.index = preservePage(v.pages, s.PageID)
	}
}

// snapshotGate decides whether an incoming snapshot may change navigation:
// only one for the transfer this host requested, from the sender session that
// is releasing, newer than any already taken from that session, and only if
// the user changed nothing here since the request (revisions of different
// sessions or hosts are never compared).
type snapshotGate struct {
	transfer string
	edits    uint64 // local navigation edits when the request went out
	session  string
	revision uint64
	taken    bool
}

// requested: this host asked for transfer while its edit counter was at edits.
func (g *snapshotGate) requested(transfer string, edits uint64) {
	*g = snapshotGate{transfer: transfer, edits: edits}
}

func (g *snapshotGate) accept(session, transfer string, s uiSnapshot, edits uint64) error {
	switch {
	case g.transfer == "" || transfer != g.transfer:
		return errors.New("snapshot for another transfer")
	case edits != g.edits:
		return errors.New("navigation changed here since the request")
	case g.taken && session == g.session && s.Revision <= g.revision:
		return errors.New("older or repeated snapshot")
	}
	g.session, g.revision, g.taken = session, s.Revision, true
	return nil
}
