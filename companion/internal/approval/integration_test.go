package approval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/leazoot/fylane/companion/internal/store"
	"github.com/leazoot/fylane/companion/internal/txn"
	"github.com/leazoot/fylane/companion/internal/workspace"
)

// TestEnginePendingRetryFlow exercises the full degradation loop the Stage 1
// timeout findings dictate: block → budget expiry → pending_approval → user
// decides → platform retry with the same change_set_id applies (or denies).
func TestEnginePendingRetryFlow(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	root := t.TempDir()

	st, err := store.Open(ctx, filepath.Join(dataDir, "fylane.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	m, err := workspace.NewManager(st, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	wsRec, err := m.Add(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := m.Open(ctx, wsRec.ID)
	if err != nil {
		t.Fatal(err)
	}

	svc, err := New(ModeSafe, testBudgets(30*time.Millisecond), nil)
	if err != nil {
		t.Fatal(err)
	}
	engine := &txn.Engine{Store: st, BackupRoot: filepath.Join(dataDir, "backups"), Approver: svc}

	target := filepath.Join(root, "a.txt")
	if err := os.WriteFile(target, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("v1\n"))
	req := txn.Request{
		ChangeSetID: store.NewID("chg"),
		Provider:    "grok", Summary: "update a.txt",
		Operations: []txn.Operation{{
			Type: txn.OpUpdate, Path: "a.txt", Content: "v2\n",
			ExpectedSHA256: hex.EncodeToString(sum[:]),
		}},
	}

	// First call: budget expires → pending_approval, nothing on disk.
	res, err := engine.Execute(ctx, ws, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != txn.StatusPending {
		t.Fatalf("first result = %+v", res)
	}
	if data, _ := os.ReadFile(target); string(data) != "v1\n" {
		t.Fatalf("pending approval touched the file: %q", data)
	}
	rec, err := st.GetChangeSet(ctx, req.ChangeSetID)
	if err != nil || rec.Status != store.ChangeSetPending {
		t.Fatalf("journal = %+v, %v", rec, err)
	}

	// Retry before any decision: still pending, still one prompt.
	res, err = engine.Execute(ctx, ws, req)
	if err != nil || res.Status != txn.StatusPending {
		t.Fatalf("retry-before-decision = %+v, %v", res, err)
	}

	// User approves in the local UI; the next retry applies.
	if !svc.Resolve(req.ChangeSetID, true, "") {
		t.Fatal("Resolve failed")
	}
	res, err = engine.Execute(ctx, ws, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != txn.StatusApplied {
		t.Fatalf("post-approval retry = %+v", res)
	}
	if data, _ := os.ReadFile(target); string(data) != "v2\n" {
		t.Fatalf("content = %q", data)
	}

	// Retrying the applied change set stays idempotent.
	res, err = engine.Execute(ctx, ws, req)
	if err != nil || res.Status != txn.StatusApplied {
		t.Fatalf("idempotent retry = %+v, %v", res, err)
	}

	// Denial path: pending → user rejects → retry returns denied, terminal.
	sum2 := sha256.Sum256([]byte("v2\n"))
	req2 := txn.Request{
		ChangeSetID: store.NewID("chg"),
		Provider:    "grok", Summary: "update again",
		Operations: []txn.Operation{{
			Type: txn.OpUpdate, Path: "a.txt", Content: "v3\n",
			ExpectedSHA256: hex.EncodeToString(sum2[:]),
		}},
	}
	if res, err = engine.Execute(ctx, ws, req2); err != nil || res.Status != txn.StatusPending {
		t.Fatalf("second change set = %+v, %v", res, err)
	}
	svc.Resolve(req2.ChangeSetID, false, "")
	if res, err = engine.Execute(ctx, ws, req2); err != nil || res.Status != txn.StatusDenied {
		t.Fatalf("denied retry = %+v, %v", res, err)
	}
	if data, _ := os.ReadFile(target); string(data) != "v2\n" {
		t.Fatalf("denied change modified file: %q", data)
	}
	if res, err = engine.Execute(ctx, ws, req2); err != nil || res.Status != txn.StatusDenied {
		t.Fatalf("denied is not terminal: %+v, %v", res, err)
	}

	// A retry with mutated operations must be rejected outright.
	req3 := txn.Request{
		ChangeSetID: store.NewID("chg"),
		Provider:    "grok",
		Operations: []txn.Operation{{
			Type: txn.OpUpdate, Path: "a.txt", Content: "v4\n",
			ExpectedSHA256: hex.EncodeToString(sum2[:]),
		}},
	}
	if res, err = engine.Execute(ctx, ws, req3); err != nil || res.Status != txn.StatusPending {
		t.Fatalf("third change set = %+v, %v", res, err)
	}
	mutated := req3
	mutated.Operations = []txn.Operation{{
		Type: txn.OpUpdate, Path: "a.txt", Content: "EVIL\n",
		ExpectedSHA256: hex.EncodeToString(sum2[:]),
	}}
	if _, err = engine.Execute(ctx, ws, mutated); err == nil {
		t.Fatal("retry with different operations accepted")
	}
}
