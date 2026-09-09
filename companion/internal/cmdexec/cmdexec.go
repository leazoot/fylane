// Package cmdexec runs one command inside a workspace under fixed bounds.
//
// The bounds are the point: a working directory resolved through the path
// sandbox, an environment built from a whitelist instead of inherited, a
// timeout that kills the whole process tree, and a cap on how much output one
// run can return. Nothing here interprets a shell — the caller passes the
// program and its arguments as separate strings, and shell interpreters are
// refused outright, so there is no `sh -c` escape hatch.
//
// Deciding *which* commands are acceptable is not this package's job; that is
// the dangerous-command rule table. This package refuses only what it
// cannot run within its own bounds.
//
// What the shell refusal does and does not buy: it removes shell grammar from
// the tool call, so quoting, pipes, redirection, and command substitution
// cannot arrive as arguments. It does not make the workspace shell-free — a
// repository's own ./scripts/build.sh still runs, and must, which means a
// caller that can also write files can write a script and then run it. That
// path is deliberately left to the layers that see it: writes go through the
// change-set transaction, and the rule table plus the approval rung
// decide whether running the result needs a human.
package cmdexec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/leazoot/fylane/companion/internal/readbox"
	"github.com/leazoot/fylane/companion/internal/sandbox"
)

// Bounds applied when a Spec leaves them unset or asks for more than the
// engine is willing to give. DefaultTimeout matches the tightest platform
// wall-clock budget for a tool call (ChatGPT/Grok ~60s, safe rung ~50s), so a
// command that fits the default also fits a synchronous tool response;
// anything longer belongs to the async task table.
const (
	DefaultTimeout        = 50 * time.Second
	MaxTimeout            = 30 * time.Minute
	DefaultMaxOutputBytes = 256 << 10
	MaxOutputBytes        = 4 << 20
)

// killGrace is how long a timed-out process tree gets between SIGTERM and
// SIGKILL. A build killed mid-write leaves a corrupt artifact behind, so the
// tree is asked to leave before it is made to.
const killGrace = 2 * time.Second

// Setup failures, exported so callers map them to stable tool-facing messages
// instead of matching on strings.
var (
	ErrNoCommand        = errors.New("no command given")
	ErrShellInterpreter = errors.New("running a shell interpreter is not allowed; pass the program and its arguments separately")
	ErrNotDirectory     = errors.New("working directory is not a directory")
	ErrProgramNotFound  = errors.New("program not found")
	ErrProgramNotFile   = errors.New("program is not a regular executable file")
)

// Spec is one command to run.
type Spec struct {
	// WorkspaceID is the opaque id this run belongs to. Carried through to
	// the audit record only; nothing here interprets it.
	WorkspaceID string
	// Root is the absolute, symlink-resolved workspace root, as produced by
	// workspace.New. It never leaves this machine.
	Root string
	// Dir is a workspace-relative subdirectory to run in; empty means the
	// workspace root. Subdirectories are allowed so a monorepo can run one
	// package's tests.
	Dir string
	// Argv is the program followed by its arguments. Never joined into a
	// string, never handed to a shell.
	Argv []string
	// Provider is the platform that asked for this run. The engine only
	// carries it to the audit record; nothing here interprets it.
	Provider string
	// Network is whether this run may reach the network, which is the
	// workspace's answer and not this package's. It is spelled positively
	// and set by the caller from the workspace record, so a Spec built
	// without it denies rather than quietly allows — the direction a
	// boundary should fail in.
	Network bool
	// RunID identifies this execution in the journal. When set, the engine
	// writes a 'started' row under it before the process runs and repeats it
	// on the row that reports the outcome, so a run the Core never saw end
	// can be found afterwards. Empty disables journalling.
	RunID string
	// Timeout defaults to DefaultTimeout and is clamped to MaxTimeout.
	Timeout time.Duration
	// MaxOutputBytes caps each stream independently; defaults to
	// DefaultMaxOutputBytes and is clamped to MaxOutputBytes.
	MaxOutputBytes int
	// Stdout and Stderr, when set, receive output as it is produced, in
	// addition to the capped copy in Result. The async task table
	// uses them to show progress for a run that outlived its synchronous
	// budget. os/exec writes from its own goroutines, so an implementation
	// must be safe for concurrent use; a write error from one of these is
	// ignored, because a broken observer must not kill the build.
	Stdout io.Writer
	Stderr io.Writer
}

// Result is what a run produced. A non-zero exit is a Result, not an error:
// a failing test suite is the normal case this engine exists to report.
type Result struct {
	ExitCode        int           `json:"exit_code"`
	Stdout          string        `json:"stdout"`
	Stderr          string        `json:"stderr"`
	StdoutTruncated bool          `json:"stdout_truncated"`
	StderrTruncated bool          `json:"stderr_truncated"`
	Duration        time.Duration `json:"duration"`
	// TimedOut reports that the tree was killed because Timeout elapsed, as
	// opposed to the parent context being cancelled.
	TimedOut bool `json:"timed_out"`
}

// Outcome values recorded for every attempt.
const (
	OutcomeOK       = "ok"
	OutcomeFailed   = "failed"
	OutcomeTimeout  = "timeout"
	OutcomeCanceled = "canceled"
	OutcomeRefused  = "refused"
	// OutcomeStarted is written when the process is up and says nothing about
	// how it ends. It is the row that makes an interrupted run visible: a
	// Core that dies mid-command writes no other.
	OutcomeStarted = "started"
)

// Record is one audit entry. It carries only workspace-relative paths and the
// argv as the caller wrote it — never the resolved absolute program path,
// which would leak the machine's layout into anything that persists this.
type Record struct {
	WorkspaceID string
	Dir         string
	Argv        []string
	Outcome     string
	ExitCode    int
	// Reason is the refusal or failure cause; empty when the command ran.
	Reason string
	// Rule is the cmdrule identifier when a policy layer refused this
	// attempt. The engine never sets it; callers that refuse before
	// reaching the engine do.
	Rule string
	// Provider is the platform that asked for this command, when the caller
	// could name one.
	Provider string
	// RunID ties this record to the other row of the same execution. An
	// attempt refused before the process started has no pair and leaves it
	// empty.
	RunID    string
	Duration time.Duration
}

// Auditor receives every attempt, including refusals. A blocked command is
// the more interesting security signal, not the less, so refusals are
// recorded on the same path as successes rather than dropped.
type Auditor interface {
	ExecAttempt(ctx context.Context, rec Record)
}

// Runner executes commands. It holds no per-run state and is safe for
// concurrent use.
type Runner struct {
	audit Auditor
	// box bounds what a command may read. Nil is a Companion with no
	// boundary, which is the answer on a platform that offers none — every
	// other check is unchanged either way.
	box *readbox.Box
}

// New builds a Runner. The auditor is required: an execution engine that can
// be constructed without one invites a caller that silently forgets it.
func New(a Auditor, box *readbox.Box) *Runner {
	if a == nil {
		panic("cmdexec: nil auditor")
	}
	return &Runner{audit: a, box: box}
}

// Run executes spec and returns what it produced. The returned error covers
// setup failures only — refusals, a missing program, an unusable directory.
// Once the process starts, everything is reported through Result.
func (r *Runner) Run(ctx context.Context, spec Spec) (*Result, error) {
	dirRel, dirAbs, prog, err := r.prepare(spec)
	if err != nil {
		r.audit.ExecAttempt(ctx, Record{
			WorkspaceID: spec.WorkspaceID,
			Dir:         dirRel,
			Argv:        spec.Argv,
			Provider:    spec.Provider,
			Outcome:     OutcomeRefused,
			Reason:      err.Error(),
		})
		return nil, err
	}

	// The process is about to exist, so the journal has to say so before it
	// does. Written after prepare and before run: a command refused for an
	// unusable directory never ran, and a 'started' row for it would make
	// startup invent an interrupted command that never existed.
	if spec.RunID != "" {
		r.audit.ExecAttempt(ctx, Record{
			WorkspaceID: spec.WorkspaceID,
			Dir:         dirRel,
			Argv:        spec.Argv,
			Provider:    spec.Provider,
			RunID:       spec.RunID,
			Outcome:     OutcomeStarted,
		})
	}

	res, runErr := run(ctx, spec, dirAbs, prog, r.box)
	if runErr != nil {
		r.audit.ExecAttempt(ctx, Record{
			WorkspaceID: spec.WorkspaceID,
			Dir:         dirRel,
			Argv:        spec.Argv,
			Provider:    spec.Provider,
			RunID:       spec.RunID,
			Outcome:     OutcomeRefused,
			Reason:      runErr.Error(),
		})
		return nil, runErr
	}

	r.audit.ExecAttempt(ctx, Record{
		WorkspaceID: spec.WorkspaceID,
		Dir:         dirRel,
		Argv:        spec.Argv,
		Provider:    spec.Provider,
		RunID:       spec.RunID,
		Outcome:     outcomeOf(ctx, res),
		ExitCode:    res.ExitCode,
		Duration:    res.Duration,
	})
	return res, nil
}

func outcomeOf(ctx context.Context, res *Result) string {
	switch {
	case res.TimedOut:
		return OutcomeTimeout
	case ctx.Err() != nil:
		return OutcomeCanceled
	case res.ExitCode == 0:
		return OutcomeOK
	default:
		return OutcomeFailed
	}
}

// prepare validates the spec and resolves the directory and program. It
// returns the canonical relative directory (for audit) alongside the absolute
// one (for the child).
func (r *Runner) prepare(spec Spec) (dirRel, dirAbs, prog string, err error) {
	if len(spec.Argv) == 0 || strings.TrimSpace(spec.Argv[0]) == "" {
		return "", "", "", ErrNoCommand
	}
	if !filepath.IsAbs(spec.Root) {
		return "", "", "", fmt.Errorf("workspace root must be absolute")
	}
	if IsShellInterpreter(spec.Argv[0]) {
		return "", "", "", fmt.Errorf("%w: %s", ErrShellInterpreter, path.Base(filepath.ToSlash(spec.Argv[0])))
	}

	dirRel, dirAbs, err = resolveDir(spec.Root, spec.Dir)
	if err != nil {
		return "", "", "", err
	}
	prog, err = resolveProgram(spec.Root, dirRel, spec.Argv[0])
	if err != nil {
		return dirRel, "", "", err
	}
	return dirRel, dirAbs, prog, nil
}

// resolveDir maps a workspace-relative directory to an absolute one. The
// workspace root itself is a legitimate working directory — running the test
// suite at the repo root is the common case — but sandbox.CleanPath rejects
// "" and "." because for a *file* operation the root is never a valid target.
// So the root is handled here and only real subpaths reach the sandbox.
func resolveDir(root, dir string) (rel, abs string, err error) {
	switch strings.Trim(strings.ReplaceAll(dir, `\`, "/"), "/") {
	case "", ".":
		return "", root, nil
	}
	abs, rel, err = sandbox.Resolve(root, dir, sandbox.OpRead)
	if err != nil {
		return "", "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", "", fmt.Errorf("working directory %s: %w", rel, err)
	}
	if !info.IsDir() {
		return "", "", fmt.Errorf("%w: %s", ErrNotDirectory, rel)
	}
	return rel, abs, nil
}

// resolveProgram turns argv[0] into an absolute program path.
//
// A name carrying a separator ("./scripts/build.sh") is a path the user means
// relative to the working directory, so it is joined with dirRel and pushed
// through the sandbox — which is what stops "../../../bin/whatever". A bare
// name ("go", "npm") is looked up on PATH, the same PATH the child inherits.
func resolveProgram(root, dirRel, name string) (string, error) {
	slashed := filepath.ToSlash(name)
	if !strings.ContainsRune(slashed, '/') {
		found, err := osexec.LookPath(name)
		if err != nil {
			return "", fmt.Errorf("%w: %s", ErrProgramNotFound, name)
		}
		return found, nil
	}

	abs, rel, err := sandbox.Resolve(root, path.Join(dirRel, slashed), sandbox.OpRead)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrProgramNotFound, rel)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: %s", ErrProgramNotFile, rel)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("%w: %s", ErrProgramNotFile, rel)
	}
	return abs, nil
}

// shellInterpreters is the set refused by ErrShellInterpreter. Blocking these
// is what makes "no shell" structural rather than advisory: without it,
// argv{"sh", "-c", anything} reintroduces the whole shell grammar — quoting,
// pipes, redirection, command substitution — and every rule in the table becomes
// trivially bypassable.
var shellInterpreters = map[string]bool{
	"ash": true, "bash": true, "busybox": true, "cmd": true, "command": true,
	"csh": true, "dash": true, "fish": true, "ksh": true, "powershell": true,
	"pwsh": true, "sh": true, "tcsh": true, "zsh": true,
}

// IsShellInterpreter reports whether name refers to a shell. Exported so the
// rule table (internal/cmdrule) can reuse the one list instead of keeping a
// second copy that drifts.
func IsShellInterpreter(name string) bool {
	return shellInterpreters[programBase(name)]
}

// programBase is the comparable name of an argument: no directory, no .exe,
// no surrounding whitespace, lower case. Windows separators are normalized
// unconditionally, because the same argv can arrive from any client and the
// answer must not depend on which OS is judging.
func programBase(name string) string { return ProgramName(name) }

// execWrappers run whatever they are handed, so a shell can hide in their
// arguments rather than at argv[0]. The list names only programs whose whole
// purpose is to launch another one.
var execWrappers = map[string]bool{
	"env": true, "timeout": true, "nohup": true, "nice": true, "ionice": true,
	"setsid": true, "stdbuf": true, "script": true, "watch": true, "time": true,
	"chroot": true, "unshare": true, "flock": true, "ssh": true,
	"docker": true, "podman": true, "kubectl": true, "find": true,
}

// SpellsAShell reports whether argv reaches a shell, either as the program
// itself or as an argument to a wrapper that runs what it is handed.
//
// Only wrappers are scanned. An earlier version of the rule table scanned
// every argument of every command and refused `grep -r bash docs/` — a daily
// action, blocked for containing a word. False positives cost more safety
// than they buy here, because the rung a nagged user reaches for is the open
// one.
//
// It lives beside IsShellInterpreter because both the rule table and the MCP
// gateway ask this same question, and they answered it with different
// strength for as long as each had its own version.
func SpellsAShell(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	if IsShellInterpreter(argv[0]) {
		return true
	}
	if !execWrappers[programBase(argv[0])] {
		return false
	}
	for _, arg := range argv[1:] {
		if IsShellInterpreter(arg) {
			return true
		}
	}
	return false
}

// tee sends output to the capped buffer and, when the caller asked for one,
// to a live observer. It is not io.MultiWriter: that propagates the observer's
// error, which would stop os/exec from draining the pipe and hang the child on
// a problem that is none of the child's business.
func tee(primary *capWriter, observer io.Writer) io.Writer {
	if observer == nil {
		return primary
	}
	return teeWriter{primary: primary, observer: observer}
}

type teeWriter struct {
	primary  *capWriter
	observer io.Writer
}

func (t teeWriter) Write(p []byte) (int, error) {
	t.observer.Write(p)
	return t.primary.Write(p)
}

// run starts the process and collects its output. Everything that can be
// refused has been refused by now.
func run(ctx context.Context, spec Spec, dirAbs, prog string, box *readbox.Box) (*Result, error) {
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if timeout > MaxTimeout {
		timeout = MaxTimeout
	}
	outCap := spec.MaxOutputBytes
	if outCap <= 0 {
		outCap = DefaultMaxOutputBytes
	}
	if outCap > MaxOutputBytes {
		outCap = MaxOutputBytes
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	stdout := newCapWriter(outCap)
	stderr := newCapWriter(outCap)

	cmd := osexec.CommandContext(runCtx, prog, spec.Argv[1:]...)
	cmd.Dir = dirAbs
	cmd.Env = buildEnv(os.LookupEnv)
	cmd.Stdout = tee(stdout, spec.Stdout)
	cmd.Stderr = tee(stderr, spec.Stderr)
	// Nil Stdin gives the child the null device, so a tool that decides to
	// prompt reads EOF and gives up instead of hanging until the timeout.
	cmd.Stdin = nil

	// The read boundary goes on before the process group, because it rewrites
	// what is being started. A boundary that cannot be built refuses the run:
	// the platform said it could do this, so running anyway would be the
	// silent downgrade readbox exists to prevent.
	if err := box.Wrap(cmd, readbox.WorkspacePolicy(spec.Root, spec.Network)); err != nil {
		return nil, err
	}

	group := newProcGroup()
	defer group.close()
	group.prepare(cmd)

	cmd.Cancel = func() error { return group.kill(cmd) }
	// A grandchild that survives its parent keeps the output pipe open, and
	// Wait blocks on the pipe, not on the process. WaitDelay bounds that;
	// group.kill is what should normally have prevented it.
	cmd.WaitDelay = killGrace + 5*time.Second

	started := time.Now()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting %s: %w", spec.Argv[0], err)
	}
	if err := group.adopt(cmd); err != nil {
		group.kill(cmd)
		cmd.Wait()
		return nil, fmt.Errorf("isolating %s: %w", spec.Argv[0], err)
	}
	waitErr := cmd.Wait()
	elapsed := time.Since(started)

	res := &Result{
		Stdout:          stdout.String(),
		Stderr:          stderr.String(),
		StdoutTruncated: stdout.truncated(),
		StderrTruncated: stderr.truncated(),
		Duration:        elapsed,
		TimedOut:        errors.Is(runCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil,
	}

	var exitErr *osexec.ExitError
	switch {
	case waitErr == nil:
		res.ExitCode = 0
	case errors.As(waitErr, &exitErr):
		res.ExitCode = exitErr.ExitCode()
	default:
		// Wait failed for a reason that is not the process's exit status
		// (a broken pipe, WaitDelay expiring). The process is gone either
		// way; report it as a failure rather than inventing an exit code.
		res.ExitCode = -1
	}
	return res, nil
}
