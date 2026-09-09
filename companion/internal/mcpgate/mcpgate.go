// Package mcpgate proxies MCP servers already installed on this machine.
//
// The reach it buys is real: a user's local toolchain — a database client, a
// design tool, an issue tracker — often already speaks MCP, and without this
// the only way to use one from a web conversation is to leave Fylane.
//
// The way it is built follows from one fact: a proxied tool is *opaque*.
// Fylane sees its name and its schema and nothing else. The rule table cannot
// tell whether `query` reads a table or drops it, the path sandbox never sees
// the paths it touches, and nothing here can bound what it reaches. Every
// other remote-facing surface in this product is safe because Fylane inspected
// the operation; this one is not, and the design says so instead of working
// around it:
//
//   - Providers are configured locally, in the Companion's own settings file.
//     No remote caller can name a program to run. If it could, this package
//     would be remote code execution with extra steps.
//   - The default trust is Ask: every call stops for the local user at every
//     approval rung, including the open one, and no workspace grant covers it
//     (that tier). An operation nobody can classify cannot honestly be
//     covered by a grant the user gave for "ordinary work in this folder".
//   - A provider is a program already on this machine. Launchers whose whole
//     job is to fetch a program and run it — npx, uvx, pipx run, go run,
//     docker run — are refused by name: the Core never downloads an
//     executable at runtime, and `npx some-server` is exactly that with a step
//     in between.
//   - A server is started for one exchange and killed after it. A long-lived
//     foreign process would need an owner, a shutdown path, and an answer for
//     what one caller's session leaks into the next one's. Per-call costs a
//     handshake and needs none of those.
package mcpgate

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/leazoot/fylane/companion/internal/cmdexec"
	"github.com/leazoot/fylane/companion/internal/readbox"
	"github.com/leazoot/fylane/shared/buildinfo"
)

// Trust is how much authority one provider's tools carry.
type Trust string

const (
	// Ask stops on every call, at every rung. It is the default and the only
	// honest answer for a server Fylane cannot inspect.
	Ask Trust = "ask"
	// Workspace treats calls as ordinary work in the current workspace: the
	// rung applies and a workspace grant covers them. The user has to write it
	// down for a named provider; there is no way to reach it by accident.
	Workspace Trust = "workspace"
)

// DefaultTimeout bounds one exchange with a provider — the handshake and the
// call together. It matches the command engine's default for the same reason:
// the platform's own wall clock is about a minute.
const DefaultTimeout = cmdexec.DefaultTimeout

// MaxTextBytes caps what one call hands back, before redaction.
const MaxTextBytes = cmdexec.DefaultMaxOutputBytes

// Provider is one stdio MCP server this machine can reach.
type Provider struct {
	// Name identifies the provider in tool input and in the audit log.
	Name string `json:"name"`
	// Command is the program and its arguments, already installed here.
	Command []string `json:"command"`
	// EnvPassthrough names environment variables to hand the server from
	// Fylane's own environment. Names only, never values: a key written into a
	// settings file is a key in every backup of that file.
	EnvPassthrough []string `json:"env_passthrough,omitempty"`
	// Trust is Ask when empty.
	Trust Trust `json:"trust,omitempty"`
}

// Tool is one tool a provider offers.
type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Schema is the tool's own input schema, passed through untouched so the
	// caller can build valid arguments without a second round trip.
	Schema any `json:"input_schema,omitempty"`
}

// Result is one proxied call's answer.
type Result struct {
	// Text is the server's textual content, joined and capped.
	Text string
	// IsError is the server's own error flag. It is not a transport failure:
	// the call arrived and the server said no.
	IsError bool
	// Truncated reports that Text was cut at the cap.
	Truncated bool
}

// Trusted reports the effective trust, resolving the empty default.
func (p Provider) Trusted() Trust {
	if p.Trust == Workspace {
		return Workspace
	}
	return Ask
}

var nameShape = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// Validate checks what can be checked without touching the disk. It is
// deliberately separate from Available: a provider whose program is missing
// today is a configuration that still means something, while one spelled
// `npx …` is a configuration that never will.
func (p Provider) Validate() error {
	if !nameShape.MatchString(p.Name) {
		return fmt.Errorf("provider name %q must be 1-32 characters of a-z, 0-9, - or _", p.Name)
	}
	if len(p.Command) == 0 {
		return fmt.Errorf("provider %q has no command", p.Name)
	}
	switch p.Trust {
	case "", Ask, Workspace:
	default:
		return fmt.Errorf("provider %q has unknown trust %q; use %q or %q", p.Name, p.Trust, Ask, Workspace)
	}
	// The whole command line, not just argv[0]: `env sh -c …` names a shell
	// too, and the rule table has always read it that way.
	if cmdexec.SpellsAShell(p.Command) {
		return fmt.Errorf("provider %q runs a shell; name the server's own program instead", p.Name)
	}
	if reason := cmdexec.FetchesAndRuns(p.Command); reason != "" {
		return fmt.Errorf("provider %q %s; Fylane never downloads an executable at runtime, so install the server and name its program", p.Name, reason)
	}
	for _, name := range p.EnvPassthrough {
		if name == "" || strings.ContainsAny(name, "= ") {
			return fmt.Errorf("provider %q passes through %q, which is not a variable name", p.Name, name)
		}
	}
	return nil
}

// Available reports why this provider cannot be used here, or nil. Probed at
// call time rather than at startup so a Companion started before the user
// installed a server still finds it later.
func (p Provider) Available() error {
	_, err := p.resolve()
	return err
}

func (p Provider) resolve() (string, error) {
	path, err := exec.LookPath(p.Command[0])
	if err != nil {
		// Only the base name: an absolute path is exactly what must not reach
		// a remote caller, and this error is written to answer one.
		return "", fmt.Errorf("%q is not installed on this machine", cmdexec.ProgramName(p.Command[0]))
	}
	return path, nil
}

// Registry holds the providers this Companion may proxy to.
type Registry struct {
	byName map[string]Provider
	names  []string
}

// NewRegistry keeps every valid provider and returns one error per rejected
// entry. A bad line in a settings file must not stop the Companion from
// starting — but it must not be swallowed either, so the caller logs what was
// dropped and why.
func NewRegistry(list []Provider) (*Registry, []error) {
	r := &Registry{byName: map[string]Provider{}}
	var errs []error
	for _, p := range list {
		if err := p.Validate(); err != nil {
			errs = append(errs, err)
			continue
		}
		if _, dup := r.byName[p.Name]; dup {
			errs = append(errs, fmt.Errorf("provider %q is configured more than once; the later one is ignored", p.Name))
			continue
		}
		r.byName[p.Name] = p
		r.names = append(r.names, p.Name)
	}
	sort.Strings(r.names)
	return r, errs
}

// Names lists the configured providers in stable order.
func (r *Registry) Names() []string {
	out := make([]string, len(r.names))
	copy(out, r.names)
	return out
}

// List returns every configured provider in stable order.
func (r *Registry) List() []Provider {
	out := make([]Provider, 0, len(r.names))
	for _, n := range r.names {
		out = append(out, r.byName[n])
	}
	return out
}

// Lookup returns the named provider. An empty or unknown name is an error
// rather than a default: picking a provider for the caller would mean guessing
// which foreign program to run.
func (r *Registry) Lookup(name string) (Provider, error) {
	if name == "" {
		return Provider{}, errors.New("provider is required; call action=list_providers to see the configured ones")
	}
	p, ok := r.byName[name]
	if !ok {
		return Provider{}, fmt.Errorf("unknown provider %q; configured: %s", name, strings.Join(r.Names(), ", "))
	}
	return p, nil
}

// Session is one live exchange with one provider.
type Session struct {
	sess   *mcp.ClientSession
	stderr *headBuffer
}

// Open starts the provider in dir and completes the MCP handshake. dir is an
// absolute workspace path: it is the strongest containment available here,
// since most servers resolve relative paths against their working directory,
// and it is not a sandbox — nothing in this package can stop a foreign process
// from reaching elsewhere.
func Open(ctx context.Context, p Provider, dir string, box *readbox.Box) (*Session, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	prog, err := p.resolve()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, prog, p.Command[1:]...)
	cmd.Dir = dir
	// The same whitelist a command gets, plus whatever this provider was
	// explicitly told to receive. Handing over the Companion's whole
	// environment would hand over the relay credential with it.
	cmd.Env = cmdexec.ChildEnv(p.EnvPassthrough...)
	// A proxied server is the surface Fylane can inspect least, so it is the
	// one that most wants a boundary it cannot argue with.
	// Network allowed, for the reason given in lsp/client.go: a provider is a
	// program the local user configured, not work a caller set in motion, and
	// several of them exist only to reach something over the network.
	if err := box.Wrap(cmd, readbox.WorkspacePolicy(dir, true)); err != nil {
		return nil, err
	}
	// A server's stderr is its own diagnostics and may hold anything. It is
	// kept, bounded, only to explain a failure to start — never streamed
	// anywhere and never returned on success.
	tail := &headBuffer{limit: 4 << 10}
	cmd.Stderr = tail

	client := mcp.NewClient(&mcp.Implementation{
		Name: "fylane-companion", Version: buildinfo.Version,
	}, nil)
	sess, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		if detail := strings.TrimSpace(tail.String()); detail != "" {
			return nil, fmt.Errorf("provider %q did not start: %w: %s", p.Name, err, detail)
		}
		return nil, fmt.Errorf("provider %q did not start: %w", p.Name, err)
	}
	return &Session{sess: sess, stderr: tail}, nil
}

// Close ends the session and the process behind it.
func (s *Session) Close() error { return s.sess.Close() }

// Tools lists what the provider offers.
func (s *Session) Tools(ctx context.Context) ([]Tool, error) {
	res, err := s.sess.ListTools(ctx, nil)
	if err != nil {
		return nil, err
	}
	out := make([]Tool, 0, len(res.Tools))
	for _, t := range res.Tools {
		if t == nil {
			continue
		}
		out = append(out, Tool{Name: t.Name, Description: t.Description, Schema: t.InputSchema})
	}
	return out, nil
}

// Call runs one tool and returns its textual answer.
func (s *Session) Call(ctx context.Context, tool string, args map[string]any) (Result, error) {
	if strings.TrimSpace(tool) == "" {
		return Result{}, errors.New("tool is required")
	}
	res, err := s.sess.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		return Result{}, err
	}
	text, truncated := renderContent(res.Content, MaxTextBytes)
	return Result{Text: text, IsError: res.IsError, Truncated: truncated}, nil
}

// renderContent flattens a result into text within limit. Content Fylane
// cannot forward is named rather than dropped: a caller reading an answer has
// to know part of it is missing.
func renderContent(content []mcp.Content, limit int) (string, bool) {
	var b strings.Builder
	truncated := false
	for _, c := range content {
		var part string
		switch v := c.(type) {
		case *mcp.TextContent:
			part = v.Text
		case nil:
			continue
		default:
			part = fmt.Sprintf("[%T omitted: this gateway forwards text only]", c)
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		if b.Len()+len(part) > limit {
			b.WriteString(part[:max(0, limit-b.Len())])
			truncated = true
			break
		}
		b.WriteString(part)
	}
	return b.String(), truncated
}

// headBuffer keeps the first limit bytes written to it and discards the rest.
// The beginning is the useful end for a process that failed to start.
type headBuffer struct {
	limit int
	buf   []byte
}

func (h *headBuffer) Write(p []byte) (int, error) {
	if room := h.limit - len(h.buf); room > 0 {
		if len(p) < room {
			room = len(p)
		}
		h.buf = append(h.buf, p[:room]...)
	}
	return len(p), nil
}

func (h *headBuffer) String() string { return string(h.buf) }

// Timeout returns the effective per-exchange budget for the requested one.
func Timeout(requested time.Duration) time.Duration {
	if requested <= 0 || requested > DefaultTimeout {
		return DefaultTimeout
	}
	return requested
}
