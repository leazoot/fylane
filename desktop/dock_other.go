//go:build !darwin

package main

// No Dock on this platform. Windows and Linux put running applications in a
// taskbar, and hiding from it is a different mechanism (a tool-window style
// on Windows) that this build does not implement — the setting says so
// instead of offering a switch that would do nothing.
const dockSupported = false

func applyDockHidden(bool) {}
