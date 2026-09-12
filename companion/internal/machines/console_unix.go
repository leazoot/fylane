//go:build !windows

package machines

import "os/exec"

func hideConsole(*exec.Cmd) {}
