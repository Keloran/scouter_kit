package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPublicFetchAndStaleRecovery(t *testing.T) {
	var failing atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Cookie") != "" {
			t.Error("public client must send only unauthenticated GETs")
		}
		if failing.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, "private upstream failure details")
			return
		}
		switch r.URL.Path {
		case "/stats":
			fmt.Fprint(w, `{"projects":2,"score":72,"pull_requests":3}`)
		case "/api/projects":
			fmt.Fprint(w, `[{"slug":"healthy","name":"Healthy","attention_mode":"grow"},{"slug":"broken","name":"Broken","attention_mode":"fix","uptime_kuma_slug":"broken"}]`)
		case "/api/projects/broken/uptime":
			fmt.Fprint(w, `{"overall_status":"down","monitors":[{"name":"Web","status":"down"}]}`)
		default:
			t.Errorf("unexpected public endpoint: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c, err := newClient(srv.URL, "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	first := c.fetch(context.Background(), nil)
	if len(first) != 3 || first[1].rows[0]["slug"] != "broken" {
		t.Fatalf("missing details or attention ordering: %#v", first)
	}
	failing.Store(true)
	stale := c.fetch(context.Background(), first)
	for _, r := range stale {
		if r.err == "" || r.at.IsZero() || len(r.rows) == 0 {
			t.Errorf("lost cached data or stale marker: %#v", r)
		}
	}
	if strings.Contains(warnings(stale), "upstream") {
		t.Fatal("server response body leaked into warnings")
	}
	failing.Store(false)
	for _, r := range c.fetch(context.Background(), stale) {
		if r.err != "" {
			t.Errorf("stale marker survived recovery: %s", r.err)
		}
	}
}

func TestPrivateViewsUseSessionCookieAndProjectFilter(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		cookie, err := r.Cookie("session")
		if err != nil || cookie.Value != "test-session" || r.Method != http.MethodGet {
			t.Error("expected read-only session authentication")
		}
		switch r.URL.Path {
		case "/stats":
			fmt.Fprint(w, `{}`)
		case "/api/projects":
			fmt.Fprint(w, `[{"slug":"one","name":"One","linear_project_id":"l","github_owner":"org","railway_project_id":"r","uptime_kuma_slug":"one"},{"slug":"two","name":"Two"}]`)
		case "/api/product-customers":
			fmt.Fprint(w, `[{"product_slug":"two"},{"product_slug":"one"}]`)
		case "/api/projects/one/uptime":
			fmt.Fprint(w, `{"overall_status":"up","uptime_24h":1}`)
		default:
			t.Errorf("unexpected endpoint: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c, err := newClient(srv.URL, "test-session", "one", true)
	if err != nil {
		t.Fatal(err)
	}
	data := c.fetch(context.Background(), nil)
	if calls.Load() != 4 || len(data) != 4 {
		t.Fatalf("missing private views: %d calls, %d results", calls.Load(), len(data))
	}
	for _, r := range data {
		if r.err != "" {
			t.Error(r.err)
		}
		if r.key == "customers" {
			if len(r.rows) != 1 || r.rows[0]["product_slug"] != "one" {
				t.Errorf("project filter failed: %#v", r.rows)
			}
		}
	}
}

func TestResponseValidationAndRedirect(t *testing.T) {
	var destinationCalls atomic.Int32
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		destinationCalls.Add(1)
	}))
	defer dest.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, dest.URL, http.StatusFound)
		case "/null":
			fmt.Fprint(w, `null`)
		case "/wrong":
			fmt.Fprint(w, `{}`)
		case "/empty":
			fmt.Fprint(w, `[]`)
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer srv.Close()
	c, _ := newClient(srv.URL, "test-session", "", false)
	for _, path := range []string{"/redirect", "/null", "/wrong", "/denied"} {
		r := c.get(context.Background(), source{path: path, list: true})
		if r.err == "" || !r.at.IsZero() {
			t.Errorf("accepted invalid response for %s: %#v", path, r)
		}
	}
	if destinationCalls.Load() != 0 {
		t.Fatal("followed redirect carrying a credential")
	}
	empty := c.get(context.Background(), source{key: "empty", path: "/empty", list: true})
	empty = retain(empty, snapshot{{source: source{key: "empty"}, rows: []record{{"name": "old"}}, at: time.Now()}})
	if empty.err != "" || len(empty.rows) != 0 {
		t.Fatal("a successful empty response must clear cached rows")
	}
}

func TestConfigValidation(t *testing.T) {
	for _, base := range []string{"", "ftp://example.com", "https://user:password@example.com", "https://example.com?token=secret", "http://example.com"} {
		if _, err := newClient(base, "test-session", "", false); err == nil {
			t.Errorf("accepted unsafe URL: %s", base)
		}
	}
	if _, err := newClient("https://example.com", "bad;cookie", "", false); err == nil {
		t.Fatal("accepted cookie injection")
	}
}
