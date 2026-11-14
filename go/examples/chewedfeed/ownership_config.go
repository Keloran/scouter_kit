package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
)

type ownershipSettings struct {
	Enabled    bool   `json:"enabled"`
	Socket     string `json:"control_socket"`
	Store      string `json:"peer_store"`
	PeerID     string `json:"peer_id"`
	Endpoint   string `json:"peer_endpoint"`
	Invitation string `json:"enroll_invitation"`
	// Telemetry for the phone (off by default): AI usage windows, and aggregate CPU, memory and battery.
	ShareAI     bool `json:"share_ai"`
	ShareSystem bool `json:"share_system"`
	config      string
}

func (s *ownershipSettings) register(f *flag.FlagSet) {
	f.BoolVar(&s.Enabled, "handover", false, "enable lid/suspend handover and private local release/resume controls")
	f.StringVar(&s.config, "handover-config", "", "handover settings JSON; explicit flags override it")
	f.StringVar(&s.Socket, "control-socket", "", "private control socket (empty: user runtime directory)")
	f.StringVar(&s.Store, "peer-store", "", "private enrollment directory (empty: user config/chewedfeed/peers)")
	f.StringVar(&s.PeerID, "peer-id", "", "enrolled phone key ID (empty: the only enrolled phone)")
	f.StringVar(&s.Endpoint, "peer-endpoint", "", "phone host:port; TLS requires an enrolled pinned key")
	f.StringVar(&s.Invitation, "enroll-invitation", "", "file containing a phone's single-use enrollment invitation")
	f.BoolVar(&s.ShareAI, "share-ai", false, "send AI usage windows (no plan or account details) to the phone")
	f.BoolVar(&s.ShareSystem, "share-system", false, "send aggregate CPU, memory and battery (no processes) to the phone")
}

// withUserPeerEndpoint takes peer_endpoint, share_ai and share_system from the user config (<user
// config>/chewedfeed/config or config.json, the file that holds the display settings) when neither the matching
// flag nor -handover-config was given, so a launcher can start the dashboard with its phone handover and no
// flags. -handover=false still disables it.
func withUserPeerEndpoint(s ownershipSettings, explicit map[string]bool, configDir string) ownershipSettings {
	if s.config != "" || explicit["handover-config"] || explicit["handover"] && !s.Enabled || configDir == "" {
		return s
	}
	for _, name := range []string{"config", "config.json"} {
		b, err := os.ReadFile(filepath.Join(configDir, "chewedfeed", name))
		if err != nil || len(b) > 16384 {
			continue
		}
		var f struct {
			PeerEndpoint string `json:"peer_endpoint"`
			ShareAI      *bool  `json:"share_ai"`
			ShareSystem  *bool  `json:"share_system"`
		}
		if json.Unmarshal(b, &f) != nil {
			continue
		}
		if f.PeerEndpoint != "" && s.Endpoint == "" && !explicit["peer-endpoint"] {
			s.Endpoint = f.PeerEndpoint // checked by configureOwnership like the flag
		}
		if f.ShareAI != nil && !explicit["share-ai"] {
			s.ShareAI = *f.ShareAI
		}
		if f.ShareSystem != nil && !explicit["share-system"] {
			s.ShareSystem = *f.ShareSystem
		}
	}
	return s
}

func configureOwnership(flags ownershipSettings, explicit map[string]bool) (ownershipSettings, error) {
	s := flags
	if flags.config != "" {
		b, err := os.ReadFile(flags.config)
		if err != nil {
			return s, errors.New("cannot read handover config")
		}
		if len(b) > 16384 {
			return s, errors.New("handover config too large")
		}
		s = ownershipSettings{config: flags.config}
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&s); err != nil {
			return s, errors.New("invalid handover config")
		}
		if err := dec.Decode(new(any)); err != io.EOF {
			return s, errors.New("invalid trailing handover config")
		}
		if explicit["handover"] {
			s.Enabled = flags.Enabled
		}
		if explicit["share-ai"] {
			s.ShareAI = flags.ShareAI
		}
		if explicit["share-system"] {
			s.ShareSystem = flags.ShareSystem
		}
		for name, pair := range map[string][2]*string{
			"control-socket": {&s.Socket, &flags.Socket}, "peer-store": {&s.Store, &flags.Store},
			"peer-id": {&s.PeerID, &flags.PeerID}, "peer-endpoint": {&s.Endpoint, &flags.Endpoint},
			"enroll-invitation": {&s.Invitation, &flags.Invitation},
		} {
			if explicit[name] {
				*pair[0] = *pair[1]
			}
		}
	}
	if s.Endpoint != "" || s.Invitation != "" || s.PeerID != "" {
		if explicit["handover"] && !s.Enabled {
			return s, errors.New("peer settings require handover")
		}
		s.Enabled = true
	}
	if s.PeerID != "" && !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(s.PeerID) {
		return s, errors.New("invalid enrolled peer ID")
	}
	if s.Endpoint != "" {
		host, port, err := net.SplitHostPort(s.Endpoint)
		n, parseErr := strconv.Atoi(port)
		if err != nil || host == "" || parseErr != nil || n < 1 || n > 65535 {
			return s, errors.New("peer endpoint must be host:port")
		}
	}
	if s.PeerID != "" && s.Endpoint == "" && s.Invitation == "" {
		return s, errors.New("peer ID needs a peer endpoint")
	}
	if s.Socket != "" && !filepath.IsAbs(s.Socket) {
		return s, errors.New("control socket must be an absolute path")
	}
	return s, nil
}
