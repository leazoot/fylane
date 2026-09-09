package mcpserver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/leazoot/fylane/companion/internal/lsp"
	"github.com/leazoot/fylane/companion/internal/nextstep"
	"github.com/leazoot/fylane/companion/internal/txn"
)

// fakeNavigators stands in for the supervisor. The real one, talking to a
// real process over a real pipe, is tested in the lsp package; what these
// tests are about is the layer above it — who gets asked, what gets hidden,
// and what a caller is told when the answer is not a list of places.
type fakeNavigators struct {
	reg *lsp.Registry

	mu       sync.Mutex
	approved map[string]bool
	running  map[string]bool
	starts   int

	refs    []lsp.Ref
	symbols []lsp.Symbol
	err     error
}

func newFakeNavigators(t *testing.T) *fakeNavigators {
	t.Helper()
	// The registry needs a server that resolves, and the test binary is one
	// that certainly exists on this machine.
	reg, errs := lsp.NewRegistry([]lsp.Server{{
		Name:       "fixture",
		Command:    []string{os.Args[0]},
		Extensions: []string{".go"},
		LanguageID: "go",
	}})
	if len(errs) != 0 {
		t.Fatalf("registry: %v", errs)
	}
	return &fakeNavigators{reg: reg, approved: map[string]bool{}, running: map[string]bool{}}
}

func (f *fakeNavigators) Registry() *lsp.Registry { return f.reg }

func (f *fakeNavigators) Approved(ws, server string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.approved[ws+"/"+server]
}

func (f *fakeNavigators) Approve(ws, server string) {
	f.mu.Lock()
	f.approved[ws+"/"+server] = true
	f.mu.Unlock()
}

func (f *fakeNavigators) Running(ws, server string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running[ws+"/"+server]
}

func (f *fakeNavigators) answer() ([]lsp.Ref, error) {
	f.mu.Lock()
	f.starts++
	f.mu.Unlock()
	return f.refs, f.err
}

func (f *fakeNavigators) Definition(context.Context, lsp.Query) ([]lsp.Ref, error) { return f.answer() }
func (f *fakeNavigators) References(context.Context, lsp.Query) ([]lsp.Ref, error) { return f.answer() }
func (f *fakeNavigators) Symbols(context.Context, lsp.Query) ([]lsp.Symbol, error) {
	return f.symbols, f.err
}

type navFixture struct {
	*execFixture
	nav *fakeNavigators
}

func newNavFixture(t *testing.T, decision txn.Decision) *navFixture {
	t.Helper()
	f := newExecFixture(t, decision)
	nav := newFakeNavigators(t)
	f.tools.navigators = nav
	for _, name := range []string{"widget.go", "notes.md", ".env", "vendor.go"} {
		if err := os.WriteFile(filepath.Join(f.root, name), []byte("package a\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &navFixture{execFixture: f, nav: nav}
}

func (f *navFixture) navigate(t *testing.T, in navigateInput) navigateOutput {
	t.Helper()
	_, out, err := f.tools.codeNavigate(context.Background(), nil, in)
	if err != nil {
		t.Fatalf("code_navigate(%s): %v", in.Action, err)
	}
	return out
}

func TestStartingALanguageServerAsksOnceAndOnlyForItself(t *testing.T) {
	// The rung is not consulted here and no workspace grant covers it: the
	// question is "may Fylane run this program in this folder", and the
	// fixture's workspace is already granted for commands, which must not
	// answer it.
	f := newNavFixture(t, txn.Decision{Approved: true})
	f.nav.refs = []lsp.Ref{{Path: "widget.go", Line: 3}}

	out := f.navigate(t, navigateInput{Action: actionDefinition, Path: "widget.go", Symbol: "Widget"})
	if out.Status != "ok" || len(out.Locations) != 1 {
		t.Fatalf("out = %+v", out)
	}
	reqs := f.approver.requests()
	if len(reqs) != 1 {
		t.Fatalf("%d approval requests, want exactly one", len(reqs))
	}
	req := reqs[0]
	if req.Rule != navigateRule || req.Reason != NavigateWarning {
		t.Fatalf("request = %+v, want the navigation rule and its warning", req)
	}
	if req.Grant {
		t.Fatal("approving a language server also granted the workspace")
	}
	if strings.Join(req.Command, " ") != os.Args[0] {
		t.Fatalf("command = %v, want the configured program", req.Command)
	}

	// Asked once, not once per call.
	f.navigate(t, navigateInput{Action: actionReferences, Path: "widget.go", Symbol: "Widget"})
	f.navigate(t, navigateInput{Action: actionSymbols, Path: "widget.go"})
	if got := len(f.approver.requests()); got != 1 {
		t.Fatalf("%d approval requests after three calls, want 1", got)
	}
}

func TestARefusedLanguageServerIsNotStarted(t *testing.T) {
	f := newNavFixture(t, txn.Decision{Approved: false, Reason: "not now"})
	out := f.navigate(t, navigateInput{Action: actionDefinition, Path: "widget.go", Symbol: "Widget"})
	if out.Status != "refused" || out.Next != nextstep.Stop {
		t.Fatalf("out = %+v, want a refusal that stops", out)
	}
	if out.Reason != "not now" {
		t.Fatalf("reason = %q, want the user's own", out.Reason)
	}
	f.nav.mu.Lock()
	starts := f.nav.starts
	f.nav.mu.Unlock()
	if starts != 0 {
		t.Fatalf("the server was asked %d questions after being refused", starts)
	}
}

func TestAPendingApprovalTellsTheCallerToComeBack(t *testing.T) {
	f := newNavFixture(t, txn.Decision{Pending: true})
	out := f.navigate(t, navigateInput{Action: actionSymbols, Path: "widget.go"})
	if out.Status != "pending_approval" || out.Next != nextstep.AskUser {
		t.Fatalf("out = %+v", out)
	}
	if !strings.Contains(out.Action, "again") {
		t.Fatalf("action = %q, want it to say to retry", out.Action)
	}
}

func TestAFileNoServerHandlesNeitherAsksNorPretends(t *testing.T) {
	// No approval question, because nothing would be started; and no silent
	// fallback to search, because a caller cannot tell the difference.
	f := newNavFixture(t, txn.Decision{Approved: true})
	out := f.navigate(t, navigateInput{Action: actionSymbols, Path: "notes.md"})
	if out.Status != "unavailable" {
		t.Fatalf("out = %+v", out)
	}
	if !strings.Contains(out.Reason, ".md") {
		t.Fatalf("reason = %q, want it to name the suffix", out.Reason)
	}
	if len(f.approver.requests()) != 0 {
		t.Fatal("a file with no server still asked to start one")
	}
}

func TestNavigationObeysTheSandboxAndTheReadRules(t *testing.T) {
	f := newNavFixture(t, txn.Decision{Approved: true})
	for _, path := range []string{"../outside.go", "/etc/passwd"} {
		if _, _, err := f.tools.codeNavigate(context.Background(), nil,
			navigateInput{Action: actionSymbols, Path: path}); err == nil {
			t.Fatalf("%s was accepted", path)
		}
	}
	// A sensitive file is not navigable just because it is being reached by a
	// different tool: what makes it sensitive is the file, not the caller.
	_, _, err := f.tools.codeNavigate(context.Background(), nil,
		navigateInput{Action: actionSymbols, Path: ".env"})
	if err == nil || !strings.Contains(err.Error(), "sensitive") {
		t.Fatalf(".env error = %v, want the sensitive-read rules to speak", err)
	}
	// And an excluded file answers the way it does everywhere else, which is
	// as though it were not there.
	if err := os.MkdirAll(filepath.Join(f.root, "node_modules", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.root, "node_modules", "pkg", "index.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err = f.tools.codeNavigate(context.Background(), nil,
		navigateInput{Action: actionSymbols, Path: "node_modules/pkg/index.go"})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("excluded file error = %v, want \"not found\"", err)
	}
}

func TestTheToolIsAbsentWhenNoServerIsInstalled(t *testing.T) {
	// A tool that answers "unavailable" to everything is worse than an absent
	// one: a model keeps trying it, paying a round trip each time to learn
	// the same thing.
	empty, errs := lsp.NewRegistry([]lsp.Server{{
		Name: "nowhere", Command: []string{"fylane-no-such-language-server"},
		Extensions: []string{".zz"},
	}})
	if len(errs) != 0 {
		t.Fatalf("registry: %v", errs)
	}
	if len(empty.Names()) != 0 {
		t.Skip("this machine has a language server named fylane-no-such-language-server")
	}
	f := newNavFixture(t, txn.Decision{Approved: true})
	f.nav.reg = empty
	if names := f.tools.navigators.Registry().Names(); len(names) != 0 {
		t.Fatalf("names = %v, want none", names)
	}
}

func TestResultsInHiddenFilesAreDroppedAndCounted(t *testing.T) {
	// A file hidden from listings and skipped in search must not reappear in
	// a reference list. Counting it is the other half: a caller that sees two
	// references where there are three should be told a third exists, not
	// left to conclude the symbol is used less than it is.
	f := newNavFixture(t, txn.Decision{Approved: true})
	f.nav.refs = []lsp.Ref{
		{Path: "widget.go", Line: 3},
		{Path: ".env", Line: 1},
		{Path: "node_modules/pkg/index.go", Line: 9},
		{Outside: true, File: "print.go", Line: 314},
	}
	out := f.navigate(t, navigateInput{Action: actionReferences, Path: "widget.go", Symbol: "Widget"})
	if out.Hidden != 2 {
		t.Fatalf("hidden = %d, want 2", out.Hidden)
	}
	if len(out.Locations) != 2 {
		t.Fatalf("locations = %+v, want the visible one and the outside one", out.Locations)
	}
	for _, l := range out.Locations {
		if l.Path == ".env" || strings.HasPrefix(l.Path, "node_modules/") {
			t.Fatalf("a hidden file came back as %+v", l)
		}
	}
}

func TestAnAmbiguousNameComesBackAsAQuestion(t *testing.T) {
	f := newNavFixture(t, txn.Decision{Approved: true})
	f.nav.err = &lsp.AmbiguousError{Symbol: "twice", Candidates: []lsp.Symbol{
		{Name: "twice", Kind: "function", Line: 11},
		{Name: "twice", Kind: "function", Line: 21},
	}}
	out := f.navigate(t, navigateInput{Action: actionReferences, Path: "widget.go", Symbol: "twice"})
	if out.Status != "ambiguous" || out.Next != nextstep.FixInput {
		t.Fatalf("out = %+v", out)
	}
	if len(out.Candidates) != 2 {
		t.Fatalf("candidates = %+v", out.Candidates)
	}
}

func TestAMissingSymbolIsSaidRatherThanSearchedFor(t *testing.T) {
	f := newNavFixture(t, txn.Decision{Approved: true})
	f.nav.err = &lsp.NotFoundError{Symbol: "Nope", Path: "widget.go"}
	out := f.navigate(t, navigateInput{Action: actionDefinition, Path: "widget.go", Symbol: "Nope"})
	if out.Status != "unavailable" || out.Next != nextstep.FixInput {
		t.Fatalf("out = %+v", out)
	}
	if len(out.Locations) != 0 {
		t.Fatal("a missing symbol produced locations")
	}
	if !strings.Contains(out.Action, "does not guess") {
		t.Fatalf("action = %q, want it to say the tool does not guess", out.Action)
	}
}

func TestAnUnknownActionIsRejectedBeforeAnythingStarts(t *testing.T) {
	f := newNavFixture(t, txn.Decision{Approved: true})
	if _, _, err := f.tools.codeNavigate(context.Background(), nil,
		navigateInput{Action: "rename", Path: "widget.go"}); err == nil {
		t.Fatal("action=rename was accepted")
	}
	if len(f.approver.requests()) != 0 {
		t.Fatal("an unknown action still asked to start a server")
	}
}
