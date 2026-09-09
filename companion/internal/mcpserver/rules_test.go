package mcpserver

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/leazoot/fylane/companion/internal/routerule"
	"github.com/leazoot/fylane/companion/internal/txn"
)

// staticRules is a RuleSource holding one table.
type staticRules struct{ rules []routerule.Rule }

func (s staticRules) Load() ([]routerule.Rule, error) { return s.rules, nil }

// recordingApprover captures what the engine asked the user, so the tests
// can assert on the request rather than on a side effect.
type recordingApprover struct{ seen []*txn.ApprovalRequest }

func (a *recordingApprover) Approve(_ context.Context, req *txn.ApprovalRequest) (txn.Decision, error) {
	a.seen = append(a.seen, req)
	return txn.Decision{Approved: true}, nil
}

func toolsetWith(t *testing.T, rules []routerule.Rule) (*toolset, *recordingApprover, string) {
	t.Helper()
	root := t.TempDir()
	m, st := testSource(t, root)
	approver := &recordingApprover{}
	engine := &txn.Engine{Store: st, BackupRoot: filepath.Join(t.TempDir(), "backups"), Approver: approver}
	return &toolset{src: m, engine: engine, rules: staticRules{rules}, provider: "claude"}, approver, root
}

// An "ask" rule reaches the MCP path: it relocates nothing, so it can only
// add the confirmation the user asked for.
func TestMCPWriteHonoursAskRules(t *testing.T) {
	tools, approver, _ := toolsetWith(t, []routerule.Rule{
		{ID: "hold", Source: routerule.SourceAny, Patterns: []string{"*.env"}, Action: routerule.ActionAsk},
	})

	if _, err := tools.execute(context.Background(), "", "", "write notes",
		txn.Operation{Type: txn.OpCreate, Path: "notes.md", Content: "hi"}); err != nil {
		t.Fatal(err)
	}
	if len(approver.seen) != 1 || approver.seen[0].MustAsk {
		t.Fatalf("unmatched write asked for confirmation: %+v", approver.seen)
	}

	if _, err := tools.execute(context.Background(), "", "", "write env",
		txn.Operation{Type: txn.OpCreate, Path: "config/.env", Content: "K=V"}); err != nil {
		t.Fatal(err)
	}
	if len(approver.seen) != 2 || !approver.seen[1].MustAsk {
		t.Fatalf("ask rule did not reach the approval request: %+v", approver.seen[1])
	}
}

// A "route" rule must not touch an MCP write. The model named the path and
// will read it back by that name; relocating it silently would break the
// next call and contradict the result just returned.
func TestMCPWriteIsNeverRelocated(t *testing.T) {
	tools, _, root := toolsetWith(t, []routerule.Rule{
		{ID: "r", Source: routerule.SourceAny, Patterns: []string{"*.md"}, Dest: "docs/", Action: routerule.ActionRoute},
	})

	out, err := tools.execute(context.Background(), "", "", "write notes",
		txn.Operation{Type: txn.OpCreate, Path: "src/notes.md", Content: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != txn.StatusApplied {
		t.Fatalf("status = %s (%s)", out.Status, out.Reason)
	}
	if len(out.Operations) != 1 || out.Operations[0].Path != "src/notes.md" {
		t.Fatalf("reported path = %+v, want the path the caller asked for", out.Operations)
	}
	if _, err := os.Stat(filepath.Join(root, "src", "notes.md")); err != nil {
		t.Fatalf("file did not land where the caller asked: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "docs", "notes.md")); !os.IsNotExist(err) {
		t.Fatal("a route rule relocated an MCP write")
	}
}
