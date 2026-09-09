//go:build linux

package readbox

import (
	"fmt"
	"os"
	osexec "os/exec"

	"golang.org/x/sys/unix"
)

// Linux uses Landlock, which needs no new dependency: x/sys already carries
// the syscall numbers, the ruleset structs and the access constants.
//
// It also needs a re-exec shim. landlock_restrict_self applies to the calling
// thread and its future children, and os/exec offers no hook between fork and
// exec — so the Companion re-executes itself, the copy restricts itself, and
// then it execs the real program. The boundary is in force before the program
// has run one instruction.

// readAccess is what the boundary handles. Only reads: writes, execution and
// everything else stay outside the ruleset entirely, so Landlock does not
// bound them at all. That is the point of this package rather than an
// oversight — writes answer to the change-set transaction, which can show a
// diff and ask, and a kernel can only refuse.
const readAccess = unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_READ_DIR

// netAccess is the whole of what Landlock can deny outbound: two TCP verbs.
// There is no UDP equivalent in any released ABI, so DNS and QUIC leave the
// machine whatever this ruleset says. That is why Linux reports NetPartial
// and never NetEnforced.
const netAccess = unix.LANDLOCK_ACCESS_NET_BIND_TCP | unix.LANDLOCK_ACCESS_NET_CONNECT_TCP

// netABI is the first Landlock version that accepts access_net at all.
const netABI = 4

// shimArg marks the re-executed copy. It is a subcommand nobody would type.
const shimArg = "__readbox-exec"

func detect() *Box {
	abi, err := landlockABI()
	if err != nil || abi < 1 {
		return &Box{platform: Absent, net: NetAbsent,
			why:    "this Linux kernel has no Landlock, so subprocess reads are not bounded",
			netWhy: "this Linux kernel has no Landlock, so subprocess network access is not bounded"}
	}
	self, err := os.Executable()
	if err != nil {
		return &Box{platform: Absent, net: NetAbsent,
			why:    "this Companion cannot locate its own executable, so subprocess reads are not bounded",
			netWhy: "this Companion cannot locate its own executable, so subprocess network access is not bounded"}
	}
	box := &Box{platform: Enforced, self: self, net: NetAbsent,
		why:    "subprocess reads are bounded by Landlock",
		netWhy: "this Linux kernel's Landlock is too old to bound outbound network access"}
	if abi >= netABI {
		box.net = NetPartial
		box.netWhy = "Landlock denies outbound TCP; it has no UDP rule, so DNS and QUIC are not stopped"
	}
	return box
}

func landlockABI() (int, error) {
	abi, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0,
		uintptr(unix.LANDLOCK_CREATE_RULESET_VERSION))
	if errno != 0 {
		return 0, errno
	}
	return int(abi), nil
}

// Wrap re-points cmd at this executable, running as the shim, with the policy
// in the environment rather than in argv: argv is world-readable in the
// process table and a policy names the user's directories.
func (b *Box) Wrap(cmd *osexec.Cmd, p Policy) error {
	reads, network := b.bounds(p)
	if !reads && !network {
		return nil
	}
	if cmd.Path == "" {
		return fmt.Errorf("read boundary: the command has no program")
	}
	encoded, err := p.encode()
	if err != nil {
		return err
	}
	if cmd.Env == nil {
		// An inherited environment would hand the child everything this
		// process holds. Every caller in this Core sets Env; refusing here
		// keeps that true rather than silently making an exception.
		return fmt.Errorf("read boundary: the command has no explicit environment")
	}
	cmd.Env = append(cmd.Env, policyEnv+"="+encoded)
	cmd.Args = append([]string{b.self, shimArg, cmd.Path}, cmd.Args[1:]...)
	cmd.Path = b.self
	return nil
}

// IsShim reports whether these are the arguments of a re-executed copy.
func IsShim(args []string) bool {
	return len(args) > 2 && args[1] == shimArg
}

// RunShim applies the boundary to this process and then becomes the real
// program. It never returns on success.
//
// Every failure here refuses to exec. A shim that could not build the
// boundary and ran the program anyway would be the silent downgrade this
// package exists to prevent.
func RunShim(args []string) error {
	if !IsShim(args) {
		return fmt.Errorf("read boundary shim: wrong arguments")
	}
	raw := os.Getenv(policyEnv)
	if raw == "" {
		return fmt.Errorf("read boundary shim: no policy")
	}
	p, err := decodePolicy(raw)
	if err != nil {
		return err
	}
	if err := restrictSelf(p); err != nil {
		return err
	}
	if err := os.Unsetenv(policyEnv); err != nil {
		return fmt.Errorf("read boundary shim: %w", err)
	}
	prog := args[2]
	argv := append([]string{prog}, args[3:]...)
	return unix.Exec(prog, argv, os.Environ())
}

func restrictSelf(p Policy) error {
	// The shim re-probes rather than trusting what the parent decided: this
	// process is the one the kernel will answer, and a policy that asked for
	// more than this kernel offers must narrow here rather than fail. A
	// kernel too old for access_net cannot deny the network, and that is an
	// absence the workspace face states — not a reason to refuse to run the
	// command, which is the settled rule.
	abi, err := landlockABI()
	if err != nil {
		return fmt.Errorf("read boundary: reading the Landlock version: %w", err)
	}
	attr := unix.LandlockRulesetAttr{Access_fs: readAccess}
	if !p.Network && abi >= netABI {
		// Handled but never granted: a ruleset that handles an access and
		// adds no rule for it denies that access outright.
		attr.Access_net = netAccess
	}
	fd, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET,
		uintptr(unsafePointer(&attr)), unsafeSizeof(attr), 0)
	if errno != 0 {
		return fmt.Errorf("read boundary: creating the ruleset: %w", errno)
	}
	ruleset := int(fd)
	defer unix.Close(ruleset)

	for _, path := range p.allPaths() {
		dirFD, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
		if err != nil {
			// A path that is not there cannot be granted, and granting the
			// rest is the right answer: the alternative is refusing to run
			// because a cache directory has not been created yet.
			continue
		}
		rule := unix.LandlockPathBeneathAttr{Allowed_access: readAccess, Parent_fd: int32(dirFD)}
		_, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, uintptr(ruleset),
			uintptr(unix.LANDLOCK_RULE_PATH_BENEATH), uintptr(unsafePointer(&rule)), 0, 0, 0)
		unix.Close(dirFD)
		if errno != 0 {
			return fmt.Errorf("read boundary: adding a rule: %w", errno)
		}
	}

	// Without no-new-privs the kernel refuses to restrict an unprivileged
	// process at all.
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("read boundary: no_new_privs: %w", err)
	}
	if _, _, errno := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, uintptr(ruleset), 0, 0); errno != 0 {
		return fmt.Errorf("read boundary: applying the ruleset: %w", errno)
	}
	return nil
}
