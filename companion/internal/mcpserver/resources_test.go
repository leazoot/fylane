package mcpserver

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// startBudgetSession starts a session with a tiny inline budget so
// truncation paths run without megabyte fixtures.
func startBudgetSession(t *testing.T, budget int) (*mcp.ClientSession, string) {
	t.Helper()
	root := t.TempDir()
	m, _ := testSource(t, root)
	httpServer := httptest.NewServer(Handler(Deps{Source: m}, &Options{MaxInlineBytes: budget}))
	t.Cleanup(httpServer.Close)

	client := mcp.NewClient(&mcp.Implementation{Name: "fylane-test-client", Version: "0.0.1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: httpServer.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session, root
}

func readResourceText(t *testing.T, session *mcp.ClientSession, uri string) (string, error) {
	t.Helper()
	res, err := session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uri})
	if err != nil {
		return "", err
	}
	if len(res.Contents) != 1 {
		t.Fatalf("resource contents = %d, want 1", len(res.Contents))
	}
	return res.Contents[0].Text, nil
}

func TestTruncatedReadPointsAtResource(t *testing.T) {
	session, root := startBudgetSession(t, 16)
	writeTree(t, root, map[string]string{"big.txt": "line one\nline two\nline three\n"})

	var rd readFileOutput
	structured(t, callTool(t, session, "read_file", map[string]any{"path": "big.txt"}), &rd)
	if !rd.Truncated {
		t.Fatalf("read over budget not truncated: %+v", rd)
	}
	// 16 bytes cover "line one\n" plus part of the next line; the first
	// incomplete line is line 2.
	if rd.NextStartLine != 2 {
		t.Fatalf("next_start_line = %d, want 2", rd.NextStartLine)
	}
	if rd.ResourceURI == "" || !strings.HasPrefix(rd.ResourceURI, "fylane://") {
		t.Fatalf("resource_uri = %q", rd.ResourceURI)
	}

	// The advertised URI serves the file through resources/read. Resource
	// reads honor the same inline budget, so paging goes line range by
	// line range.
	for want, uri := range map[string]string{
		"line two\n":   rd.ResourceURI + "?start_line=2&end_line=2",
		"line three\n": rd.ResourceURI + "?start_line=3",
	} {
		got, err := readResourceText(t, session, uri)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("paged resource %q = %q, want %q", uri, got, want)
		}
	}
}

func TestReadResourceFullFile(t *testing.T) {
	session, root := startSession(t)
	writeTree(t, root, map[string]string{"a.txt": "hello\n"})

	var rd readFileOutput
	structured(t, callTool(t, session, "read_file", map[string]any{"path": "a.txt"}), &rd)
	if rd.Truncated || rd.ResourceURI != "" {
		t.Fatalf("small read must stay inline: %+v", rd)
	}

	ws, err := testCurrentWorkspaceID(session)
	if err != nil {
		t.Fatal(err)
	}
	got, err := readResourceText(t, session, "fylane://"+ws+"/a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello\n" {
		t.Fatalf("resource text = %q", got)
	}
}

func TestReadResourceEnforcesRules(t *testing.T) {
	session, root := startSession(t)
	writeTree(t, root, map[string]string{
		"a.txt":             "ok\n",
		"node_modules/x.js": "hidden\n",
	})
	ws, err := testCurrentWorkspaceID(session)
	if err != nil {
		t.Fatal(err)
	}

	// Excluded paths behave as nonexistent; traversal is rejected by the
	// sandbox; unknown URIs are not found.
	for _, uri := range []string{
		"fylane://" + ws + "/node_modules/x.js",
		"fylane://" + ws + "/../etc/passwd",
		"fylane://" + ws + "/a.txt?start_line=zero",
		"fylane://" + ws + "/",
		"wrong://" + ws + "/a.txt",
	} {
		if _, err := readResourceText(t, session, uri); err == nil {
			t.Errorf("resource read of %q must fail", uri)
		}
	}
}

func TestReadResourceSensitiveNeedsConfirmation(t *testing.T) {
	session, svc, root := startApprovalSession(t)
	writeTree(t, root, map[string]string{".env": "SECRET=1\n"})
	ws, err := testCurrentWorkspaceID(session)
	if err != nil {
		t.Fatal(err)
	}

	uri := "fylane://" + ws + "/.env"
	if _, err := readResourceText(t, session, uri); err == nil ||
		!strings.Contains(err.Error(), "awaiting local confirmation") {
		t.Fatalf("sensitive resource read = %v, want confirmation error", err)
	}
	// The prompt's key names the caller as well as the file, so take
	// it from the prompt rather than rebuilding it.
	pend := svc.Pending()
	if len(pend) != 1 {
		t.Fatalf("pending prompts = %d, want 1", len(pend))
	}
	if !svc.Resolve(pend[0].Request.ChangeSetID, true, "") {
		t.Fatal("Resolve read failed")
	}
	got, err := readResourceText(t, session, uri)
	if err != nil {
		t.Fatal(err)
	}
	if got != "SECRET=1\n" {
		t.Fatalf("approved sensitive resource = %q", got)
	}
}

func TestWorkspaceInfoReportsBudget(t *testing.T) {
	session, _ := startBudgetSession(t, 4096)
	res := callTool(t, session, "workspace_info", map[string]any{})
	var info struct {
		MaxReadBytes int64 `json:"max_read_bytes"`
	}
	structured(t, res, &info)
	if info.MaxReadBytes != 4096 {
		t.Fatalf("max_read_bytes = %d, want 4096", info.MaxReadBytes)
	}
}
