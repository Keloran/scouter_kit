package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"scouterkit/examples/internal/demo"
	h "scouterkit/examples/internal/handover"
	"scouterkit/examples/internal/localcontrol"
)

type dashboardNative struct {
	host     *glassesHost
	commands chan<- hostCommand
	done     <-chan struct{}
}

func (n dashboardNative) command(ctx context.Context, release bool) error {
	reply := make(chan error, 1)
	select {
	case n.commands <- hostCommand{release: release, done: reply, ctx: ctx}:
	case <-ctx.Done():
		return ctx.Err()
	case <-n.done:
		return errors.New("dashboard stopped")
	}
	// An accepted native operation must finish before teardown may start.
	select {
	case err := <-reply:
		return err
	case <-n.done:
		return errors.New("dashboard stopped")
	}
}

func (n dashboardNative) Acquire(ctx context.Context) (h.Arms, error) {
	err := n.command(ctx, false)
	return n.Readiness(), err
}

func (n dashboardNative) Release() error {
	select {
	case <-n.done:
		return nil
	default:
	}
	return n.command(context.Background(), true)
}

func (n dashboardNative) Readiness() h.Arms {
	s := n.host.Status()
	return h.Arms{Left: s.LeftReady, Right: s.RightReady}
}

// prepareOwnershipPeer does not run for preview or desktop-only handover.
// Invitations and credentials never enter log messages or local status.
func prepareOwnershipPeer(ctx context.Context, s ownershipSettings) (*h.Store, string, h.Route, error) {
	if s.Endpoint == "" && s.Invitation == "" {
		return nil, "", nil, nil
	}
	dir := s.Store
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return nil, "", nil, errors.New("cannot locate private peer storage")
		}
		dir = filepath.Join(base, "chewedfeed", "peers")
	}
	store, err := h.OpenStore(dir)
	if err != nil {
		return nil, "", nil, errors.New("cannot open private peer storage")
	}
	id, endpoint := s.PeerID, s.Endpoint
	if s.Invitation != "" {
		f, err := os.Open(s.Invitation)
		if err != nil {
			return nil, "", nil, errors.New("cannot read enrollment invitation")
		}
		b, readErr := io.ReadAll(io.LimitReader(f, 4097))
		f.Close()
		if readErr != nil || len(b) > 4096 {
			return nil, "", nil, errors.New("invalid enrollment invitation")
		}
		inv, err := h.ParseInvitation(strings.TrimSpace(string(b)), time.Now())
		if err != nil || inv.HostRole != h.Phone || id != "" && id != inv.HostID {
			return nil, "", nil, errors.New("invitation must match the selected phone")
		}
		if endpoint == "" && len(inv.Endpoints) > 0 {
			endpoint = inv.Endpoints[0]
		}
		if _, err := configureOwnership(ownershipSettings{Endpoint: endpoint}, nil); err != nil || endpoint == "" {
			return nil, "", nil, errors.New("enrollment needs a valid peer endpoint")
		}
		route := h.DirectRoute{Addr: endpoint}
		enrollCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		peer, code, err := h.Enroll(enrollCtx, store, inv, h.Laptop, route.Dial)
		cancel()
		if err != nil {
			return nil, "", nil, errors.New("phone enrollment failed")
		}
		id = peer.ID
		fmt.Printf("Phone enrollment code: %s\n", code)
	}
	if id == "" {
		for _, peer := range store.Peers() {
			if peer.Role != h.Phone {
				continue
			}
			if id != "" {
				return nil, "", nil, errors.New("multiple enrolled phones; select -peer-id")
			}
			id = peer.ID
		}
	}
	peer, ok := store.Enrolled(id)
	if !ok || peer.Role != h.Phone {
		return nil, "", nil, errors.New("enroll the selected phone before connecting")
	}
	return store, id, h.DirectRoute{Addr: endpoint}, nil
}

func coordinatedDisplay(ctx context.Context, f demo.Flags, o options, client *client) error {
	if f.Left == "none" || f.Right == "none" {
		return errors.New("handover requires both arms")
	}
	store, peerID, route, err := prepareOwnershipPeer(ctx, o.ownership)
	if err != nil {
		return err
	}
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	// Keep the UI worker alive until the controller has joined acquisition and release.
	dashCtx, stopDash := context.WithCancel(context.WithoutCancel(ctx))
	defer stopDash()
	o.managed = true
	host := &glassesHost{f: f, o: o}
	commands := make(chan hostCommand)
	dashDone := make(chan struct{})
	var dashErr error
	power := newPowerWatcher().watch(runCtx)
	ctrl := newOwnershipController(dashboardNative{host, commands, dashDone}, power, store != nil)
	path := o.ownership.Socket
	if path == "" {
		path = localcontrol.DefaultPath()
	}
	server, err := localcontrol.Listen(path, ctrl)
	if err != nil {
		stop()
		for range power {
		} // join the watcher even when a second instance is refused
		return err
	}
	defer server.Close()
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve() }()
	go func() { defer close(dashDone); dashErr = dashboard(dashCtx, host, o, client, commands) }()
	go ctrl.run(runCtx)
	var network sync.WaitGroup
	if store != nil {
		network.Add(1)
		go func() {
			defer network.Done()
			h.Link(runCtx, store, route, peerID, h.Options{Role: h.Laptop, Optional: []string{}, Heartbeat: ctrl.Heartbeat}, func(conn *h.Conn) {
				ctrl.serveLink(runCtx, conn)
			})
		}()
	}
	var serverErr error
	serverEnded := false
	select {
	case <-ctx.Done():
	case <-dashDone:
	case serverErr = <-serverDone:
		serverEnded = true
	}
	stop()
	<-ctrl.done
	stopDash()
	<-dashDone
	network.Wait()
	for range power {
	} // the power worker drops its inhibitor and joins its D-Bus subprocess
	server.Close()
	if !serverEnded {
		serverErr = <-serverDone
	}
	return errors.Join(dashErr, serverErr)
}

func (c *ownershipController) serveLink(ctx context.Context, conn *h.Conn) {
	select {
	case c.packets <- ownershipPacket{link: conn}:
	case <-ctx.Done():
		conn.Close()
		return
	}
	for {
		select {
		case m, ok := <-conn.Recv():
			if !ok {
				select {
				case c.packets <- ownershipPacket{link: conn, down: true}:
				case <-ctx.Done():
				}
				return
			}
			select {
			case c.packets <- ownershipPacket{link: conn, message: &m}:
			case <-ctx.Done():
				conn.Close()
				return
			}
		case <-ctx.Done():
			conn.Close()
			return
		}
	}
}
