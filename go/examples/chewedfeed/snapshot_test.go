package main

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func snapshotNav() navigation {
	sites := []page{{id: "summary"}, {id: "projects"}, {id: "site-one"}, {id: "site-two"}}
	ai := []page{{id: "codex"}, {id: "claude"}}
	system := []page{{id: "system/cpu"}, {id: "system/memory"}}
	return newNavigation(sites, ai, system, false)
}

func TestSnapshotRoundTrip(t *testing.T) {
	from := snapshotNav()
	from.selected = viewAI
	from.views[viewAI].index = 1
	from.paused = true
	b, err := exportSnapshot(&from, "alpha", 15*time.Second, 7).encode()
	if err != nil {
		t.Fatal(err)
	}
	s, err := decodeSnapshot(b)
	if err != nil {
		t.Fatal(err)
	}
	to := snapshotNav()
	rotate, project := s.apply(&to, snapshotOverrides{}, 12*time.Second, "")
	if to.selected != viewAI || to.views[viewAI].index != 1 || !to.paused || rotate != 15*time.Second || project != "alpha" {
		t.Fatalf("after apply: view %d page %d paused %t rotate %s project %q", to.selected, to.views[viewAI].index, to.paused, rotate, project)
	}
	// Notification history stays local: only the empty page travels.
	from.selected = viewNotifications
	from.views[viewNotifications].pages = []page{{id: "notifications/4/0"}, {id: "notifications/3/0"}}
	from.views[viewNotifications].index = 1
	if s := exportSnapshot(&from, "", 0, 8); s.PageID != notificationPage || s.validate() != nil {
		t.Fatalf("notifications exported as %+v", s)
	}
}

// Only the allowed fields leave the device, whatever the local state holds.
func TestSnapshotPrivacy(t *testing.T) {
	var policy struct {
		Snapshot struct{ Allow, Forbid []string }
	}
	b, err := os.ReadFile("../../../tests/handover/policy.json")
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(b, &policy)
	nav := snapshotNav()
	nav.views[viewSites].pages[0].lines = []string{"Customers: 12"} // API data on the page
	out, err := exportSnapshot(&nav, "", time.Second, 1).encode()
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	json.Unmarshal(out, &fields)
	for k := range fields {
		if !slices.Contains(policy.Snapshot.Allow, k) {
			t.Errorf("exported %q", k)
		}
	}
	if len(fields) != len(policy.Snapshot.Allow) || strings.Contains(string(out), "Customers") {
		t.Errorf("snapshot %s", out)
	}
}

func TestSnapshotSharedFixtures(t *testing.T) {
	var f struct {
		Valid, Invalid []struct {
			Name    string
			Message struct {
				Body struct{ Snapshot json.RawMessage }
			}
		}
	}
	b, err := os.ReadFile("../../../tests/handover/messages.json")
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(b, &f)
	n := 0
	for _, c := range f.Valid {
		if strings.HasPrefix(c.Name, "snapshot") {
			if _, err := decodeSnapshot(c.Message.Body.Snapshot); err != nil {
				t.Errorf("%s: %v", c.Name, err)
			}
			n++
		}
	}
	for _, c := range f.Invalid {
		if strings.HasPrefix(c.Name, "snapshot") {
			if _, err := decodeSnapshot(c.Message.Body.Snapshot); err == nil {
				t.Errorf("%s accepted", c.Name)
			}
			n++
		}
	}
	if n < 7 {
		t.Fatalf("only %d snapshot fixtures", n)
	}
}

func TestSnapshotRejects(t *testing.T) {
	good := `{"schema_version":1,"revision":3,"view":"sites","page_id":"summary","project":null,"rotation_paused":false,"rotation_ms":12000}`
	for name, text := range map[string]string{
		"version 2":     strings.Replace(good, `"schema_version":1`, `"schema_version":2`, 1),
		"missing view":  strings.Replace(good, `"view":"sites",`, ``, 1),
		"unknown view":  strings.Replace(good, `"sites"`, `"settings"`, 1),
		"long page":     strings.Replace(good, `"summary"`, `"`+strings.Repeat("a", 129)+`"`, 1),
		"path in page":  strings.Replace(good, `"summary"`, `"../home/someone"`, 1),
		"bad project":   strings.Replace(good, `"project":null`, `"project":"Has Spaces"`, 1),
		"geometry":      strings.Replace(good, `}`, `,"font_scale":2}`, 1),
		"truncated":     good[:len(good)-5],
		"not an object": `[1,2]`,
		"too large":     strings.Replace(good, `}`, `,"x":"`+strings.Repeat("a", maxSnapshotBytes)+`"}`, 1),
		"negative":      strings.Replace(good, `"rotation_ms":12000`, `"rotation_ms":-1`, 1),
	} {
		if _, err := decodeSnapshot([]byte(text)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := decodeSnapshot([]byte(good)); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotFallbacks(t *testing.T) {
	nav := snapshotNav()
	s := uiSnapshot{SchemaVersion: 1, View: "stats", PageID: "system/gpu"} // a page this host lacks
	s.apply(&nav, snapshotOverrides{}, 0, "")
	if nav.selected != viewSystem || nav.views[viewSystem].index != 0 {
		t.Fatalf("missing page: view %d page %d", nav.selected, nav.views[viewSystem].index)
	}
	nav.views[viewAI].pages = nil // a view with no pages
	uiSnapshot{SchemaVersion: 1, View: "ai", PageID: "claude"}.apply(&nav, snapshotOverrides{}, 0, "")
	if nav.selected != viewSites || nav.views[viewSites].index != 0 {
		t.Fatalf("empty view: view %d page %d", nav.selected, nav.views[viewSites].index)
	}
	// Stale navigation keeps working.
	nav.move(1)
	nav.touch(2, false)
}

func TestSnapshotLocalOverridesWin(t *testing.T) {
	nav := snapshotNav()
	alpha := "alpha"
	s := uiSnapshot{SchemaVersion: 1, View: "sites", PageID: "site-two", Project: &alpha, RotationMs: 30000}
	rotate, project := s.apply(&nav, snapshotOverrides{rotate: true, project: true}, 5*time.Second, "beta")
	if rotate != 5*time.Second || project != "beta" {
		t.Fatalf("local flags lost: %s %q", rotate, project)
	}
	rotate, project = s.apply(&nav, snapshotOverrides{}, 5*time.Second, "beta")
	if rotate != 30*time.Second || project != "alpha" {
		t.Fatalf("snapshot not used: %s %q", rotate, project)
	}
}

func TestSnapshotGate(t *testing.T) {
	var g snapshotGate
	s := uiSnapshot{SchemaVersion: 1, View: "sites", PageID: "summary", Revision: 5}
	if g.accept("sess-a", "t1", s, 0) == nil {
		t.Fatal("accepted without a request")
	}
	g.requested("t1", 3)
	if g.accept("sess-a", "t2", s, 3) == nil {
		t.Fatal("accepted another transfer's snapshot")
	}
	if g.accept("sess-a", "t1", s, 4) == nil {
		t.Fatal("overwrote a local change made after the request")
	}
	if err := g.accept("sess-a", "t1", s, 3); err != nil {
		t.Fatal(err)
	}
	if g.accept("sess-a", "t1", s, 3) == nil {
		t.Fatal("replayed snapshot accepted")
	}
	old := s
	old.Revision = 4
	if g.accept("sess-a", "t1", old, 3) == nil {
		t.Fatal("delayed older snapshot accepted")
	}
	newer := s
	newer.Revision = 6
	if err := g.accept("sess-a", "t1", newer, 3); err != nil {
		t.Fatal(err)
	}
	// A new sender session's revisions are not compared with the old one's.
	low := s
	low.Revision = 1
	if err := g.accept("sess-b", "t1", low, 3); err != nil {
		t.Fatalf("new session: %v", err)
	}
}

// tests/handover/snapshot_vectors.json: decode then encode gives the same bytes (the Kotlin side checks the same).
func TestSnapshotCanonicalVectors(t *testing.T) {
	var f struct{ Canonical []string }
	b, err := os.ReadFile("../../../tests/handover/snapshot_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &f); err != nil || len(f.Canonical) < 4 {
		t.Fatalf("vectors: %v", err)
	}
	for _, c := range f.Canonical {
		s, err := decodeSnapshot([]byte(c))
		if err != nil {
			t.Fatalf("%s: %v", c, err)
		}
		out, err := s.encode()
		if err != nil || string(out) != c {
			t.Fatalf("re-encoded %s as %s (%v)", c, out, err)
		}
	}
}
