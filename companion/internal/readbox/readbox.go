// Package readbox puts a kernel read boundary around the programs the
// Companion starts.
//
// It bounds reads and nothing else, and that is the whole design .
// Writes already answer to the 14-step change-set transaction, which can say
// "here is the diff, shall I?" — a kernel can only say no, so putting writes
// behind one would trade a better mechanism for a worse one. Reads are the
// opposite case: nothing in this product can see what a subprocess reads, and
// the F21/F22 residual is exactly that. A name list of "readers" can never be
// complete; a kernel boundary does not need to be.
//
// Three rules are written into the design rather than left to callers.
//
// It is an addition, never a replacement. The rule table, the path sandbox,
// the approval rung and the audit log all run exactly as before, on every
// platform, whether or not a boundary is in force.
//
// Absence and failure are different. A platform that cannot do this — Windows,
// a Linux kernel before Landlock, a macOS without sandbox-exec — is an absence:
// it is said at startup and commands run as they always did. A platform that
// can do it, where building the boundary then fails, is a failure: the command
// is refused. Silently running unboxed where a boundary was expected is the
// one outcome this package will not produce.
//
// The allowed set is the workspace, read-write, plus the toolchain caches,
// read-only. That list will fall behind — it is the same species as the F21
// reader tables — but it fails in the opposite direction: a missing entry
// makes a build fail loudly, not a secret leak quietly.
package readbox

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
)

// State is what this Companion can do about reads.
type State string

const (
	// Enforced means every child this package wraps is bounded.
	Enforced State = "enforced"
	// Absent means the platform offers no boundary. Commands run as they did
	// before, and every other check is unchanged.
	Absent State = "absent"
	// Off means the user turned the boundary off. It is not a bypass flag:
	// nothing else changes, and the setting is visible.
	Off State = "off"
)

// NetState is what this Companion can do about a child's outbound network.
// It is a separate word from State because the two boundaries are not equally
// strong on the same machine, and one enum reporting both would have to pick
// which of them it was lying about.
type NetState string

const (
	// NetEnforced denies every outbound socket.
	NetEnforced NetState = "enforced"
	// NetPartial denies TCP and nothing else. Landlock handles
	// LANDLOCK_ACCESS_NET_BIND_TCP and CONNECT_TCP and has no UDP equivalent,
	// so DNS and QUIC still leave the machine. It stops most accidental
	// traffic — HTTPS is TCP — and does not stop deliberate exfiltration.
	// Saying "enforced" here would be the silent downgrade this package was
	// written to prevent, so it has its own word.
	NetPartial NetState = "partial"
	// NetAbsent means the platform offers no outbound boundary.
	NetAbsent NetState = "absent"
)

// Policy is what one child may read, and whether it may reach the network.
type Policy struct {
	// ReadWrite are absolute paths the child may read. Writes are not bounded
	// by this package at all; the name says which side of the boundary the
	// workspace sits on, not that writes elsewhere are stopped.
	ReadWrite []string `json:"read_write"`
	// ReadOnly are absolute paths the child may read but that are not its
	// working material: system directories and toolchain caches.
	ReadOnly []string `json:"read_only"`
	// Network is whether this child may talk to the network. True is the
	// permissive value on purpose: refusing by default would make the first
	// `npm install` in every workspace fail with an error that never mentions
	// the network, and a defence people turn off is worse than one they never
	// had — it still makes them believe they are covered.
	Network bool `json:"network"`
}

// Box applies the boundary. The zero value is unusable; call New.
type Box struct {
	// platform is what this machine can do, and it is only ever Enforced or
	// Absent. It is probed once, at New, and never changes afterwards.
	platform State
	why      string
	// self is this executable, re-executed as the shim on platforms that need
	// one. Empty elsewhere.
	self string
	// net and netWhy are the outbound boundary's capability, probed once
	// beside the read boundary's. There is no user switch here: whether a
	// child may reach the network is a property of the workspace it is
	// working in, and it travels in the Policy.
	net    NetState
	netWhy string
	// on is the user's setting. It is separate from platform because the two
	// answer different questions, and a single field that held either would
	// make "this machine cannot" and "I turned it off" the same value on the
	// one screen whose whole job is to tell them apart.
	on atomic.Bool
}

// New builds a Box. The platform is probed either way: a Box that is off still
// knows whether this machine could enforce a boundary, and the settings page
// needs that to say "off" where it is true and "absent" where it is not.
func New(enabled bool) *Box {
	b := detect()
	b.on.Store(enabled)
	return b
}

// SetEnabled applies the user's choice to the running Core. It takes effect on
// the next child started, not on the next restart: a settings page whose
// switch does nothing until the process comes back is the quiet kind of
// failure this product does not ship.
func (b *Box) SetEnabled(v bool) {
	if b != nil {
		b.on.Store(v)
	}
}

// State reports what this Box does.
func (b *Box) State() State {
	if b == nil || b.platform != Enforced {
		return Absent
	}
	if !b.on.Load() {
		return Off
	}
	return Enforced
}

// Why is one line for the startup log and the settings page. It never carries
// a workspace path.
func (b *Box) Why() string {
	if b == nil {
		return "no read boundary is configured"
	}
	if b.State() == Off {
		// Not b.why: that sentence describes the mechanism this machine has,
		// and reporting it while the boundary is off would describe a
		// boundary that is not in force.
		return "the read boundary is turned off in settings"
	}
	return b.why
}

// Enforcing reports whether a wrapped child's reads are actually bounded.
func (b *Box) Enforcing() bool { return b.State() == Enforced }

// Network reports how much of a child's outbound traffic this machine can
// deny. It is the platform's answer only: the choice of whether to deny it
// belongs to a workspace and arrives in the Policy.
func (b *Box) Network() NetState {
	if b == nil || b.net == "" {
		return NetAbsent
	}
	return b.net
}

// NetworkWhy is one line for the startup log and the workspace face, saying
// what this machine can and cannot stop. It never carries a workspace path.
func (b *Box) NetworkWhy() string {
	if b == nil || b.netWhy == "" {
		return "no outbound network boundary is available on this machine"
	}
	return b.netWhy
}

// Reach is what a child actually gets, in one word, for the approval face and
// the workspace row. It is deliberately four values rather than a boolean:
// "the workspace asked for no network and this machine cannot deny it" is a
// different answer from either yes or no, and the screen that hides it would
// be reporting a boundary that is not there.
type Reach string

const (
	// ReachAllowed is a workspace that permits outbound traffic.
	ReachAllowed Reach = "allowed"
	// ReachDenied is asked for and fully delivered.
	ReachDenied Reach = "denied"
	// ReachPartial is asked for and delivered against TCP only.
	ReachPartial Reach = "partial"
	// ReachUnbounded is asked for and not delivered at all. It is not a
	// failure — the command still runs, exactly as it did before this
	// boundary existed — but it is never silent.
	ReachUnbounded Reach = "unbounded"
)

// Reach reports what a child working under this policy gets.
func (b *Box) Reach(p Policy) Reach {
	if p.Network {
		return ReachAllowed
	}
	switch b.Network() {
	case NetEnforced:
		return ReachDenied
	case NetPartial:
		return ReachPartial
	default:
		return ReachUnbounded
	}
}

// bounds reports which of the two boundaries this call actually applies.
//
// They are separate promises and are answered separately: turning the read
// boundary off in settings must not quietly drop a workspace's "no network",
// and a workspace that allows the network must still have its reads bounded.
func (b *Box) bounds(p Policy) (reads, network bool) {
	return b.Enforcing(), !p.Network && b.Network() != NetAbsent
}

// WorkspacePolicy is the boundary for a program working in one workspace: the
// workspace itself, plus what a toolchain has to be able to read to run.
//
// network is passed rather than defaulted because nothing here can work it
// out: whether the programs of one workspace may reach the network is the
// local user's decision about that workspace, and a caller that forgot to
// answer would silently get the permissive half.
func WorkspacePolicy(root string, network bool) Policy {
	p := Policy{ReadWrite: []string{root}, Network: network}
	p.ReadOnly = append(p.ReadOnly, systemReadable()...)
	p.ReadOnly = append(p.ReadOnly, tempArea()...)
	p.ReadOnly = append(p.ReadOnly, toolchainCaches()...)
	p.ReadOnly = append(p.ReadOnly, toolConfigs()...)
	p.ReadOnly = existing(p.ReadOnly)
	return p
}

// tempArea is where the toolchains put their scratch files. Without it `go
// build` cannot read back the importcfg it just wrote and git cannot open its
// own cache — measured, not guessed. On macOS TMPDIR is a per-user container
// holding both the temp and cache directories, so its parent is what is
// named.
func tempArea() []string {
	out := []string{os.TempDir()}
	if runtime.GOOS == "darwin" {
		if resolved, err := filepath.EvalSymlinks(os.TempDir()); err == nil {
			out = append(out, resolved, filepath.Dir(resolved))
		}
		out = append(out, "/private/tmp")
	}
	return out
}

// toolConfigs are the per-user configuration files a toolchain reads before
// it will run at all. The line is drawn at configuration, not credentials:
// .gitconfig is here and .netrc, .npmrc, .ssh, .aws and .docker are not. A
// build that needs a private registry token therefore fails, loudly, which is
// the direction this boundary is supposed to fail in.
func toolConfigs() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(home, ".gitconfig"),
		filepath.Join(home, ".config", "git"),
		filepath.Join(home, ".gitignore_global"),
		filepath.Join(home, ".editorconfig"),
	}
}

// toolchainCaches are the directories a build reads and that are nobody's
// secrets: module caches, compiler caches, installed toolchains.
//
// The environment is consulted first because a machine that moved its caches
// has said where they are, and the defaults under the home directory are
// added regardless — a user with GOMODCACHE set may still have a Rust
// toolchain in the usual place.
func toolchainCaches() []string {
	var out []string
	for _, name := range []string{
		"GOROOT", "GOPATH", "GOMODCACHE", "GOCACHE", "CARGO_HOME", "RUSTUP_HOME",
		"npm_config_cache", "PNPM_HOME", "PYENV_ROOT", "NVM_DIR", "JAVA_HOME",
	} {
		if v := os.Getenv(name); v != "" && filepath.IsAbs(v) {
			out = append(out, v)
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return out
	}
	for _, rel := range []string{
		"go/pkg/mod", "go/bin", ".cargo", ".rustup", ".npm", ".nvm", ".bun",
		".cache", ".pyenv", ".rbenv", ".sdkman", ".gradle", ".m2", ".deno",
		".local/share/pnpm", ".local/lib", ".local/bin", ".volta", ".asdf",
	} {
		out = append(out, filepath.Join(home, filepath.FromSlash(rel)))
	}
	if runtime.GOOS == "darwin" {
		// macOS puts build caches under ~/Library/Caches, which also holds
		// every browser's and every app's. The named subdirectories are
		// allowed and the parent is not: a compiler cache is a toolchain, a
		// browser cache is the user's browsing.
		//
		// GOCACHE is the one that bites first — the Go toolchain does not
		// export it, so reading the environment finds nothing and a build
		// fails with "cannot find package", which is what happened here.
		for _, rel := range []string{
			"go-build", "pip", "Homebrew", "node-gyp", "typescript", "deno",
			"ms-playwright", "electron", "yarn", "uv",
		} {
			out = append(out, filepath.Join(home, "Library", "Caches", rel))
		}
		out = append(out, filepath.Join(home, "Library", "pnpm"))
	}
	return out
}

// existing drops paths that are not there, and sorts what is left. A boundary
// naming directories that do not exist is not wrong, but the kernel
// interfaces differ on whether they mind, and a short list is easier to read
// in a bug report.
func existing(paths []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil || seen[abs] {
			continue
		}
		if _, err := os.Stat(abs); err != nil {
			continue
		}
		seen[abs] = true
		out = append(out, abs)
	}
	sort.Strings(out)
	return out
}

// systemReadable are the directories every program needs before it can do
// anything at all: the dynamic loader, the shared libraries, the certificate
// store, the terminal database.
func systemReadable() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{
			"/usr", "/bin", "/sbin", "/System", "/Library", "/opt", "/private/etc",
			"/private/var/db", "/private/var/select", "/dev", "/Applications",
			"/private/var/run",
		}
	case "linux":
		return []string{
			"/usr", "/bin", "/sbin", "/lib", "/lib32", "/lib64", "/etc", "/opt",
			"/proc", "/sys", "/dev", "/run", "/tmp", "/var/tmp", "/snap", "/nix",
		}
	default:
		return nil
	}
}

// policyEnv is how a policy reaches the shim. It travels in the environment
// rather than in argv because argv is world-readable in the process table and
// a policy names the user's directories.
const policyEnv = "FYLANE_READBOX_POLICY"

func (p Policy) encode() (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("encoding the read boundary: %w", err)
	}
	return string(b), nil
}

func decodePolicy(s string) (Policy, error) {
	var p Policy
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		return Policy{}, fmt.Errorf("unreadable read boundary: %w", err)
	}
	if len(p.ReadWrite) == 0 && len(p.ReadOnly) == 0 {
		return Policy{}, fmt.Errorf("read boundary allows nothing at all")
	}
	return p, nil
}

// allPaths is every directory the policy allows reading, deduplicated.
func (p Policy) allPaths() []string {
	out := append([]string{}, p.ReadWrite...)
	out = append(out, p.ReadOnly...)
	seen := map[string]bool{}
	kept := out[:0]
	for _, s := range out {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		kept = append(kept, s)
	}
	return kept
}

// quoteSBPL renders a path as a macOS sandbox profile string literal.
func quoteSBPL(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
