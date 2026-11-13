package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOwnershipConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "handover.json")
	if err := os.WriteFile(path, []byte(`{"enabled":true,"peer_endpoint":"phone.invalid:4567","peer_store":"configured-store"}`), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := configureOwnership(ownershipSettings{config: path, Endpoint: "override.invalid:4321"}, map[string]bool{"peer-endpoint": true})
	if err != nil || !s.Enabled || s.Endpoint != "override.invalid:4321" || s.Store != "configured-store" {
		t.Fatalf("configuration precedence: %+v %v", s, err)
	}
	for _, s := range []ownershipSettings{
		{Endpoint: "phone.invalid"}, {Endpoint: "phone.invalid:70000"}, {Endpoint: ":1234"},
		{Socket: "relative.sock"}, {PeerID: "bad"},
	} {
		if _, err := configureOwnership(s, nil); err == nil {
			t.Fatalf("accepted invalid settings: %+v", s)
		}
	}
	if _, err := configureOwnership(ownershipSettings{Endpoint: "phone.invalid:4321"}, map[string]bool{"handover": true}); err == nil {
		t.Fatal("explicit disable ignored")
	}
	if err := os.WriteFile(path, []byte(`{"enabled":true,"unknown":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := configureOwnership(ownershipSettings{config: path}, nil); err == nil {
		t.Fatal("unknown config field ignored")
	}
}
