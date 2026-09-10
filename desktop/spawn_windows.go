package main

import (
	"os/exec"
	"syscall"
)

// A console-subsystem child started from a GUI process gets a console window
// of its own on Windows, and the Core is a console program on purpose — its
// command-line uses (`share`, `serve` from a terminal) need to print. So the
// window is suppressed here, at the one place the shell starts it, rather
// than by making the Core a GUI program and losing its output everywhere.
func hideChildConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: syscall.CREATE_NO_WINDOW}
}
