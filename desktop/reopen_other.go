//go:build !darwin

package main

// installDockReopen is macOS-only: nowhere else has a Dock tile that stays
// while the window is hidden.
func installDockReopen() {}
