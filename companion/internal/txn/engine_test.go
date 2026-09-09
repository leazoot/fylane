package txn

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"

	"github.com/leazoot/fylane/companion/internal/store"
	"github.com/leazoot/fylane/companion/internal/workspace"
)

// recordingApprover approves or denies everything and captures the request.
type recordingApprover struct {
	deny   bool
	reason string
	last   *ApprovalRequest
	calls  int
}

func (a *recordingApprover) Approve(_ context.Context, req *ApprovalRequest) (Decision, error) {
	a.calls++
	a.last = req
	if a.deny {
		return Decision{Approved: false, Reason: a.reason}, nil
	}
	return Decision{Approved: true}, nil
}

type fixture struct {
	engine   *Engine
	approver *recordingApprover
	st       *store.Store
	ws       *workspace.Workspace
	root     string
	backups  string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	dataDir := t.TempDir()
	root := t.TempDir()

	st, err := store.Open(ctx, filepath.Join(dataDir, "fylane.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	m, err := workspace.NewManager(st, dataDir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	rec, err := m.Add(ctx, root)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	ws, err := m.Open(ctx, rec.ID)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	approver := &recordingApprover{}
	backups := filepath.Join(dataDir, "backups")
	return &fixture{
		engine:   &Engine{Store: st, BackupRoot: backups, Approver: approver},
		approver: approver,
		st:       st,
		ws:       ws,
		root:     root,
		backups:  backups,
	}
}

func (f *fixture) write(t *testing.T, rel, content string) string {
	t.Helper()
	p := filepath.Join(f.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return hashBytes([]byte(content))
}

func (f *fixture) read(t *testing.T, rel string) (string, bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.root, filepath.FromSlash(rel)))
	if os.IsNotExist(err) {
		return "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data), true
}

func (f *fixture) execute(t *testing.T, ops ...Operation) *Result {
	t.Helper()
	res, err := f.engine.Execute(context.Background(), f.ws, Request{
		Provider: "test", Summary: "test change", Operations: ops,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return res
}

func TestCreateUpdateMoveDelete(t *testing.T) {
	f := newFixture(t)
	oldHash := f.write(t, "src/app.go", "v1\n")
	movHash := f.write(t, "docs/old.md", "moved content\n")
	delHash := f.write(t, "tmp/junk.txt", "junk\n")

	res := f.execute(t,
		Operation{Type: OpCreate, Path: "src/new.go", Content: "package new\n"},
		Operation{Type: OpUpdate, Path: "src/app.go", Content: "v2\n", ExpectedSHA256: oldHash},
		Operation{Type: OpMove, From: "docs/old.md", To: "docs/new.md", ExpectedSHA256: movHash},
		Operation{Type: OpDelete, Path: "tmp/junk.txt", ExpectedSHA256: delHash},
	)
	if res.Status != StatusApplied {
		t.Fatalf("status = %s (%s)", res.Status, res.Reason)
	}
	if len(res.Operations) != 4 {
		t.Fatalf("op results = %+v", res.Operations)
	}
	wantStatus := []string{"created", "updated", "moved", "deleted"}
	for i, or := range res.Operations {
		if or.Status != wantStatus[i] {
			t.Errorf("op %d status = %s, want %s", i, or.Status, wantStatus[i])
		}
	}
	if got, _ := f.read(t, "src/new.go"); got != "package new\n" {
		t.Errorf("created content = %q", got)
	}
	if got, _ := f.read(t, "src/app.go"); got != "v2\n" {
		t.Errorf("updated content = %q", got)
	}
	if got, _ := f.read(t, "docs/new.md"); got != "moved content\n" {
		t.Errorf("moved content = %q", got)
	}
	if _, exists := f.read(t, "docs/old.md"); exists {
		t.Error("move source still exists")
	}
	if _, exists := f.read(t, "tmp/junk.txt"); exists {
		t.Error("deleted file still exists")
	}
	if res.RollbackAvailableUntil.Before(time.Now().Add(6 * 24 * time.Hour)) {
		t.Errorf("rollback deadline = %v, want ~7 days out", res.RollbackAvailableUntil)
	}

	// Journal: change set applied with hashes; audit trail complete.
	rec, err := f.st.GetChangeSet(context.Background(), res.ChangeSetID)
	if err != nil {
		t.Fatalf("GetChangeSet: %v", err)
	}
	if rec.Status != store.ChangeSetApplied || rec.ApprovedAt.IsZero() || rec.AppliedAt.IsZero() {
		t.Fatalf("journal record = %+v", rec)
	}
	if rec.BeforeHashes["src/app.go"] != oldHash || rec.AfterHashes["src/app.go"] != hashBytes([]byte("v2\n")) {
		t.Fatalf("hashes = %v / %v", rec.BeforeHashes, rec.AfterHashes)
	}
	events, err := f.st.ListAuditEvents(context.Background(), res.ChangeSetID)
	if err != nil || len(events) != 5 { // 4 ops + summary
		t.Fatalf("audit events = %d (%v)", len(events), err)
	}

	// Backups hold the pre-images of update/move/delete.
	backupBase := filepath.Join(f.backups, res.ChangeSetID, "files")
	for rel, want := range map[string]string{
		"src/app.go":   "v1\n",
		"docs/old.md":  "moved content\n",
		"tmp/junk.txt": "junk\n",
	} {
		data, err := os.ReadFile(filepath.Join(backupBase, filepath.FromSlash(rel)))
		if err != nil || string(data) != want {
			t.Errorf("backup of %s = %q, %v; want %q", rel, data, err, want)
		}
	}
	// The approval saw diffs.
	if f.approver.last == nil || len(f.approver.last.Operations) != 4 {
		t.Fatalf("approval request = %+v", f.approver.last)
	}
	if d := f.approver.last.Operations[1].Diff; !strings.Contains(d, "-v1") || !strings.Contains(d, "+v2") {
		t.Errorf("update diff = %q", d)
	}
}

func TestConflictsHaveNoSideEffects(t *testing.T) {
	f := newFixture(t)
	hash := f.write(t, "a.txt", "v1\n")

	cases := []struct {
		name   string
		op     Operation
		reason string
	}{
		{"stale hash", Operation{Type: OpUpdate, Path: "a.txt", Content: "x", ExpectedSHA256: hashBytes([]byte("other"))}, "base_hash_mismatch"},
		{"missing expected", Operation{Type: OpUpdate, Path: "a.txt", Content: "x"}, "expected_sha256_missing"},
		{"create over existing", Operation{Type: OpCreate, Path: "a.txt", Content: "x"}, "already_exists"},
		{"update missing", Operation{Type: OpUpdate, Path: "nope.txt", Content: "x", ExpectedSHA256: hash}, "target_missing"},
		{"delete missing", Operation{Type: OpDelete, Path: "nope.txt", ExpectedSHA256: hash}, "target_missing"},
		{"move onto existing", Operation{Type: OpMove, From: "a.txt", To: "a2.txt", ExpectedSHA256: hash}, "target_exists"},
	}
	f.write(t, "a2.txt", "occupied\n")
	for _, c := range cases {
		res := f.execute(t, c.op)
		if res.Status != StatusConflict || res.Conflict == nil || res.Conflict.Reason != c.reason {
			t.Errorf("%s: result = %+v, want conflict %s", c.name, res, c.reason)
		}
		if res.ChangeSetID != "" {
			t.Errorf("%s: conflict must not create a change set", c.name)
		}
	}
	if got, _ := f.read(t, "a.txt"); got != "v1\n" {
		t.Fatalf("conflicts modified the file: %q", got)
	}
	if f.approver.calls != 0 {
		t.Fatalf("conflicts must never reach approval (calls=%d)", f.approver.calls)
	}
}

func TestDeniedChangesNothing(t *testing.T) {
	f := newFixture(t)
	hash := f.write(t, "a.txt", "v1\n")
	f.approver.deny = true
	f.approver.reason = "user_rejected"

	res := f.execute(t,
		Operation{Type: OpUpdate, Path: "a.txt", Content: "v2\n", ExpectedSHA256: hash},
		Operation{Type: OpCreate, Path: "b.txt", Content: "new\n"},
	)
	if res.Status != StatusDenied || res.Reason != "user_rejected" {
		t.Fatalf("result = %+v", res)
	}
	if got, _ := f.read(t, "a.txt"); got != "v1\n" {
		t.Errorf("denied update modified the file: %q", got)
	}
	if _, exists := f.read(t, "b.txt"); exists {
		t.Error("denied create wrote a file")
	}
	if _, err := os.Stat(filepath.Join(f.backups, res.ChangeSetID)); !os.IsNotExist(err) {
		t.Error("denied change set left a backup directory")
	}
	rec, err := f.st.GetChangeSet(context.Background(), res.ChangeSetID)
	if err != nil || rec.Status != store.ChangeSetDenied {
		t.Fatalf("journal = %+v, %v", rec, err)
	}
}

func TestIdempotentRetry(t *testing.T) {
	f := newFixture(t)
	hash := f.write(t, "a.txt", "v1\n")

	req := Request{
		ChangeSetID: store.NewID("chg"),
		Provider:    "test", Summary: "update a",
		Operations: []Operation{{Type: OpUpdate, Path: "a.txt", Content: "v2\n", ExpectedSHA256: hash}},
	}
	res1, err := f.engine.Execute(context.Background(), f.ws, req)
	if err != nil || res1.Status != StatusApplied {
		t.Fatalf("first execute = %+v, %v", res1, err)
	}

	// The retry must not re-apply: the disk hash has moved past
	// expected_sha256, so a real second apply would conflict.
	res2, err := f.engine.Execute(context.Background(), f.ws, req)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if res2.Status != StatusApplied || res2.ChangeSetID != res1.ChangeSetID {
		t.Fatalf("retry result = %+v", res2)
	}
	if f.approver.calls != 1 {
		t.Fatalf("retry went through approval again (calls=%d)", f.approver.calls)
	}
	if got, _ := f.read(t, "a.txt"); got != "v2\n" {
		t.Fatalf("content after retry = %q", got)
	}
	// Exactly one backup directory.
	entries, err := os.ReadDir(f.backups)
	if err != nil || len(entries) != 1 {
		t.Fatalf("backup dirs = %d, %v", len(entries), err)
	}
}

func TestMidApplyFailureRestoresEverything(t *testing.T) {
	f := newFixture(t)
	hash := f.write(t, "ok.txt", "keep\n")
	f.write(t, "locked/target.txt", "old\n")
	lockedHash := hashBytes([]byte("old\n"))
	lockedDir := filepath.Join(f.root, "locked")
	if runtime.GOOS == "windows" {
		// Directory modes are ignored here. An open handle on the target is
		// what refuses the rename that fs.go uses to replace it.
		held, err := os.Open(filepath.Join(lockedDir, "target.txt"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { held.Close() })
	} else {
		if err := os.Chmod(lockedDir, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(lockedDir, 0o755) })
	}

	res := f.execute(t,
		Operation{Type: OpCreate, Path: "created.txt", Content: "temp\n"},
		Operation{Type: OpUpdate, Path: "ok.txt", Content: "changed\n", ExpectedSHA256: hash},
		Operation{Type: OpUpdate, Path: "locked/target.txt", Content: "new\n", ExpectedSHA256: lockedHash},
	)
	if res.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", res.Status)
	}
	if _, exists := f.read(t, "created.txt"); exists {
		t.Error("failed transaction left the created file behind")
	}
	if got, _ := f.read(t, "ok.txt"); got != "keep\n" {
		t.Errorf("failed transaction did not restore ok.txt: %q", got)
	}
	if got, _ := f.read(t, "locked/target.txt"); got != "old\n" {
		t.Errorf("locked file was modified: %q", got)
	}
	rec, err := f.st.GetChangeSet(context.Background(), res.ChangeSetID)
	if err != nil || rec.Status != store.ChangeSetFailed {
		t.Fatalf("journal = %+v, %v", rec, err)
	}
}

func TestRecursiveDirectoryDelete(t *testing.T) {
	f := newFixture(t)
	f.write(t, "old/a.txt", "a\n")
	f.write(t, "old/sub/b.txt", "b\n")

	// Non-recursive delete of a non-empty directory is refused outright.
	_, err := f.engine.Execute(context.Background(), f.ws, Request{
		Provider: "test", Operations: []Operation{{Type: OpDelete, Path: "old"}},
	})
	if err == nil || !strings.Contains(err.Error(), "recursive") {
		t.Fatalf("non-recursive delete: err = %v", err)
	}

	res := f.execute(t, Operation{Type: OpDelete, Path: "old", Recursive: true})
	if res.Status != StatusApplied {
		t.Fatalf("status = %s (%s)", res.Status, res.Reason)
	}
	if _, err := os.Stat(filepath.Join(f.root, "old")); !os.IsNotExist(err) {
		t.Error("directory still exists")
	}
	// The whole tree is in the backup (recycle) area.
	for rel, want := range map[string]string{"old/a.txt": "a\n", "old/sub/b.txt": "b\n"} {
		data, err := os.ReadFile(filepath.Join(f.backups, res.ChangeSetID, "files", filepath.FromSlash(rel)))
		if err != nil || string(data) != want {
			t.Errorf("recycle copy of %s = %q, %v", rel, data, err)
		}
	}
	// The approver saw the double-confirm flag.
	if !f.approver.last.Operations[0].RecursiveDelete {
		t.Error("recursive delete not flagged for double confirmation")
	}
}

func TestChangeDuringApprovalAborts(t *testing.T) {
	f := newFixture(t)
	hash := f.write(t, "a.txt", "v1\n")
	// Simulate a change landing while approval is pending: the approver
	// mutates the file before answering.
	mutator := approverFunc(func(_ context.Context, req *ApprovalRequest) (Decision, error) {
		f.write(t, "a.txt", "changed meanwhile\n")
		return Decision{Approved: true}, nil
	})
	f.engine.Approver = mutator

	res := f.execute(t, Operation{Type: OpUpdate, Path: "a.txt", Content: "v2\n", ExpectedSHA256: hash})
	if res.Status != StatusFailed || !strings.Contains(res.Reason, "changed while awaiting approval") {
		t.Fatalf("result = %+v", res)
	}
	if got, _ := f.read(t, "a.txt"); got != "changed meanwhile\n" {
		t.Fatalf("concurrent edit was clobbered: %q", got)
	}
}

type approverFunc func(context.Context, *ApprovalRequest) (Decision, error)

func (f approverFunc) Approve(ctx context.Context, req *ApprovalRequest) (Decision, error) {
	return f(ctx, req)
}

func TestSensitiveFlaggedAndSandboxEnforced(t *testing.T) {
	f := newFixture(t)

	res := f.execute(t, Operation{Type: OpCreate, Path: ".env", Content: "KEY=1\n"})
	if res.Status != StatusApplied {
		t.Fatalf("sensitive create = %+v", res)
	}
	if !f.approver.last.Operations[0].Sensitive {
		t.Error("sensitive path not flagged in approval request")
	}

	for _, p := range []string{"../escape.txt", "/abs.txt", ".git/config"} {
		_, err := f.engine.Execute(context.Background(), f.ws, Request{
			Provider: "test", Operations: []Operation{{Type: OpCreate, Path: p, Content: "x"}},
		})
		if err == nil {
			t.Errorf("sandbox let %q through", p)
		}
	}
}

func TestDuplicatePathsRejected(t *testing.T) {
	f := newFixture(t)
	_, err := f.engine.Execute(context.Background(), f.ws, Request{
		Provider: "test", Operations: []Operation{
			{Type: OpCreate, Path: "x.txt", Content: "1"},
			{Type: OpCreate, Path: "x.txt", Content: "2"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "more than one operation") {
		t.Fatalf("duplicate paths: err = %v", err)
	}
}

func TestReadOnlyWorkspaceRefused(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	rec, err := f.st.GetWorkspace(ctx, f.ws.ID())
	if err != nil {
		t.Fatal(err)
	}
	rec.Mode = store.ModeReadOnly
	if err := f.st.UpdateWorkspace(ctx, rec); err != nil {
		t.Fatal(err)
	}
	ro, err := workspace.FromRecord(rec)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.engine.Execute(ctx, ro, Request{
		Provider: "test", Operations: []Operation{{Type: OpCreate, Path: "x.txt", Content: "1"}},
	})
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("read-only workspace: err = %v", err)
	}
}

func TestRecoverCleansInterruptedChangeSets(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// Fabricate a crash: a pending change set plus a staging temp file.
	f.write(t, "src/app.go", "v1\n")
	tmp := filepath.Join(f.root, "src", ".fylane-tmp-crashed")
	if err := os.WriteFile(tmp, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := &store.ChangeSet{
		ID: store.NewID("chg"), WorkspaceID: f.ws.ID(), Provider: "test",
		Operations: []byte(`[{"type":"update","path":"src/app.go","content":"v2","expected_sha256":"x"}]`),
		Status:     store.ChangeSetPending,
	}
	if err := f.st.CreateChangeSet(ctx, rec); err != nil {
		t.Fatal(err)
	}

	n, err := f.engine.Recover(ctx)
	if err != nil || n != 1 {
		t.Fatalf("Recover = %d, %v", n, err)
	}
	got, err := f.st.GetChangeSet(ctx, rec.ID)
	if err != nil || got.Status != store.ChangeSetFailed {
		t.Fatalf("recovered change set = %+v, %v", got, err)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Error("staging temp file not cleaned up")
	}
	// Idempotent: nothing left to recover.
	if n, err := f.engine.Recover(ctx); err != nil || n != 0 {
		t.Fatalf("second Recover = %d, %v", n, err)
	}
}

func TestConcurrentSameIDRefused(t *testing.T) {
	f := newFixture(t)
	id := store.NewID("chg")
	release := make(chan struct{})
	blocked := make(chan struct{})
	f.engine.Approver = approverFunc(func(ctx context.Context, _ *ApprovalRequest) (Decision, error) {
		close(blocked)
		<-release
		return Decision{Approved: true}, nil
	})

	done := make(chan error, 1)
	go func() {
		_, err := f.engine.Execute(context.Background(), f.ws, Request{
			ChangeSetID: id, Provider: "test",
			Operations: []Operation{{Type: OpCreate, Path: "x.txt", Content: "1"}},
		})
		done <- err
	}()
	<-blocked

	_, err := f.engine.Execute(context.Background(), f.ws, Request{
		ChangeSetID: id, Provider: "test",
		Operations: []Operation{{Type: OpCreate, Path: "y.txt", Content: "2"}},
	})
	if err == nil || !strings.Contains(err.Error(), "already executing") {
		t.Fatalf("concurrent same-ID: err = %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("first execute: %v", err)
	}
}

func TestCaseOnlyRenameAllowed(t *testing.T) {
	f := newFixture(t)
	hash := f.write(t, "readme.md", "content\n")

	res := f.execute(t, Operation{Type: OpMove, From: "readme.md", To: "README.md", ExpectedSHA256: hash})
	// On case-insensitive filesystems this is a same-file rename; on
	// case-sensitive ones a plain move. Both must succeed.
	if res.Status != StatusApplied {
		t.Fatalf("case-only rename = %+v", res)
	}
	entries, err := os.ReadDir(f.root)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 1 || names[0] != "README.md" {
		t.Fatalf("directory after rename = %v", names)
	}
}

func TestEmptyAndOversizedRequests(t *testing.T) {
	f := newFixture(t)
	if _, err := f.engine.Execute(context.Background(), f.ws, Request{Provider: "test"}); err == nil {
		t.Error("empty change set accepted")
	}
	ops := make([]Operation, maxOps+1)
	for i := range ops {
		ops[i] = Operation{Type: OpCreate, Path: "f" + string(rune('a'+i%26)) + ".txt", Content: "x"}
	}
	if _, err := f.engine.Execute(context.Background(), f.ws, Request{Provider: "test", Operations: ops}); err == nil {
		t.Error("oversized change set accepted")
	}

	// One operation over the per-op content budget must be refused before
	// any disk work, and must not leave a partial file behind.
	huge := strings.Repeat("x", maxOpContentBytes+1)
	if _, err := f.engine.Execute(context.Background(), f.ws, Request{
		Provider: "test", Operations: []Operation{{Type: OpCreate, Path: "huge.bin", Content: huge}},
	}); err == nil {
		t.Error("oversized operation content accepted")
	}
	if _, exists := f.read(t, "huge.bin"); exists {
		t.Error("oversized operation left a file on disk")
	}
}

// The two change-set caps are published numbers: the security notes name
// them as the defence against oversized writes, and an auditor reads that as
// a commitment. Every other test here spends the constants rather
// than the values, so widening one would keep the suite green while quietly
// retiring the promise. This test is the only thing that notices.
func TestThePublishedLimitsAreTheOnesWeShip(t *testing.T) {
	if maxOps != 100 {
		t.Errorf("maxOps = %d, published as 100 operations per change set", maxOps)
	}
	if maxOpContentBytes != 16<<20 {
		t.Errorf("maxOpContentBytes = %d, published as 16 MiB per operation", maxOpContentBytes)
	}
}

func TestIdempotentDeleteRetry(t *testing.T) {
	f := newFixture(t)
	hash := f.write(t, "tmp/junk.txt", "junk\n")
	req := Request{
		ChangeSetID: "chg_del_retry", Provider: "test", Summary: "delete",
		Operations: []Operation{{Type: OpDelete, Path: "tmp/junk.txt", ExpectedSHA256: hash}},
	}
	res, err := f.engine.Execute(context.Background(), f.ws, req)
	if err != nil || res.Status != StatusApplied {
		t.Fatalf("first delete = %+v, %v", res, err)
	}

	// A retried delete must replay the stored result, not fail on the
	// now-missing file or touch the backup again.
	res2, err := f.engine.Execute(context.Background(), f.ws, req)
	if err != nil || res2.Status != StatusApplied || res2.ChangeSetID != res.ChangeSetID {
		t.Fatalf("retried delete = %+v, %v", res2, err)
	}
	if f.approver.calls != 1 {
		t.Fatalf("approver calls = %d, want 1", f.approver.calls)
	}
	backups, err := os.ReadDir(f.backups)
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("backup directories = %d, want 1", len(backups))
	}
}

func TestPreviewDiffDecodesEncodedText(t *testing.T) {
	f := newFixture(t)
	gbk, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte("旧内容\n"))
	if err != nil {
		t.Fatal(err)
	}
	hash := f.write(t, "notes.txt", string(gbk))
	newGBK, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte("新内容\n"))
	if err != nil {
		t.Fatal(err)
	}

	res := f.execute(t, Operation{Type: OpUpdate, Path: "notes.txt", Content: string(newGBK), ExpectedSHA256: hash})
	if res.Status != StatusApplied {
		t.Fatalf("status = %s", res.Status)
	}
	diff := f.approver.last.Operations[0].Diff
	if !strings.Contains(diff, "-旧内容") || !strings.Contains(diff, "+新内容") {
		t.Fatalf("approval diff is not decoded text:\n%s", diff)
	}

	// Real binary stays labeled — raw bytes never reach the approval UI.
	binHash := f.write(t, "blob.bin", "\x00\x01\x02")
	res = f.execute(t, Operation{Type: OpUpdate, Path: "blob.bin", Content: "\x00\x03", ExpectedSHA256: binHash})
	if res.Status != StatusApplied {
		t.Fatalf("binary update status = %s", res.Status)
	}
	if got := f.approver.last.Operations[0].Diff; got != binaryDiffLabel {
		t.Fatalf("binary diff preview = %q, want %q", got, binaryDiffLabel)
	}
}
