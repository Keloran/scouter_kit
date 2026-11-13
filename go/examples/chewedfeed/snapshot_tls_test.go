//go:build emulator

package main

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	h "scouterkit/examples/internal/handover"
	"scouterkit/examples/internal/localcontrol"
)

// Authenticated TLS with an intentionally raw peer, so faults can reach the
// actual receiver instead of being rejected by Conn.Send at the test sender.
type snapshotWirePeer struct {
	tc       *tls.Conn
	out      h.Outbox
	messages chan h.Message
	done     chan struct{}
}

func snapshotTLS(t *testing.T, optional []string) (context.Context, *h.Conn, *snapshotWirePeer) {
	t.Helper()
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
	left, right := net.Pipe()
	peer := &snapshotWirePeer{tc: tls.Server(right, phone.ServerTLS()), messages: make(chan h.Message, 32), done: make(chan struct{})}
	ready := make(chan error, 1)
	go func() {
		defer close(peer.done)
		defer close(peer.messages)
		if err := peer.tc.HandshakeContext(ctx); err != nil {
			ready <- err
			return
		}
		hello, err := h.ReadFrame(peer.tc)
		if err != nil {
			ready <- err
			return
		}
		peer.out = h.Outbox{Sender: phone.Identity().ID, Recipient: laptop.Identity().ID, Session: h.NewNonce()}
		m, err := peer.out.Next("hello", 0, "", h.HelloBody{Role: h.Phone, RequiredCapabilities: h.RequiredCapabilities, OptionalCapabilities: optional})
		if err == nil {
			err = h.WriteFrame(peer.tc, m)
		}
		peer.out.PeerSession = hello.Session
		ready <- err
		if err != nil {
			return
		}
		for {
			m, err := h.ReadFrame(peer.tc)
			if err != nil {
				return
			}
			select {
			case peer.messages <- m:
			case <-ctx.Done():
				return
			}
		}
	}()
	conn, err := h.Dial(ctx, laptop, ownershipPipeRoute{left}, phone.Identity().ID, h.Options{Role: h.Laptop, Optional: []string{h.CapSnapshot}})
	if err != nil {
		cancel()
		left.Close()
		right.Close()
		t.Fatal(err)
	}
	if err := <-ready; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		conn.Close()
		left.Close()
		right.Close()
		peer.tc.Close()
		<-peer.done
	})
	return ctx, conn, peer
}

func (p *snapshotWirePeer) message(t *testing.T, kind, transfer string, body any) h.Message {
	t.Helper()
	m, err := p.out.Next(kind, 2, transfer, body)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(m)
	if _, err := h.DecodeMessage(payload); err != nil {
		t.Fatalf("invalid test fixture: %v", err)
	}
	return m
}

func (p *snapshotWirePeer) write(t *testing.T, m h.Message) {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	frame := make([]byte, 4, 4+len(b))
	binary.BigEndian.PutUint32(frame, uint32(len(b)))
	p.tc.SetWriteDeadline(time.Now().Add(time.Second))
	if _, err := p.tc.Write(append(frame, b...)); err != nil {
		t.Fatal(err)
	}
}

func (p *snapshotWirePeer) read(t *testing.T, ctx context.Context, kind string) h.Message {
	t.Helper()
	for {
		select {
		case m, ok := <-p.messages:
			if !ok {
				t.Fatalf("peer ended before %s", kind)
			}
			if m.Kind == kind {
				return m
			}
		case <-ctx.Done():
			t.Fatalf("no %s: %v", kind, ctx.Err())
		}
	}
}

func requestSnapshotOwnership(t *testing.T, bridge *snapshotBridge, conn *h.Conn, peer *snapshotWirePeer, ctx context.Context, n *ownershipTestNative) (*ownershipController, string) {
	t.Helper()
	c, power, clock := startOwnershipTest(t, n, true, false, func(c *ownershipController) { c.snapshots = bridge })
	forwardDone := make(chan struct{})
	go func() { defer close(forwardDone); c.serveLink(ctx, conn) }()
	t.Cleanup(func() { conn.Close(); <-forwardDone })
	previousTransfer := h.NewNonce()
	status := h.StatusBody{Role: h.Phone, Phase: h.Active, Eligible: true, Arms: h.Arms{Left: true, Right: true}, RendererReady: true, ReadyEpoch: 2, Transfer: &previousTransfer}
	peer.write(t, peer.message(t, "status", "", status))
	awaitOwnership(t, c, func(s localcontrol.Status) bool { return s.Owner == "peer" })
	power <- powerEvent{Kind: powerLid, State: powerState{Lid: lidOpen}}
	awaitOwnership(t, c, func(s localcontrol.Status) bool { return s.Eligible })
	peer.write(t, peer.message(t, "status", "", status))
	awaitOwnership(t, c, func(s localcontrol.Status) bool { return s.Phase == string(h.Idle) })
	clock.advance(h.LaptopStabilityMs * time.Millisecond)
	m := peer.read(t, ctx, "request")
	if m.Transfer == nil {
		t.Fatal("request without transfer")
	}
	return c, *m.Transfer
}

func TestOwnershipSnapshotTLSRoundTrip(t *testing.T) {
	for _, mode := range []string{"negotiated", "required-only", "local-edit", "missing"} {
		t.Run(mode, func(t *testing.T) {
			optional := []string{h.CapSnapshot}
			if mode == "required-only" {
				optional = []string{}
			}
			ctx, conn, peer := snapshotTLS(t, optional)
			bridge := newSnapshotBridge()
			nav := snapshotNav()
			bridge.publish(exportSnapshot(&nav, "", 12*time.Second, 0))
			var applied atomic.Int32
			n := &ownershipTestNative{acquire: func(context.Context) error {
				if s, ok := bridge.take(); ok {
					applied.Add(1)
					bridge.publish(s)
				}
				return nil
			}}
			c, transfer := requestSnapshotOwnership(t, bridge, conn, peer, ctx, n)
			remote := exportSnapshot(&nav, "alpha", 17*time.Second, 7)
			remote.View, remote.PageID, remote.RotationPaused = "stats", "system/cpu", true
			if mode == "negotiated" {
				// A snapshot before the release notice has no effect.
				peer.write(t, peer.message(t, "snapshot", transfer, h.SnapshotBody{Snapshot: h.Snapshot(remote)}))
			}
			peer.write(t, peer.message(t, "release", transfer, h.ReleaseBody{Target: h.Laptop, Reason: "preference"}))
			if mode == "local-edit" {
				bridge.edited()
			}
			if mode != "required-only" && mode != "missing" {
				peer.write(t, peer.message(t, "snapshot", transfer, h.SnapshotBody{Snapshot: h.Snapshot(remote)}))
			}
			peer.write(t, peer.message(t, "released", transfer, h.ReleasedBody{Target: h.Laptop, WorkersStopped: true, ReconnectDisabled: true}))
			peer.read(t, ctx, "ready")
			awaitOwnership(t, c, func(s localcontrol.Status) bool { return s.Phase == string(h.Active) })
			want := int32(0)
			if mode == "negotiated" {
				want = 1
			}
			if applied.Load() != want {
				t.Fatalf("applied %d, want %d", applied.Load(), want)
			}
			if mode == "negotiated" {
				s, _ := bridge.export()
				if s.View != remote.View || s.PageID != remote.PageID || s.Project == nil || *s.Project != "alpha" || s.RotationMs != 17000 || !s.RotationPaused {
					t.Fatalf("state lost: %+v", s)
				}
			}
			// Return to the phone: release notice, optional snapshot, then proof.
			back := h.NewNonce()
			peer.write(t, peer.message(t, "request", back, h.RequestBody{Target: h.Phone, Reason: "manual", Pinned: true}))
			peer.read(t, ctx, "release")
			seen := false
			for {
				select {
				case m, ok := <-peer.messages:
					if !ok {
						t.Fatal("peer closed during return")
					}
					if m.Kind == "snapshot" {
						if seen || m.Transfer == nil || *m.Transfer != back {
							t.Fatal("duplicate or unfenced snapshot")
						}
						seen = true
						var body struct{ Snapshot json.RawMessage }
						json.Unmarshal(m.Body, &body)
						if _, err := decodeSnapshot(body.Snapshot); err != nil {
							t.Fatal(err)
						}
					}
					if m.Kind == "released" {
						if seen != (mode != "required-only") {
							t.Fatal("incorrect optional negotiation")
						}
						return
					}
				case <-ctx.Done():
					t.Fatal("no return proof")
				}
			}
		})
	}
}

func TestOwnershipSnapshotTLSRejectsFaults(t *testing.T) {
	for _, fault := range []string{"malformed", "oversized", "transfer", "session", "replay", "unnegotiated", "after-ready"} {
		t.Run(fault, func(t *testing.T) {
			optional := []string{h.CapSnapshot}
			if fault == "unnegotiated" {
				optional = []string{}
			}
			ctx, conn, peer := snapshotTLS(t, optional)
			bridge := newSnapshotBridge()
			var applied atomic.Int32
			n := &ownershipTestNative{acquire: func(context.Context) error {
				if _, ok := bridge.take(); ok {
					applied.Add(1)
				}
				return nil
			}}
			_, transfer := requestSnapshotOwnership(t, bridge, conn, peer, ctx, n)
			peer.write(t, peer.message(t, "release", transfer, h.ReleaseBody{Target: h.Laptop, Reason: "preference"}))
			nav := snapshotNav()
			m := peer.message(t, "snapshot", transfer, h.SnapshotBody{Snapshot: h.Snapshot(exportSnapshot(&nav, "", 12*time.Second, 1))})
			switch fault {
			case "malformed":
				m.Body = json.RawMessage(`{"snapshot":{}}`)
			case "oversized":
				m.Body = json.RawMessage(fmt.Sprintf(`{"snapshot":{"page_id":%q}}`, strings.Repeat("x", 9000)))
			case "transfer":
				other := h.NewNonce()
				m.Transfer = &other
			case "session":
				m.Session = h.NewNonce()
			case "replay":
				peer.write(t, m)
			case "after-ready":
				peer.write(t, peer.message(t, "released", transfer, h.ReleasedBody{Target: h.Laptop, WorkersStopped: true, ReconnectDisabled: true}))
				peer.read(t, ctx, "ready")
				m = peer.message(t, "snapshot", transfer, h.SnapshotBody{Snapshot: h.Snapshot(exportSnapshot(&nav, "", 12*time.Second, 1))})
			}
			peer.write(t, m)
			select {
			case <-conn.Done():
			case <-ctx.Done():
				t.Fatal("invalid snapshot did not close stream")
			}
			if applied.Load() != 0 {
				t.Fatal("fault changed UI")
			}
		})
	}
}
