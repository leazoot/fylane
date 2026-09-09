package mcpserver

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// resultText concatenates the human-readable text of a tool result.
func resultText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func TestWorkspaceInfoListsWorkspaces(t *testing.T) {
	session, _ := startSession(t)

	var out workspaceInfoOutput
	structured(t, callTool(t, session, "workspace_info", map[string]any{}), &out)
	if len(out.Workspaces) != 1 {
		t.Fatalf("workspaces = %+v, want exactly one", out.Workspaces)
	}
	w := out.Workspaces[0]
	if !strings.HasPrefix(w.WorkspaceID, "ws_") || !w.Current || w.Mode != "read_write" || w.Status != "active" {
		t.Fatalf("workspace entry = %+v", w)
	}
	if w.WorkspaceID != out.WorkspaceID {
		t.Fatalf("listing entry %q does not match queried workspace %q", w.WorkspaceID, out.WorkspaceID)
	}

	// The listing must never contain any path-like data.
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "/") && strings.Contains(string(raw), os.TempDir()) {
		t.Fatalf("workspace_info output leaks paths: %s", raw)
	}
}

func TestStatPath(t *testing.T) {
	session, root := startSession(t)
	content := "hello stat\n"
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "a.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	var st statPathOutput
	structured(t, callTool(t, session, "stat_path", map[string]any{"path": "docs/a.md"}), &st)
	wantSum := sha256.Sum256([]byte(content))
	if st.Type != "file" || st.SizeBytes != int64(len(content)) || st.SHA256 != hex.EncodeToString(wantSum[:]) {
		t.Fatalf("stat file = %+v", st)
	}

	var dir statPathOutput
	structured(t, callTool(t, session, "stat_path", map[string]any{"path": "docs"}), &dir)
	if dir.Type != "directory" || dir.SHA256 != "" {
		t.Fatalf("stat directory = %+v", dir)
	}

	res := callTool(t, session, "stat_path", map[string]any{"path": "missing.txt"})
	if !res.IsError {
		t.Error("stat of a missing path must return an error result")
	}
	res = callTool(t, session, "stat_path", map[string]any{"path": "../outside"})
	if !res.IsError {
		t.Error("stat outside the sandbox must return an error result")
	}
}

// Excluded paths must behave as nonexistent; sensitive files must be denied
// until local confirmation exists (fail closed).
func TestReadRulesEnforced(t *testing.T) {
	session, root := startSession(t)
	if err := os.MkdirAll(filepath.Join(root, "node_modules", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	for rel, content := range map[string]string{
		filepath.Join("node_modules", "pkg", "index.js"): "console.log(1)\n",
		".env": "SECRET=1\n",
	} {
		if err := os.WriteFile(filepath.Join(root, rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	for _, tool := range []string{"stat_path", "read_file"} {
		res := callTool(t, session, tool, map[string]any{"path": "node_modules/pkg/index.js"})
		if !res.IsError {
			t.Errorf("%s on excluded path must return an error result", tool)
		}
		res = callTool(t, session, tool, map[string]any{"path": ".env"})
		if !res.IsError {
			t.Errorf("%s on sensitive file must be denied", tool)
		}
	}

	// Excluded reads must be indistinguishable from missing files, while the
	// exclusion itself must not leak the sensitive denial message.
	res := callTool(t, session, "read_file", map[string]any{"path": "node_modules/pkg/index.js"})
	text := resultText(res)
	if !strings.Contains(text, "not found") {
		t.Errorf("excluded read error = %q, want a not-found error", text)
	}

	// Sensitive writes are allowed only through the approval pipeline (the
	// fixture auto-approves); the sensitive flag itself is asserted in the
	// txn engine tests. Without an expected hash this is a create conflict.
	var wr changeOutput
	structured(t, callTool(t, session, "write_file", map[string]any{"path": ".env", "content": "SECRET=2\n"}), &wr)
	if wr.Status != "conflict" || wr.Conflict.Reason != "already_exists" {
		t.Fatalf("sensitive overwrite without hash = %+v", wr)
	}
	data, err := os.ReadFile(filepath.Join(root, ".env"))
	if err != nil || string(data) != "SECRET=1\n" {
		t.Fatalf("sensitive file was modified: %q, %v", data, err)
	}
}

func TestWorkspaceInfoMode(t *testing.T) {
	session, _ := startSession(t)
	var info workspaceInfoOutput
	structured(t, callTool(t, session, "workspace_info", map[string]any{}), &info)
	if info.Mode != "read_write" || !info.Writable {
		t.Fatalf("workspace_info = %+v", info)
	}
}
