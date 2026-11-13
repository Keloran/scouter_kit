package main

import (
	"encoding/json"
	"reflect"
	"sync"
)

// The UI alone owns navigation. The coordinator exchanges bounded immutable
// copies here without waiting for the UI or calling native code.
type snapshotBridge struct {
	mu        sync.Mutex
	current   uiSnapshot
	have      bool
	edits     uint64
	gate      snapshotGate
	session   string
	receiving bool
	pending   *uiSnapshot
	committed bool
	notify    chan struct{}
}

func newSnapshotBridge() *snapshotBridge { return &snapshotBridge{notify: make(chan struct{}, 1)} }

func (b *snapshotBridge) publish(s uiSnapshot) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	s.Revision = b.current.Revision
	if !b.have || !reflect.DeepEqual(s, b.current) {
		s.Revision++
	}
	if s.validate() != nil {
		return
	}
	b.current, b.have = s, true
}

func (b *snapshotBridge) export() (uiSnapshot, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.current, b.have
}

func (b *snapshotBridge) edited() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.edits++
	b.pending = nil
}

func (b *snapshotBridge) requested(session, transfer string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.gate.requested(transfer, b.edits)
	b.session, b.receiving = session, session != "" && transfer != ""
	b.pending, b.committed = nil, false
}

func (b *snapshotBridge) receive(session, transfer string, body []byte) bool {
	if b == nil || len(body) > maxSnapshotBytes+32 {
		return false
	}
	var envelope struct {
		Snapshot json.RawMessage `json:"snapshot"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || len(fields) != 1 {
		return false
	}
	s, err := decodeSnapshot(envelope.Snapshot)
	if err != nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.receiving || session != b.session || b.gate.accept(session, transfer, s, b.edits) != nil {
		return false
	}
	b.pending = &s
	return true
}

// Only matching release proof commits staged state; native acquisition can
// consume it immediately, even if the UI has not selected notify yet.
func (b *snapshotBridge) finish(commit bool) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.receiving = false
	b.committed = commit
	if !commit {
		b.pending = nil
		b.session = ""
	}
	if b.pending != nil && commit {
		select {
		case b.notify <- struct{}{}:
		default:
		}
	}
}

func (b *snapshotBridge) take() (uiSnapshot, bool) {
	if b == nil {
		return uiSnapshot{}, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.committed || b.pending == nil || b.gate.edits != b.edits {
		return uiSnapshot{}, false
	}
	s := *b.pending
	b.pending = nil
	return s, true
}
