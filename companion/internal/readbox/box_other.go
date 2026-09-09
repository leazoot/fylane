//go:build !linux && !darwin

package readbox

import (
	"fmt"
	osexec "os/exec"
	"runtime"
)

// Windows has no equivalent of a per-process read boundary that this design
// could use, and saying so is the honest answer. Every other check — the path
// sandbox, the dangerous-command rule table, the approval rung, the audit log
// — runs here exactly as it does everywhere else. What is missing is stated
// at startup rather than implied by silence.

func detect() *Box {
	return &Box{platform: Absent, net: NetAbsent,
		why:    "no read boundary is available on " + runtime.GOOS + ", so subprocess reads are not bounded",
		netWhy: "no outbound network boundary is available on " + runtime.GOOS}
}

// Wrap does nothing where there is no boundary to apply.
func (b *Box) Wrap(*osexec.Cmd, Policy) error { return nil }

// IsShim is never true off Linux: no platform here needs a re-exec shim.
func IsShim([]string) bool { return false }

// RunShim cannot be reached; it exists so the command dispatcher compiles
// everywhere without build tags of its own.
func RunShim([]string) error {
	return fmt.Errorf("read boundary shim: not used on %s", runtime.GOOS)
}
