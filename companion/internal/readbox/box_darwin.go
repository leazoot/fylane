//go:build darwin

package readbox

import (
	"fmt"
	"os"
	osexec "os/exec"
	"strings"
)

// sandboxExec is the only route left on macOS. sandbox_init(3) needs cgo and
// the Companion ships CGO_ENABLED=0, so the deprecated command-line front end
// is what there is. It has been deprecated for years and is still shipped, and
// the kernel mechanism behind it is the one the browsers use. If Apple ever
// removes it, this becomes an absence — said at startup, commands still run —
// and not a silent unbounded child.
const sandboxExec = "/usr/bin/sandbox-exec"

func detect() *Box {
	info, err := os.Stat(sandboxExec)
	if err != nil || info.IsDir() {
		return &Box{platform: Absent, net: NetAbsent,
			why:    "macOS on this machine has no " + sandboxExec + ", so subprocess reads are not bounded",
			netWhy: "macOS on this machine has no " + sandboxExec + ", so subprocess network access is not bounded"}
	}
	// The same profile denies every socket, not only TCP: measured against a
	// UDP DNS lookup, which fails to bind under it. macOS is therefore the
	// stronger of the two platforms here, and Linux says so about itself.
	return &Box{platform: Enforced, net: NetEnforced,
		why:    "subprocess reads are bounded by the macOS sandbox",
		netWhy: "subprocess network access is bounded by the macOS sandbox"}
}

// Wrap rewrites cmd to run under a profile that denies reads outside the
// allowed set.
//
// The profile allows everything by default and then denies reads. That is not
// laziness: this package bounds reads and only reads, so a deny-by-default
// profile would be claiming a boundary around writes, execution and the
// network that it does not have and that other layers already hold.
func (b *Box) Wrap(cmd *osexec.Cmd, p Policy) error {
	reads, network := b.bounds(p)
	if !reads && !network {
		return nil
	}
	if cmd.Path == "" {
		return fmt.Errorf("read boundary: the command has no program")
	}
	profile, err := profileFor(p, reads, network)
	if err != nil {
		return err
	}
	argv := append([]string{sandboxExec, "-p", profile, "--", cmd.Path}, cmd.Args[1:]...)
	cmd.Path = sandboxExec
	cmd.Args = argv
	return nil
}

func profileFor(p Policy, reads, network bool) (string, error) {
	paths := p.allPaths()
	if reads && len(paths) == 0 {
		return "", fmt.Errorf("read boundary: nothing would be readable")
	}
	// Only file-read-data is denied, not file-read-metadata. Resolving any
	// absolute path stats each component, so denying metadata turns every
	// program into a failure unrelated to what it was reading; contents are
	// what leak, and contents are what this stops. Measured, not assumed.
	//
	// (literal "/") is not decoration either: a subpath rule on /usr does not
	// cover the root directory itself, and without it the dynamic loader
	// aborts before the program's first instruction — which looked exactly
	// like a broken profile until it was bisected.
	var b strings.Builder
	b.WriteString("(version 1)\n(allow default)\n")
	if reads {
		subs := make([]string, 0, len(paths)+1)
		subs = append(subs, `(literal "/")`)
		for _, path := range paths {
			subs = append(subs, "(subpath "+quoteSBPL(path)+")")
		}
		b.WriteString("(deny file-read-data)\n")
		b.WriteString("(allow file-read-data " + strings.Join(subs, " ") + ")\n")
	}
	if network {
		// Every socket, measured: a UDP DNS lookup cannot bind under this,
		// which is why macOS reports NetEnforced and Linux reports NetPartial.
		// Local work is untouched — the profile denies nothing else.
		b.WriteString("(deny network*)\n")
	}
	return b.String(), nil
}

// IsShim is never true on macOS: sandbox-exec is the shim.
func IsShim([]string) bool { return false }

// RunShim cannot be reached on macOS; it exists so the command dispatcher
// needs no build tags of its own.
func RunShim([]string) error {
	return fmt.Errorf("read boundary shim: not used on darwin")
}
