package cmdexec

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/leazoot/fylane/companion/internal/readbox"
	"github.com/leazoot/fylane/companion/internal/sandbox"
)

// recorder collects audit records so tests can assert that every attempt,
// including a refused one, reached the auditor.
type recorder struct {
	mu   sync.Mutex
	recs []Record
}

func (r *recorder) ExecAttempt(_ context.Context, rec Record) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recs = append(r.recs, rec)
}

func (r *recorder) last(t *testing.T) Record {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.recs) == 0 {
		t.Fatal("no audit record was written")
	}
	return r.recs[len(r.recs)-1]
}

// newWorkspace returns a symlink-resolved temporary root, matching what
// workspace.New hands to callers.
func newWorkspace(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolving temp root: %v", err)
	}
	return root
}

func newRunner(t *testing.T) (*Runner, *recorder) {
	t.Helper()
	rec := &recorder{}
	return New(rec, nil), rec
}

// Commands run through a real read boundary here, and on Linux that boundary
// is applied by re-executing this binary as a shim (readbox/box_linux.go).
// Under `go test` this binary is the test binary: without this interception
// the child would run the package again instead of the command, and the
// command would appear to produce nothing for as long as its budget lasts.
func TestMain(m *testing.M) {
	if readbox.IsShim(os.Args) {
		if err := readbox.RunShim(os.Args); err != nil {
			os.Stderr.WriteString("read boundary shim: " + err.Error() + "\n")
			os.Exit(1)
		}
		return
	}
	os.Exit(m.Run())
}

func TestNewRejectsAMissingAuditor(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("constructing a Runner without an auditor should panic")
		}
	}()
	New(nil, nil)
}

func TestRunRefusesShellInterpreters(t *testing.T) {
	root := newWorkspace(t)
	// Every spelling that would otherwise reintroduce shell grammar: a bare
	// name, an absolute path, a Windows executable suffix, mixed case.
	for _, argv0 := range []string{"sh", "bash", "zsh", "/bin/sh", "/usr/bin/env/../bash", "cmd.exe", "PowerShell", "pwsh", "busybox"} {
		t.Run(argv0, func(t *testing.T) {
			r, rec := newRunner(t)
			_, err := r.Run(context.Background(), Spec{Root: root, Argv: []string{argv0, "-c", "echo pwned"}})
			if !errors.Is(err, ErrShellInterpreter) {
				t.Fatalf("argv[0]=%q: got %v, want ErrShellInterpreter", argv0, err)
			}
			if got := rec.last(t); got.Outcome != OutcomeRefused {
				t.Fatalf("refusal was not audited: outcome %q", got.Outcome)
			}
		})
	}
}

func TestRunRefusesAnEmptyCommand(t *testing.T) {
	root := newWorkspace(t)
	for name, argv := range map[string][]string{
		"nil":         nil,
		"empty slice": {},
		"blank argv0": {"   "},
	} {
		t.Run(name, func(t *testing.T) {
			r, _ := newRunner(t)
			if _, err := r.Run(context.Background(), Spec{Root: root, Argv: argv}); !errors.Is(err, ErrNoCommand) {
				t.Fatalf("got %v, want ErrNoCommand", err)
			}
		})
	}
}

func TestRunRefusesARelativeWorkspaceRoot(t *testing.T) {
	r, _ := newRunner(t)
	_, err := r.Run(context.Background(), Spec{Root: "relative/root", Argv: []string{"echo"}})
	if err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("got %v, want a complaint about the root not being absolute", err)
	}
}

func TestResolveDirTreatsTheRootAsAValidWorkingDirectory(t *testing.T) {
	root := newWorkspace(t)
	// sandbox.CleanPath rejects "" and "." because the root is never a valid
	// target for a file operation, but it is the ordinary place to run a
	// build. Every spelling of "the root" must land there.
	for _, dir := range []string{"", ".", "/", "./", `\`} {
		rel, abs, err := resolveDir(root, dir)
		if err != nil {
			t.Fatalf("dir=%q: %v", dir, err)
		}
		if rel != "" || abs != root {
			t.Fatalf("dir=%q: got rel=%q abs=%q, want rel=\"\" abs=%q", dir, rel, abs, root)
		}
	}
}

func TestResolveDirRejectsEscapes(t *testing.T) {
	root := newWorkspace(t)
	for _, dir := range []string{"..", "../etc", "a/../../b", "/etc", `..\..\windows`, "C:/Windows"} {
		if _, _, err := resolveDir(root, dir); err == nil {
			t.Fatalf("dir=%q was accepted; it escapes the workspace", dir)
		}
	}
}

func TestResolveDirAcceptsASubdirectory(t *testing.T) {
	root := newWorkspace(t)
	if err := os.MkdirAll(filepath.Join(root, "packages", "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	rel, abs, err := resolveDir(root, "packages/web")
	if err != nil {
		t.Fatalf("a monorepo subdirectory must be usable: %v", err)
	}
	if rel != "packages/web" {
		t.Fatalf("got rel %q, want packages/web", rel)
	}
	if abs != filepath.Join(root, "packages", "web") {
		t.Fatalf("got abs %q", abs)
	}
}

func TestResolveDirRejectsAFile(t *testing.T) {
	root := newWorkspace(t)
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveDir(root, "go.mod"); !errors.Is(err, ErrNotDirectory) {
		t.Fatalf("got %v, want ErrNotDirectory", err)
	}
}

func TestResolveDirRejectsAMissingDirectory(t *testing.T) {
	root := newWorkspace(t)
	if _, _, err := resolveDir(root, "nope"); err == nil {
		t.Fatal("a missing working directory must be an error, not a silent fallback to the root")
	}
}

func TestResolveProgramRejectsPathsLeavingTheWorkspace(t *testing.T) {
	root := newWorkspace(t)
	for _, name := range []string{"../evil.sh", "../../bin/evil", "/usr/bin/whoami", `..\evil.bat`} {
		if _, err := resolveProgram(root, "", name); err == nil {
			t.Fatalf("argv[0]=%q was accepted; it resolves outside the workspace", name)
		}
	}
}

func TestResolveProgramJoinsRelativePathsToTheWorkingDirectory(t *testing.T) {
	root := newWorkspace(t)
	// "./build.sh" from packages/web means packages/web/build.sh, and a
	// traversal out of that subdirectory must still be caught against the
	// workspace root rather than the subdirectory.
	dir := filepath.Join(root, "packages", "web")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "build.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := resolveProgram(root, "packages/web", "./build.sh")
	if err != nil {
		t.Fatalf("resolving ./build.sh from packages/web: %v", err)
	}
	if got != script {
		t.Fatalf("got %q, want %q", got, script)
	}
	if _, err := resolveProgram(root, "packages/web", "../../../etc/passwd"); !errors.Is(err, sandbox.ErrPathTraversal) {
		t.Fatalf("got %v, want ErrPathTraversal", err)
	}
}

func TestResolveProgramReportsAMissingProgramByName(t *testing.T) {
	root := newWorkspace(t)
	_, err := resolveProgram(root, "", "fylane-definitely-not-installed")
	if !errors.Is(err, ErrProgramNotFound) {
		t.Fatalf("got %v, want ErrProgramNotFound", err)
	}
	if strings.Contains(err.Error(), root) {
		t.Fatal("the error leaks the absolute workspace path")
	}
}

func TestBuildEnvKeepsOnlyTheWhitelist(t *testing.T) {
	parent := map[string]string{
		"PATH":                  "/usr/bin",
		"HOME":                  "/home/tester",
		"AWS_SECRET_ACCESS_KEY": "should-not-survive",
		"GITHUB_TOKEN":          "should-not-survive",
		"OPENAI_API_KEY":        "should-not-survive",
		"FYLANE_TUNNEL_TOKEN":   "should-not-survive",
		"TERM":                  "xterm-256color",
	}
	env := buildEnv(func(k string) (string, bool) {
		v, ok := parent[k]
		return v, ok
	})

	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "should-not-survive") {
		t.Fatalf("a non-whitelisted variable reached the child:\n%s", joined)
	}
	for _, want := range []string{"PATH=/usr/bin", "HOME=/home/tester", "TERM=dumb", "NO_COLOR=1"} {
		if !contains(env, want) {
			t.Fatalf("missing %q in %v", want, env)
		}
	}
	// TERM is fixed, not inherited: the parent's xterm value must lose.
	if contains(env, "TERM=xterm-256color") {
		t.Fatal("the parent's TERM overrode the fixed one")
	}
}

func TestBuildEnvSkipsUnsetVariables(t *testing.T) {
	env := buildEnv(func(string) (string, bool) { return "", false })
	for _, kv := range env {
		if strings.HasSuffix(kv, "=") {
			t.Fatalf("unset variable exported as empty: %q", kv)
		}
	}
	if len(env) != len(fixedEnv) {
		t.Fatalf("got %v, want only the fixed variables", env)
	}
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func TestCapWriterTruncatesWithoutShortWrites(t *testing.T) {
	w := newCapWriter(10)
	// A short write would make os/exec stop draining the pipe and the child
	// would block, so every Write must claim the full length.
	n, err := w.Write([]byte("0123456789abcdef"))
	if err != nil || n != 16 {
		t.Fatalf("got n=%d err=%v, want n=16 err=nil", n, err)
	}
	if got := w.String(); got != "0123456789" {
		t.Fatalf("got %q, want the first 10 bytes", got)
	}
	if !w.truncated() {
		t.Fatal("truncation was not reported")
	}
}

func TestCapWriterKeepsAcceptingAfterTheLimit(t *testing.T) {
	w := newCapWriter(4)
	for i := 0; i < 100; i++ {
		if n, err := w.Write([]byte("xxxx")); n != 4 || err != nil {
			t.Fatalf("write %d: n=%d err=%v", i, n, err)
		}
	}
	if got := w.String(); got != "xxxx" {
		t.Fatalf("got %q, want 4 bytes retained", got)
	}
}

func TestCapWriterUnderLimitIsNotTruncated(t *testing.T) {
	w := newCapWriter(10)
	w.Write([]byte("abc"))
	if w.truncated() {
		t.Fatal("reported truncation for a write under the limit")
	}
	if w.String() != "abc" {
		t.Fatalf("got %q", w.String())
	}
}

func TestSpellsAShellReadsTheWholeCommandLine(t *testing.T) {
	// The gateway and the rule table both ask this, so it is asserted here
	// rather than only through either of them.
	shells := [][]string{
		{"sh"},
		{"/bin/bash", "-c", "id"},
		{"env", "FOO=1", "sh", "-c", "id"},
		{"timeout", "30", "bash", "-c", "id"},
		{"docker", "run", "img", "zsh"},
		{" sh "},
		{`C:\Windows\System32\cmd.exe`, "/c", "dir"},
	}
	for _, argv := range shells {
		if !SpellsAShell(argv) {
			t.Errorf("%v should read as a shell", argv)
		}
	}

	notShells := [][]string{
		nil,
		{"go", "test", "./..."},
		{"grep", "-r", "bash", "docs/"},   // a word, not a program
		{"env", "FOO=1", "go", "build"},   // a wrapper around something else
		{"mcp-server-git", "--repo", "."}, // an ordinary provider command
	}
	for _, argv := range notShells {
		if SpellsAShell(argv) {
			t.Errorf("%v should not read as a shell", argv)
		}
	}
}

// The read boundary has to be applied by the thing that runs commands, not
// only to exist. Nothing else in this suite would notice if the wrap were
// removed — which is exactly what a mutation of it proved.
func TestACommandRunsInsideTheReadBoundary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no boundary and no /bin/cat")
	}
	box := readbox.New(true)
	if !box.Enforcing() {
		t.Skipf("no read boundary on this machine: %s", box.Why())
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory on this machine")
	}
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	if err := os.WriteFile(filepath.Join(root, "inside.txt"), []byte("workspace content\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := New(&recorder{}, box)
	inside, err := r.Run(context.Background(), Spec{
		WorkspaceID: "ws_1", Root: root, Argv: []string{"cat", "inside.txt"}})
	if err != nil {
		t.Fatalf("reading inside the workspace: %v", err)
	}
	if !strings.Contains(inside.Stdout, "workspace content") {
		t.Fatalf("the workspace file was not readable: %q %q", inside.Stdout, inside.Stderr)
	}

	// The home directory is what this boundary exists to keep out of reach:
	// other repositories, documents, keys.
	outside, err := r.Run(context.Background(), Spec{
		WorkspaceID: "ws_1", Root: root, Argv: []string{"ls", home}})
	if err != nil {
		t.Fatalf("running ls: %v", err)
	}
	if outside.ExitCode == 0 {
		t.Fatalf("the home directory was listable from a bounded command: %q", outside.Stdout)
	}
	said := strings.ToLower(outside.Stderr)
	if !strings.Contains(said, "not permitted") && !strings.Contains(said, "denied") {
		t.Fatalf("the refusal does not say it was one: %q", outside.Stderr)
	}
}
