package lsp

import (
	"context"
	"path/filepath"
	"strconv"
)

// maxReachSymbols bounds how many symbols one operation is asked about. A
// change that touches more than this is reported as a floor rather than a
// total: the number is there to inform a decision, and eight references
// queries is already the outer edge of what an approval prompt can wait for.
const maxReachSymbols = 8

// Reach is how far a change carries: the symbols it disturbs, and how many
// places in other files use them.
type Reach struct {
	Symbols []string
	Callers int
	Partial bool
}

// Reach answers only from a language server that is already running.
//
// That is not a performance rule. Starting a server asks the local user
// , and this runs inside a prompt that is already asking them whether
// to allow a write — those are not one question, and a prompt that
// spawns a second prompt to decorate itself is exactly the shape that rule
// forbids. So a cold workspace gets no impact line, and the line's absence
// says so.
//
// lines are 1-based pre-image lines; empty means the whole file.
func (s *Supervisor) Reach(ctx context.Context, q Query, lines []int) (*Reach, error) {
	srv, ok := s.reg.For(q.Path)
	if !ok {
		return nil, nil
	}
	if !s.Running(q.WorkspaceID, srv.Name) {
		return nil, nil
	}
	c, _, err := s.open(ctx, q)
	if err != nil {
		return nil, err
	}
	nodes, err := c.symbols(ctx, q.Path)
	if err != nil {
		return nil, s.recover(q, srv, err)
	}
	targets := touched(nodes, lines)
	out := &Reach{}
	if len(targets) > maxReachSymbols {
		targets = targets[:maxReachSymbols]
		out.Partial = true
	}

	seen := map[string]bool{}
	for _, t := range targets {
		if ctx.Err() != nil {
			out.Partial = true
			return out, nil
		}
		locs, err := c.references(ctx, q.Path, t.start())
		if err != nil {
			// One symbol's answer missing makes the total a floor, not a
			// reason to throw away the ones that arrived.
			out.Partial = true
			continue
		}
		out.Symbols = append(out.Symbols, t.Name)
		for _, l := range locs {
			abs, err := uriToPath(l.URI)
			if err != nil {
				continue
			}
			if sameFile(q.Root, q.Path, abs) {
				// The user is looking at this file's diff, so what they
				// cannot already see is what is elsewhere.
				continue
			}
			key := abs + ":" + strconv.Itoa(l.Range.Start.Line)
			if seen[key] {
				continue
			}
			seen[key] = true
			out.Callers++
		}
	}
	return out, nil
}

// touched is the deepest symbol containing each line, in file order. A method
// is a better answer than the struct that holds it: "who calls Size" is the
// question, not "who mentions Widget".
func touched(nodes []symbolNode, lines []int) []symbolNode {
	if len(lines) == 0 {
		return dedupe(topLevel(nodes))
	}
	var out []symbolNode
	for _, line := range lines {
		if n, ok := deepest(nodes, line-1); ok {
			out = append(out, n)
		}
	}
	return dedupe(out)
}

func topLevel(nodes []symbolNode) []symbolNode {
	out := make([]symbolNode, 0, len(nodes))
	for _, n := range nodes {
		if n.Name != "" {
			out = append(out, n)
		}
	}
	return out
}

func deepest(nodes []symbolNode, line int) (symbolNode, bool) {
	for _, n := range nodes {
		r := n.Range
		if n.Location != nil {
			r = n.Location.Range
		}
		if line < r.Start.Line || line > r.End.Line {
			continue
		}
		if inner, ok := deepest(n.Children, line); ok {
			return inner, true
		}
		return n, true
	}
	return symbolNode{}, false
}

// dedupe keeps the first occurrence of each symbol, by name and position: a
// twenty-line edit inside one function must ask about it once.
func dedupe(nodes []symbolNode) []symbolNode {
	seen := map[string]bool{}
	out := make([]symbolNode, 0, len(nodes))
	for _, n := range nodes {
		key := n.Name + ":" + strconv.Itoa(n.start().Line)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, n)
	}
	return out
}

func sameFile(root, rel, abs string) bool {
	want := filepath.Join(root, filepath.FromSlash(rel))
	if abs == want {
		return true
	}
	resolved, err := filepath.EvalSymlinks(abs)
	return err == nil && resolved == want
}
