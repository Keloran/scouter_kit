// Command peertest is a laptop-side handover peer for cross-language tests (the Android app's
// HandoverInteropTest). Synthetic only: a throwaway store in -dir.
//
// As the desktop does it (-invitation): read the phone's invitation, enroll as the laptop guest (printing
// {"code": "123456"}), dial the phone, send one availability message, print {"got": "<kind>"} for the
// phone's first message, then exit.
//
// As a host (no -invitation): listen on loopback, open one invitation and print
// {"invitation": "CFE1....", "id": "<host key ID>"}; enroll the guest that proves it and give every
// enrolled peer a protocol connection that sends one availability message and answers each heartbeat
// with its own; exit after -for.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"time"

	"scouterkit/examples/internal/handover"
)

func main() {
	dir := flag.String("dir", "", "a fresh directory for this peer's key and enrolled peers")
	stay := flag.Duration("for", time.Minute, "how long to serve")
	invitation := flag.String("invitation", "", "a phone's invitation: enroll as the laptop and dial it")
	flag.Parse()
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "peertest: -dir is required")
		os.Exit(2)
	}
	store, err := handover.OpenStore(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "peertest:", err)
		os.Exit(1)
	}
	if *invitation != "" {
		if err := guest(store, *invitation, *stay); err != nil {
			fmt.Fprintln(os.Stderr, "peertest:", err)
			os.Exit(1)
		}
		return
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, "peertest:", err)
		os.Exit(1)
	}
	enroller := &handover.Enroller{Store: store}
	inv := enroller.Invite(handover.Laptop, []string{ln.Addr().String()})
	json.NewEncoder(os.Stdout).Encode(map[string]string{"invitation": inv.Encode(), "id": store.Identity().ID})
	ctx, cancel := context.WithTimeout(context.Background(), *stay)
	defer cancel()
	go func() { <-ctx.Done(); ln.Close() }()
	for {
		raw, err := ln.Accept()
		if err != nil {
			return
		}
		go serve(ctx, raw, store, enroller)
	}
}

func serve(ctx context.Context, raw net.Conn, store *handover.Store, enroller *handover.Enroller) {
	c, err := handover.Accept(ctx, raw, store, enroller, handover.Options{Role: handover.Laptop})
	if err != nil {
		fmt.Fprintln(os.Stderr, "peertest: accept:", err)
		return
	}
	defer c.Close()
	c.Send("availability", 0, "", handover.AvailabilityBody{Eligible: true, Lid: "open", Awake: true, Reason: "resume"})
	for {
		select {
		case m, ok := <-c.Recv():
			if !ok {
				return
			}
			if m.Kind == "heartbeat" {
				c.Send("heartbeat", 0, "", handover.HeartbeatBody{Phase: handover.Idle})
			}
		case <-ctx.Done():
			return
		}
	}
}

func guest(store *handover.Store, text string, stay time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), stay)
	defer cancel()
	inv, err := handover.ParseInvitation(text, time.Now())
	if err != nil {
		return err
	}
	route := handover.DirectRoute{Addr: inv.Endpoints[0]}
	peer, code, err := handover.Enroll(ctx, store, inv, handover.Laptop, route.Dial)
	if err != nil {
		return err
	}
	out := json.NewEncoder(os.Stdout)
	out.Encode(map[string]string{"code": code})
	c, err := handover.Dial(ctx, store, route, peer.ID, handover.Options{Role: handover.Laptop})
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.Send("availability", 0, "", handover.AvailabilityBody{Eligible: true, Lid: "open", Awake: true, Reason: "resume"}); err != nil {
		return err
	}
	select {
	case m, ok := <-c.Recv():
		if !ok {
			return c.Err()
		}
		out.Encode(map[string]string{"got": m.Kind})
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
