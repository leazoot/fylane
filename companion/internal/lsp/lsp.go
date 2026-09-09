// Package lsp answers three questions about code — where is this defined, who
// uses it, what is in this file — by asking a language server the user already
// has installed.
//
// It is built on the mcpgate model with one deliberate exception.
//
// What it borrows: servers are named in the Companion's own settings file and
// nowhere else, so no remote caller can pick the program that runs; a server
// is a program already on this machine, and launchers whose whole job is to
// fetch one — npx, uvx, go run, docker run — are refused by name, because
// the Core never downloads an executable at runtime and `npx some-server` is
// that with a step in between; the child gets a
// whitelisted environment and the workspace as its working directory.
//
// What it does not borrow: mcpgate starts a server for one exchange and kills
// it, and says why — a long-lived foreign process needs an owner, a shutdown
// path, and an answer for what one caller's session leaks into the next one's.
// A language server is kept alive instead. gopls spends seconds to tens of
// seconds building its view of a repository, so per-call would not be slow, it
// would be unusable. The third of those three reasons does not apply here: a
// language server's state is the workspace index, which every caller sees the
// same way, not a per-caller session. The first two do apply, and the
// supervisor answers them rather than avoiding them.
//
// Unlike an MCP provider, a language server is not opaque. It speaks a
// standard protocol and this package sends exactly three requests. That is why
// a built-in table of known server names is defensible here and was not there:
// Fylane knows what it is going to ask.
package lsp

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/leazoot/fylane/companion/internal/cmdexec"
)

// Server is one language server: a program on this machine, the file
// extensions it answers for, and the language id to announce for its
// documents.
type Server struct {
	// Name identifies the server in settings and in the approval prompt.
	Name string `json:"name"`
	// Command is the program and its arguments, already installed here.
	Command []string `json:"command"`
	// Extensions are the file suffixes this server handles, with the dot.
	Extensions []string `json:"extensions"`
	// LanguageID is the LSP languageId for its documents; Name when empty.
	LanguageID string `json:"language_id,omitempty"`
	// EnvPassthrough names environment variables to hand the server on top of
	// the standard whitelist. Names only — values stay in the environment.
	EnvPassthrough []string `json:"env_passthrough,omitempty"`
}

// builtins are the servers Fylane knows how to ask for without being told.
// Only the program name is recorded, never a path: the binary has to be on
// PATH already, or the user has to name it themselves in settings.
//
// This list will fall behind — it is the same species as the reader tables
// F21 was about — which is why a settings entry with the same name replaces
// the built-in outright instead of merging with it.
var builtins = []Server{
	{Name: "go", Command: []string{"gopls"}, Extensions: []string{".go"}, LanguageID: "go"},
	{
		Name:       "typescript",
		Command:    []string{"typescript-language-server", "--stdio"},
		Extensions: []string{".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs"},
		LanguageID: "typescript",
	},
	{
		Name:       "python",
		Command:    []string{"pyright-langserver", "--stdio"},
		Extensions: []string{".py", ".pyi"},
		LanguageID: "python",
	},
	{Name: "rust", Command: []string{"rust-analyzer"}, Extensions: []string{".rs"}, LanguageID: "rust"},
}

// Language is the languageId announced for this server's documents.
func (s Server) Language() string {
	if s.LanguageID != "" {
		return s.LanguageID
	}
	return s.Name
}

// validate rejects a configured server the same way the MCP gateway rejects a
// provider, and for the same reasons.
func (s Server) validate() error {
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("language server has no name")
	}
	if len(s.Command) == 0 {
		return fmt.Errorf("language server %q has no command", s.Name)
	}
	if len(s.Extensions) == 0 {
		return fmt.Errorf("language server %q handles no file extensions", s.Name)
	}
	for _, ext := range s.Extensions {
		if !strings.HasPrefix(ext, ".") || len(ext) < 2 || strings.ContainsAny(ext, `/\`) {
			return fmt.Errorf("language server %q has an unusable extension %q; write it as \".go\"", s.Name, ext)
		}
	}
	if cmdexec.SpellsAShell(s.Command) {
		return fmt.Errorf("language server %q runs a shell interpreter; name the program and its arguments instead", s.Name)
	}
	if reason := cmdexec.FetchesAndRuns(s.Command); reason != "" {
		return fmt.Errorf("language server %q %s; install it and name the installed program", s.Name, reason)
	}
	for _, name := range s.EnvPassthrough {
		if strings.ContainsRune(name, '=') {
			return fmt.Errorf("language server %q passes through %q; name the variable, not a value", s.Name, name)
		}
	}
	return nil
}

// resolve finds the program on this machine. A server that is configured but
// not installed is not a load-time error — most machines have some of these
// and not others — but it must not be offered.
func (s Server) resolve() (string, error) {
	path, err := exec.LookPath(s.Command[0])
	if err == nil {
		return path, nil
	}
	if filepath.IsAbs(s.Command[0]) {
		return "", fmt.Errorf("%q is not an executable on this machine", s.Command[0])
	}
	return "", fmt.Errorf("%q is not installed on this machine", cmdexec.ProgramName(s.Command[0]))
}

// Registry holds the language servers this Companion can start: the built-in
// table, overridden by name from settings, filtered down to what is installed.
type Registry struct {
	byExt  map[string]Server
	byName map[string]Server
	names  []string
}

// NewRegistry merges configured servers over the built-in table and keeps the
// ones present on this machine. It returns one error per rejected entry: a bad
// line in a settings file must not stop the Companion from starting, and it
// must not disappear silently either.
//
// A configured entry replaces the built-in of the same name completely.
// Merging the two would produce a server the user did not write and cannot
// read back out of their own settings file.
func NewRegistry(configured []Server) (*Registry, []error) {
	var errs []error
	merged := map[string]Server{}
	var order []string
	for _, s := range builtins {
		merged[s.Name] = s
		order = append(order, s.Name)
	}
	for _, s := range configured {
		if err := s.validate(); err != nil {
			errs = append(errs, err)
			continue
		}
		if _, seen := merged[s.Name]; !seen {
			order = append(order, s.Name)
		}
		merged[s.Name] = s
	}

	r := &Registry{byExt: map[string]Server{}, byName: map[string]Server{}}
	for _, name := range order {
		s := merged[name]
		if _, err := s.resolve(); err != nil {
			continue
		}
		r.byName[s.Name] = s
		r.names = append(r.names, s.Name)
		for _, ext := range s.Extensions {
			ext = strings.ToLower(ext)
			// First server wins an extension, so a built-in cannot take a
			// suffix a configured one already claimed.
			if _, taken := r.byExt[ext]; !taken {
				r.byExt[ext] = s
			}
		}
	}
	sort.Strings(r.names)
	return r, errs
}

// Names lists the installed servers, for the tool description and the desktop.
func (r *Registry) Names() []string {
	if r == nil {
		return nil
	}
	return append([]string(nil), r.names...)
}

// List returns the installed servers themselves, for the settings page. Names
// alone would not say what each one reads, and "gopls" means nothing to a
// person who has not met it.
func (r *Registry) List() []Server {
	if r == nil {
		return nil
	}
	out := make([]Server, 0, len(r.names))
	for _, name := range r.names {
		out = append(out, r.byName[name])
	}
	return out
}

// For returns the server that handles a workspace-relative path.
func (r *Registry) For(rel string) (Server, bool) {
	if r == nil {
		return Server{}, false
	}
	s, ok := r.byExt[strings.ToLower(filepath.Ext(rel))]
	return s, ok
}
