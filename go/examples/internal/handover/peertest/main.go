// Command peertest is a laptop-side handover peer for cross-language tests (the Android app's
// HandoverInteropTest). Synthetic only: a throwaway store in -dir.
//
// As the desktop does it (-invitation): read the phone's invitation, enroll as the laptop guest (printing
// {"code": "123456"}), dial the phone, send one availability message, print {"got": "<kind>"} for the
// phone's first message, then exit.
//
// With -script handover (and -invitation), after connecting it plays the laptop's side of a handover:
// lid closed (availability not eligible), answer the phone's request with release and released, then
// awake again with its own request, and prints {"got": "<kind>"} for every phone message until the
// phone's released; then exits.
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
	script := flag.String("script", "", `with -invitation: "handover" plays a laptop handing the glasses over and back; `+
		`"telemetry" sends one system and one AI reading as the desktop builds them`)
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
		if err := guest(store, *invitation, *stay, *script); err != nil {
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

func guest(store *handover.Store, text string, stay time.Duration, script string) error {
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
	if script == "handover" {
		return handoverScript(ctx, c, out)
	}
	if script == "telemetry" {
		return telemetryScript(ctx, c, out)
	}
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

// handoverScript: the laptop's lid closes (the phone takes the glasses), then it wakes and asks for them back.
func handoverScript(ctx context.Context, c *handover.Conn, out *json.Encoder) error {
	next := func() (handover.Message, error) {
		select {
		case m, ok := <-c.Recv():
			if !ok {
				return m, fmt.Errorf("connection ended: %v", c.Err())
			}
			out.Encode(map[string]string{"got": m.Kind})
			return m, nil
		case <-ctx.Done():
			return handover.Message{}, ctx.Err()
		}
	}
	until := func(kind string) (handover.Message, error) {
		for {
			m, err := next()
			if err != nil || m.Kind == kind {
				return m, err
			}
		}
	}
	// Lid closed: not eligible. The phone asks for the glasses.
	if err := c.Send("availability", 0, "", handover.AvailabilityBody{Eligible: false, Lid: "closed", Awake: true, Reason: "lid_closed"}); err != nil {
		return err
	}
	req, err := until("request")
	if err != nil {
		return err
	}
	t := *req.Transfer
	c.Expect(t)
	if err := c.Send("release", req.Epoch, t, handover.ReleaseBody{Target: handover.Phone, Reason: "lid_closed"}); err != nil {
		return err
	}
	if err := c.Send("released", req.Epoch, t, handover.ReleasedBody{Target: handover.Phone, ReconnectDisabled: true, WorkersStopped: true}); err != nil {
		return err
	}
	ready, err := until("ready")
	if err != nil {
		return err
	}
	// Awake again: the laptop asks for the glasses back.
	back := handover.NewNonce()
	c.Expect(back)
	if err := c.Send("availability", ready.Epoch, "", handover.AvailabilityBody{Eligible: true, Lid: "open", Awake: true, Reason: "resume"}); err != nil {
		return err
	}
	if err := c.Send("request", ready.Epoch, back, handover.RequestBody{Target: handover.Laptop, Reason: "resume"}); err != nil {
		return err
	}
	_, err = until("released")
	return err
}

// telemetryScript sends a system and an AI reading built from the protocol's Go types, as the desktop's
// telemetry.go does, then waits for the phone's next message (it must keep the link) and prints it.
func telemetryScript(ctx context.Context, c *handover.Conn, out *json.Encoder) error {
	if !c.Negotiated(handover.CapTelemetry) {
		return fmt.Errorf("the phone did not negotiate %s", handover.CapTelemetry)
	}
	cpu, battery := 37.5, 64
	at := uint64(time.Now().Add(-3 * time.Second).UnixMilli())
	system, _ := json.Marshal(handover.SystemValues{CPUPercent: &cpu, MemoryUsedBytes: 6 << 30, MemoryTotalBytes: 16 << 30, BatteryPercent: &battery})
	used, reset := 42.0, uint64(time.Now().Add(2*time.Hour).UnixMilli())
	ai, _ := json.Marshal([]handover.AIWindow{{Provider: "claude", Window: "short", UsedPercent: &used, ResetUnixMs: &reset}})
	for _, t := range []handover.Telemetry{
		{SchemaVersion: 1, Revision: 1, Source: "system", SampleUnixMs: &at, AgeMs: 3000, Status: "fresh", Values: system},
		{SchemaVersion: 1, Revision: 1, Source: "ai", SampleUnixMs: &at, AgeMs: 3000, Status: "fresh", Values: ai},
	} {
		if err := c.Send("telemetry", 0, "", handover.TelemetryBody{Telemetry: t}); err != nil {
			return err
		}
	}
	select {
	case m, ok := <-c.Recv():
		if !ok {
			return fmt.Errorf("connection ended: %v", c.Err())
		}
		out.Encode(map[string]string{"got": m.Kind})
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
