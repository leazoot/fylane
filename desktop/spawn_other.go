//go:build !windows

package main

import "os/exec"

// Only Windows hands a console child its own window.
func hideChildConsole(*exec.Cmd) {}
