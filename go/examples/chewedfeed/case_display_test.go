//go:build emulator

package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"scouterkit"
	"scouterkit/examples/internal/demo"
)

type systemTestTransport func(*http.Request) (*http.Response, error)

func (f systemTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDashboardCaseEntryStaysOffAcrossUpdatesAndResumes(t *testing.T) {
	c, err := newClient("https://example.com", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	c.http.Transport = systemTestTransport(func(r *http.Request) (*http.Response, error) {
		body := `{"projects":1,"customers":2,"paid_customers":1}`
		if r.URL.Path == "/api/projects" {
			body = `[{"id":"one","slug":"one","name":"Site"}]`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- display(ctx, demo.Flags{}, options{dx: -10, dy: -8, fontScale: 1.5, refresh: 5 * time.Minute, aiRefresh: 30 * time.Second, rotate: 0}, c)
	}()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("dashboard: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("dashboard did not stop")
		}
	}()
	waitPanel := func(want uint32) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			ready := true
			for _, arm := range []scouterkit.Arm{scouterkit.Left, scouterkit.Right} {
				p, err := arm.Props(scouterkit.PropPanel)
				if err != nil || len(p) != 1 || p[0] != want {
					ready = false
				}
			}
			if ready {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("panels did not reach power state %d", want)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	waitPanel(1)
	scouterkit.Right.SetEmulatedProperty(scouterkit.PropInCase, 1)
	waitPanel(0)
	// System updates continue every two seconds; none may relight the displays.
	time.Sleep(2200 * time.Millisecond)
	waitPanel(0)
	scouterkit.Right.SetEmulatedProperty(scouterkit.PropInCase, 0)
	waitPanel(1)
}
