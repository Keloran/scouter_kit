package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSavedDisplaySettingsAndFlagOverrides(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "display.json")
	o := options{configFile: path, dx: -8, dy: -6, fontScale: 1.5}
	if err := saveDisplaySettings(o); err != nil {
		t.Fatal(err)
	}
	got, err := configureDisplay(options{configFile: path}, nil)
	if err != nil || got.dx != -8 || got.dy != -6 || got.fontScale != 1.5 {
		t.Fatalf("saved settings: %+v (%v)", got, err)
	}
	got, err = configureDisplay(options{configFile: path, dx: 0, fontScale: 2}, map[string]bool{"dx": true, "font-scale": true})
	if err != nil || got.dx != 0 || got.dy != -6 || got.fontScale != 2 {
		t.Fatalf("flag overrides: %+v (%v)", got, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private config permissions: %v (%v)", info, err)
	}
	if err := os.WriteFile(path, []byte(`{"dx":-8}`), 0600); err != nil {
		t.Fatal(err)
	}
	got, err = configureDisplay(options{configFile: path}, nil)
	if err != nil || got.dx != -8 || got.dy != 9 || got.fontScale != 2 {
		t.Fatalf("partial config defaults: %+v (%v)", got, err)
	}
}

func TestDisplayConfigErrors(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "display.json")
	if _, err := configureDisplay(options{configFile: path}, nil); err != nil {
		t.Fatalf("missing default config: %v", err)
	}
	if _, err := configureDisplay(options{configFile: path}, map[string]bool{"config": true}); err == nil {
		t.Fatal("missing explicitly selected config accepted")
	}
	for _, data := range []string{`{`, `{"dx":241}`, `{"dy":-121}`, `{"font_scale":0}`, `{"font_scale":0.09}`, `{"font_scale":4}`} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := configureDisplay(options{configFile: path}, nil); err == nil {
			t.Fatalf("invalid config accepted: %s", data)
		}
	}
}

func TestUserConfigLayersAndLocalFieldOverrides(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	userDir := filepath.Join(dir, "chewedfeed")
	if err := os.Mkdir(userDir, 0700); err != nil {
		t.Fatal(err)
	}
	write := func(path, data string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	plain := filepath.Join(userDir, "config")
	jsonPath := filepath.Join(userDir, "config.json")
	local := filepath.Join(dir, "chewedfeed.local.json")
	write(plain, `{"dx":-15,"dy":-8,"font_scale":1.5}`)
	o, err := configureDisplay(options{configFile: local}, nil)
	if err != nil || o.dx != -15 || o.dy != -8 || o.fontScale != 1.5 {
		t.Fatalf("extensionless user config: %+v (%v)", o, err)
	}
	write(jsonPath, `{"dy":-6}`)
	o, err = configureDisplay(options{configFile: local}, nil)
	if err != nil || o.dx != -15 || o.dy != -6 || o.fontScale != 1.5 {
		t.Fatalf("user config.json overlay: %+v (%v)", o, err)
	}
	write(local, `{"dx":0,"font_scale":0.1}`)
	o, err = configureDisplay(options{configFile: local}, nil)
	if err != nil || o.dx != 0 || o.dy != -6 || o.fontScale != 0.1 {
		t.Fatalf("partial local overrides: %+v (%v)", o, err)
	}
	o, err = configureDisplay(options{configFile: local, dy: 0}, map[string]bool{"dy": true})
	if err != nil || o.dx != 0 || o.dy != 0 || o.fontScale != 0.1 {
		t.Fatalf("explicit flag over layers: %+v (%v)", o, err)
	}
	if err := saveDisplaySettings(o); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(jsonPath); err != nil || string(data) != `{"dy":-6}` {
		t.Fatal("saving local settings modified user config")
	}
	if err := os.Remove(plain); err != nil {
		t.Fatal(err)
	}
	o, err = configureDisplay(options{}, nil)
	if err != nil || o.dx != 100 || o.dy != -6 || o.fontScale != 2 {
		t.Fatalf("config.json without extensionless file: %+v (%v)", o, err)
	}
	write(jsonPath, `{`)
	if _, err := configureDisplay(options{configFile: local}, nil); err == nil {
		t.Fatal("malformed user config was ignored")
	}
}
