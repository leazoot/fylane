package autostart

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The entry is a file (or a registry value) the user can inspect and delete.
// These tests drive the platform this build runs on through the whole cycle;
// the other platforms' implementations are checked by `GOOS=… go vet`.

func cfg(t *testing.T) Config {
	t.Helper()
	home := t.TempDir()
	// XDG_CONFIG_HOME would win over Home on the non-darwin, non-windows
	// implementation and put the entry outside the temp directory.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	return Config{Home: home, Exe: "/opt/fylane/fylane-companion", Args: []string{"serve"}}
}

func TestOffByDefault(t *testing.T) {
	got := Status(cfg(t))
	if !got.Supported {
		t.Fatalf("autostart reports unsupported on this platform: %s", got.Detail)
	}
	if got.Enabled {
		t.Fatal("a fresh profile must not already start Fylane at login")
	}
}

func TestEnableThenDisableLeavesNothingBehind(t *testing.T) {
	c := cfg(t)
	if err := Set(c, true); err != nil {
		t.Fatal(err)
	}
	if !Status(c).Enabled {
		t.Fatal("the entry was written but does not read back as enabled")
	}
	if err := Set(c, false); err != nil {
		t.Fatal(err)
	}
	if Status(c).Enabled {
		t.Fatal("turning the setting off left the entry in place")
	}
	// Turning it off twice is the state the caller asked for, not an error:
	// the settings page must not fail because the user removed the file.
	if err := Set(c, false); err != nil {
		t.Fatalf("disabling an absent entry reported an error: %v", err)
	}
}

func TestEnablingTwiceIsNotTwoEntries(t *testing.T) {
	c := cfg(t)
	for range 2 {
		if err := Set(c, true); err != nil {
			t.Fatal(err)
		}
	}
	if !Status(c).Enabled {
		t.Fatal("the entry disappeared when written twice")
	}
	if err := Set(c, false); err != nil {
		t.Fatal(err)
	}
	if Status(c).Enabled {
		t.Fatal("one removal did not clear a doubly written entry")
	}
}

func TestTheEntryNamesTheCoreAndItsArguments(t *testing.T) {
	// A Companion on a non-default data directory has to come back on the
	// same one, or after a reboot it serves an empty machine.
	c := cfg(t)
	c.Args = []string{"serve", "-data-dir", "/Volumes/Work/fylane data"}
	if err := Set(c, true); err != nil {
		t.Fatal(err)
	}
	body := entryBody(t, c)
	if body == "" {
		t.Skip("this platform does not store the entry as a readable file")
	}
	for _, want := range []string{"fylane-companion", "serve", "-data-dir", "fylane data"} {
		if !strings.Contains(body, want) {
			t.Errorf("the login entry does not carry %q:\n%s", want, body)
		}
	}
}

// entryBody reads the entry back where it is a file. Windows stores it in the
// registry and returns "".
func entryBody(t *testing.T, c Config) string {
	t.Helper()
	home, err := c.home()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		filepath.Join(home, "Library", "LaunchAgents", label+".plist"),
		filepath.Join(home, ".config", "autostart", label+".desktop"),
	} {
		if raw, err := os.ReadFile(p); err == nil {
			return string(raw)
		}
	}
	return ""
}
