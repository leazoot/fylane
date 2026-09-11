package autostart

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"golang.org/x/sys/windows/registry"
)

// Windows: the per-user Run key. HKCU, so no elevation, and the entry is
// visible in Task Manager's Startup tab where a user expects to find it.

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

// valueName is what the user sees in Task Manager. It is a display name, not
// an identifier, which is why it is not `label`.
const valueName = "Fylane"

// runKeyFor is the key the entry lives in. Production uses the real Run key.
// A Config with Home set is a test — that is the field's only purpose — and
// a test must not touch the user's actual login entries: two test packages
// driving one registry value in parallel is a race, and a developer who
// genuinely starts Fylane at login would fail "off by default". So a Home
// selects a private key of its own, which Set(false) deletes outright.
func runKeyFor(cfg Config) (path string, private bool) {
	if cfg.Home == "" {
		return runKey, false
	}
	sum := sha256.Sum256([]byte(cfg.Home))
	return `Software\FylaneTest-` + hex.EncodeToString(sum[:6]), true
}

// Status reports whether the Run value exists.
func Status(cfg Config) State {
	path, _ := runKeyFor(cfg)
	k, err := registry.OpenKey(registry.CURRENT_USER, path, registry.QUERY_VALUE)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return State{Supported: true, Enabled: false}
		}
		return State{Supported: false, Detail: err.Error()}
	}
	defer k.Close()
	_, _, err = k.GetStringValue(valueName)
	return State{Supported: true, Enabled: err == nil}
}

// Set writes or deletes the Run value.
func Set(cfg Config, enable bool) error {
	path, private := runKeyFor(cfg)
	if !enable {
		return remove(path, private)
	}
	cfg, err := cfg.resolve()
	if err != nil {
		return err
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, path, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("opening the Run key: %w", err)
	}
	defer k.Close()
	if err := k.SetStringValue(valueName, cfg.commandLine()); err != nil {
		return fmt.Errorf("writing the login item: %w", err)
	}
	return nil
}

// remove deletes the value; a private key goes with it so a test run leaves
// nothing in the registry. Already absent is the state the caller asked
// for, not an error.
func remove(path string, private bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, path, registry.SET_VALUE)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("opening the Run key: %w", err)
	}
	err = k.DeleteValue(valueName)
	k.Close()
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return fmt.Errorf("removing the login item: %w", err)
	}
	if private {
		if err := registry.DeleteKey(registry.CURRENT_USER, path); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return fmt.Errorf("removing the test key: %w", err)
		}
	}
	return nil
}
