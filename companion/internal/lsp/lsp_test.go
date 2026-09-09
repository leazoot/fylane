package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The fixture is this test binary re-run with fixtureEnv set, speaking LSP on
// its own stdio. It is the same trick mcpgate uses, and for the same reason:
// a stdio protocol needs a real process on the other end, and the suite may
// not fetch one.
const fixtureEnv = "FYLANE_TEST_LSP_SERVER"

// leakedEnv stands for everything in the Companion's environment a foreign
// program has no business seeing.
const leakedEnv = "FYLANE_TEST_RELAY_TOKEN"

// startsEnv names a file each fixture process appends a line to as it starts.
// Counting processes is the only way to check the supervisor starts one: the
// number of entries in its own map is a proxy, and a proxy that stays 1 while
// two gopls processes chew through the same repository.
const startsEnv = "FYLANE_TEST_LSP_STARTS"

func TestMain(m *testing.M) {
	if os.Getenv(fixtureEnv) != "" {
		runFixture()
		return
	}
	os.Exit(m.Run())
}

// runFixture answers the three requests this package sends, plus the handshake.
// It deliberately asks the client for configuration first and waits for the
// answer: a client that ignores server-initiated requests deadlocks here, which
// is exactly the failure this fixture exists to make visible.
func runFixture() {
	in := bufio.NewReader(os.Stdin)
	out := os.Stdout
	send := func(m map[string]any) {
		body, _ := json.Marshal(m)
		fmt.Fprintf(out, "Content-Length: %d\r\n\r\n", len(body))
		out.Write(body)
	}
	read := func() (map[string]any, bool) {
		length := -1
		for {
			line, err := in.ReadString('\n')
			if err != nil {
				return nil, false
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				break
			}
			if name, value, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(name), "content-length") {
				length, _ = strconv.Atoi(strings.TrimSpace(value))
			}
		}
		if length < 0 {
			return nil, false
		}
		body := make([]byte, length)
		if _, err := io.ReadFull(in, body); err != nil {
			return nil, false
		}
		var m map[string]any
		if err := json.Unmarshal(body, &m); err != nil {
			return nil, false
		}
		return m, true
	}

	if path := os.Getenv(startsEnv); path != "" {
		if f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			f.WriteString("started\n")
			f.Close()
		}
	}

	cwd, _ := os.Getwd()
	fileURI := func(rel string) string { return pathToURI(filepath.Join(cwd, rel)) }
	loc := func(uri string, line int) map[string]any {
		return map[string]any{"uri": uri, "range": map[string]any{
			"start": map[string]any{"line": line, "character": 5},
			"end":   map[string]any{"line": line, "character": 9},
		}}
	}

	for {
		m, ok := read()
		if !ok {
			return
		}
		method, _ := m["method"].(string)
		id := m["id"]
		switch method {
		case "initialize":
			// Ask the client something before answering. If it never
			// replies, this fixture hangs and the test times out.
			send(map[string]any{"jsonrpc": "2.0", "id": 9001, "method": "workspace/configuration",
				"params": map[string]any{"items": []any{map[string]any{"section": "gopls"}}}})
			if reply, ok := read(); !ok || reply["id"] == nil {
				return
			}
			send(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
				"capabilities": map[string]any{},
			}})
		case "textDocument/documentSymbol":
			send(map[string]any{"jsonrpc": "2.0", "id": id, "result": []any{
				map[string]any{
					"name": "Widget", "kind": 23,
					"range":          map[string]any{"start": map[string]any{"line": 2, "character": 0}, "end": map[string]any{"line": 8, "character": 1}},
					"selectionRange": map[string]any{"start": map[string]any{"line": 2, "character": 5}, "end": map[string]any{"line": 2, "character": 11}},
					"children": []any{
						map[string]any{
							"name": "Size", "kind": 6,
							"range":          map[string]any{"start": map[string]any{"line": 4, "character": 1}, "end": map[string]any{"line": 6, "character": 2}},
							"selectionRange": map[string]any{"start": map[string]any{"line": 4, "character": 1}, "end": map[string]any{"line": 4, "character": 5}},
						},
					},
				},
				map[string]any{
					"name": "twice", "kind": 12,
					"range":          map[string]any{"start": map[string]any{"line": 10, "character": 0}, "end": map[string]any{"line": 12, "character": 1}},
					"selectionRange": map[string]any{"start": map[string]any{"line": 10, "character": 5}, "end": map[string]any{"line": 10, "character": 10}},
				},
				map[string]any{
					"name": "twice", "kind": 12,
					"range":          map[string]any{"start": map[string]any{"line": 20, "character": 0}, "end": map[string]any{"line": 22, "character": 1}},
					"selectionRange": map[string]any{"start": map[string]any{"line": 20, "character": 5}, "end": map[string]any{"line": 20, "character": 10}},
				},
			}})
		case "textDocument/definition":
			params, _ := m["params"].(map[string]any)
			pos, _ := params["position"].(map[string]any)
			line, _ := pos["line"].(float64)
			if int(line) == 4 {
				// A definition the workspace does not contain.
				send(map[string]any{"jsonrpc": "2.0", "id": id,
					"result": loc(pathToURI(filepath.Join(os.TempDir(), "elsewhere", "print.go")), 313)})
				continue
			}
			send(map[string]any{"jsonrpc": "2.0", "id": id, "result": loc(fileURI("widget.go"), 2)})
		case "textDocument/references":
			send(map[string]any{"jsonrpc": "2.0", "id": id, "result": []any{
				loc(fileURI("widget.go"), 2),
				loc(fileURI("sub/use.go"), 6),
			}})
		case "shutdown":
			send(map[string]any{"jsonrpc": "2.0", "id": id, "result": nil})
		case "exit":
			return
		case "probe/env":
			send(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
				"cwd":    cwd,
				"leaked": os.Getenv(leakedEnv),
				"path":   os.Getenv("PATH") != "",
			}})
		default:
			if id != nil {
				send(map[string]any{"jsonrpc": "2.0", "id": id, "result": nil})
			}
		}
	}
}

// fixtureServer is the test binary, spoken to as a language server.
func fixtureServer(t *testing.T) Server {
	t.Helper()
	t.Setenv(fixtureEnv, "1")
	return Server{
		Name:           "fixture",
		Command:        []string{os.Args[0]},
		Extensions:     []string{".go"},
		LanguageID:     "go",
		EnvPassthrough: []string{fixtureEnv, startsEnv},
	}
}

// countStarts makes every fixture process this test starts record itself, and
// returns how many did.
func countStarts(t *testing.T) func() int {
	t.Helper()
	path := filepath.Join(t.TempDir(), "starts")
	t.Setenv(startsEnv, path)
	return func() int {
		body, err := os.ReadFile(path)
		if err != nil {
			return 0
		}
		return strings.Count(string(body), "started\n")
	}
}

func workspaceWithCode(t *testing.T) string {
	t.Helper()
	// Resolved, the way workspace.New hands a root to everything downstream.
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("line\n", 30)
	for _, name := range []string{"widget.go", filepath.Join("sub", "use.go")} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func superviseFixture(t *testing.T) (*Supervisor, string) {
	t.Helper()
	srv := fixtureServer(t)
	root := workspaceWithCode(t)
	reg, errs := NewRegistry([]Server{srv})
	if len(errs) != 0 {
		t.Fatalf("NewRegistry: %v", errs)
	}
	s := NewSupervisor(reg, time.Minute, nil)
	t.Cleanup(s.Close)
	return s, root
}

func query(root, path string) Query {
	return Query{WorkspaceID: "ws_1", Root: root, Path: path}
}

// ── the registry ───────────────────────────────────────────────────────────

func TestRegistryRefusesWhatTheGatewayRefuses(t *testing.T) {
	// The launcher rule is the Core's, not the gateway's, so it has to hold on
	// every surface where a locally configured program is named. `npx` here
	// would be exactly the hole this was built to keep shut.
	cases := []struct {
		name string
		s    Server
		want string
	}{
		{"npx", Server{Name: "x", Command: []string{"npx", "some-ls"}, Extensions: []string{".x"}}, "downloads"},
		{"go run", Server{Name: "x", Command: []string{"go", "run", "./ls"}, Extensions: []string{".x"}}, "downloads"},
		{"shell", Server{Name: "x", Command: []string{"sh", "-c", "ls"}, Extensions: []string{".x"}}, "shell"},
		{"no command", Server{Name: "x", Extensions: []string{".x"}}, "no command"},
		{"no name", Server{Command: []string{"gopls"}, Extensions: []string{".x"}}, "no name"},
		{"no extensions", Server{Name: "x", Command: []string{"gopls"}}, "no file extensions"},
		{"bare extension", Server{Name: "x", Command: []string{"gopls"}, Extensions: []string{"go"}}, "unusable extension"},
		{"env value", Server{Name: "x", Command: []string{"gopls"}, Extensions: []string{".x"},
			EnvPassthrough: []string{"TOKEN=abc"}}, "name the variable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, errs := NewRegistry([]Server{tc.s})
			if len(errs) != 1 {
				t.Fatalf("errors = %v, want exactly one", errs)
			}
			if !strings.Contains(errs[0].Error(), tc.want) {
				t.Fatalf("error = %q, want it to mention %q", errs[0], tc.want)
			}
		})
	}
}

func TestRegistryOnlyOffersWhatIsInstalled(t *testing.T) {
	// A built-in nobody has installed must not be offered: the tool would
	// then advertise a language it cannot answer for.
	reg, errs := NewRegistry(nil)
	if len(errs) != 0 {
		t.Fatalf("built-ins produced errors: %v", errs)
	}
	for _, name := range reg.Names() {
		s, ok := reg.byName[name]
		if !ok {
			t.Fatalf("%s is named but not registered", name)
		}
		if _, err := s.resolve(); err != nil {
			t.Fatalf("%s is offered but not installed: %v", name, err)
		}
	}
}

func TestAConfiguredServerReplacesTheBuiltInOfTheSameName(t *testing.T) {
	// Replacing rather than merging: a merged server is one the user did not
	// write and cannot read back out of their own settings file.
	t.Setenv(fixtureEnv, "1")
	reg, errs := NewRegistry([]Server{{
		Name:       "go",
		Command:    []string{os.Args[0]},
		Extensions: []string{".go", ".goo"},
	}})
	if len(errs) != 0 {
		t.Fatalf("NewRegistry: %v", errs)
	}
	got, ok := reg.For("a.go")
	if !ok {
		t.Fatal("no server for .go")
	}
	if got.Command[0] != os.Args[0] {
		t.Fatalf("command = %v, want the configured one", got.Command)
	}
	if _, ok := reg.For("a.goo"); !ok {
		t.Fatal("the configured server's extra extension was dropped")
	}
	if got.Language() != "go" {
		t.Fatalf("language = %q, want the name to stand in", got.Language())
	}
}

func TestNoServerForAnUnknownExtension(t *testing.T) {
	reg, _ := NewRegistry(nil)
	if _, ok := reg.For("notes.md"); ok {
		t.Fatal("a markdown file was matched to a language server")
	}
}

// ── URIs ───────────────────────────────────────────────────────────────────

func TestURIsSurviveAwkwardNames(t *testing.T) {
	// A `#` in a directory name turns the rest of a naively built URI into a
	// fragment, which silently points at the wrong file.
	for _, name := range []string{"plain", "with space", "with#hash", "with%percent", "with?question", "中文"} {
		abs := filepath.Join(string(filepath.Separator)+"tmp", name, "a.go")
		back, err := uriToPath(pathToURI(abs))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if back != abs {
			t.Fatalf("%s: round trip gave %q, want %q", name, back, abs)
		}
	}
}

func TestARemoteURIIsNotAPath(t *testing.T) {
	for _, uri := range []string{"http://example.com/a.go", "file://elsewhere/a.go", "jdt://contents/x"} {
		if p, err := uriToPath(uri); err == nil {
			t.Fatalf("%s was accepted as the local path %q", uri, p)
		}
	}
}

// ── talking to a server ────────────────────────────────────────────────────

func TestTheHandshakeAnswersTheServersOwnRequest(t *testing.T) {
	// The fixture asks for configuration and waits. Reaching a definition at
	// all proves the client answered rather than ignoring it.
	s, root := superviseFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	refs, err := s.Definition(ctx, Query{WorkspaceID: "ws_1", Root: root, Path: "widget.go", Symbol: "Widget"})
	if err != nil {
		t.Fatalf("Definition: %v", err)
	}
	if len(refs) != 1 || refs[0].Path != "widget.go" || refs[0].Line != 3 {
		t.Fatalf("refs = %+v, want widget.go line 3", refs)
	}
}

func TestReferencesComeBackRelativeAndOneBased(t *testing.T) {
	s, root := superviseFixture(t)
	refs, err := s.References(t.Context(), Query{WorkspaceID: "ws_1", Root: root, Path: "widget.go", Symbol: "Widget"})
	if err != nil {
		t.Fatalf("References: %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("refs = %+v, want two", refs)
	}
	for _, r := range refs {
		if filepath.IsAbs(r.Path) || strings.Contains(r.Path, root) {
			t.Fatalf("reference %q carries the machine's layout", r.Path)
		}
	}
	if refs[1].Path != "sub/use.go" || refs[1].Line != 7 {
		t.Fatalf("second reference = %+v, want sub/use.go line 7", refs[1])
	}
}

func TestADefinitionOutsideTheWorkspaceSaysSoWithoutSayingWhere(t *testing.T) {
	// The standard library and the module cache are the common case here, and
	// both live at paths that describe this machine. Dropping the answer would
	// be a lie; giving the path would be a leak.
	s, root := superviseFixture(t)
	refs, err := s.Definition(t.Context(), Query{WorkspaceID: "ws_1", Root: root, Path: "widget.go", Symbol: "Size"})
	if err != nil {
		t.Fatalf("Definition: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("refs = %+v, want one", refs)
	}
	got := refs[0]
	if !got.Outside || got.File != "print.go" || got.Line != 314 {
		t.Fatalf("ref = %+v, want an outside print.go at line 314", got)
	}
	if got.Path != "" {
		t.Fatalf("an outside reference carried a path: %q", got.Path)
	}
	blob, _ := json.Marshal(got)
	if strings.Contains(string(blob), os.TempDir()) || strings.Contains(string(blob), "elsewhere") {
		t.Fatalf("the outside reference leaks its directory: %s", blob)
	}
}

func TestSymbolsAreFlattenedWithTheirOwners(t *testing.T) {
	s, root := superviseFixture(t)
	syms, err := s.Symbols(t.Context(), query(root, "widget.go"))
	if err != nil {
		t.Fatalf("Symbols: %v", err)
	}
	var names []string
	for _, sym := range syms {
		names = append(names, sym.Name)
	}
	want := []string{"Widget", "Widget.Size", "twice", "twice"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("symbols = %v, want %v", names, want)
	}
	if syms[0].Kind != "struct" || syms[1].Kind != "method" {
		t.Fatalf("kinds = %q, %q", syms[0].Kind, syms[1].Kind)
	}
	if syms[0].Line != 3 {
		t.Fatalf("Widget is on line %d, want 3", syms[0].Line)
	}
}

func TestAnAmbiguousNameIsAskedAboutRatherThanGuessed(t *testing.T) {
	s, root := superviseFixture(t)
	_, err := s.References(t.Context(), Query{WorkspaceID: "ws_1", Root: root, Path: "widget.go", Symbol: "twice"})
	var amb *AmbiguousError
	if !errors.As(err, &amb) {
		t.Fatalf("err = %v, want an AmbiguousError", err)
	}
	if len(amb.Candidates) != 2 {
		t.Fatalf("candidates = %+v, want two", amb.Candidates)
	}
	if !strings.Contains(amb.Error(), "line 11") || !strings.Contains(amb.Error(), "line 21") {
		t.Fatalf("message = %q, want both lines named", amb.Error())
	}

	// Naming the line settles it.
	refs, err := s.References(t.Context(), Query{WorkspaceID: "ws_1", Root: root, Path: "widget.go", Symbol: "twice", Line: 21})
	if err != nil {
		t.Fatalf("References with a line: %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("refs = %+v", refs)
	}
}

func TestAMissingSymbolFailsInsteadOfFallingBackToSearch(t *testing.T) {
	// The failure is the feature. A navigation answer that quietly became a
	// text-search result is worse than no answer, because nothing downstream
	// can tell which one it got.
	s, root := superviseFixture(t)
	_, err := s.Definition(t.Context(), Query{WorkspaceID: "ws_1", Root: root, Path: "widget.go", Symbol: "Nonexistent"})
	var missing *NotFoundError
	if !errors.As(err, &missing) {
		t.Fatalf("err = %v, want a NotFoundError", err)
	}
}

func TestAFileNoServerHandlesIsSaidPlainly(t *testing.T) {
	s, root := superviseFixture(t)
	_, err := s.Symbols(t.Context(), query(root, "notes.md"))
	if err == nil || !strings.Contains(err.Error(), "md files") {
		t.Fatalf("err = %v, want it to name the extension", err)
	}
}

// ── the server process ─────────────────────────────────────────────────────

func TestTheServerRunsInTheWorkspaceWithABoundedEnvironment(t *testing.T) {
	t.Setenv(leakedEnv, "should-not-be-visible")
	s, root := superviseFixture(t)
	if _, err := s.Symbols(t.Context(), query(root, "widget.go")); err != nil {
		t.Fatalf("Symbols: %v", err)
	}
	c, _, err := s.open(t.Context(), query(root, "widget.go"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := c.conn.call(t.Context(), "probe/env", map[string]any{})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	var got struct {
		CWD    string `json:"cwd"`
		Leaked string `json:"leaked"`
		Path   bool   `json:"path"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	// t.TempDir on macOS is under a symlinked /var, and the child reports the
	// resolved form; compare what the filesystem says rather than the strings.
	same, err := sameDir(got.CWD, root)
	if err != nil || !same {
		t.Fatalf("cwd = %q, want the workspace root %q (%v)", got.CWD, root, err)
	}
	if got.Leaked != "" {
		t.Fatalf("the server inherited %s", leakedEnv)
	}
	if !got.Path {
		t.Fatal("the server got no PATH, so it cannot find its own tools")
	}
}

func TestOneServerPerWorkspaceEvenUnderAStampede(t *testing.T) {
	// Calls arriving together must share one start. Racing several gopls
	// processes onto one repository is the expensive mistake this guards, and
	// the count that matters is of processes, not of map entries: a
	// supervisor that starts eight and keeps the last one still has one entry.
	starts := countStarts(t)
	s, root := superviseFixture(t)
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = s.Symbols(t.Context(), query(root, "widget.go"))
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	s.mu.Lock()
	n := len(s.clients)
	s.mu.Unlock()
	if n != 1 {
		t.Fatalf("%d servers tracked, want 1", n)
	}
	if got := starts(); got != 1 {
		t.Fatalf("%d server processes were started, want 1", got)
	}
}

func TestAnIdleServerIsReclaimedAndTheAuthorizationIsNot(t *testing.T) {
	// Reclaim frees memory. It must not also throw away the user's answer:
	// they authorized the server, not the process.
	srv := fixtureServer(t)
	root := workspaceWithCode(t)
	reg, _ := NewRegistry([]Server{srv})
	s := NewSupervisor(reg, 40*time.Millisecond, nil)
	defer s.Close()
	s.Approve("ws_1", "fixture")

	if _, err := s.Symbols(t.Context(), query(root, "widget.go")); err != nil {
		t.Fatalf("Symbols: %v", err)
	}
	if !s.Running("ws_1", "fixture") {
		t.Fatal("the server is not running after a call")
	}
	deadline := time.Now().Add(5 * time.Second)
	for s.Running("ws_1", "fixture") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if s.Running("ws_1", "fixture") {
		t.Fatal("an idle server was never reclaimed")
	}
	if !s.Approved("ws_1", "fixture") {
		t.Fatal("reclaiming the process also forgot the user's authorization")
	}
	// And the next call brings it back without another question.
	if _, err := s.Symbols(t.Context(), query(root, "widget.go")); err != nil {
		t.Fatalf("Symbols after reclaim: %v", err)
	}
}

// What the settings page gets to know: which servers are up, by name. The
// same call answers for every workspace at once, and one server indexing two
// folders is still one name — the page is not asking which folders, and this
// answer must not be able to carry one.
func TestRunningServersNamesThemOnceAcrossWorkspaces(t *testing.T) {
	srv := fixtureServer(t)
	first := workspaceWithCode(t)
	second := workspaceWithCode(t)
	reg, _ := NewRegistry([]Server{srv})
	s := NewSupervisor(reg, time.Minute, nil)
	defer s.Close()

	if got := s.RunningServers(); len(got) != 0 {
		t.Fatalf("RunningServers before any call = %v, want none", got)
	}
	for _, root := range []string{first, second} {
		if _, err := s.Symbols(t.Context(), Query{WorkspaceID: root, Root: root, Path: "widget.go"}); err != nil {
			t.Fatalf("Symbols: %v", err)
		}
	}
	got := s.RunningServers()
	if len(got) != 1 || got[0] != "fixture" {
		t.Fatalf("RunningServers = %v, want the one name once", got)
	}
	// Two processes are running; the answer names one server and no folder.
	for _, name := range got {
		if strings.Contains(name, first) || strings.Contains(name, second) {
			t.Errorf("RunningServers carries a workspace path: %q", name)
		}
	}

	s.Close()
	if got := s.RunningServers(); len(got) != 0 {
		t.Errorf("RunningServers after Close = %v, want none", got)
	}
}

// The list has to say what each server reads. A name on its own tells a person
// who has not met gopls nothing at all.
func TestTheRegistryListsWhatEachServerReads(t *testing.T) {
	srv := fixtureServer(t)
	reg, _ := NewRegistry([]Server{srv})
	// Built-ins the developer's machine happens to have are in this list too,
	// so the fixture is looked up rather than indexed.
	var fixture Server
	for _, srv := range reg.List() {
		if srv.Name == "fixture" {
			fixture = srv
		}
	}
	if fixture.Name == "" {
		t.Fatalf("List = %+v, want the installed fixture among them", reg.List())
	}
	if len(fixture.Extensions) == 0 {
		t.Errorf("List gave %+v with no suffixes; a name alone says nothing", fixture)
	}
	if len(reg.List()) != len(reg.Names()) {
		t.Errorf("List has %d entries and Names has %d", len(reg.List()), len(reg.Names()))
	}
	if (*Registry)(nil).List() != nil {
		t.Error("a nil registry listed something")
	}
}

func TestCloseStopsEveryServerAndRefusesMore(t *testing.T) {
	s, root := superviseFixture(t)
	if _, err := s.Symbols(t.Context(), query(root, "widget.go")); err != nil {
		t.Fatalf("Symbols: %v", err)
	}
	s.Close()
	if s.Running("ws_1", "fixture") {
		t.Fatal("a server survived Close")
	}
	_, err := s.Symbols(t.Context(), query(root, "widget.go"))
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("err = %v, want ErrClosed", err)
	}
	s.Close() // twice is allowed
}

func sameDir(a, b string) (bool, error) {
	fa, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	fb, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	return os.SameFile(fa, fb), nil
}
