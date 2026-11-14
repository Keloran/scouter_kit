package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"scouterkit/examples/internal/localcontrol"
)

// The checked-in service template: systemd specifiers only (no local paths, addresses or secret flags), and it
// does not restart on the exit statuses that retrying cannot fix.
func TestServiceTemplate(t *testing.T) {
	b, err := os.ReadFile("chewedfeed.service")
	if err != nil {
		t.Fatal(err)
	}
	unit := string(b)
	keys := map[string]string{}
	for _, line := range strings.Split(unit, "\n") {
		if k, v, ok := strings.Cut(line, "="); ok && !strings.HasPrefix(line, "#") {
			keys[k] = v
		}
	}
	if keys["ExecStart"] != "%h/.local/bin/chewedfeed" {
		t.Fatalf("ExecStart %q: the binary through %%h, no flags", keys["ExecStart"])
	}
	if keys["Restart"] != "on-failure" || keys["RestartPreventExitStatus"] != fmt.Sprintf("%d %d", exitSetup, exitRunning) {
		t.Fatalf("restart policy: %v", keys)
	}
	for _, bad := range []*regexp.Regexp{
		regexp.MustCompile(`/home/|/Users/|/root/`), regexp.MustCompile(`\b\d{1,3}(\.\d{1,3}){3}\b`),
		regexp.MustCompile(`(?i)token-file\s|invitation|peer-endpoint`),
	} {
		for _, line := range strings.Split(unit, "\n") {
			if !strings.HasPrefix(line, "#") && bad.MatchString(line) {
				t.Fatalf("template line %q matches %v", line, bad)
			}
		}
	}
}

// One driver for the glasses: a second instance (any mode) gets an actionable error and exit status 3; the lock
// goes with the first.
func TestInstanceLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run", "control.sock")
	o := options{}
	o.ownership.Socket = path
	first, err := instanceLock(o)
	if err != nil {
		t.Fatal(err)
	}
	_, err = instanceLock(o)
	if !errors.Is(err, localcontrol.ErrRunning) || exitCode(err) != exitRunning || !strings.Contains(err.Error(), "systemctl --user stop chewedfeed") {
		t.Fatalf("second instance: %v (exit %d)", err, exitCode(err))
	}
	if _, err := localcontrol.Listen(path, nil); !errors.Is(err, localcontrol.ErrRunning) {
		t.Fatalf("a handover instance must not start beside it: %v", err)
	}
	first.Close()
	again, err := instanceLock(o)
	if err != nil {
		t.Fatalf("after the first stopped: %v", err)
	}
	again.Close()
	if exitCode(setupError{errors.New("x")}) != exitSetup || exitCode(errors.New("bluetooth")) != 1 {
		t.Fatal("exit codes")
	}
}

// An upgrade keeps the user config: a file written for an earlier version (display only) and one with every
// current key both load, display and handover settings alike.
func TestUpgradedUserConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	os.MkdirAll(filepath.Join(dir, "chewedfeed"), 0o700)
	write := func(s string) {
		if err := os.WriteFile(filepath.Join(dir, "chewedfeed", "config"), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"dx":-10,"dy":-8}`)
	o, err := configureDisplay(options{}, nil)
	if err != nil || o.dx != -10 || o.fontScale != 2 || o.showAbove != 9 {
		t.Fatalf("earlier config: %+v %v", o, err)
	}
	write(`{"dx":-10,"dy":-8,"font_scale":1.5,"show_above":7,"show_below":-25,"always_notes":true,
		"peer_endpoint":"phone.invalid:47613","share_ai":true,"share_system":false}`)
	o, err = configureDisplay(options{}, nil)
	if err != nil || o.fontScale != 1.5 || o.showBelow != -25 {
		t.Fatalf("current config: %+v %v", o, err)
	}
	s, err := configureOwnership(withUserPeerEndpoint(ownershipSettings{}, nil, dir), nil)
	if err != nil || s.Endpoint != "phone.invalid:47613" || !s.ShareAI || s.ShareSystem {
		t.Fatalf("current handover settings: %+v %v", s, err)
	}
}
