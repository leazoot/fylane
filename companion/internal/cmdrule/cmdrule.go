// Package cmdrule decides how much authority a command needs before it runs.
//
// It answers one question — allow, ask, or refuse — and answers it from a
// readable table rather than scattered conditionals, because this table is
// the thing a user has to be able to audit before turning the approval rung
// down.
//
// The limit worth stating plainly: cmdexec bounds where a process *starts*
// (its working directory, its program, its environment), not what it can
// *reach*. A process launched inside the workspace still runs with the user's
// own permissions and can touch any file they can. Until Companion runs
// commands under an OS-level sandbox — seatbelt on macOS, namespaces or
// bubblewrap on Linux, AppContainer on Windows — this table is the only thing
// standing between a command and the rest of the disk. It is written for that
// responsibility, and it is still not a substitute for the real sandbox.
package cmdrule

import (
	"path"
	"path/filepath"
	"strings"

	"github.com/leazoot/fylane/companion/internal/cmdexec"
)

// Verdict is how much authority a command needs.
type Verdict string

const (
	// Allow runs under whatever grant the workspace already has.
	Allow Verdict = "allow"
	// Confirm needs a human, at every approval rung except the open one.
	Confirm Verdict = "confirm"
	// Disclose changes nothing and therefore never triggered any of the
	// rules above — it hands over the machine's own state instead: the
	// process table, the environment, a file outside the workspace. It asks
	// at *every* rung, the open one included.
	//
	// This is the one place the open rung does not mean "stop asking" (
	// ③). The reason is that what the open rung was sold as — run my daily
	// commands without nagging me — is not what these commands do. A single
	// `ps -eo args` sent 944 lines of process table, and another process's
	// tunnel token with it, to a platform. Nothing was modified, so every
	// other tier answered allow. The user turning the rung down was agreeing
	// to unattended work in a workspace, not to handing over the machine.
	Disclose Verdict = "disclose"
	// Block never runs, at any rung. The open rung turns off *asking*, not
	// checking, so these stay refused.
	Block Verdict = "block"
)

// Decision is the table's answer. Rule is a stable identifier so the desktop
// UI and the audit log can key off it instead of matching on prose.
type Decision struct {
	Verdict Verdict `json:"verdict"`
	Rule    string  `json:"rule,omitempty"`
	Reason  string  `json:"reason,omitempty"`
}

// Request is one command to judge.
type Request struct {
	Argv []string
	// Root is the absolute workspace root and Dir the workspace-relative
	// working directory. Both are needed to tell whether a path argument
	// points out of the workspace.
	Root string
	Dir  string
}

// Check returns the most severe verdict any rule reaches for req. Tiers are
// evaluated in descending severity — Block, then Disclose, then Confirm — and
// within a tier the first match wins, so the reported rule is always the most
// serious reason to stop.
//
// Disclose outranks Confirm because it is the stricter answer in practice:
// Confirm can be waived by the open rung, Disclose cannot.
func Check(req Request) Decision {
	c := parse(req)
	if c.prog == "" {
		return Decision{Verdict: Allow}
	}
	for _, tier := range []struct {
		verdict Verdict
		rules   []rule
	}{
		{Block, blockRules},
		{Disclose, discloseRules},
		{Confirm, confirmRules},
	} {
		for _, r := range tier.rules {
			if r.match(c) {
				return Decision{Verdict: tier.verdict, Rule: r.id, Reason: r.reason}
			}
		}
	}
	return Decision{Verdict: Allow}
}

type rule struct {
	id     string
	reason string
	match  func(c cmdline) bool
}

// blockRules cover what has no legitimate use from an agent and no bounded
// blast radius: another user's privileges, a shell smuggled in as an
// argument, raw devices, git's own object store, and deletion outside the
// workspace.
var blockRules = []rule{
	{
		id:     "privilege-escalation",
		reason: "runs the command as another user, outside anything the workspace can bound",
		match: func(c cmdline) bool {
			return in(c.prog, "sudo", "doas", "su", "runas", "pkexec", "gsudo")
		},
	},
	{
		id:     "shell-in-arguments",
		reason: "an argument names a shell interpreter, which would bring back the shell grammar the engine refuses",
		// The wrapper list and the reasoning behind scanning only wrappers
		// live in cmdexec beside the shell list itself, because the MCP
		// gateway has to ask the same question and answered it more weakly
		// while it had its own version.
		match: func(c cmdline) bool {
			return cmdexec.SpellsAShell(c.argv)
		},
	},
	{
		id:     "builds-command-lines",
		reason: "constructs and runs command lines of its own, which this table cannot inspect",
		match: func(c cmdline) bool {
			return in(c.prog, "xargs", "parallel")
		},
	},
	{
		id:     "raw-device-access",
		reason: "writes to disks or devices rather than to files",
		match: func(c cmdline) bool {
			if in(c.prog, "fdisk", "sfdisk", "parted", "diskutil", "mkswap", "wipefs", "badblocks") {
				return true
			}
			if strings.HasPrefix(c.prog, "mkfs") {
				return true
			}
			if c.prog == "dd" {
				for _, a := range c.args {
					if strings.HasPrefix(a, "of=") {
						return true
					}
				}
			}
			return false
		},
	},
	{
		id:     "git-internals",
		reason: "modifies git's own object store, which corrupts history rather than changing it",
		match: func(c cmdline) bool {
			if !in(c.prog, mutators...) {
				return false
			}
			for _, p := range c.pathArgs() {
				for _, comp := range strings.Split(slashed(p), "/") {
					if strings.EqualFold(comp, ".git") {
						return true
					}
				}
			}
			return false
		},
	},
	{
		id:     "delete-workspace-root",
		reason: "deletes the workspace itself",
		match: func(c cmdline) bool {
			if !in(c.prog, "rm", "rmdir", "unlink", "shred") {
				return false
			}
			for _, p := range c.pathArgs() {
				if c.isWorkspaceRoot(p) {
					return true
				}
			}
			return false
		},
	},
	{
		id:     "delete-outside-workspace",
		reason: "deletes a path outside the workspace",
		match: func(c cmdline) bool {
			if !in(c.prog, "rm", "rmdir", "unlink", "shred") {
				return false
			}
			for _, p := range c.pathArgs() {
				if c.outsideWorkspace(p) {
					return true
				}
			}
			return false
		},
	},
}

// discloseRules cover commands that modify nothing — which is exactly why no
// other tier catches them — and report the machine instead of the workspace.
// The workspace boundary is the promise this product makes; a command that
// reads around it has to be asked about, at every rung.
var discloseRules = []rule{
	{
		// What this tier holds is not "reads the machine" but "is where the
		// credentials are kept". An environment dump and a keychain query do
		// not merely risk carrying a secret the way a process list does —
		// reading them out *is* handing over the store.
		id:     "reads-credential-store",
		reason: "reads where this machine keeps its secrets — environment variables and the keychain are the store itself, not a report about the machine",
		match: func(c cmdline) bool {
			switch c.prog {
			case "printenv", "security", "dscl", "defaults":
				return true
			case "env":
				// env is also how a command gets its environment set, and
				// that form execs a program the table judges on its own
				// terms. Only the form that prints the environment discloses.
				return !c.execsAProgram()
			}
			return false
		},
	},
	{
		// This rule defends the boundary rather than a list of programs, and
		// that is the whole point of it.
		//
		// It used to name the programs whose job is to print a file — cat,
		// head, grep and a dozen more. Measured against the real table, that
		// left `sort`, `awk`, `sed`, `perl`, `jq`, `tar` and `python3 -c`
		// reading anything on the disk with nobody asked, because a short
		// list of readers is an enumeration over an open set and the next
		// program nobody thought of is always outside it.
		//
		// So the question is no longer "is this a reader" but "does this
		// command name a path outside the workspace", which no new program
		// can walk around. Writing outside is judged by the same rule for the
		// same reason, and lands in the same tier: the open rung is a user
		// saying not to interrupt their work in a folder, which was never
		// consent to reach past it.
		//
		// The cost is real and was accepted deliberately: `cat
		// ../sibling-repo/README.md` and `go test ../shared/...` now ask.
		id:     "touches-outside-workspace",
		reason: "names a path outside the workspace, which is the boundary the caller was given",
		match: func(c cmdline) bool {
			for _, p := range c.pathArgs() {
				if c.outsideWorkspace(p) {
					return true
				}
			}
			return false
		},
	},
}

// confirmRules cover what a developer does on purpose and an agent should not
// do behind their back: throwing away work, sending it somewhere, widening
// permissions, or changing the machine outside this workspace.
var confirmRules = []rule{
	{
		// Machine-shaped, but ordinary development work: a developer looks at
		// processes, ports and logs all day. It follows the rung like any
		// other gated command — asked about at the two rungs that ask, and
		// allowed at the rung where the user said not to interrupt them
		// (2026-08-30).
		//
		// It stays gated rather than allowed outright because the incident
		// that created internal/redact was exactly this: `ps -eo …,args` sent
		// 944 lines of process table to a platform with another process's
		// tunnel token on a command line. The output is still redacted and
		// still audited at every rung.
		id:     "reads-machine-state",
		reason: "reports the state of this whole machine rather than of the workspace, and command lines routinely carry credentials",
		match: func(c cmdline) bool {
			switch c.prog {
			case "ps", "top", "htop", "pgrep", "pidof",
				"lsof", "netstat", "ss",
				"ioreg", "dmesg", "journalctl", "last":
				return true
			case "launchctl", "systemctl", "service", "sc":
				// The mutating forms belong to changes-the-machine; only the
				// reporting subcommands read state.
				return in(c.sub(), "list", "list-units", "list-unit-files",
					"status", "show", "print", "dumpstate", "query")
			}
			return false
		},
	},
	{
		id:     "recursive-delete",
		reason: "removes a directory and everything under it",
		match: func(c cmdline) bool {
			return in(c.prog, "rm", "rmdir") && (c.hasShortOpt('r') || c.hasShortOpt('R') || c.hasLongOpt("recursive"))
		},
	},
	{
		id:     "discards-uncommitted-work",
		reason: "throws away uncommitted changes, which no backup in this tool can restore",
		match: func(c cmdline) bool {
			if c.prog != "git" {
				return false
			}
			switch c.sub() {
			case "reset":
				return c.hasLongOpt("hard")
			case "clean":
				return c.hasShortOpt('f') || c.hasLongOpt("force")
			case "checkout", "switch", "restore":
				return c.hasShortOpt('f') || c.hasLongOpt("force")
			case "stash":
				return in(c.subArg(1), "drop", "clear")
			}
			return false
		},
	},
	{
		id:     "rewrites-published-history",
		reason: "force-pushes, which overwrites history other people may already have",
		match: func(c cmdline) bool {
			return c.prog == "git" && c.sub() == "push" &&
				(c.hasShortOpt('f') || c.hasLongOpt("force") || c.hasLongOpt("force-with-lease"))
		},
	},
	{
		id:     "leaves-this-machine",
		reason: "sends code or artifacts to a remote, which cannot be undone locally",
		match: func(c cmdline) bool {
			switch c.prog {
			case "curl", "wget", "http", "https", "xh":
				// The table used to hold publishing shapes only — git push,
				// npm publish, the infrastructure CLIs — so an HTTP client
				// with a file on its command line walked straight past it
				//. Fetching is still ordinary work and stays allowed;
				// what is gated is the form that carries a local file out.
				return uploadsAFile(c)
			case "nc", "ncat", "netcat", "socat", "telnet":
				// A raw pipe to another machine. There is no fetching form to
				// keep out of the way of: whatever these do, they do it to
				// somewhere else.
				return true
			case "git":
				return c.sub() == "push"
			case "npm", "pnpm", "yarn", "bun":
				return c.sub() == "publish"
			case "cargo", "poetry", "gem":
				return in(c.sub(), "publish", "push")
			case "twine":
				return c.sub() == "upload"
			case "docker", "podman":
				return c.sub() == "push"
			case "gh", "glab":
				return c.sub() == "release" && in(c.subArg(1), "create", "upload", "delete")
			case "ssh", "scp", "sftp", "rsync":
				// Reaches another machine, and in ssh's case runs a command
				// there that nothing on this side can bound.
				return true
			case "mvn":
				return containsArg(c.args, "deploy")
			case "dotnet":
				return c.sub() == "nuget" && c.subArg(1) == "push"
			case "aws", "gcloud", "az", "kubectl", "terraform", "flyctl", "vercel", "netlify", "wrangler":
				// Infrastructure CLIs act on live systems; none of them has a
				// read-only default worth trusting a rule table to detect.
				return true
			}
			return false
		},
	},
	{
		id:     "widens-permissions",
		reason: "changes file permissions or ownership",
		match: func(c cmdline) bool {
			return in(c.prog, "chmod", "chown", "chgrp", "setfacl", "icacls", "takeown")
		},
	},
	{
		id:     "changes-the-machine",
		reason: "installs software, or schedules work that outlives this session, outside this workspace",
		match: func(c cmdline) bool {
			switch c.prog {
			case "crontab", "at", "batch":
				// Scheduling is persistence by another name: whatever is
				// installed here keeps running after this command, this
				// workspace and this Companion. `launchctl load` was already
				// gated and these were not, which was an inconsistency rather
				// than a decision. The listing forms install nothing.
				return !c.hasShortOpt('l')
			case "schtasks":
				// The Windows counterpart. Its options are `/Create` shaped,
				// which this parser does not read, so the read-only `/Query`
				// is gated too — over-asking is the safe direction for a tier
				// that only asks.
				return true
			case "npm", "pnpm", "yarn", "bun":
				return in(c.sub(), "install", "add", "i", "uninstall", "remove") &&
					(c.hasShortOpt('g') || c.hasLongOpt("global") || c.hasLongOpt("location=global"))
			case "brew", "apt", "apt-get", "yum", "dnf", "pacman", "apk", "choco", "winget", "scoop", "port":
				return true
			case "pipx", "gem":
				return in(c.sub(), "install", "uninstall")
			case "cargo":
				return in(c.sub(), "install", "uninstall")
			case "go":
				return c.sub() == "install"
			case "rustup", "nvm", "asdf", "systemctl", "launchctl", "sc":
				return true
			}
			return false
		},
	},
	// There was a writes-outside-workspace rule here, matching mutators whose
	// path arguments left the workspace. touches-outside-workspace now matches
	// the same commands at the stricter tier and is evaluated first, so this
	// one could never be reached again. A rule that cannot fire is worse than
	// no rule: it reads like a second line of defence that is not there.
	{
		id:     "runs-a-nested-command",
		reason: "runs another command per match, which this table judges only as a whole",
		match: func(c cmdline) bool {
			if c.prog != "find" {
				return false
			}
			// find spells these with one dash, so hasLongOpt does not see them.
			return containsArg(c.args, "-exec", "-execdir", "-ok", "-okdir")
		},
	},
}

// mutators are the programs whose job is to change the filesystem. Path
// arguments to anything else are read targets, and a rule that treated them
// as writes would ask for approval to run `cat ../README.md`.
var mutators = []string{
	"rm", "rmdir", "unlink", "mv", "cp", "chmod", "chown", "chgrp",
	"ln", "tee", "install", "truncate", "touch", "mkdir", "dd", "rsync",
}

// cmdline is a parsed argv. Parsing is deliberately shallow: this table
// judges the shape of a command, and pretending to fully understand every
// CLI's grammar would give false confidence rather than more safety.
type cmdline struct {
	req  Request
	argv []string
	prog string
	args []string
}

func parse(req Request) cmdline {
	c := cmdline{req: req, argv: req.Argv}
	if len(req.Argv) == 0 {
		return c
	}
	c.prog = normalizeProg(req.Argv[0])
	c.args = req.Argv[1:]
	return c
}

func normalizeProg(name string) string {
	base := strings.ToLower(path.Base(slashed(strings.TrimSpace(name))))
	return strings.TrimSuffix(base, ".exe")
}

// slashed normalizes Windows separators unconditionally. The verdict must not
// depend on which OS is judging: the same argv can arrive from any client, and
// filepath.ToSlash is a no-op on Unix, so `C:\tools\rm.exe` would otherwise
// read as one long file name and match nothing.
func slashed(s string) string { return strings.ReplaceAll(s, `\`, "/") }

// sub returns the first non-flag argument — the subcommand for the many CLIs
// that have one.
func (c cmdline) sub() string { return c.subArg(0) }

// execsAProgram reports whether a non-flag argument names something to run
// rather than a KEY=VALUE assignment. It answers the one question `env` poses:
// `env` and `env FOO=1` print the environment, `env FOO=1 make` runs make.
func (c cmdline) execsAProgram() bool {
	for _, a := range c.args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		if !strings.Contains(a, "=") {
			return true
		}
	}
	return false
}

// subArg returns the nth non-flag argument, so `git stash drop` can be told
// from `git stash`.
func (c cmdline) subArg(n int) string {
	seen := 0
	for _, a := range c.args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		if seen == n {
			return strings.ToLower(a)
		}
		seen++
	}
	return ""
}

// hasShortOpt reports whether a single-dash flag carries the given letter,
// so `-rf` and `-r -f` are seen the same way.
func (c cmdline) hasShortOpt(letter rune) bool {
	for _, a := range c.args {
		if !strings.HasPrefix(a, "-") || strings.HasPrefix(a, "--") || a == "-" {
			continue
		}
		if strings.ContainsRune(a[1:], letter) {
			return true
		}
	}
	return false
}

// hasLongOpt matches --name and --name=value.
func (c cmdline) hasLongOpt(name string) bool {
	for _, a := range c.args {
		if a == "--"+name || strings.HasPrefix(a, "--"+name+"=") {
			return true
		}
	}
	return false
}

// pathArgs returns the arguments that look like filesystem targets: anything
// after the flags that is not itself a flag. For chmod and chown the first
// such argument is the mode or owner, not a path, so it is dropped.
func (c cmdline) pathArgs() []string {
	var out []string
	for _, a := range c.args {
		if c.windowsSwitch(a) {
			continue
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			// A path attached to its flag is still a path: --output=/etc/x
			// hides from a check that only reads bare arguments, and hiding
			// from it is not something the caller has to intend.
			if i := strings.IndexByte(a, '='); i >= 0 {
				out = append(out, a[i+1:])
			}
			continue
		}
		if strings.Contains(a, "=") && in(c.prog, "dd", "rsync") {
			// dd's of=/path and friends carry the path after the '='.
			if i := strings.IndexByte(a, '='); i >= 0 {
				out = append(out, a[i+1:])
			}
			continue
		}
		out = append(out, a)
	}
	if in(c.prog, "chmod", "chown", "chgrp") && len(out) > 0 {
		out = out[1:]
	}
	return out
}

// windowsSwitch reports whether arg is an option rather than a path. A few
// Windows-only tools spell their options /Grant and /Create, which on a Unix
// host reads as an absolute path and would put `icacls x /grant …` outside
// every workspace. Their real paths carry a drive letter, so for these
// programs a leading slash is never a path.
func (c cmdline) windowsSwitch(arg string) bool {
	if !strings.HasPrefix(arg, "/") {
		return false
	}
	return in(c.prog, "icacls", "cacls", "takeown", "attrib", "schtasks",
		"sc", "reg", "net", "robocopy", "xcopy")
}

// outsideWorkspace reports whether a path argument lands outside the
// workspace. The check is lexical: the target may not exist yet, and a policy
// layer that touched the filesystem would be a way to probe it.
//
// The residue is a symlink inside the workspace pointing out of it. This
// table cannot see it without resolving paths on disk, and the execution
// sandbox does not cover it either — the sandbox resolves the working
// directory and argv[0], never the arguments. Recorded rather than implied
// (audit finding).
func (c cmdline) outsideWorkspace(arg string) bool {
	abs, ok := c.abs(arg)
	if !ok {
		return false
	}
	return abs != c.req.Root && !strings.HasPrefix(abs, c.req.Root+string(filepath.Separator))
}

func (c cmdline) isWorkspaceRoot(arg string) bool {
	abs, ok := c.abs(arg)
	return ok && abs == c.req.Root
}

func (c cmdline) abs(arg string) (string, bool) {
	if arg == "" || c.req.Root == "" {
		return "", false
	}
	p := filepath.FromSlash(arg)
	if !filepath.IsAbs(p) {
		p = filepath.Join(c.req.Root, filepath.FromSlash(c.req.Dir), p)
	}
	return filepath.Clean(p), true
}

// uploadsAFile reports whether an HTTP client's arguments carry a local file
// outward. Two shapes cover the clients this table knows: an upload flag, and
// the `@file` convention every one of them uses to mean "the body is this
// file".
func uploadsAFile(c cmdline) bool {
	// Per program, because the same letter means different things: curl's -T
	// is an upload and wget's is a timeout.
	switch c.prog {
	case "curl":
		if c.hasShortOpt('T') || c.hasLongOpt("upload-file") {
			return true
		}
	case "wget":
		if c.hasLongOpt("post-file") || c.hasLongOpt("body-file") {
			return true
		}
	}
	for _, a := range c.args {
		// -d @body.json, -F field=@report.pdf, and the long forms of both.
		if strings.HasPrefix(a, "@") || strings.Contains(a, "=@") {
			return true
		}
	}
	return false
}

// containsArg reports whether any of want appears verbatim in args.
func containsArg(args []string, want ...string) bool {
	for _, a := range args {
		if in(a, want...) {
			return true
		}
	}
	return false
}

func in(v string, set ...string) bool {
	for _, s := range set {
		if v == s {
			return true
		}
	}
	return false
}
