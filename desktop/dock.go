package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// shellPrefsName is the shell's own settings file. The Core keeps the
// machine-level preferences it acts on; this one is about how the shell
// presents itself, is needed before the Core has answered anything, and so
// lives with the shell.
const shellPrefsName = "desktop.json"

type shellPrefs struct {
	DockHidden bool `json:"dock_hidden"`
}

// DockInfo is what the settings page draws from. Where there is no Dock the
// page draws nothing — hiding an icon is a convenience, not a defence, so an
// absent option needs no explanation the way an absent boundary does.
type DockInfo struct {
	Supported bool `json:"supported"`
	Hidden    bool `json:"hidden"`
}

func (a *App) readShellPrefs() shellPrefs {
	var p shellPrefs
	raw, err := os.ReadFile(filepath.Join(a.dataDir, shellPrefsName))
	if err != nil {
		// Absent is the ordinary first-run state; unreadable is treated the
		// same way rather than refusing to start the shell over a setting.
		return p
	}
	_ = json.Unmarshal(raw, &p)
	return p
}

func (a *App) writeShellPrefs(p shellPrefs) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(a.dataDir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(a.dataDir, shellPrefsName), raw, 0o600)
}

func (a *App) dockInfo() DockInfo {
	if !dockSupported {
		return DockInfo{Supported: false}
	}
	return DockInfo{Supported: true, Hidden: a.readShellPrefs().DockHidden}
}

// Dock reports whether the Dock icon is hidden, and whether it can be.
func (a *App) Dock() (string, error) {
	return marshal(a.dockInfo())
}

// SetDockHidden hides or shows the Dock icon and remembers the choice. It
// takes effect at once and again at every start, before the window appears.
func (a *App) SetDockHidden(hidden bool) (string, error) {
	if !dockSupported {
		return "", errors.New("this system has no Dock icon to hide")
	}
	if err := a.writeShellPrefs(shellPrefs{DockHidden: hidden}); err != nil {
		return "", fmt.Errorf("remembering the Dock setting: %w", err)
	}
	applyDockHidden(hidden)
	return marshal(a.dockInfo())
}

func marshal(v any) (string, error) {
	raw, err := json.Marshal(v)
	return string(raw), err
}
