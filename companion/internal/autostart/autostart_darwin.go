package autostart

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
)

// macOS: a per-user LaunchAgent. It needs no elevation, survives updates, and
// the user can see and delete it in ~/Library/LaunchAgents.

func plistPath(cfg Config) (string, error) {
	home, err := cfg.home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist"), nil
}

// Status reports whether the login entry exists.
func Status(cfg Config) State {
	path, err := plistPath(cfg)
	if err != nil {
		return State{Supported: false, Detail: err.Error()}
	}
	_, err = os.Stat(path)
	return State{Supported: true, Enabled: err == nil}
}

// Set writes or removes the LaunchAgent. Removing is unconditional: an entry
// that is already gone is the state the caller asked for, not an error.
func Set(cfg Config, enable bool) error {
	path, err := plistPath(cfg)
	if err != nil {
		return err
	}
	if !enable {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing the login item: %w", err)
		}
		return nil
	}
	cfg, err = cfg.resolve()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating LaunchAgents: %w", err)
	}
	body, err := plist(cfg)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return fmt.Errorf("writing the login item: %w", err)
	}
	return nil
}

func plist(cfg Config) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n<dict>\n")
	b.WriteString("  <key>Label</key>\n  <string>" + label + "</string>\n")
	b.WriteString("  <key>ProgramArguments</key>\n  <array>\n")
	// A path may contain & or <; writing it raw would produce a plist launchd
	// refuses to parse, and the failure would only show up at the next login.
	for _, arg := range append([]string{cfg.Exe}, cfg.Args...) {
		var esc bytes.Buffer
		if err := xml.EscapeText(&esc, []byte(arg)); err != nil {
			return nil, fmt.Errorf("encoding the login item: %w", err)
		}
		b.WriteString("    <string>" + esc.String() + "</string>\n")
	}
	b.WriteString("  </array>\n")
	b.WriteString("  <key>RunAtLoad</key>\n  <true/>\n")
	// KeepAlive is deliberately off. The Core is allowed to exit — the user
	// can quit it — and a watchdog that restarted it would take that away.
	b.WriteString("  <key>KeepAlive</key>\n  <false/>\n")
	b.WriteString("  <key>ProcessType</key>\n  <string>Background</string>\n")
	b.WriteString("</dict>\n</plist>\n")
	return b.Bytes(), nil
}
