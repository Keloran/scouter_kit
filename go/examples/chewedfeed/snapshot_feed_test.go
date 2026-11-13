package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestSnapshotProjectRefreshIsolation(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var first sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/projects" {
			first.Do(func() {
				close(entered)
				select {
				case <-release:
				case <-r.Context().Done():
				}
			})
			w.Write([]byte(`[{"slug":"alpha","name":"Alpha"},{"slug":"beta","name":"Beta"}]`))
		} else {
			w.Write([]byte(`{}`))
		}
	}))
	defer server.Close()
	c, err := newClient(server.URL, "", "alpha", false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	requests, updates := make(chan apiSelection, 1), make(chan apiUpdate, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		watchDashboardAPI(ctx, c, time.Hour, apiSelection{project: "alpha"}, requests, updates)
	}()
	defer func() { cancel(); <-done }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("fetch did not start")
	}
	selection := apiSelection{project: "beta", generation: 1}
	selectDashboardProject(requests, apiSelection{project: "missing", generation: 1})
	selectDashboardProject(requests, selection) // latest selection replaces queued work
	close(release)
	var old, current apiUpdate
	select {
	case old = <-updates:
	case <-ctx.Done():
		t.Fatal("old fetch did not finish")
	}
	select {
	case current = <-updates:
	case <-ctx.Done():
		t.Fatal("project change did not trigger refresh")
	}
	if old.selection == selection || current.selection != selection {
		t.Fatal("refresh generations were lost")
	}
	if c.project != "alpha" {
		t.Fatal("worker mutated the shared client")
	}
	for _, r := range current.data {
		if r.key == "projects" && (len(r.rows) != 1 || r.rows[0]["slug"] != "beta") {
			t.Fatalf("old project rows leaked: %+v", r.rows)
		}
	}
	selectDashboardProject(requests, apiSelection{project: "missing", generation: 2})
	select {
	case missing := <-updates:
		if !missingSnapshotProject(missing.data) {
			t.Fatal("unknown project did not expose fallback")
		}
		for _, r := range missing.data {
			if r.key == "projects" && len(r.rows) != 0 {
				t.Fatal("retained a previous project's cache")
			}
		}
	case <-ctx.Done():
		t.Fatal("missing project did not refresh")
	}
}
