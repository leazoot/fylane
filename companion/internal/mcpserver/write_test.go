package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/leazoot/fylane/companion/internal/approval"
	"github.com/leazoot/fylane/companion/internal/txn"
)

func shaOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestApplyPatch(t *testing.T) {
	session, root := startSession(t)
	old := "line one\nline two\nline three\n"
	writeTree(t, root, map[string]string{"a.txt": old})

	patch := "--- a/a.txt\n+++ b/a.txt\n@@ -1,3 +1,3 @@\n line one\n-line two\n+line 2\n line three\n"
	var out changeOutput
	structured(t, callTool(t, session, "apply_patch", map[string]any{
		"path": "a.txt", "patch": patch, "expected_sha256": shaOf(old),
	}), &out)
	if out.Status != "applied" || out.Operations[0].Status != "updated" {
		t.Fatalf("apply_patch = %+v", out)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(data) != "line one\nline 2\nline three\n" {
		t.Fatalf("patched content = %q", data)
	}

	// Stale base hash → conflict before anything is patched.
	structured(t, callTool(t, session, "apply_patch", map[string]any{
		"path": "a.txt", "patch": patch, "expected_sha256": shaOf(old),
	}), &out)
	if out.Status != "conflict" || out.Conflict.Reason != "base_hash_mismatch" {
		t.Fatalf("stale patch = %+v", out)
	}

	// A patch that does not apply is a clear error.
	current := "line one\nline 2\nline three\n"
	res := callTool(t, session, "apply_patch", map[string]any{
		"path": "a.txt", "patch": "--- a/a.txt\n+++ b/a.txt\n@@ -1,1 +1,1 @@\n-nonexistent\n+x\n",
		"expected_sha256": shaOf(current),
	})
	if !res.IsError {
		t.Fatal("unappliable patch must return an error")
	}
}

func TestMoveAndDeleteTools(t *testing.T) {
	session, root := startSession(t)
	writeTree(t, root, map[string]string{
		"docs/old.md":  "content\n",
		"junk/a.txt":   "a\n",
		"junk/b/c.txt": "c\n",
	})

	var out changeOutput
	structured(t, callTool(t, session, "change_manage", map[string]any{
		"action": "move", "from": "docs/old.md", "to": "docs/new.md", "expected_sha256": shaOf("content\n"),
	}), &out)
	if out.Status != "applied" || out.Operations[0].Status != "moved" || out.Operations[0].Path != "docs/new.md" {
		t.Fatalf("move = %+v", out)
	}

	// Deleting a non-empty directory without recursive is an error.
	res := callTool(t, session, "change_manage", map[string]any{"action": "delete", "path": "junk"})
	if !res.IsError {
		t.Fatal("non-recursive delete of non-empty directory must fail")
	}
	structured(t, callTool(t, session, "change_manage", map[string]any{"action": "delete", "path": "junk", "recursive": true}), &out)
	if out.Status != "applied" || out.Operations[0].Status != "deleted" {
		t.Fatalf("delete = %+v", out)
	}
	if _, err := os.Stat(filepath.Join(root, "junk")); !os.IsNotExist(err) {
		t.Fatal("directory still exists")
	}

	// Per-action validation and unknown actions are clear errors.
	if res := callTool(t, session, "change_manage", map[string]any{"action": "move", "from": "docs/new.md"}); !res.IsError {
		t.Error("move without to must fail")
	}
	if res := callTool(t, session, "change_manage", map[string]any{"action": "delete"}); !res.IsError {
		t.Error("delete without path must fail")
	}
	if res := callTool(t, session, "change_manage", map[string]any{"action": "shred", "path": "docs/new.md"}); !res.IsError {
		t.Error("unknown action must fail")
	}
}

func TestApplyChangeSetAtomic(t *testing.T) {
	session, root := startSession(t)
	writeTree(t, root, map[string]string{"src/a.ts": "old a\n"})

	var out changeOutput
	structured(t, callTool(t, session, "change_manage", map[string]any{
		"action":  "apply_change_set",
		"summary": "Refactor",
		"operations": []map[string]any{
			{"type": "create", "path": "src/b.ts", "content": "new b\n"},
			{"type": "update", "path": "src/a.ts", "content": "new a\n", "expected_sha256": shaOf("old a\n")},
		},
	}), &out)
	if out.Status != "applied" || len(out.Operations) != 2 {
		t.Fatalf("apply_change_set = %+v", out)
	}

	// One conflicting operation rejects the whole set with no partial apply.
	structured(t, callTool(t, session, "change_manage", map[string]any{
		"action":  "apply_change_set",
		"summary": "Bad batch",
		"operations": []map[string]any{
			{"type": "create", "path": "src/c.ts", "content": "c\n"},
			{"type": "update", "path": "src/a.ts", "content": "x\n", "expected_sha256": shaOf("stale")},
		},
	}), &out)
	if out.Status != "conflict" {
		t.Fatalf("conflicting change set = %+v", out)
	}
	if _, err := os.Stat(filepath.Join(root, "src/c.ts")); !os.IsNotExist(err) {
		t.Fatal("conflicting change set partially applied")
	}
}

func TestRollbackChangeSetTool(t *testing.T) {
	session, root := startSession(t)
	writeTree(t, root, map[string]string{"a.txt": "v1\n"})

	var wr changeOutput
	structured(t, callTool(t, session, "write_file", map[string]any{
		"path": "a.txt", "content": "v2\n", "expected_sha256": shaOf("v1\n"),
	}), &wr)
	if wr.Status != "applied" {
		t.Fatalf("write = %+v", wr)
	}

	var rb changeOutput
	structured(t, callTool(t, session, "change_manage", map[string]any{
		"action": "rollback", "change_set_id": wr.ChangeSetID,
	}), &rb)
	if rb.Status != "rolled_back" {
		t.Fatalf("rollback = %+v", rb)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(data) != "v1\n" {
		t.Fatalf("content after rollback = %q", data)
	}

	if res := callTool(t, session, "change_manage", map[string]any{"action": "rollback"}); !res.IsError {
		t.Error("rollback without change_set_id must fail")
	}
	if res := callTool(t, session, "change_manage", map[string]any{"action": "rollback", "change_set_id": "chg_nope"}); !res.IsError {
		t.Error("rollback of unknown change set must fail")
	}
}

// startApprovalSession wires a real approval.Service with a tiny budget so
// the pending_approval degradation is exercised end to end over MCP.
func startApprovalSession(t *testing.T) (*mcp.ClientSession, *approval.Service, string) {
	t.Helper()
	root := t.TempDir()
	m, st := testSource(t, root)
	svc, err := approval.New(approval.ModeSafe,
		approval.Budgets{Default: 50 * time.Millisecond}, nil)
	if err != nil {
		t.Fatal(err)
	}
	engine := &txn.Engine{Store: st, BackupRoot: filepath.Join(t.TempDir(), "backups"), Approver: svc}
	httpServer := httptest.NewServer(Handler(Deps{Source: m, Engine: engine, Reads: svc}, nil))
	t.Cleanup(httpServer.Close)

	client := mcp.NewClient(&mcp.Implementation{Name: "fylane-test-client", Version: "0.0.1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: httpServer.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session, svc, root
}

func TestPendingApprovalRetryOverMCP(t *testing.T) {
	session, svc, root := startApprovalSession(t)

	var out changeOutput
	structured(t, callTool(t, session, "write_file", map[string]any{
		"path": "a.txt", "content": "v1\n",
	}), &out)
	if out.Status != "pending_approval" || out.ChangeSetID == "" {
		t.Fatalf("first call = %+v", out)
	}
	if _, err := os.Stat(filepath.Join(root, "a.txt")); !os.IsNotExist(err) {
		t.Fatal("pending write touched the disk")
	}

	if !svc.Resolve(out.ChangeSetID, true, "") {
		t.Fatal("Resolve failed")
	}
	var retry changeOutput
	structured(t, callTool(t, session, "write_file", map[string]any{
		"path": "a.txt", "content": "v1\n", "change_set_id": out.ChangeSetID,
	}), &retry)
	if retry.Status != "applied" {
		t.Fatalf("retry after approval = %+v", retry)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(data) != "v1\n" {
		t.Fatalf("content = %q", data)
	}
}

func TestSensitiveReadConfirmationOverMCP(t *testing.T) {
	session, svc, root := startApprovalSession(t)
	writeTree(t, root, map[string]string{".env": "SECRET=1\n"})

	// First read: pending confirmation.
	res := callTool(t, session, "read_file", map[string]any{"path": ".env"})
	if !res.IsError || !strings.Contains(resultText(res), "awaiting local confirmation") {
		t.Fatalf("first sensitive read = %v", resultText(res))
	}

	// User approves; the retry returns content. Resolve by the key the prompt
	// actually carries rather than rebuilding it here: the key names the
	// caller as well as the file, and a test that reconstructs it is
	// testing its own copy of the rule.
	pend := svc.Pending()
	if len(pend) != 1 {
		t.Fatalf("pending prompts = %d, want 1", len(pend))
	}
	if !svc.Resolve(pend[0].Request.ChangeSetID, true, "") {
		t.Fatal("Resolve read failed")
	}
	var rd readFileOutput
	structured(t, callTool(t, session, "read_file", map[string]any{"path": ".env"}), &rd)
	if rd.Content != "SECRET=1\n" {
		t.Fatalf("approved sensitive read = %+v", rd)
	}
}

// Everything the sensitive rules protect is protected in order to keep the
// file's bytes off the wire, so the assertion belongs on the wire: the JSON
// this tool hands back is what reaches the platform. A rollback conflict
// used to answer with the file's pre-image, and a rollback is reachable
// after any approved write to the path — the user answered a question about
// a write, not about a disclosure.
func TestARollbackConflictDoesNotPutASensitiveFileOnTheWire(t *testing.T) {
	const secret = "AWS_SECRET_ACCESS_KEY=live-key-9d1f\n"
	session, root := startSession(t)
	writeTree(t, root, map[string]string{".env": secret, "notes.md": "before\n"})

	payload := func(t *testing.T, changeSetID string) string {
		t.Helper()
		res := callTool(t, session, "change_manage", map[string]any{
			"action": "rollback", "change_set_id": changeSetID,
		})
		raw, err := json.Marshal(res.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}

	var env changeOutput
	structured(t, callTool(t, session, "write_file", map[string]any{
		"path": ".env", "content": "AWS_SECRET_ACCESS_KEY=rotated\n", "expected_sha256": shaOf(secret),
	}), &env)
	if env.Status != "applied" {
		t.Fatalf("write to .env = %+v", env)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("AWS_SECRET_ACCESS_KEY=user-edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := payload(t, env.ChangeSetID); strings.Contains(got, "live-key-9d1f") {
		t.Errorf("the rollback response carried the contents of .env: %s", got)
	}

	// Paired with the ordinary case: a rule that empties every conflict
	// would pass the assertion above and break the conflict-diff contract for every file.
	var plain changeOutput
	structured(t, callTool(t, session, "write_file", map[string]any{
		"path": "notes.md", "content": "after\n", "expected_sha256": shaOf("before\n"),
	}), &plain)
	if err := os.WriteFile(filepath.Join(root, "notes.md"), []byte("user edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := payload(t, plain.ChangeSetID); !strings.Contains(got, "before") {
		t.Errorf("an ordinary file lost the conflict diff the contract requires: %s", got)
	}
}

func testCurrentWorkspaceID(session *mcp.ClientSession) (string, error) {
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "workspace_info", Arguments: map[string]any{},
	})
	if err != nil {
		return "", err
	}
	m, ok := res.StructuredContent.(map[string]any)
	if !ok {
		return "", os.ErrInvalid
	}
	id, _ := m["workspace_id"].(string)
	return id, nil
}

func TestAFailedResultNamesWhatWasLeftBehind(t *testing.T) {
	// The sentence a model reads. "Re-read the affected files" was the only
	// thing this could say while the result was the only evidence; now the
	// engine has reconciled each path, so the sentence has to distinguish
	// the paths that need re-reading from the ones that provably do not.
	for _, tc := range []struct {
		name    string
		effects []txn.OpEffect
		want    []string
		absent  []string
	}{
		{"nothing landed", []txn.OpEffect{
			{Path: "a.txt", Effect: txn.NotStarted},
			{Path: "b.txt", Effect: txn.NotStarted},
		}, []string{"nothing was left behind"}, []string{"a.txt", "re-read"}},

		{"something stuck", []txn.OpEffect{
			{Path: "a.txt", Effect: txn.NotStarted},
			{Path: "b.txt", Effect: txn.StateChanged},
		}, []string{"left changed", "b.txt"}, []string{"a.txt", "re-read"}},

		{"something is undecidable", []txn.OpEffect{
			{Path: "a.txt", Effect: txn.NotStarted},
			{Path: "b.txt", Effect: txn.OutcomeUnknown},
		}, []string{"re-read", "b.txt"}, []string{"a.txt"}},

		{"both", []txn.OpEffect{
			{Path: "b.txt", Effect: txn.StateChanged},
			{Path: "c.txt", Effect: txn.OutcomeUnknown},
		}, []string{"left changed", "b.txt", "re-read", "c.txt"}, nil},

		// The fallback. An effect the engine never decided must not be
		// dressed up as one it did.
		{"an undecided effect", []txn.OpEffect{
			{Path: "a.txt", Effect: txn.NotStarted},
			{Path: "b.txt"},
		}, []string{"re-read the affected files"}, []string{"nothing was left behind"}},

		{"no effects at all", nil, []string{"re-read the affected files"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := failedAction(tc.effects)
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("action %q does not mention %q", got, want)
				}
			}
			for _, no := range tc.absent {
				if strings.Contains(got, no) {
					t.Errorf("action %q mentions %q, which it must not", got, no)
				}
			}
		})
	}
}

func TestTheEffectsReachTheToolResult(t *testing.T) {
	// A field the engine fills and the mapper drops is the exact shape of
	// the protocol carries it, the surface never shows it.
	out := toChangeOutput(&txn.Result{
		Status: txn.StatusFailed, ChangeSetID: "chg_1", Reason: "disk on fire",
		Effects: []txn.OpEffect{{Path: "a.txt", Effect: txn.StateChanged}},
	})
	if len(out.Effects) != 1 || out.Effects[0].Effect != txn.StateChanged {
		t.Fatalf("effects = %+v, want the engine's own answer", out.Effects)
	}
	blob, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshalling the tool result: %v", err)
	}
	for _, want := range []string{`"effects"`, `"state_changed"`, `"a.txt"`} {
		if !strings.Contains(string(blob), want) {
			t.Errorf("the wire form is missing %s: %s", want, blob)
		}
	}
}
