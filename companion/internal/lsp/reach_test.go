package lsp

import (
	"context"
	"strings"
	"testing"
)

func node(name string, kind, from, to int, children ...symbolNode) symbolNode {
	return symbolNode{
		Name: name, Kind: kind,
		Range:          lspRange{Start: position{Line: from}, End: position{Line: to}},
		SelectionRange: lspRange{Start: position{Line: from, Character: 5}, End: position{Line: from, Character: 9}},
		Children:       children,
	}
}

func TestTouchedPicksTheDeepestSymbolOnTheLine(t *testing.T) {
	// "Who calls Size" is the question a changed method body raises. "Who
	// mentions Widget" is a different and much larger one.
	tree := []symbolNode{
		node("Widget", 23, 2, 12,
			node("Size", 6, 4, 6),
			node("Name", 6, 8, 10),
		),
		node("twice", 12, 20, 24),
	}
	got := touched(tree, []int{6})
	if len(got) != 1 || got[0].Name != "Size" {
		t.Fatalf("touched = %v, want Size", names(got))
	}
	// A line inside the struct but in none of its methods lands on the struct.
	if got := touched(tree, []int{3}); len(got) != 1 || got[0].Name != "Widget" {
		t.Fatalf("touched(3) = %v, want Widget", names(got))
	}
	// A line outside every symbol asks about nothing rather than guessing.
	if got := touched(tree, []int{30}); len(got) != 0 {
		t.Fatalf("touched(30) = %v, want nothing", names(got))
	}
}

func TestOneEditedFunctionIsAskedAboutOnce(t *testing.T) {
	// A twenty-line edit inside one function is one question, not twenty.
	tree := []symbolNode{node("twice", 12, 20, 40)}
	got := touched(tree, []int{22, 23, 24, 25, 30, 39})
	if len(got) != 1 {
		t.Fatalf("touched = %v, want one entry", names(got))
	}
}

func TestNoLinesMeansTheWholeFile(t *testing.T) {
	// Which is what a delete is: every symbol in the file is in question.
	tree := []symbolNode{
		node("Widget", 23, 2, 12, node("Size", 6, 4, 6)),
		node("twice", 12, 20, 24),
	}
	got := touched(tree, nil)
	if strings.Join(names(got), ",") != "Widget,twice" {
		t.Fatalf("touched = %v, want the top-level symbols", names(got))
	}
}

func TestReachSaysNothingWhenNoServerIsRunning(t *testing.T) {
	// The rule this enforces is not about speed. Starting a server asks the
	// user, and Reach runs inside a prompt that is already asking them
	// something else. So a cold workspace gets no number, and the
	// caller must be able to tell that from a number that happens to be zero.
	starts := countStarts(t)
	s, root := superviseFixture(t)
	reach, err := s.Reach(t.Context(), query(root, "widget.go"), []int{3})
	if err != nil {
		t.Fatalf("Reach: %v", err)
	}
	if reach != nil {
		t.Fatalf("reach = %+v, want nothing from a cold workspace", reach)
	}
	if got := starts(); got != 0 {
		t.Fatalf("%d language servers were started by an impact query", got)
	}
}

func TestReachCountsUsesElsewhereOnce(t *testing.T) {
	s, root := superviseFixture(t)
	// Warm the server the way a navigation call would.
	if _, err := s.Symbols(t.Context(), query(root, "widget.go")); err != nil {
		t.Fatalf("Symbols: %v", err)
	}
	reach, err := s.Reach(t.Context(), query(root, "widget.go"), []int{3})
	if err != nil {
		t.Fatalf("Reach: %v", err)
	}
	if reach == nil {
		t.Fatal("a warm server answered nothing")
	}
	// The fixture answers with one reference in this file and one in another.
	// Only the other counts: this file's diff is already on the screen.
	if reach.Callers != 1 {
		t.Fatalf("callers = %d, want 1", reach.Callers)
	}
	if len(reach.Symbols) != 1 || reach.Symbols[0] != "Widget" {
		t.Fatalf("symbols = %v, want [Widget]", reach.Symbols)
	}
	if reach.Partial {
		t.Fatal("a complete answer was marked partial")
	}
}

func TestReachStopsAtTheBudgetAndSaysSo(t *testing.T) {
	// A number that ran out of time must not read as a total.
	s, root := superviseFixture(t)
	if _, err := s.Symbols(t.Context(), query(root, "widget.go")); err != nil {
		t.Fatalf("Symbols: %v", err)
	}
	// Cancelled outright rather than given a nanosecond: a deadline that
	// short is already past on a fine-grained clock and still ahead on a
	// coarse one, and Windows ticks coarsely enough for the whole call to
	// finish inside it. Reach keys on ctx.Err() either way.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	reach, err := s.Reach(ctx, query(root, "widget.go"), []int{3})
	if err != nil {
		// An expired context may end the symbol call instead, which is the
		// same honest outcome reached by a different route.
		return
	}
	if reach != nil && !reach.Partial && reach.Callers > 0 {
		t.Fatalf("reach = %+v, want the shortfall admitted", reach)
	}
}

func names(nodes []symbolNode) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.Name)
	}
	return out
}
