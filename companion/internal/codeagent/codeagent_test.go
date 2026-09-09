package codeagent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeAgent struct {
	name string
	err  error
}

func (f fakeAgent) Name() string     { return f.name }
func (f fakeAgent) Available() error { return f.err }
func (f fakeAgent) Run(context.Context, Task, io.Writer) (Result, error) {
	return Result{Summary: f.name + " ran"}, nil
}

func TestRegistryPicksAnInstalledAgentWhenNoneIsNamed(t *testing.T) {
	r := NewRegistry(
		fakeAgent{name: "aardvark", err: errors.New("not installed")},
		fakeAgent{name: "zebra"},
	)
	got, err := r.Lookup("")
	if err != nil {
		t.Fatal(err)
	}
	// Order is stable, but an uninstalled agent must never be the pick just
	// because its name sorts first.
	if got.Name() != "zebra" {
		t.Fatalf("got %s", got.Name())
	}
	if names := r.Installed(); len(names) != 1 || names[0] != "zebra" {
		t.Fatalf("got installed %v", names)
	}
}

func TestRegistryReportsWhyANamedAgentCannotRun(t *testing.T) {
	r := NewRegistry(fakeAgent{name: "codex", err: errors.New("codex is not installed")})
	_, err := r.Lookup("codex")
	if err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("got %v; a missing agent must say so, not fail silently", err)
	}
	_, err = r.Lookup("nonesuch")
	if err == nil || !strings.Contains(err.Error(), "codex") {
		t.Fatalf("got %v; an unknown name should list what is available", err)
	}
}

func TestRegistryWithNothingInstalled(t *testing.T) {
	r := NewRegistry(fakeAgent{name: "codex", err: errors.New("nope")})
	if _, err := r.Lookup(""); err == nil {
		t.Fatal("an empty registry pick succeeded")
	}
}

func TestScrubberRemovesAbsolutePaths(t *testing.T) {
	root := filepath.FromSlash("/Users/dev/project")
	s := newScrubber(root)

	got := s.clean("edited " + filepath.Join(root, "src", "main.go") + " and read " + root)
	if strings.Contains(got, root) {
		t.Fatalf("the workspace root survived scrubbing: %q", got)
	}
	if !strings.Contains(got, filepath.Join("src", "main.go")) {
		t.Fatalf("the relative path was mangled: %q", got)
	}
}

func TestScrubberReplacesTheHomeDirectory(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory")
	}
	s := newScrubber(filepath.Join(home, "project"))
	got := s.clean("config at " + filepath.Join(home, ".config", "thing"))
	if strings.Contains(got, home) {
		t.Fatalf("the home directory leaked: %q", got)
	}
	if !strings.Contains(got, "~") {
		t.Fatalf("got %q", got)
	}
}

func TestScrubbingWriterReportsTheCallersLength(t *testing.T) {
	// The scrubbed form is shorter; reporting its length would look like a
	// short write to whoever is copying into us and stop the copy.
	var sink strings.Builder
	root := filepath.FromSlash("/Users/dev/project")
	w := scrubbingWriter{scrub: newScrubber(root), to: &sink}

	in := []byte("built " + filepath.Join(root, "dist") + "\n")
	n, err := w.Write(in)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(in) {
		t.Fatalf("reported %d of %d bytes", n, len(in))
	}
	if strings.Contains(sink.String(), root) {
		t.Fatalf("output was not scrubbed: %q", sink.String())
	}
}

func TestWorkDirRejectsEscapes(t *testing.T) {
	// Absolute on every platform: a drive-less path is relative on Windows,
	// and workDir is right to refuse it — which is not what this test is for.
	root := filepath.Join(filepath.VolumeName(t.TempDir())+string(filepath.Separator), "Users", "dev", "project")
	if _, err := workDir(Task{Root: root, Dir: "../elsewhere"}); err == nil {
		t.Fatal("a directory outside the workspace was accepted")
	}
	if _, err := workDir(Task{Root: "relative", Dir: ""}); err == nil {
		t.Fatal("a relative workspace root was accepted")
	}
	got, err := workDir(Task{Root: root, Dir: "packages/web"})
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(root, "packages", "web") {
		t.Fatalf("got %q", got)
	}
}

// --- opencode, against a server that follows the documented contract ------

func TestOpenCodeRunsATaskAndReturnsTheSummary(t *testing.T) {
	root := t.TempDir()
	var gotPrompt string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/session":
			json.NewEncoder(w).Encode([]any{})
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			json.NewEncoder(w).Encode(openCodeSession{ID: "ses_1"})
		case r.Method == http.MethodPost && r.URL.Path == "/session/ses_1/message":
			var body struct {
				Parts []openCodePart `json:"parts"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			if len(body.Parts) > 0 {
				gotPrompt = body.Parts[0].Text
			}
			json.NewEncoder(w).Encode(openCodeMessage{Parts: []openCodePart{
				{Type: "text", Text: "edited " + filepath.Join(root, "main.go")},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	agent := &OpenCode{BaseURL: srv.URL}
	var out strings.Builder
	res, err := agent.Run(context.Background(), Task{Prompt: "fix the bug", Root: root}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if gotPrompt != "fix the bug" {
		t.Fatalf("the prompt did not reach the agent: %q", gotPrompt)
	}
	// The agent's own output is full of absolute paths, and it goes back
	// through MCP to a platform.
	if strings.Contains(res.Summary, root) || strings.Contains(out.String(), root) {
		t.Fatalf("an absolute path escaped:\nsummary %q\noutput %q", res.Summary, out.String())
	}
	if !strings.Contains(res.Summary, "main.go") {
		t.Fatalf("got summary %q", res.Summary)
	}
}

func TestOpenCodeSaysWhenItIsNotRunning(t *testing.T) {
	agent := &OpenCode{BaseURL: "http://127.0.0.1:1"}
	err := agent.Available()
	if err == nil || !strings.Contains(err.Error(), "opencode serve") {
		t.Fatalf("got %v; the error should say how to start it", err)
	}
	// Run must fail the same way rather than half-starting a session.
	if _, err := agent.Run(context.Background(), Task{Prompt: "x", Root: t.TempDir()}, io.Discard); err == nil {
		t.Fatal("Run succeeded against a server that is not there")
	}
}

func TestOpenCodeReportsAServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode([]any{})
			return
		}
		http.Error(w, "model not configured", http.StatusBadRequest)
	}))
	defer srv.Close()

	agent := &OpenCode{BaseURL: srv.URL}
	_, err := agent.Run(context.Background(), Task{Prompt: "x", Root: t.TempDir()}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "model not configured") {
		t.Fatalf("got %v; the agent's own error should reach the caller", err)
	}
}

func TestOpenCodeSendsThePasswordWhenOneIsSet(t *testing.T) {
	t.Setenv("FYLANE_TEST_OPENCODE_PW", "s3cret")
	var sawAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := r.BasicAuth(); ok {
			sawAuth = true
		}
		json.NewEncoder(w).Encode([]any{})
	}))
	defer srv.Close()

	agent := &OpenCode{BaseURL: srv.URL, PasswordEnv: "FYLANE_TEST_OPENCODE_PW"}
	if err := agent.Available(); err != nil {
		t.Fatal(err)
	}
	if !sawAuth {
		t.Fatal("the password was not sent")
	}
}

func TestARejectedPasswordSaysWhichVariableToSet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	agent := &OpenCode{BaseURL: srv.URL}
	err := agent.Available()
	if err == nil || !strings.Contains(err.Error(), "OPENCODE_SERVER_PASSWORD") {
		t.Fatalf("got %v", err)
	}
}

func TestEmptyPromptsAreRefusedBeforeAnythingStarts(t *testing.T) {
	for name, agent := range map[string]Agent{
		"codex":    &Codex{Binary: "/nonexistent/codex"},
		"opencode": &OpenCode{BaseURL: "http://127.0.0.1:1"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := agent.Run(context.Background(), Task{Root: t.TempDir(), Prompt: "  "}, io.Discard); err == nil {
				t.Fatal("an empty prompt was accepted")
			}
		})
	}
}

func TestCodexReportsAMissingBinary(t *testing.T) {
	agent := &Codex{Binary: filepath.Join(t.TempDir(), "codex")}
	if err := agent.Available(); err == nil {
		t.Fatal("a missing binary was reported as available")
	}
	if _, err := agent.Run(context.Background(), Task{Prompt: "x", Root: t.TempDir()}, io.Discard); err == nil {
		t.Fatal("Run started with no binary")
	}
}
