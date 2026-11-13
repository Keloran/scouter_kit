package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"scouterkit"
)

type displaySettings struct {
	DX        int     `json:"dx"`
	DY        int     `json:"dy"`
	FontScale float64 `json:"font_scale"`
	// Head pitch (degrees, + looking up): the dashboard shows above ShowAbove or below ShowBelow.
	ShowAbove   int  `json:"show_above"`
	ShowBelow   int  `json:"show_below"`
	AlwaysNotes bool `json:"always_show_notifications"`
}

const minFontScale, maxFontScale float64 = 0.1, 3

func (s displaySettings) validate() error {
	if s.DX < -240 || s.DX > 240 || s.DY < -120 || s.DY > 120 {
		return errors.New("use -dx between -240 and 240, and -dy between -120 and 120")
	}
	if math.IsNaN(s.FontScale) || s.FontScale < minFontScale || s.FontScale > maxFontScale {
		return fmt.Errorf("use -font-scale between %g and %g", minFontScale, maxFontScale)
	}
	if s.ShowAbove < -90 || s.ShowAbove > 90 || s.ShowBelow < -90 || s.ShowBelow > 90 {
		return errors.New("use -show-above and -show-below between -90 and 90")
	}
	return nil
}

// Load user settings first, then overlay the local file and explicit flags.
// Unmarshalling each layer into the same struct preserves omitted fields.
func configureDisplay(o options, explicit map[string]bool) (options, error) {
	cfg := scouterkit.DefaultConfig()
	s := displaySettings{DX: cfg.DX, DY: cfg.DY, FontScale: 2, ShowAbove: 9, ShowBelow: -30, AlwaysNotes: true}
	load := func(path, label string, required bool) error {
		b, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			if required {
				return errors.New("specified display config does not exist")
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading %s: %w", label, err)
		}
		if err := json.Unmarshal(b, &s); err != nil {
			return fmt.Errorf("invalid %s: %w", label, err)
		}
		progress("Loaded %s", label)
		return nil
	}
	if dir, err := os.UserConfigDir(); err == nil {
		for _, name := range []string{"config", "config.json"} {
			if err := load(filepath.Join(dir, "chewedfeed", name), "user "+name, false); err != nil {
				return o, err
			}
		}
	}
	if o.configFile != "" {
		if err := load(o.configFile, "local display config", explicit["config"] && !o.saveConfig); err != nil {
			return o, err
		}
	}
	if explicit["dx"] {
		s.DX = o.dx
	}
	if explicit["dy"] {
		s.DY = o.dy
	}
	if explicit["font-scale"] {
		s.FontScale = o.fontScale
	}
	if explicit["show-above"] {
		s.ShowAbove = o.showAbove
	}
	if explicit["show-below"] {
		s.ShowBelow = o.showBelow
	}
	if explicit["always-show-notifications"] {
		s.AlwaysNotes = o.alwaysNotes
	}
	if err := s.validate(); err != nil {
		return o, err
	}
	o.dx, o.dy, o.fontScale = s.DX, s.DY, s.FontScale
	o.showAbove, o.showBelow, o.alwaysNotes = s.ShowAbove, s.ShowBelow, s.AlwaysNotes
	return o, nil
}

func saveDisplaySettings(o options) error {
	if o.configFile == "" {
		return errors.New("-save-config needs a nonempty -config path")
	}
	s := displaySettings{DX: o.dx, DY: o.dy, FontScale: o.fontScale, ShowAbove: o.showAbove, ShowBelow: o.showBelow,
		AlwaysNotes: o.alwaysNotes}
	if err := s.validate(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(o.configFile), ".chewedfeed-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), o.configFile)
}
