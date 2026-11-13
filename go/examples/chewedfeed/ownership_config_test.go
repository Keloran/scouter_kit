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

// peer_endpoint in the user config (next to the display settings): used when no flag or handover config says
// otherwise; -handover=false still disables it.
func TestUserConfigPeerEndpoint(t *testing.T) {
	dir := t.TempDir()
	if s := withUserPeerEndpoint(ownershipSettings{}, nil, dir); s.Endpoint != "" {
		t.Fatalf("no file: %+v", s)
	}
	os.MkdirAll(filepath.Join(dir, "chewedfeed"), 0700)
	if err := os.WriteFile(filepath.Join(dir, "chewedfeed", "config"), []byte(`{"dx":-10,"dy":-8,"font_scale":1.5,"peer_endpoint":"phone.invalid:47613"}`), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := configureOwnership(withUserPeerEndpoint(ownershipSettings{}, nil, dir), nil)
	if err != nil || !s.Enabled || s.Endpoint != "phone.invalid:47613" {
		t.Fatalf("user config: %+v %v", s, err)
	}
	for _, c := range []struct {
		s        ownershipSettings
		explicit map[string]bool
		want     string
	}{
		{ownershipSettings{Endpoint: "other.invalid:1"}, map[string]bool{"peer-endpoint": true}, "other.invalid:1"}, // the flag wins
		{ownershipSettings{config: "given.json"}, map[string]bool{"handover-config": true}, ""},                     // its own file decides
		{ownershipSettings{}, map[string]bool{"handover": true}, ""},                                                // -handover=false
	} {
		if got := withUserPeerEndpoint(c.s, c.explicit, dir).Endpoint; got != c.want {
			t.Fatalf("%+v: endpoint %q, want %q", c, got, c.want)
		}
	}
	// The display settings in the same file still load: the field is simply not theirs.
	os.WriteFile(filepath.Join(dir, "chewedfeed", "config"), []byte(`{"peer_endpoint":"not-an-endpoint"}`), 0600)
	if _, err := configureOwnership(withUserPeerEndpoint(ownershipSettings{}, nil, dir), nil); err == nil {
		t.Fatal("an invalid peer_endpoint was accepted")
	}
}
