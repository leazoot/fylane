// Package codeagent delegates a whole coding task to an agent already
// installed on this machine.
//
// This is the escape hatch from the 60-second wall: the web
// conversation cannot itself drive a twenty-minute refactor through a tool
// surface that dies after a minute, so it hands the task over and polls.
//
// What Fylane gives up by delegating is stated plainly because it is the
// whole trade: the agent runs its own loop with its own model and its
// own permissions. The files it edits and the commands it runs are invisible
// to Fylane's sandbox, rule table, and audit — only the task's start is
// approved. Adapters here narrow that where the agent lets them (Codex is
// told to keep its writes inside the workspace), but nothing here can enforce
// it, and pretending otherwise would be worse than saying so.
package codeagent

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Task is one delegated piece of work.
type Task struct {
	// Prompt is what the user (through the model) asked for.
	Prompt string
	// Root is the absolute workspace root and Dir the workspace-relative
	// subdirectory to work in. Root never leaves this machine.
	Root string
	Dir  string
	// Model optionally overrides the agent's default.
	Model string
	// Network is the workspace's answer about outbound traffic. A delegated
	// agent is the least inspectable surface this product has, so
	// the workspace's answer reaches it exactly as it reaches a command.
	Network bool
}

// Result is what the agent reported when it finished.
type Result struct {
	// Summary is the agent's closing message, already scrubbed of absolute
	// paths.
	Summary string
}

// Agent is one delegate. Implementations live next to this file.
type Agent interface {
	// Name is the stable identifier used in configuration and tool input.
	Name() string
	// Available reports why this agent cannot be used here, or nil.
	Available() error
	// Run performs the task, writing progress to out as it goes so a poller
	// sees something before the end, and returns when the agent is done.
	Run(ctx context.Context, task Task, out io.Writer) (Result, error)
}

// Registry holds the agents this Companion can delegate to.
type Registry struct {
	agents map[string]Agent
}

// NewRegistry builds a registry from the given agents, later entries winning
// on a name collision.
func NewRegistry(agents ...Agent) *Registry {
	r := &Registry{agents: map[string]Agent{}}
	for _, a := range agents {
		if a != nil {
			r.agents[a.Name()] = a
		}
	}
	return r
}

// Lookup returns the named agent. An empty name picks the first available
// one in stable order, so a caller that does not care still gets a working
// delegate rather than an error.
func (r *Registry) Lookup(name string) (Agent, error) {
	if name != "" {
		a, ok := r.agents[name]
		if !ok {
			return nil, fmt.Errorf("unknown agent %q; available: %s", name, strings.Join(r.Names(), ", "))
		}
		if err := a.Available(); err != nil {
			return nil, err
		}
		return a, nil
	}
	for _, n := range r.Names() {
		a := r.agents[n]
		if a.Available() == nil {
			return a, nil
		}
	}
	return nil, fmt.Errorf("no coding agent is installed; install one of: %s", strings.Join(r.Names(), ", "))
}

// Names lists every registered agent in stable order.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.agents))
	for n := range r.agents {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Installed lists the agents usable on this machine right now.
func (r *Registry) Installed() []string {
	var out []string
	for _, n := range r.Names() {
		if r.agents[n].Available() == nil {
			out = append(out, n)
		}
	}
	return out
}

// scrubber rewrites absolute paths out of an agent's output.
//
// Agents print absolute paths constantly, and that output goes back through
// MCP to a platform — which is exactly what "absolute paths never leave the
// machine" forbids. Replacing the workspace root with a relative form and the
// home directory with ~ removes the two that appear in almost every line.
//
// It is best-effort and says so: an agent that prints a path somewhere else
// entirely will get through. The alternative — returning nothing — would make
// delegation useless, so the honest position is to scrub what can be scrubbed
// and not to claim the output is sanitized.
type scrubber struct {
	replacements []string
}

func newScrubber(root string) *scrubber {
	var pairs []string
	if root != "" {
		pairs = append(pairs, root+string(filepath.Separator), "", root, ".")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" && home != root {
		pairs = append(pairs, home, "~")
	}
	return &scrubber{replacements: pairs}
}

func (s *scrubber) clean(text string) string {
	if len(s.replacements) == 0 {
		return text
	}
	return strings.NewReplacer(s.replacements...).Replace(text)
}

// Write implements io.Writer so streamed output is scrubbed on its way past.
type scrubbingWriter struct {
	scrub *scrubber
	to    io.Writer
}

func (w scrubbingWriter) Write(p []byte) (int, error) {
	if _, err := io.WriteString(w.to, w.scrub.clean(string(p))); err != nil {
		return 0, err
	}
	// Report the caller's length: the scrubbed form is shorter, and a short
	// write would look like a failure to whoever is copying into us.
	return len(p), nil
}

// workDir resolves the absolute directory an agent should run in.
func workDir(task Task) (string, error) {
	if !filepath.IsAbs(task.Root) {
		return "", fmt.Errorf("workspace root must be absolute")
	}
	if task.Dir == "" {
		return task.Root, nil
	}
	dir := filepath.Join(task.Root, filepath.FromSlash(task.Dir))
	// The sandbox has already vetted Dir upstream; this is the second check
	// that stops a caller wiring this package up without one.
	rel, err := filepath.Rel(task.Root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("working directory escapes the workspace")
	}
	return dir, nil
}
