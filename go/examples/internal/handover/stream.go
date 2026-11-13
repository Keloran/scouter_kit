package handover

import (
	"encoding/json"
	"slices"
	"sync"
)

// Stream checks the messages of one authenticated peer connection
// (contract.py PeerStream). It runs after TLS has proven the peer's key, and
// after DecodeMessage; it never authenticates by itself. Watermarks move only
// when a message passes every check, so a rejected message cannot poison them.
type Stream struct {
	Sender, Recipient string // the enrolled peer's ID and ours
	LocalSession      string // ours for this connection

	retired      map[string]bool
	pending      string // transfer the peer is releasing for (release_notice), or that we requested
	releaseEpoch uint64
	hasRelease   bool
	local        []string // capabilities we support

	session      string
	role         Role
	lastSeq      uint64
	lastEpoch    uint64
	active       bool
	capabilities []string
}

// NewStream: retired lists sessions of earlier connections, never accepted again.
// pending is a transfer this host has requested ("" none). local defaults to
// every known capability.
func NewStream(sender, recipient, localSession string, retired []string, pending string, local []string) *Stream {
	if local == nil {
		local = append(slices.Clone(RequiredCapabilities), OptionalCapabilities...)
	}
	r := map[string]bool{}
	for _, s := range retired {
		r[s] = true
	}
	return &Stream{Sender: sender, Recipient: recipient, LocalSession: localSession, retired: r, pending: pending, local: local}
}

// Receive accepts or rejects one decoded message.
func (s *Stream) Receive(m Message) error {
	if err := payloadChecks(m); err != nil {
		return err
	}
	if m.Sender != s.Sender || m.Recipient != s.Recipient {
		return invalid("wrong enrolled peer")
	}
	if m.Sender == m.Recipient {
		return invalid("self message")
	}
	if s.retired[m.Session] {
		return invalid("retired session")
	}
	if m.Seq <= s.lastSeq || m.Epoch < s.lastEpoch {
		return invalid("replay or stale epoch")
	}
	peerSession := ""
	if m.PeerSession != nil {
		peerSession = *m.PeerSession
	}
	transfer := ""
	if m.Transfer != nil {
		transfer = *m.Transfer
	}
	var hello HelloBody
	if s.session == "" {
		if m.Kind != "hello" {
			return invalid("hello required first")
		}
		if peerSession != "" && peerSession != s.LocalSession {
			return invalid("wrong recipient session")
		}
		json.Unmarshal(m.Body, &hello)
		for _, c := range hello.RequiredCapabilities {
			if !slices.Contains(s.local, c) {
				return invalid("locally unsupported required capability %s", c)
			}
		}
	} else {
		if m.Kind == "hello" || m.Session != s.session {
			return invalid("session changed without handshake")
		}
		if peerSession != s.LocalSession {
			return invalid("wrong recipient session")
		}
	}
	var target struct {
		Target Role   `json:"target"`
		Role   Role   `json:"role"`
		Pinned bool   `json:"pinned"`
		Reason string `json:"reason"`
	}
	json.Unmarshal(m.Body, &target)
	switch m.Kind {
	case "status":
		if target.Role != s.role {
			return invalid("role changed")
		}
	case "request", "acquiring", "ready":
		if target.Target != s.role {
			return invalid("wrong incoming target")
		}
		if m.Kind == "request" && target.Pinned && (s.role != Phone || target.Reason != "manual") {
			return invalid("invalid pinned request")
		}
	case "release", "released":
		if target.Target == s.role {
			return invalid("wrong release target")
		}
		if s.pending != "" && s.pending != transfer {
			return invalid("wrong transfer")
		}
		if m.Kind == "release" && s.pending == "" && !s.active {
			return invalid("unsolicited release without known owner")
		}
		if m.Kind == "released" && (s.pending != transfer || !s.hasRelease || s.releaseEpoch != m.Epoch) {
			return invalid("released without matching owner release")
		}
	case "snapshot":
		if !slices.Contains(s.capabilities, CapSnapshot) || s.pending != transfer {
			return invalid("unnegotiated or stale snapshot")
		}
	case "telemetry":
		if !slices.Contains(s.capabilities, CapTelemetry) {
			return invalid("unnegotiated telemetry")
		}
	}
	// Every check passed: commit.
	s.session, s.lastSeq, s.lastEpoch = m.Session, m.Seq, m.Epoch
	switch m.Kind {
	case "hello":
		s.role = hello.Role
		s.capabilities = nil
		for _, c := range append(slices.Clone(hello.RequiredCapabilities), hello.OptionalCapabilities...) {
			if slices.Contains(s.local, c) {
				s.capabilities = append(s.capabilities, c)
			}
		}
	case "status":
		var b StatusBody
		json.Unmarshal(m.Body, &b)
		s.active = b.Phase == Active
	case "ready":
		s.active = true
	case "release":
		s.pending, s.releaseEpoch, s.hasRelease = transfer, m.Epoch, true
	case "released":
		s.pending, s.releaseEpoch, s.hasRelease, s.active = "", 0, false, false
	}
	return nil
}

// Role is the peer's role from its hello ("" before it).
func (s *Stream) Role() Role { return s.role }

// Session is the peer's session for this connection ("" before its hello).
func (s *Stream) Session() string { return s.session }

// Negotiated reports whether both hellos listed an optional capability.
func (s *Stream) Negotiated(capability string) bool {
	return slices.Contains(s.capabilities, capability)
}

// Expect records a transfer this host requested, so the peer's release and
// released for it are accepted.
func (s *Stream) Expect(transfer string) { s.pending = transfer }

// Outbox allocates the sender side of one connection: seq strictly increasing
// from 1, epoch never decreasing. Safe for concurrent senders.
type Outbox struct {
	mu                sync.Mutex
	Sender, Recipient string
	Session           string // ours, fresh per connection
	PeerSession       string // theirs, once their hello arrived ("" before)
	seq, epoch        uint64
}

// Next stamps m with the envelope; epoch may only grow.
func (o *Outbox) Next(kind string, epoch uint64, transfer string, body any) (Message, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return Message{}, err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.seq++
	o.epoch = max(o.epoch, epoch)
	m := Message{Version: ProtocolVersion, Sender: o.Sender, Recipient: o.Recipient, Session: o.Session,
		Seq: o.seq, Epoch: o.epoch, Kind: kind, Body: raw}
	if o.PeerSession != "" {
		ps := o.PeerSession
		m.PeerSession = &ps
	}
	if transfer != "" {
		t := transfer
		m.Transfer = &t
	}
	return m, nil
}
