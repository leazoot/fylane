//go:build !darwin && !windows

package autostart

import (
	"fmt"
	"os"
	"path/filepath"
)

// Everything else: the XDG autostart directory. It is what GNOME, KDE and Xfce
// all read, and a plain file the user can delete.

func desktopPath(cfg Config) (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" && cfg.Home == "" {
		return filepath.Join(dir, "autostart", label+".desktop"), nil
	}
	home, err := cfg.home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "autostart", label+".desktop"), nil
}

// Status reports whether the autostart entry exists.
func Status(cfg Config) State {
	path, err := desktopPath(cfg)
	if err != nil {
		return State{Supported: false, Detail: err.Error()}
	}
	_, err = os.Stat(path)
	return State{Supported: true, Enabled: err == nil}
}

// Set writes or removes the autostart entry.
func Set(cfg Config, enable bool) error {
	path, err := desktopPath(cfg)
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
		return fmt.Errorf("creating the autostart directory: %w", err)
	}
	body := "[Desktop Entry]\n" +
		"Type=Application\n" +
		"Name=Fylane Companion\n" +
		"Exec=" + cfg.commandLine() + "\n" +
		"Terminal=false\n" +
		"X-GNOME-Autostart-enabled=true\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return fmt.Errorf("writing the login item: %w", err)
	}
	return nil
}
