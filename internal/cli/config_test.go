package cli

// The config file is shared state between the pin and the profiles; a
// re-pin must never wipe the profiles another path saved (it did, once).

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSavePinnedPreservesProfiles(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	// macOS ignores XDG; point UserConfigDir's HOME fallback there too.
	t.Setenv("HOME", dir)

	if err := savePinned(&pinnedConfig{
		Context: "home-ctx", Namespace: "agents",
		Profiles:    map[string]profileTarget{"work": {Context: "work-ctx", Namespace: "team"}},
		LastProfile: "work",
	}); err != nil {
		t.Fatal(err)
	}
	// A later re-pin that says nothing about profiles…
	if err := savePinned(&pinnedConfig{Context: "other-ctx", Namespace: "tiny"}); err != nil {
		t.Fatal(err)
	}
	c := loadPinned()
	if c == nil {
		path, _ := configPath()
		if _, err := os.Stat(filepath.Dir(path)); err != nil {
			t.Skipf("config dir unavailable in this env: %v", err)
		}
		t.Fatal("config unreadable after save")
	}
	if c.Context != "other-ctx" {
		t.Fatalf("pin = %q, want other-ctx", c.Context)
	}
	if c.Profiles["work"].Context != "work-ctx" {
		t.Fatalf("profiles wiped by re-pin: %+v", c.Profiles)
	}
	if c.LastProfile != "work" {
		t.Fatalf("lastProfile lost: %q", c.LastProfile)
	}
}

func TestPickProfileOrdersLastUsedFirst(t *testing.T) {
	c := &pinnedConfig{
		Profiles: map[string]profileTarget{
			"aaa":  {Context: "a"},
			"work": {Context: "w"},
			"home": {Context: "h"},
		},
		LastProfile: "work",
	}
	names := profileOrder(c)
	if names[0] != "work" {
		t.Fatalf("order = %v, want work first (enter repeats yesterday)", names)
	}
	if len(names) != 3 {
		t.Fatalf("order = %v, want all three", names)
	}
}
