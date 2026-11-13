//go:build emulator

package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"scouterkit"
	"scouterkit/examples/internal/demo"
	h "scouterkit/examples/internal/handover"
	"scouterkit/examples/internal/localcontrol"
)

type ownershipPipeRoute struct{ conn net.Conn }

func (r ownershipPipeRoute) Dial(context.Context) (net.Conn, error) { return r.conn, nil }

func TestOwnershipAuthenticatedExchange(t *testing.T) {
	laptop, err := h.OpenStore(filepath.Join(t.TempDir(), "laptop"))
	if err != nil {
		t.Fatal(err)
	}
	phone, err := h.OpenStore(filepath.Join(t.TempDir(), "phone"))
	if err != nil {
		t.Fatal(err)
	}
	if err := laptop.AddPeer(h.Peer{ID: phone.Identity().ID, Role: h.Phone}); err != nil {
		t.Fatal(err)
	}
	if err := phone.AddPeer(h.Peer{ID: laptop.Identity().ID, Role: h.Laptop}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	accepted := make(chan *h.Conn, 1)
	acceptErr := make(chan error, 1)
	go func() {
		c, err := h.Accept(ctx, right, phone, nil, h.Options{Role: h.Phone, Optional: []string{}})
		if err != nil {
			acceptErr <- err
		} else {
			accepted <- c
		}
	}()
	conn, err := h.Dial(ctx, laptop, ownershipPipeRoute{left}, phone.Identity().ID, h.Options{Role: h.Laptop, Optional: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var remote *h.Conn
	select {
	case remote = <-accepted:
	case err := <-acceptErr:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	defer remote.Close()
	n := &ownershipTestNative{}
	c, power, clock := startOwnershipTest(t, n, true, false)
	forwardDone := make(chan struct{})
	go func() { defer close(forwardDone); c.serveLink(ctx, conn) }()
	defer func() { cancel(); conn.Close(); <-forwardDone }()
	oldTransfer := "11111111111111111111111111111111"
	status := h.StatusBody{Role: h.Phone, Phase: h.Active, Eligible: true, Arms: h.Arms{Left: true, Right: true}, RendererReady: true, ReadyEpoch: 1, Transfer: &oldTransfer}
	if err := remote.Send("status", 1, "", status); err != nil {
		t.Fatal(err)
	}
	awaitOwnership(t, c, func(s localcontrol.Status) bool { return s.Owner == "peer" })
	power <- powerEvent{Kind: powerLid, State: powerState{Lid: lidOpen}}
	awaitOwnership(t, c, func(s localcontrol.Status) bool { return s.Eligible })
	if err := remote.Send("status", 1, "", status); err != nil {
		t.Fatal(err)
	}
	awaitOwnership(t, c, func(s localcontrol.Status) bool { return s.Phase == string(h.Idle) })
	clock.advance(h.LaptopStabilityMs * time.Millisecond)
	readKind := func(kind string) h.Message {
		t.Helper()
		for {
			select {
			case m, ok := <-remote.Recv():
				if !ok {
					t.Fatalf("TLS stream ended: %v", remote.Err())
				}
				if m.Kind == kind {
					return m
				}
			case <-ctx.Done():
				t.Fatalf("no %s: %v", kind, ctx.Err())
			}
		}
	}
	request := readKind("request")
	if request.Transfer == nil {
		t.Fatal("request without transfer")
	}
	transfer := *request.Transfer
	if err := remote.Send("release", 2, transfer, h.ReleaseBody{Target: h.Laptop, Reason: "preference"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if a, _, _ := n.counts(); a != 0 {
		t.Fatal("release notice was treated as completed teardown")
	}
	if err := remote.Send("released", 2, transfer, h.ReleasedBody{Target: h.Laptop, WorkersStopped: true, ReconnectDisabled: true}); err != nil {
		t.Fatal(err)
	}
	ready := readKind("ready")
	var body h.ReadyBody
	if err := json.Unmarshal(ready.Body, &body); err != nil {
		t.Fatal(err)
	}
	if !body.RendererReady || !body.Arms.Left || !body.Arms.Right || ready.Transfer == nil || *ready.Transfer != transfer {
		t.Fatalf("premature or unfenced ready: %+v", ready)
	}
	awaitOwnership(t, c, func(s localcontrol.Status) bool { return s.Phase == string(h.Active) })
}

func awaitNativeOwnership(t *testing.T, c *ownershipController, phase h.Phase) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if c.Status().Phase == string(phase) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("native dashboard did not reach %s: %+v", phase, c.Status())
}

func TestOwnershipDashboardAndLocalControls(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	o := sessionOptions()
	o.managed = true
	host := &glassesHost{o: o}
	client := fakeClient(t, `[{"id":"one","slug":"one","name":"Site"}]`)
	commands := make(chan hostCommand)
	dashCtx, stopDash := context.WithCancel(context.Background())
	dashDone := make(chan struct{})
	var dashErr error
	go func() {
		defer close(dashDone)
		dashErr = dashboard(dashCtx, host, o, client, commands)
	}()
	power := make(chan powerEvent, 8)
	power <- powerEvent{Kind: powerLid, State: powerState{Lid: lidClosed}}
	c := newOwnershipController(dashboardNative{host, commands, dashDone}, power, false)
	ctx, cancel := context.WithCancel(context.Background())
	go c.run(ctx)
	t.Cleanup(func() {
		cancel()
		select {
		case <-c.done:
		case <-time.After(10 * time.Second):
			t.Error("controller teardown did not join")
		}
		stopDash()
		select {
		case <-dashDone:
			if dashErr != nil {
				t.Error(dashErr)
			}
		case <-time.After(10 * time.Second):
			t.Error("dashboard workers did not stop")
		}
	})
	path := filepath.Join(t.TempDir(), "control.sock")
	server, err := localcontrol.Listen(path, c)
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve() }()
	t.Cleanup(func() {
		server.Close()
		if err := <-serveDone; err != nil {
			t.Error(err)
		}
	})
	if s := host.Status(); s.State != hostReleased {
		t.Fatal("closed startup acquired glasses")
	}
	power <- powerEvent{Kind: powerLid, State: powerState{Lid: lidOpen}}
	awaitNativeOwnership(t, c, h.Active)
	for _, arm := range []scouterkit.Arm{scouterkit.Left, scouterkit.Right} {
		if s := arm.State(); !s.Connected || s.ShownUpdate == 0 {
			t.Fatalf("ready before full frame on %s: %+v", arm, s)
		}
	}
	for _, command := range []string{localcontrol.CmdRelease, localcontrol.CmdResume} {
		commandCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		response, err := localcontrol.Do(commandCtx, path, localcontrol.Request{Command: command})
		stop()
		if err != nil || response.Result != localcontrol.ResultDone {
			t.Fatalf("%s: %+v %v", command, response, err)
		}
		if command == localcontrol.CmdRelease {
			if s := host.Status(); s.State != hostReleased || s.Arms != 0 || s.LeftReady || s.RightReady {
				t.Fatalf("release acknowledged while held: %+v", s)
			}
		} else if s := host.Status(); s.Generation != 2 || !s.LeftReady || !s.RightReady {
			t.Fatalf("resume reused a session or skipped render: %+v", s)
		}
	}
	power <- powerEvent{Kind: powerLid, State: powerState{Lid: lidClosed}}
	awaitNativeOwnership(t, c, h.Idle)
}

func TestOwnershipPreviewSkipsAllConnections(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	output, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	previousOutput := os.Stdout
	os.Stdout = output
	defer func() { os.Stdout = previousOutput; output.Close() }()
	previous := http.DefaultTransport
	http.DefaultTransport = systemTestTransport(func(r *http.Request) (*http.Response, error) {
		body := `{"projects":2,"customers":2}`
		if r.URL.Path == "/api/projects" {
			body = `[]`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	defer func() { http.DefaultTransport = previous }()
	o := sessionOptions()
	o.preview, o.calibrate = true, true
	o.api = "https://example.invalid"
	o.ownership = ownershipSettings{Enabled: true, Endpoint: "phone.invalid:4321", PeerID: strings.Repeat("a", 64), Store: filepath.Join(t.TempDir(), "peers"), Socket: filepath.Join(t.TempDir(), "control.sock")}
	if err := run(demo.Flags{Left: "none", Right: "none"}, o); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{o.ownership.Store, o.ownership.Socket} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("preview created control or enrollment state: %v", err)
		}
	}
	if scouterkit.Left.State().Connected || scouterkit.Right.State().Connected {
		t.Fatal("preview connected glasses")
	}
}
