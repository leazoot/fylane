package codeagent

import (
	"context"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"

	"github.com/leazoot/fylane/companion/internal/cmdexec"
	"github.com/leazoot/fylane/companion/internal/readbox"
)

// Codex delegates to the OpenAI Codex CLI through `codex exec`.
//
// Of the adapters here this is the one that streams for free: `--json` puts a
// JSONL event per step on stdout, so piping it at the task's output writer
// gives the desktop live progress without this package having to understand
// the events.
type Codex struct {
	// Binary overrides the program name; empty resolves "codex" on PATH.
	Binary string
	// Sandbox is the sandbox policy passed to codex. The default keeps the
	// agent's writes inside the workspace it was pointed at — Fylane cannot
	// enforce that itself once it delegates, so it asks for it where
	// the agent offers it.
	Sandbox string
	// EnvPassthrough names the environment variables this agent may inherit,
	// on top of the base set every child gets. Names only: the value stays in
	// the environment and is never written into a settings file. Configured
	// locally, the same way a gateway provider's passthrough is.
	EnvPassthrough []string
	// Box is the kernel read boundary. This is the point named as
	// the known cost of delegation: once the agent starts, Fylane reviews
	// none of its steps, so a boundary the agent cannot talk its way past is
	// worth more here than anywhere else.
	Box *readbox.Box
}

// DefaultCodexSandbox is what a delegated task runs under unless configured
// otherwise. Not "danger-full-access": delegation means Fylane stops watching,
// not that the boundaries stop existing.
const DefaultCodexSandbox = "workspace-write"

func (c *Codex) Name() string { return "codex" }

func (c *Codex) binary() string {
	if c.Binary != "" {
		return c.Binary
	}
	return "codex"
}

func (c *Codex) Available() error {
	path := c.binary()
	if strings.ContainsRune(filepath.ToSlash(path), '/') {
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("codex is not installed at %s", path)
		}
		return nil
	}
	if _, err := osexec.LookPath(path); err != nil {
		return fmt.Errorf("codex is not installed: install the Codex CLI and make sure `codex` is on PATH")
	}
	return nil
}

func (c *Codex) Run(ctx context.Context, task Task, out io.Writer) (Result, error) {
	if err := c.Available(); err != nil {
		return Result{}, err
	}
	dir, err := workDir(task)
	if err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(task.Prompt) == "" {
		return Result{}, fmt.Errorf("a delegated task needs a prompt")
	}

	// The agent's closing message goes to a file rather than being dug out
	// of the event stream: it is the one piece this package has to read, and
	// asking for it directly is less to get wrong than parsing JSONL.
	last, err := os.CreateTemp("", "fylane-codex-*.txt")
	if err != nil {
		return Result{}, fmt.Errorf("preparing the agent result file: %w", err)
	}
	last.Close()
	defer os.Remove(last.Name())

	sandbox := c.Sandbox
	if sandbox == "" {
		sandbox = DefaultCodexSandbox
	}
	args := []string{
		"exec", "--json",
		"--skip-git-repo-check", // a workspace need not be a git repository
		"-C", dir,
		"-s", sandbox,
		"-o", last.Name(),
	}
	if task.Model != "" {
		args = append(args, "-m", task.Model)
	}
	args = append(args, task.Prompt)

	scrub := newScrubber(task.Root)
	sink := scrubbingWriter{scrub: scrub, to: out}

	cmd := osexec.CommandContext(ctx, c.binary(), args...)
	cmd.Dir = dir
	cmd.Stdout = sink
	cmd.Stderr = sink
	// This used to be os.Environ(), on the reasoning that the agent
	// authenticates as the user and this package cannot know which variables
	// it needs. The second half was true and the conclusion did not follow:
	// handing over every variable includes Fylane's own relay credentials, to
	// a process Fylane cannot watch. The gateway had already answered the
	// same question with a named list, and one repository should not hold two
	// opposite answers (audit finding F23). HOME is in the base set, so an
	// agent that keeps its credentials in a config file needs nothing here.
	cmd.Env = cmdexec.ChildEnv(c.EnvPassthrough...)
	if err := c.Box.Wrap(cmd, readbox.WorkspacePolicy(task.Root, task.Network)); err != nil {
		return Result{}, err
	}

	runErr := cmd.Run()
	summary, readErr := os.ReadFile(last.Name())
	if readErr == nil && len(summary) > 0 {
		return Result{Summary: scrub.clean(string(summary))}, runErr
	}
	if runErr != nil {
		if len(c.EnvPassthrough) == 0 {
			// The one failure this change can cause, named where it happens
			// rather than left to look like the agent breaking.
			return Result{}, fmt.Errorf("codex exec: %w (if codex authenticates through an environment variable, list its name under agent_env_passthrough in the Companion's settings — the child no longer inherits the whole environment)", runErr)
		}
		return Result{}, fmt.Errorf("codex exec: %w", runErr)
	}
	return Result{}, nil
}
