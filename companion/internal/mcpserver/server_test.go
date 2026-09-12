package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/leazoot/fylane/companion/internal/store"
	"github.com/leazoot/fylane/companion/internal/txn"
	"github.com/leazoot/fylane/companion/internal/workspace"
	"github.com/leazoot/fylane/shared/tunnel"
)

// testSource builds a database-backed workspace manager serving root as the
// current workspace.
func testSource(t *testing.T, root string) (*workspace.Manager, *store.Store) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "fylane.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	m, err := workspace.NewManager(st, dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	rec, err := m.Add(ctx, root)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := m.SetCurrent(ctx, rec.ID); err != nil {
		t.Fatalf("SetCurrent: %v", err)
	}
	return m, st
}

// autoApprover approves everything instantly — test wiring only; production
// always uses approval.Service.
type autoApprover struct{}

func (autoApprover) Approve(context.Context, *txn.ApprovalRequest) (txn.Decision, error) {
	return txn.Decision{Approved: true}, nil
}

// testDeps builds database-backed dependencies serving root as the current
// workspace, with writes auto-approved and no read approver (sensitive reads
// fail closed).
func testDeps(t *testing.T, root string) Deps {
	t.Helper()
	m, st := testSource(t, root)
	engine := &txn.Engine{Store: st, BackupRoot: filepath.Join(t.TempDir(), "backups"), Approver: autoApprover{}}
	return Deps{Source: m, Engine: engine}
}

// startSession spins up a Streamable HTTP server over a temp workspace and
// returns a connected client session.
func startSession(t *testing.T) (*mcp.ClientSession, string) {
	t.Helper()
	root := t.TempDir()
	httpServer := httptest.NewServer(Handler(testDeps(t, root), nil))
	t.Cleanup(httpServer.Close)

	client := mcp.NewClient(&mcp.Implementation{Name: "fylane-test-client", Version: "0.0.1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: httpServer.URL}, nil)
	if err != nil {
		t.Fatalf("client.Connect (handshake): %v", err)
	}
	t.Cleanup(func() { session.Close() })
	return session, root
}

func TestProviderPicker(t *testing.T) {
	pick := providerPicker(testDeps(t, t.TempDir()), nil)
	req := func(header string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		if header != "" {
			r.Header.Set(tunnel.ProviderHeader, header)
		}
		return r
	}

	claude := pick(req("claude"))
	if claude == nil || claude != pick(req("Claude")) {
		t.Fatal("same provider must reuse one cached server (case-insensitive)")
	}
	if claude == pick(req("chatgpt")) {
		t.Fatal("different providers must get distinct servers")
	}
	// Absent, unknown, and hostile header values all share the generic
	// budget server — the header can select a budget, nothing else.
	fallback := pick(req(""))
	if fallback == claude || fallback != pick(req("evil-platform")) || fallback != pick(req("unknown")) {
		t.Fatal("unrecognized providers must share the generic server")
	}
}

func TestProviderPickerReportsWhoCalled(t *testing.T) {
	// The lane's "connected" list is written from here, and this is the one
	// place both modes pass through: the relay stamps the header on the way
	// in, direct mode stamps it in its own handler. Without this the list
	// reads "not connected" for a platform that is calling right now.
	var seen []string
	deps := testDeps(t, t.TempDir())
	deps.Seen = func(p string) { seen = append(seen, p) }
	pick := providerPicker(deps, nil)

	req := func(header string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		if header != "" {
			r.Header.Set(tunnel.ProviderHeader, header)
		}
		return r
	}

	pick(req("claude"))
	pick(req("ChatGPT"))
	if len(seen) != 2 || seen[0] != "claude" || seen[1] != "chatgpt" {
		t.Fatalf("reported %v; want the normalized provider for each call", seen)
	}

	// A caller the product does not know is not a source. Recording it would
	// put a row on a screen that only ever lists the three it supports.
	before := len(seen)
	pick(req(""))
	pick(req("evil-platform"))
	if len(seen) != before {
		t.Fatalf("reported %v; an unknown caller is not a connected source", seen[before:])
	}
}

func callTool(t *testing.T, s *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	return res
}

func structured(t *testing.T, res *mcp.CallToolResult, out any) {
	t.Helper()
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("unmarshal structured content: %v", err)
	}
}

func TestHandshakeAndToolList(t *testing.T) {
	session, _ := startSession(t)

	var names []string
	for tool, err := range session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatalf("listing tools: %v", err)
		}
		names = append(names, tool.Name)
	}
	want := map[string]bool{
		"workspace_info": true, "stat_path": true,
		"list_directory": true, "search_files": true, "read_file": true,
		"read_files": true, "write_file": true, "apply_patch": true,
		"edit_file": true, "change_manage": true,
	}
	if len(names) != len(want) {
		t.Fatalf("got tools %v, want exactly %v", names, want)
	}
	for _, n := range names {
		if !want[n] {
			t.Fatalf("unexpected tool %q in %v", n, names)
		}
	}
}

func TestWorkspaceInfo(t *testing.T) {
	session, _ := startSession(t)

	var info workspaceInfoOutput
	structured(t, callTool(t, session, "workspace_info", map[string]any{}), &info)
	if !strings.HasPrefix(info.WorkspaceID, "ws_") {
		t.Errorf("workspace_id = %q, want ws_ prefix", info.WorkspaceID)
	}
	if info.MaxReadBytes != maxReadBytes || !info.Writable {
		t.Errorf("unexpected limits: %+v", info)
	}

	// The returned ID must be accepted; a foreign ID must be rejected.
	res := callTool(t, session, "workspace_info", map[string]any{"workspace_id": info.WorkspaceID})
	if res.IsError {
		t.Errorf("valid workspace_id rejected: %v", res.Content)
	}
	res = callTool(t, session, "workspace_info", map[string]any{"workspace_id": "ws_deadbeef0000"})
	if !res.IsError {
		t.Error("foreign workspace_id accepted")
	}
}

func TestWriteReadRoundTrip(t *testing.T) {
	session, root := startSession(t)
	content := "line one\nline two\n第三行\n"

	var wr changeOutput
	structured(t, callTool(t, session, "write_file", map[string]any{
		"path": "notes/hello.txt", "content": content,
	}), &wr)
	if wr.Status != "applied" || len(wr.Operations) != 1 || wr.Operations[0].Status != "created" {
		t.Fatalf("write result = %+v", wr)
	}
	wantSum := sha256.Sum256([]byte(content))
	wantSHA := hex.EncodeToString(wantSum[:])
	if wr.Operations[0].SHA256 != wantSHA {
		t.Errorf("write sha256 = %q, want %q", wr.Operations[0].SHA256, wantSHA)
	}
	if wr.ChangeSetID == "" || wr.RollbackAvailableUntil == "" {
		t.Errorf("missing change set metadata: %+v", wr)
	}

	onDisk, err := os.ReadFile(filepath.Join(root, "notes", "hello.txt"))
	if err != nil || string(onDisk) != content {
		t.Fatalf("on-disk content = %q, %v; want %q", onDisk, err, content)
	}

	var rd readFileOutput
	structured(t, callTool(t, session, "read_file", map[string]any{"path": "notes/hello.txt"}), &rd)
	if rd.Content != content || rd.SHA256 != wantSHA || rd.Truncated || rd.TotalLines != 3 || rd.Encoding != "utf-8" {
		t.Errorf("read output mismatch: %+v", rd)
	}

	// Line-range read.
	structured(t, callTool(t, session, "read_file", map[string]any{
		"path": "notes/hello.txt", "start_line": 2, "end_line": 2,
	}), &rd)
	if rd.Content != "line two\n" {
		t.Errorf("line range content = %q, want %q", rd.Content, "line two\n")
	}
	if rd.SHA256 != wantSHA {
		t.Errorf("line-range read must still hash the full file")
	}
}

func TestWriteConflicts(t *testing.T) {
	session, _ := startSession(t)

	var wr changeOutput
	structured(t, callTool(t, session, "write_file", map[string]any{
		"path": "a.txt", "content": "v1",
	}), &wr)
	if wr.Status != "applied" {
		t.Fatalf("initial write = %+v", wr)
	}
	v1SHA := wr.Operations[0].SHA256

	// Overwrite without expected hash → create conflict, file untouched.
	var conflict changeOutput
	structured(t, callTool(t, session, "write_file", map[string]any{
		"path": "a.txt", "content": "v2",
	}), &conflict)
	if conflict.Status != "conflict" || conflict.Conflict == nil || conflict.Conflict.Reason != "already_exists" {
		t.Fatalf("unexpected conflict payload: %+v", conflict)
	}

	// Stale hash → base_hash_mismatch with the real current hash.
	stale := sha256.Sum256([]byte("something else"))
	structured(t, callTool(t, session, "write_file", map[string]any{
		"path": "a.txt", "content": "v2", "expected_sha256": hex.EncodeToString(stale[:]),
	}), &conflict)
	if conflict.Status != "conflict" || conflict.Conflict.Reason != "base_hash_mismatch" || conflict.Conflict.CurrentSHA256 != v1SHA {
		t.Fatalf("unexpected conflict payload: %+v", conflict)
	}

	// Correct hash → applied update.
	structured(t, callTool(t, session, "write_file", map[string]any{
		"path": "a.txt", "content": "v2", "expected_sha256": v1SHA,
	}), &conflict)
	if conflict.Status != "applied" || conflict.Operations[0].Status != "updated" {
		t.Fatalf("write with correct hash: %+v", conflict)
	}

	var rd readFileOutput
	structured(t, callTool(t, session, "read_file", map[string]any{"path": "a.txt"}), &rd)
	if rd.Content != "v2" {
		t.Errorf("final content = %q, want v2", rd.Content)
	}
}

func TestSandboxRejectsHostilePaths(t *testing.T) {
	session, _ := startSession(t)

	for _, p := range []string{"../outside.txt", "/etc/passwd", "..\\outside.txt", "C:\\Windows\\x", "a/../../b"} {
		res := callTool(t, session, "read_file", map[string]any{"path": p})
		if !res.IsError {
			t.Errorf("read_file(%q): expected error result", p)
		}
		res = callTool(t, session, "write_file", map[string]any{"path": p, "content": "x"})
		if !res.IsError {
			t.Errorf("write_file(%q): expected error result", p)
		}
	}
}

// maxFileBytes is a published number: the security notes name the
// 64 MiB read ceiling. The test above proves the guard fires; it spends the
// constant, so it would keep passing if the ceiling were raised.
func TestThePublishedReadLimitIsTheOneWeShip(t *testing.T) {
	if maxFileBytes != 64<<20 {
		t.Errorf("maxFileBytes = %d, published as a 64 MiB read ceiling", maxFileBytes)
	}
}

func TestReadFileErrors(t *testing.T) {
	session, root := startSession(t)

	res := callTool(t, session, "read_file", map[string]any{"path": "missing.txt"})
	if !res.IsError {
		t.Error("reading a missing file must return an error result")
	}

	// Undecodable bytes are binary: metadata comes back, raw content never.
	if err := os.WriteFile(filepath.Join(root, "bin.dat"), []byte{0x89, 0x50, 0x00, 0x01}, 0o644); err != nil {
		t.Fatal(err)
	}
	res = callTool(t, session, "read_file", map[string]any{"path": "bin.dat"})
	if res.IsError {
		t.Fatalf("binary read must return metadata, got error: %v", res.Content)
	}
	var rd readFileOutput
	structured(t, res, &rd)
	if rd.Encoding != "binary" || rd.Content != "" {
		t.Errorf("binary read = %+v", rd)
	}

	// A file over the hard read cap is refused with a clear error instead of
	// being buffered into memory. Sparse-sized via Truncate to keep the test
	// cheap; the guard checks Stat, not content.
	big, err := os.Create(filepath.Join(root, "big.log"))
	if err != nil {
		t.Fatal(err)
	}
	if err := big.Truncate(maxFileBytes + 1); err != nil {
		t.Fatal(err)
	}
	big.Close()
	res = callTool(t, session, "read_file", map[string]any{"path": "big.log"})
	if !res.IsError {
		t.Fatal("read over the hard cap must return an error result")
	}
	if text := resultText(res); !strings.Contains(text, "read limit") {
		t.Errorf("oversize read error = %q", text)
	}
}

// One inline figure could not be right for three platforms whose measured
// ceilings span 128×. What is asserted here is the direction of every
// entry rather than the exact numbers, which are expected to move as the
// platforms do: each measured platform stays under the generic figure that
// Grok and ChatGPT could not carry, Claude gets more than the two that
// measured smaller, and nothing sits at or above the ceiling it was derived
// from.
func TestEachPlatformGetsAnInlineBudgetItCanCarry(t *testing.T) {
	budgetFor := func(provider string) int {
		return (&toolset{provider: provider}).budget()
	}

	// The measured response ceilings, and the halving that turns a wire size
	// into a file size. A budget at or above these is a call that fails
	// rather than a call that pages.
	measured := map[string]int{"claude": 8 << 20, "chatgpt": 256 << 10, "grok": 64 << 10}
	for p, ceiling := range measured {
		got := budgetFor(p)
		if got <= 0 {
			t.Errorf("%s has no inline budget", p)
			continue
		}
		if got*2 >= ceiling {
			t.Errorf("%s budget %d doubles to %d on the wire, at or over the measured %d",
				p, got, got*2, ceiling)
		}
	}
	if budgetFor("grok") >= budgetFor("chatgpt") || budgetFor("chatgpt") >= budgetFor("claude") {
		t.Errorf("budgets are not ordered as the platforms measured: grok=%d chatgpt=%d claude=%d",
			budgetFor("grok"), budgetFor("chatgpt"), budgetFor("claude"))
	}

	// An unrecognised caller keeps the old global figure. It is not
	// necessarily a small platform — in direct mode it is usually a local
	// client with no limit at all.
	if got := budgetFor("unknown"); got != maxReadBytes {
		t.Errorf("unknown budget = %d, want the generic %d", got, maxReadBytes)
	}
	if budgetFor("chatgpt") >= maxReadBytes || budgetFor("grok") >= maxReadBytes {
		t.Error("a platform that measured smaller than the generic figure did not get a smaller budget")
	}
	// And the entry has to earn its place in the other direction too: Claude
	// measured well above the generic figure, so an entry that does not beat
	// it is a row that changes nothing.
	if budgetFor("claude") <= maxReadBytes {
		t.Errorf("claude budget %d is no better than the generic %d, so recognising it buys nothing",
			budgetFor("claude"), maxReadBytes)
	}

	// An explicit -max-inline-bytes wins everywhere. It is the wrong shape
	// for this problem, but a setting that silently does not apply is worse.
	set := &toolset{provider: "grok", inlineBudget: 7777}
	if got := set.budget(); got != 7777 {
		t.Errorf("explicit budget = %d, want 7777", got)
	}
}

func TestWorkspaceInfoListsWorkspacesOnOtherMachines(t *testing.T) {
	root := t.TempDir()
	deps := testDeps(t, root)
	deps.Remotes = func(context.Context) []RemoteWorkspace {
		return []RemoteWorkspace{{WorkspaceID: "ws_remote1", Name: "api", Mode: "read_write", Status: "active", Machine: "vps"}}
	}
	httpServer := httptest.NewServer(Handler(deps, nil))
	t.Cleanup(httpServer.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "fylane-test-client", Version: "0.0.1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: httpServer.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })

	var out workspaceInfoOutput
	structured(t, callTool(t, session, "workspace_info", map[string]any{}), &out)
	var remote *workspaceEntry
	for i := range out.Workspaces {
		if out.Workspaces[i].WorkspaceID == "ws_remote1" {
			remote = &out.Workspaces[i]
		}
	}
	if remote == nil || remote.Machine != "vps" || remote.Current {
		t.Fatalf("workspaces = %+v", out.Workspaces)
	}
	if out.Workspaces[0].Machine != "" {
		t.Errorf("the local workspace must not carry a machine name: %+v", out.Workspaces[0])
	}
}

func TestWorkspaceInfoFollowsTheMachineTheWindowStandsOn(t *testing.T) {
	root := t.TempDir()
	deps := testDeps(t, root)
	deps.Remotes = func(context.Context) []RemoteWorkspace {
		return []RemoteWorkspace{
			{WorkspaceID: "ws_remote1", Name: "api", Mode: "read_write", Status: "active", Machine: "vps"},
			{WorkspaceID: "ws_remote2", Name: "site", Mode: "read_only", Status: "active", Machine: "vps", Current: true},
		}
	}
	httpServer := httptest.NewServer(Handler(deps, nil))
	t.Cleanup(httpServer.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "fylane-test-client", Version: "0.0.1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: httpServer.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })

	var out workspaceInfoOutput
	structured(t, callTool(t, session, "workspace_info", map[string]any{}), &out)
	if out.WorkspaceID != "ws_remote2" || out.Name != "site" || out.Writable || out.Mode != "read_only" {
		t.Fatalf("the answer must be the standing machine's current folder: %+v", out)
	}
	var current []string
	for _, w := range out.Workspaces {
		if w.Current {
			current = append(current, w.WorkspaceID)
		}
	}
	if len(current) != 1 || current[0] != "ws_remote2" {
		t.Errorf("exactly the remote current folder is current, got %v in %+v", current, out.Workspaces)
	}
	if out.Workspaces[0].Machine != "" {
		t.Errorf("the local folder is still listed, first: %+v", out.Workspaces[0])
	}
	// Asking for the local folder by id still answers with it.
	structured(t, callTool(t, session, "workspace_info", map[string]any{"workspace_id": out.Workspaces[0].WorkspaceID}), &out)
	if out.WorkspaceID != out.Workspaces[0].WorkspaceID {
		t.Errorf("by id = %+v", out)
	}
}
