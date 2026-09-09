package lsp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Query names one place in the workspace to ask about.
type Query struct {
	WorkspaceID string
	// Root is the absolute, symlink-resolved workspace root. It never leaves
	// this machine and never appears in a Ref.
	Root string
	// Path is workspace-relative, in slash form, already validated by the
	// caller's sandbox.
	Path string
	// Symbol names what to ask about. Line narrows it when a name occurs more
	// than once; either may be given alone.
	Symbol string
	Line   int
}

// Ref is one place a symbol is defined or used. Paths are workspace-relative,
// lines are 1-based, and a location outside the workspace is reported as one
// rather than being dropped or given an absolute path.
type Ref struct {
	Path string `json:"path,omitempty"`
	Line int    `json:"line"`
	// Outside marks a location the workspace does not contain — a definition
	// in the standard library or in a dependency. The file's own name is
	// given because it is useful and says nothing about this machine's
	// layout; the directory is not, because it would.
	Outside bool   `json:"outside_workspace,omitempty"`
	File    string `json:"file,omitempty"`
}

// Symbol is one entry in a file's outline.
type Symbol struct {
	Name string `json:"name"`
	Kind string `json:"kind,omitempty"`
	Line int    `json:"line"`
}

// AmbiguousError says a name occurs more than once in the file, and where.
// It is an error rather than a guess: picking one silently would answer a
// question the caller did not ask.
type AmbiguousError struct {
	Symbol     string
	Candidates []Symbol
}

func (e *AmbiguousError) Error() string {
	lines := make([]string, 0, len(e.Candidates))
	for _, c := range e.Candidates {
		lines = append(lines, fmt.Sprintf("line %d", c.Line))
	}
	return fmt.Sprintf("%q appears %d times in this file (%s); pass line to say which",
		e.Symbol, len(e.Candidates), strings.Join(lines, ", "))
}

// NotFoundError says the file has no such symbol. It is deliberately not a
// fallback to text search: a navigation answer that quietly became a grep
// result is worse than no answer, because nothing downstream can tell.
type NotFoundError struct {
	Symbol string
	Path   string
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("no symbol named %q in %s", e.Symbol, e.Path)
}

// Definition answers where the symbol is defined.
func (s *Supervisor) Definition(ctx context.Context, q Query) ([]Ref, error) {
	return s.locate(ctx, q, func(c *client, at position) ([]location, error) {
		return c.definition(ctx, q.Path, at)
	})
}

// References answers everywhere the symbol is used, its declaration included.
func (s *Supervisor) References(ctx context.Context, q Query) ([]Ref, error) {
	return s.locate(ctx, q, func(c *client, at position) ([]location, error) {
		return c.references(ctx, q.Path, at)
	})
}

// Symbols answers what a file contains.
func (s *Supervisor) Symbols(ctx context.Context, q Query) ([]Symbol, error) {
	c, srv, err := s.open(ctx, q)
	if err != nil {
		return nil, err
	}
	nodes, err := c.symbols(ctx, q.Path)
	if err != nil {
		return nil, s.recover(q, srv, err)
	}
	return flatten(nodes, ""), nil
}

func (s *Supervisor) locate(ctx context.Context, q Query, ask func(*client, position) ([]location, error)) ([]Ref, error) {
	c, srv, err := s.open(ctx, q)
	if err != nil {
		return nil, err
	}
	at, err := s.position(ctx, c, q)
	if err != nil {
		return nil, err
	}
	locs, err := ask(c, at)
	if err != nil {
		return nil, s.recover(q, srv, err)
	}
	refs := make([]Ref, 0, len(locs))
	for _, l := range locs {
		ref, err := toRef(q.Root, l)
		if err != nil {
			continue
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

// open starts or reuses the server for this file and hands it the document.
func (s *Supervisor) open(ctx context.Context, q Query) (*client, Server, error) {
	srv, ok := s.reg.For(q.Path)
	if !ok {
		return nil, Server{}, fmt.Errorf("no language server on this machine handles %s files", strings.TrimPrefix(filepath.Ext(q.Path), "."))
	}
	c, err := s.get(ctx, q.WorkspaceID, q.Root, srv)
	if err != nil {
		return nil, srv, err
	}
	if err := c.didOpen(q.Path); err != nil {
		return nil, srv, s.recover(q, srv, err)
	}
	return c, srv, nil
}

// recover drops a server that has stopped talking so the next call starts a
// fresh one, and returns the error unchanged.
func (s *Supervisor) recover(q Query, srv Server, err error) error {
	if strings.Contains(err.Error(), "the language server stopped") {
		s.drop(q.WorkspaceID, srv.Name)
	}
	return err
}

// position turns "the symbol called X" into a place in the file. When the
// caller gave only a line, the first non-blank character on it is used.
func (s *Supervisor) position(ctx context.Context, c *client, q Query) (position, error) {
	if q.Symbol == "" && q.Line <= 0 {
		return position{}, fmt.Errorf("say which symbol to look up, or which line it is on")
	}
	lines, err := readLines(filepath.Join(q.Root, filepath.FromSlash(q.Path)))
	if err != nil {
		return position{}, err
	}
	if q.Symbol == "" {
		if q.Line > len(lines) {
			return position{}, fmt.Errorf("%s has %d lines; there is no line %d", q.Path, len(lines), q.Line)
		}
		text := lines[q.Line-1]
		return position{Line: q.Line - 1, Character: utf16Column(text, len(text)-len(strings.TrimLeft(text, " \t")))}, nil
	}

	nodes, err := c.symbols(ctx, q.Path)
	if err != nil {
		return position{}, err
	}
	var matches []symbolNode
	for _, n := range collect(nodes) {
		if n.Name != q.Symbol {
			continue
		}
		if q.Line > 0 && n.start().Line != q.Line-1 {
			continue
		}
		matches = append(matches, n)
	}
	switch len(matches) {
	case 0:
		return position{}, &NotFoundError{Symbol: q.Symbol, Path: q.Path}
	case 1:
		return matches[0].start(), nil
	default:
		return position{}, &AmbiguousError{Symbol: q.Symbol, Candidates: toSymbols(matches)}
	}
}

func toRef(root string, l location) (Ref, error) {
	abs, err := uriToPath(l.URI)
	if err != nil {
		return Ref{}, err
	}
	line := l.Range.Start.Line + 1
	if rel, ok := within(root, abs); ok {
		return Ref{Path: rel, Line: line}, nil
	}
	// "Outside" is a claim about the user's machine, so it is checked twice:
	// once on the paths as given, once with symlinks resolved. A workspace
	// reached through a link — /var on macOS is one — would otherwise have
	// every one of its own files reported as somewhere else.
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		if rel, ok := within(root, resolved); ok {
			return Ref{Path: rel, Line: line}, nil
		}
	}
	// The standard library, a module cache, a vendored dependency. The base
	// name is useful; the path to it would describe this machine.
	return Ref{Outside: true, File: filepath.Base(abs), Line: line}, nil
}

// within reports the workspace-relative form of abs, or that there is none.
func within(root, abs string) (string, bool) {
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func collect(nodes []symbolNode) []symbolNode {
	var out []symbolNode
	for _, n := range nodes {
		out = append(out, n)
		out = append(out, collect(n.Children)...)
	}
	return out
}

func flatten(nodes []symbolNode, prefix string) []Symbol {
	var out []Symbol
	for _, n := range nodes {
		name := n.Name
		if prefix != "" {
			name = prefix + "." + n.Name
		}
		out = append(out, Symbol{Name: name, Kind: kindName(n.Kind), Line: n.start().Line + 1})
		out = append(out, flatten(n.Children, name)...)
	}
	return out
}

func toSymbols(nodes []symbolNode) []Symbol {
	out := make([]Symbol, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, Symbol{Name: n.Name, Kind: kindName(n.Kind), Line: n.start().Line + 1})
	}
	return out
}

// kindNames are LSP's SymbolKind values. An unknown number is reported as the
// number rather than as a wrong word.
var kindNames = map[int]string{
	1: "file", 2: "module", 3: "namespace", 4: "package", 5: "class",
	6: "method", 7: "property", 8: "field", 9: "constructor", 10: "enum",
	11: "interface", 12: "function", 13: "variable", 14: "constant",
	15: "string", 16: "number", 17: "boolean", 18: "array", 19: "object",
	20: "key", 21: "null", 22: "enum member", 23: "struct", 24: "event",
	25: "operator", 26: "type parameter",
}

func kindName(kind int) string {
	if name, ok := kindNames[kind]; ok {
		return name
	}
	if kind == 0 {
		return ""
	}
	return fmt.Sprintf("kind %d", kind)
}

func readLines(abs string) ([]string, error) {
	body, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	return strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n"), nil
}
