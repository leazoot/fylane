package mcpgate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The provider these tests proxy to is this test binary, re-executed with
// fixtureEnv set. That is how a stdio transport gets a real process on the
// other end without shipping a fixture binary or fetching one — the rule
// against fetching applies to the test suite too.
const fixtureEnv = "FYLANE_TEST_MCP_PROVIDER"

// leakedEnv stands in for everything in the Companion's own environment that
// a foreign server has no business seeing.
const leakedEnv = "FYLANE_TEST_RELAY_TOKEN"

func TestMain(m *testing.M) {
	if os.Getenv(fixtureEnv) != "" {
		runFixture()
		return
	}
	os.Exit(m.Run())
}

type echoIn struct {
	Text string `json:"text,omitempty"`
}

type echoOut struct {
	Echo string `json:"echo,omitempty"`
}

func runFixture() {
	srv := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "0"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "echo", Description: "returns what it was given"},
		func(_ context.Context, _ *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, echoOut, error) {
			if in.Text == "boom" {
				return &mcp.CallToolResult{
					IsError: true,
					Content: []mcp.Content{&mcp.TextContent{Text: "the provider says no"}},
				}, echoOut{}, nil
			}
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "echo: " + in.Text}},
			}, echoOut{Echo: in.Text}, nil
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "probe", Description: "reports its own process state"},
		func(_ context.Context, _ *mcp.CallToolRequest, _ echoIn) (*mcp.CallToolResult, echoOut, error) {
			cwd, _ := os.Getwd()
			lines := []string{
				"cwd=" + cwd,
				"leaked=" + os.Getenv(leakedEnv),
				"passed=" + os.Getenv("FYLANE_TEST_PASSED"),
				"path_set=" + boolWord(os.Getenv("PATH") != ""),
			}
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: strings.Join(lines, "\n")}},
			}, echoOut{}, nil
		})
	if err := srv.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		os.Exit(1)
	}
}

func boolWord(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// fixtureProvider is the test binary, spoken to as an MCP server.
func fixtureProvider(t *testing.T, passthrough ...string) Provider {
	t.Helper()
	t.Setenv(fixtureEnv, "1")
	return Provider{
		Name:           "fixture",
		Command:        []string{os.Args[0]},
		EnvPassthrough: append([]string{fixtureEnv}, passthrough...),
	}
}

func open(t *testing.T, p Provider, dir string) *Session {
	t.Helper()
	sess, err := Open(t.Context(), p, dir, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

func callText(t *testing.T, sess *Session, tool, text string) Result {
	t.Helper()
	res, err := sess.Call(t.Context(), tool, map[string]any{"text": text})
	if err != nil {
		t.Fatalf("Call %q: %v", tool, err)
	}
	return res
}

// This is the whole reason this product cannot copy the other design's
// one-command trial, so it cannot be walked around inside a provider entry
// either: `npx some-server` downloads and runs an executable, with one extra
// step between the config file and the download.
func TestAProviderMayNotBeSpelledAsADownloader(t *testing.T) {
	downloaders := [][]string{
		{"npx", "some-mcp-server"},
		{"/opt/homebrew/bin/npx", "some-mcp-server"},
		{"npx.cmd", "some-mcp-server"},
		{"uvx", "some-mcp-server"},
		{"bunx", "some-mcp-server"},
		{"npm", "exec", "some-mcp-server"},
		{"npm", "--yes", "x", "some-mcp-server"},
		{"pnpm", "dlx", "some-mcp-server"},
		{"yarn", "dlx", "some-mcp-server"},
		{"uv", "tool", "run", "some-mcp-server"},
		{"pipx", "run", "some-mcp-server"},
		{"go", "run", "example.com/server@latest"},
		{"deno", "run", "https://example.com/server.ts"},
		{"cargo", "install", "some-server"},
		{"docker", "run", "some/image"},
		{"podman", "run", "some/image"},
		{"nix", "run", "nixpkgs#server"},
	}
	for _, argv := range downloaders {
		p := Provider{Name: "p", Command: argv}
		err := p.Validate()
		if err == nil {
			t.Errorf("%v was accepted; it downloads the program it runs", argv)
			continue
		}
		if !strings.Contains(err.Error(), "never downloads an executable") {
			t.Errorf("%v refused without naming the rule: %v", argv, err)
		}
	}
}

// The same programs do ordinary, local work in other forms. Refusing those
// would push users to the one workaround this package must not have.
func TestOrdinaryProgramsAreStillAllowed(t *testing.T) {
	fine := [][]string{
		{"node", "/opt/servers/db/index.js"},
		{"go", "build", "./..."},
		{"docker", "compose", "up"},
		{"uv", "--version"},
		{"my-mcp-server", "--port", "0"},
		{"deno", "--version"},
	}
	for _, argv := range fine {
		if err := (Provider{Name: "p", Command: argv}).Validate(); err != nil {
			t.Errorf("%v was refused: %v", argv, err)
		}
	}
}

// The backend rules forbid a free-form `sh -c` escape hatch anywhere. A
// provider entry is a command line the Companion runs, so it is one.
func TestAProviderMayNotBeAShell(t *testing.T) {
	for _, argv := range [][]string{
		{"sh", "-c", "server"}, {"bash", "-lc", "server"}, {"/bin/zsh"},
		// Behind a wrapper counts too. This check used to read argv[0] only
		// while the rule table read the whole line, so the same threat had
		// two answers depending on which surface asked.
		{"env", "FOO=1", "sh", "-c", "server"},
		{"timeout", "30", "bash", "-c", "server"},
		{"docker", "run", "img", "sh", "-c", "server"},
	} {
		if err := (Provider{Name: "p", Command: argv}).Validate(); err == nil {
			t.Errorf("%v was accepted as a provider", argv)
		}
	}
	// A wrapper around an actual server is still a configuration that means
	// something, and refusing it would be a different rule than this one.
	if err := (Provider{Name: "p", Command: []string{"env", "FOO=1", "mcp-server-git"}}).Validate(); err != nil {
		t.Errorf("a wrapped server was refused: %v", err)
	}
}

// The name reaches tool input and the audit log, so it has to be a name and
// not a sentence, a path, or an injection.
func TestProviderNamesAreConstrained(t *testing.T) {
	for _, name := range []string{"", "Has Caps", "with space", "../escape", strings.Repeat("a", 33), "a/b"} {
		if err := (Provider{Name: name, Command: []string{"server"}}).Validate(); err == nil {
			t.Errorf("name %q was accepted", name)
		}
	}
	for _, name := range []string{"db", "issue-tracker", "a_b2", strings.Repeat("a", 32)} {
		if err := (Provider{Name: name, Command: []string{"server"}}).Validate(); err != nil {
			t.Errorf("name %q was refused: %v", name, err)
		}
	}
}

// Ask is what an unset trust means, and it is what an opaque tool gets. A
// provider that reached Workspace by omission would be the whole failure mode
// of this design.
func TestTrustDefaultsToAsking(t *testing.T) {
	if got := (Provider{}).Trusted(); got != Ask {
		t.Errorf("unset trust = %q, want %q", got, Ask)
	}
	if got := (Provider{Trust: Workspace}).Trusted(); got != Workspace {
		t.Errorf("configured trust = %q, want %q", got, Workspace)
	}
	if err := (Provider{Name: "p", Command: []string{"s"}, Trust: "always"}).Validate(); err == nil {
		t.Error("an unknown trust was accepted; it would read as neither of the two")
	}
}

// One bad line must not stop the Companion from starting, and must not vanish
// either: the caller is handed the reason so it can be logged.
func TestABadProviderIsDroppedWithItsReason(t *testing.T) {
	reg, errs := NewRegistry([]Provider{
		{Name: "good", Command: []string{"server"}},
		{Name: "bad", Command: []string{"npx", "server"}},
		{Name: "good", Command: []string{"other"}},
	})
	if got := reg.Names(); len(got) != 1 || got[0] != "good" {
		t.Fatalf("names = %v, want just the valid one", got)
	}
	if len(errs) != 2 {
		t.Fatalf("errors = %v, want one for the downloader and one for the duplicate", errs)
	}
	if _, err := reg.Lookup("bad"); err == nil {
		t.Error("a rejected provider is still reachable by name")
	}
}

// Picking a provider for the caller would mean guessing which foreign program
// to start.
func TestAnEmptyProviderNameIsNotADefault(t *testing.T) {
	reg, _ := NewRegistry([]Provider{{Name: "only", Command: []string{"server"}}})
	if _, err := reg.Lookup(""); err == nil {
		t.Error("an empty provider name selected the only provider")
	}
}

func TestListsWhatTheProviderOffers(t *testing.T) {
	sess := open(t, fixtureProvider(t), t.TempDir())
	tools, err := sess.Tools(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, tool := range tools {
		if tool.Name == "echo" {
			found = true
			if tool.Schema == nil {
				t.Error("the tool came back without its input schema, so a caller can only guess at arguments")
			}
		}
	}
	if !found {
		t.Fatalf("tools = %v, want the provider's echo tool", tools)
	}
}

func TestForwardsACallAndItsAnswer(t *testing.T) {
	sess := open(t, fixtureProvider(t), t.TempDir())
	res := callText(t, sess, "echo", "hello")
	if !strings.Contains(res.Text, "hello") {
		t.Errorf("text = %q, want the provider's answer", res.Text)
	}
	if res.IsError {
		t.Error("a successful call was reported as an error")
	}
}

// The provider saying no is not the transport failing. Collapsing the two
// would leave a caller unable to tell "fix your arguments" from "this is
// broken".
func TestTheProvidersOwnRefusalIsCarriedAsSuch(t *testing.T) {
	sess := open(t, fixtureProvider(t), t.TempDir())
	res := callText(t, sess, "echo", "boom")
	if !res.IsError {
		t.Fatal("the provider's error flag was lost")
	}
	if !strings.Contains(res.Text, "no") {
		t.Errorf("text = %q, want the provider's own message", res.Text)
	}
}

// Running in the workspace is the only containment available here: most
// servers resolve relative paths against their working directory.
func TestTheProviderStartsInsideTheWorkspace(t *testing.T) {
	dir := t.TempDir()
	// macOS puts temp directories under a symlink (/var -> /private/var), so
	// the child reports the resolved form of the very directory it was given.
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	sess := open(t, fixtureProvider(t), dir)
	res := callText(t, sess, "probe", "")
	if !strings.Contains(res.Text, "cwd="+want) {
		t.Errorf("probe = %q, want the provider started in %q", res.Text, want)
	}
}

// The Companion's environment holds the relay credential and whatever the user
// exported into the shell that launched the desktop app. Handing all of it to
// a foreign process would hand over the credential with it.
func TestTheProviderDoesNotInheritFylanesEnvironment(t *testing.T) {
	t.Setenv(leakedEnv, "a-real-secret")
	t.Setenv("FYLANE_TEST_PASSED", "asked-for")
	sess := open(t, fixtureProvider(t, "FYLANE_TEST_PASSED"), t.TempDir())
	res := callText(t, sess, "probe", "")

	if strings.Contains(res.Text, "a-real-secret") {
		t.Error("the provider received a variable nobody passed through to it")
	}
	if !strings.Contains(res.Text, "passed=asked-for") {
		t.Errorf("probe = %q, want the explicitly passed variable to arrive", res.Text)
	}
	if !strings.Contains(res.Text, "path_set=yes") {
		t.Error("the provider started without PATH, so it can resolve nothing")
	}
}

// A value in the settings file is a value in every backup of that file, so a
// provider may name variables and never carry them.
func TestPassthroughNamesAreNamesNotValues(t *testing.T) {
	p := Provider{Name: "p", Command: []string{"server"}, EnvPassthrough: []string{"TOKEN=secret"}}
	if err := p.Validate(); err == nil {
		t.Error("a provider carried a value in its passthrough list")
	}
}

// A missing program is a runtime condition, not a broken configuration: the
// user may install the server after the Companion started.
func TestAMissingProgramIsAvailabilityNotValidity(t *testing.T) {
	p := Provider{Name: "p", Command: []string{"fylane-no-such-program-anywhere"}}
	if err := p.Validate(); err != nil {
		t.Errorf("Validate rejected a well-formed provider: %v", err)
	}
	err := p.Available()
	if err == nil {
		t.Fatal("Available accepted a program that is not installed")
	}
	if strings.Contains(err.Error(), string(os.PathSeparator)) {
		t.Errorf("error %q carries a path; absolute paths never leave this machine", err)
	}
}

// An absolute program path in the config must not come back out in an error
// that is written to answer a remote caller.
func TestAnUnavailableProviderNamesNoPath(t *testing.T) {
	p := Provider{Name: "p", Command: []string{"/opt/definitely/not/here/server"}}
	err := p.Available()
	if err == nil {
		t.Fatal("Available accepted a program that is not installed")
	}
	if strings.Contains(err.Error(), "/opt") {
		t.Errorf("error %q leaks the machine's layout", err)
	}
}

func TestAnswersAreCappedAndSaySo(t *testing.T) {
	long := strings.Repeat("x", 100)
	text, truncated := renderContent([]mcp.Content{&mcp.TextContent{Text: long}}, 10)
	if !truncated {
		t.Error("an answer was cut without the caller being told")
	}
	if len(text) != 10 {
		t.Errorf("text = %d bytes, want the cap of 10", len(text))
	}
}

// Content this gateway cannot forward is named rather than dropped: a caller
// reading a partial answer has to know part of it is missing.
func TestUnforwardableContentIsNamedNotDropped(t *testing.T) {
	text, _ := renderContent([]mcp.Content{
		&mcp.TextContent{Text: "before"},
		&mcp.ImageContent{MIMEType: "image/png"},
	}, 1000)
	if !strings.Contains(text, "before") {
		t.Errorf("text = %q, lost the part it could forward", text)
	}
	if !strings.Contains(text, "omitted") {
		t.Errorf("text = %q, dropped content without saying so", text)
	}
}

func TestTimeoutIsBoundedByTheDefault(t *testing.T) {
	if got := Timeout(0); got != DefaultTimeout {
		t.Errorf("Timeout(0) = %v, want the default", got)
	}
	if got := Timeout(DefaultTimeout * 10); got != DefaultTimeout {
		t.Errorf("a caller raised the budget past the default: %v", got)
	}
	if got := Timeout(DefaultTimeout / 2); got != DefaultTimeout/2 {
		t.Errorf("a caller asking for less did not get less: %v", got)
	}
}
