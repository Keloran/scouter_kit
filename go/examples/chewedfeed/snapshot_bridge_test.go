package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func snapshotBody(t *testing.T, s uiSnapshot) []byte {
	t.Helper()
	b, err := json.Marshal(struct {
		Snapshot uiSnapshot `json:"snapshot"`
	}{s})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSnapshotBridgeCommitAndFences(t *testing.T) {
	b := newSnapshotBridge()
	nav := snapshotNav()
	s := exportSnapshot(&nav, "alpha", 12*time.Second, 4)
	body := snapshotBody(t, s)
	if b.receive("session-a", "t1", body) {
		t.Fatal("unsolicited snapshot")
	}
	b.requested("session-a", "t1")
	for _, bad := range []struct {
		session, transfer string
		body              []byte
	}{
		{"session-b", "t1", body}, {"session-a", "t2", body},
		{"session-a", "t1", []byte(`{"snapshot":{}}`)},
		{"session-a", "t1", []byte(`{"snapshot":{},"extra":true}`)},
		{"session-a", "t1", []byte(strings.Repeat(" ", maxSnapshotBytes+33))},
	} {
		if b.receive(bad.session, bad.transfer, bad.body) {
			t.Fatal("invalid snapshot accepted")
		}
	}
	if !b.receive("session-a", "t1", body) {
		t.Fatal("valid snapshot rejected")
	}
	if _, ok := b.take(); ok {
		t.Fatal("applied before release proof")
	}
	if b.receive("session-a", "t1", body) {
		t.Fatal("replay accepted")
	}
	s.Revision++
	if !b.receive("session-a", "t1", snapshotBody(t, s)) {
		t.Fatal("newer snapshot rejected")
	}
	b.finish(true)
	got, ok := b.take()
	if !ok || got.Revision != s.Revision {
		t.Fatalf("committed: %+v %t", got, ok)
	}
	if _, ok := b.take(); ok {
		t.Fatal("applied twice")
	}
	s.Revision++
	if b.receive("session-a", "t1", snapshotBody(t, s)) {
		t.Fatal("late snapshot accepted after proof")
	}

	// An edit after the request, including one after staging, always wins.
	b.requested("session-a", "t2")
	if !b.receive("session-a", "t2", body) {
		t.Fatal("staging failed")
	}
	b.edited()
	b.finish(true)
	if _, ok := b.take(); ok {
		t.Fatal("overwrote a local edit")
	}
	b.requested("session-a", "t3")
	b.edited()
	if b.receive("session-a", "t3", body) {
		t.Fatal("accepted after a local edit")
	}

	// Connection retirement drops staged state, even after proof was queued.
	b.requested("session-a", "t4")
	if !b.receive("session-a", "t4", body) {
		t.Fatal("staging failed")
	}
	b.finish(true)
	b.finish(false)
	if _, ok := b.take(); ok {
		t.Fatal("retired connection changed UI")
	}
	b.requested("session-b", "t5")
	if b.receive("session-a", "t5", body) {
		t.Fatal("old session accepted")
	}
	s.Revision = 1
	if !b.receive("session-b", "t5", snapshotBody(t, s)) {
		t.Fatal("new session's revision rejected")
	}
}

func TestSnapshotBridgeCacheAndDisabledRotation(t *testing.T) {
	b := newSnapshotBridge()
	nav := snapshotNav()
	if _, ok := b.export(); ok {
		t.Fatal("exported uninitialized state")
	}
	s := exportSnapshot(&nav, "alpha", 12*time.Second, 0)
	b.publish(s)
	first, ok := b.export()
	if !ok || first.Revision == 0 {
		t.Fatal("no initial revision")
	}
	b.publish(s)
	unchanged, _ := b.export()
	if unchanged.Revision != first.Revision {
		t.Fatal("poll incremented revision")
	}
	nav.move(1)
	b.edited()
	b.publish(exportSnapshot(&nav, "alpha", 12*time.Second, 0))
	changed, _ := b.export()
	if changed.Revision != first.Revision+1 || changed.PageID == first.PageID {
		t.Fatal("navigation not published")
	}
	for _, local := range []snapshotOverrides{{}, {rotate: true}} {
		s.RotationPaused = false
		s.RotationMs = 0
		rotation, _ := s.apply(&nav, local, 0, "")
		if rotation != 0 || !nav.paused {
			t.Fatal("zero rotation was enabled by a snapshot")
		}
	}
}
