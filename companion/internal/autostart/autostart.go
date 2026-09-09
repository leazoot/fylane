// Package autostart registers the Companion to start when the user logs in.
//
// What gets registered is the Core, not the window: the setting's promise is
// that the lane keeps working after a reboot, and the lane is the Core. The
// window can be opened whenever the user wants one.
//
// Every platform stores this somewhere the user can inspect and delete by
// hand — a LaunchAgent plist, a registry value, a .desktop file. None of them
// needs elevation, and turning the setting off removes the entry rather than
// disabling it, so nothing is left behind that a later version has to know
// about.
package autostart

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// label identifies the entry on every platform. It is stable: changing it
// would orphan the entry an earlier version wrote.
const label = "com.fylane.companion"

// State is what the settings row shows.
type State struct {
	// Supported is false where this build has no way to register an entry.
	// The row is then shown disabled with Detail as its explanation, rather
	// than offering a switch that would silently do nothing.
	Supported bool `json:"supported"`
	Enabled   bool `json:"enabled"`
	Detail    string `json:"detail,omitempty"`
}

// Config is what the login entry has to reproduce. Exe is the Core's own
// path; Args are the arguments `serve` was started with, so a Companion using
// a non-default data directory keeps using it after a reboot.
type Config struct {
	Exe  string
	Args []string
	// Home overrides the user's home directory. Tests set it; production
	// leaves it empty and the OS answers.
	Home string
}

func (c Config) home() (string, error) {
	if c.Home != "" {
		return c.Home, nil
	}
	dir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating the home directory: %w", err)
	}
	return dir, nil
}

// resolve fills in the executable when the caller did not name one.
func (c Config) resolve() (Config, error) {
	if c.Exe == "" {
		exe, err := os.Executable()
		if err != nil {
			return c, fmt.Errorf("locating this executable: %w", err)
		}
		// A symlinked binary is normal on macOS (Homebrew, /usr/local/bin);
		// recording the link would break the entry the day it is repointed.
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		c.Exe = exe
	}
	if len(c.Args) == 0 {
		c.Args = []string{"serve"}
	}
	return c, nil
}

// commandLine renders exe + args for the platforms that store one string.
func (c Config) commandLine() string {
	parts := make([]string, 0, len(c.Args)+1)
	parts = append(parts, quote(c.Exe))
	for _, a := range c.Args {
		parts = append(parts, quote(a))
	}
	return strings.Join(parts, " ")
}

func quote(s string) string {
	if s == "" || strings.ContainsAny(s, " \t\"") {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return s
}
