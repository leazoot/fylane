package autostart

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows/registry"
)

// Windows: the per-user Run key. HKCU, so no elevation, and the entry is
// visible in Task Manager's Startup tab where a user expects to find it.

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

// valueName is what the user sees in Task Manager. It is a display name, not
// an identifier, which is why it is not `label`.
const valueName = "Fylane Companion"

// Status reports whether the Run value exists.
func Status(cfg Config) State {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
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
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("opening the Run key: %w", err)
	}
	defer k.Close()
	if !enable {
		// Already absent is the state the caller asked for, not an error.
		if err := k.DeleteValue(valueName); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return fmt.Errorf("removing the login item: %w", err)
		}
		return nil
	}
	cfg, err = cfg.resolve()
	if err != nil {
		return err
	}
	if err := k.SetStringValue(valueName, cfg.commandLine()); err != nil {
		return fmt.Errorf("writing the login item: %w", err)
	}
	return nil
}
