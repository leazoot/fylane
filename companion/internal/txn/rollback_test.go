package txn

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leazoot/fylane/companion/internal/store"
)

func (f *fixture) rollback(t *testing.T, changeSetID string) *Result {
	t.Helper()
	res, err := f.engine.Rollback(context.Background(), f.ws, changeSetID)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	return res
}

func TestRollbackAllOperationTypes(t *testing.T) {
	f := newFixture(t)
	updHash := f.write(t, "upd.txt", "original\n")
	movHash := f.write(t, "old.md", "moved\n")
	delHash := f.write(t, "gone.txt", "bye\n")
	f.write(t, "dir/a.txt", "in dir\n")

	res := f.execute(t,
		Operation{Type: OpCreate, Path: "created.txt", Content: "new\n"},
		Operation{Type: OpUpdate, Path: "upd.txt", Content: "changed\n", ExpectedSHA256: updHash},
		Operation{Type: OpMove, From: "old.md", To: "new.md", ExpectedSHA256: movHash},
		Operation{Type: OpDelete, Path: "gone.txt", ExpectedSHA256: delHash},
		Operation{Type: OpDelete, Path: "dir", Recursive: true},
	)
	if res.Status != StatusApplied {
		t.Fatalf("apply = %+v", res)
	}

	rb := f.rollback(t, res.ChangeSetID)
	if rb.Status != StatusRolledBack {
		t.Fatalf("rollback = %+v", rb)
	}
	if _, exists := f.read(t, "created.txt"); exists {
		t.Error("created file not removed")
	}
	if got, _ := f.read(t, "upd.txt"); got != "original\n" {
		t.Errorf("update not restored: %q", got)
	}
	if got, _ := f.read(t, "old.md"); got != "moved\n" {
		t.Errorf("move not reversed: %q", got)
	}
	if _, exists := f.read(t, "new.md"); exists {
		t.Error("move target still present")
	}
	if got, _ := f.read(t, "gone.txt"); got != "bye\n" {
		t.Errorf("deleted file not restored: %q", got)
	}
	if got, _ := f.read(t, "dir/a.txt"); got != "in dir\n" {
		t.Errorf("deleted directory not restored: %q", got)
	}

	rec, err := f.st.GetChangeSet(context.Background(), res.ChangeSetID)
	if err != nil || rec.Status != store.ChangeSetRolledBack {
		t.Fatalf("journal = %+v, %v", rec, err)
	}

	// Idempotent: rolling back again reports rolled_back, no error.
	if rb := f.rollback(t, res.ChangeSetID); rb.Status != StatusRolledBack {
		t.Fatalf("second rollback = %+v", rb)
	}
}

func TestRollbackConflictWhenFileChanged(t *testing.T) {
	f := newFixture(t)
	hash := f.write(t, "a.txt", "v1\n")
	res := f.execute(t, Operation{Type: OpUpdate, Path: "a.txt", Content: "v2\n", ExpectedSHA256: hash})

	// The file moves on after apply.
	f.write(t, "a.txt", "v3 user edit\n")

	rb := f.rollback(t, res.ChangeSetID)
	if rb.Status != StatusConflict || rb.Conflict == nil || rb.Conflict.Reason != "changed_since_apply" {
		t.Fatalf("rollback of changed file = %+v", rb)
	}
	if rb.Conflict.Diff == "" || !strings.Contains(rb.Conflict.Diff, "v1") {
		t.Errorf("conflict diff missing restore preview: %q", rb.Conflict.Diff)
	}
	if got, _ := f.read(t, "a.txt"); got != "v3 user edit\n" {
		t.Fatalf("conflicted rollback modified the file: %q", got)
	}
}

func TestRollbackAfterDeadlineFails(t *testing.T) {
	f := newFixture(t)
	res := f.execute(t, Operation{Type: OpCreate, Path: "a.txt", Content: "x\n"})

	ctx := context.Background()
	rec, err := f.st.GetChangeSet(ctx, res.ChangeSetID)
	if err != nil {
		t.Fatal(err)
	}
	rec.RollbackDeadline = time.Now().Add(-time.Minute)
	if err := f.st.UpdateChangeSet(ctx, rec); err != nil {
		t.Fatal(err)
	}

	rb := f.rollback(t, res.ChangeSetID)
	if rb.Status != StatusFailed || !strings.Contains(rb.Reason, "expired") {
		t.Fatalf("expired rollback = %+v", rb)
	}
	if _, exists := f.read(t, "a.txt"); !exists {
		t.Fatal("expired rollback removed the file")
	}
}

func TestRollbackDenied(t *testing.T) {
	f := newFixture(t)
	res := f.execute(t, Operation{Type: OpCreate, Path: "a.txt", Content: "x\n"})

	f.approver.deny = true
	rb := f.rollback(t, res.ChangeSetID)
	if rb.Status != StatusDenied {
		t.Fatalf("denied rollback = %+v", rb)
	}
	if _, exists := f.read(t, "a.txt"); !exists {
		t.Fatal("denied rollback removed the file")
	}
	rec, _ := f.st.GetChangeSet(context.Background(), res.ChangeSetID)
	if rec.Status != store.ChangeSetApplied {
		t.Fatalf("denied rollback changed journal status: %s", rec.Status)
	}
}

func TestRollbackUnknownAndForeign(t *testing.T) {
	f := newFixture(t)
	if _, err := f.engine.Rollback(context.Background(), f.ws, "chg_nope"); err == nil {
		t.Fatal("unknown change set rolled back")
	}
}

func TestCleanupBackupsRetention(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	hash1 := f.write(t, "a.txt", "a1\n")
	res1 := f.execute(t, Operation{Type: OpUpdate, Path: "a.txt", Content: "a2\n", ExpectedSHA256: hash1})
	hash2 := f.write(t, "b.txt", "b1\n")
	res2 := f.execute(t, Operation{Type: OpUpdate, Path: "b.txt", Content: "b2\n", ExpectedSHA256: hash2})

	// Nothing expired, size within budget: nothing removed.
	if n, err := f.engine.CleanupBackups(ctx); err != nil || n != 0 {
		t.Fatalf("first cleanup = %d, %v", n, err)
	}

	// Expire the first change set's window: its backup goes away.
	rec1, _ := f.st.GetChangeSet(ctx, res1.ChangeSetID)
	rec1.RollbackDeadline = time.Now().Add(-time.Minute)
	if err := f.st.UpdateChangeSet(ctx, rec1); err != nil {
		t.Fatal(err)
	}
	if n, err := f.engine.CleanupBackups(ctx); err != nil || n != 1 {
		t.Fatalf("expiry cleanup = %d, %v", n, err)
	}
	if _, err := os.Stat(filepath.Join(f.backups, res1.ChangeSetID)); !os.IsNotExist(err) {
		t.Fatal("expired backup still present")
	}
	if _, err := os.Stat(filepath.Join(f.backups, res2.ChangeSetID)); err != nil {
		t.Fatal("in-window backup was deleted")
	}

	// Size pressure: a tiny budget evicts the oldest in-window backup and
	// closes its rollback window first.
	f.engine.MaxBackupBytes = 1
	if n, err := f.engine.CleanupBackups(ctx); err != nil || n != 1 {
		t.Fatalf("size cleanup = %d, %v", n, err)
	}
	rec2, _ := f.st.GetChangeSet(ctx, res2.ChangeSetID)
	if rec2.RollbackDeadline.After(time.Now()) {
		t.Fatal("evicted backup's rollback window still open")
	}
	rb := f.rollback(t, res2.ChangeSetID)
	if rb.Status != StatusFailed || !strings.Contains(rb.Reason, "expired") {
		t.Fatalf("rollback after eviction = %+v", rb)
	}

	// Orphan backup directories are removed.
	if err := os.MkdirAll(filepath.Join(f.backups, "chg_orphan", "files"), 0o700); err != nil {
		t.Fatal(err)
	}
	if n, err := f.engine.CleanupBackups(ctx); err != nil || n != 1 {
		t.Fatalf("orphan cleanup = %d, %v", n, err)
	}
}

// The pre-image a rollback conflict carries is the file itself. For an
// ordinary file that is the contract working as written — show what a rollback
// would overwrite rather than overwriting it silently. For a sensitive file
// it is the secret, and this conflict is returned before Approve is ever
// called, so nothing gates it: an exact read of the same path would have
// required a local confirmation (security.md), and the write path marks it
// high-risk. The two halves are asserted together because a rule that
// withholds every diff is indistinguishable from one that withholds none.
func TestRollbackConflictWithholdsASensitiveFilesContents(t *testing.T) {
	const secret = "AWS_SECRET_ACCESS_KEY=live-key-9d1f\n"
	f := newFixture(t)

	envHash := f.write(t, ".env", secret)
	envRes := f.execute(t, Operation{Type: OpUpdate, Path: ".env", Content: "AWS_SECRET_ACCESS_KEY=rotated\n", ExpectedSHA256: envHash})
	f.write(t, ".env", "AWS_SECRET_ACCESS_KEY=user-edited\n")

	rb := f.rollback(t, envRes.ChangeSetID)
	if rb.Status != StatusConflict || rb.Conflict == nil {
		t.Fatalf("rollback of a changed .env = %+v", rb)
	}
	if strings.Contains(rb.Conflict.Diff, "live-key-9d1f") {
		t.Errorf("the conflict handed the caller the contents of .env: %q", rb.Conflict.Diff)
	}
	if rb.Conflict.CurrentSHA256 == "" {
		t.Error("the caller still needs to be told the file moved on")
	}
	if !strings.Contains(rb.Conflict.Action, "sensitive") {
		t.Errorf("action %q does not say why there is no diff to review", rb.Conflict.Action)
	}

	// The same shape on an ordinary file keeps its diff.
	plainHash := f.write(t, "notes.md", "before\n")
	plainRes := f.execute(t, Operation{Type: OpUpdate, Path: "notes.md", Content: "after\n", ExpectedSHA256: plainHash})
	f.write(t, "notes.md", "user edit\n")

	rb2 := f.rollback(t, plainRes.ChangeSetID)
	if rb2.Status != StatusConflict || rb2.Conflict == nil {
		t.Fatalf("rollback of a changed notes.md = %+v", rb2)
	}
	if !strings.Contains(rb2.Conflict.Diff, "before") {
		t.Errorf("an ordinary file lost the diff the contract requires: %q", rb2.Conflict.Diff)
	}
}

// A rollback writes to the same files the change set wrote to, so it is a
// write to a sensitive file whenever the original was. The forward path
// marks every preview (engine.go); the inverse path is the same question
// asked backwards and the approval face has no way to know unless it is
// told.
func TestRollbackPreviewsCarryTheSensitiveFlag(t *testing.T) {
	const secret = "AWS_SECRET_ACCESS_KEY=live-key-9d1f\n"

	sensitiveOf := func(t *testing.T, f *fixture, path string) bool {
		t.Helper()
		for _, op := range f.approver.last.Operations {
			if op.Path == path {
				return op.Sensitive
			}
		}
		t.Fatalf("no preview for %s in %+v", path, f.approver.last.Operations)
		return false
	}

	// Undoing an update restores the old secret.
	upd := newFixture(t)
	updHash := upd.write(t, ".env", secret)
	updRes := upd.execute(t, Operation{Type: OpUpdate, Path: ".env", Content: "x=1\n", ExpectedSHA256: updHash})
	if !sensitiveOf(t, upd, ".env") {
		t.Fatal("the forward write did not mark .env sensitive; the fixture is wrong")
	}
	if upd.rollback(t, updRes.ChangeSetID).Status != StatusRolledBack {
		t.Fatal("rollback did not apply")
	}
	if !sensitiveOf(t, upd, ".env") {
		t.Error("undoing an update of .env was shown as an ordinary write")
	}

	// Undoing a delete puts the secret back. This is the shape that also
	// costs the prompt outright: balanced mode auto-approves change sets
	// made only of non-sensitive creates, and this rollback is exactly that
	// unless the flag is set.
	del := newFixture(t)
	delHash := del.write(t, ".env", secret)
	delRes := del.execute(t, Operation{Type: OpDelete, Path: ".env", ExpectedSHA256: delHash})
	if del.rollback(t, delRes.ChangeSetID).Status != StatusRolledBack {
		t.Fatal("rollback did not apply")
	}
	if !sensitiveOf(t, del, ".env") {
		t.Error("undoing a delete of .env was shown as an ordinary create")
	}

	// A move is sensitive when either end is, as on the forward path.
	mov := newFixture(t)
	movHash := mov.write(t, "config/local.ts", secret)
	movRes := mov.execute(t, Operation{Type: OpMove, From: "config/local.ts", To: ".env", ExpectedSHA256: movHash})
	if mov.rollback(t, movRes.ChangeSetID).Status != StatusRolledBack {
		t.Fatal("rollback did not apply")
	}
	if !sensitiveOf(t, mov, ".env") {
		t.Error("undoing a move whose destination is .env was shown as an ordinary move")
	}

	// An ordinary file must not pick the flag up, or the marker means
	// nothing wherever it appears.
	plain := newFixture(t)
	plainHash := plain.write(t, "notes.md", "before\n")
	plainRes := plain.execute(t, Operation{Type: OpUpdate, Path: "notes.md", Content: "after\n", ExpectedSHA256: plainHash})
	if plain.rollback(t, plainRes.ChangeSetID).Status != StatusRolledBack {
		t.Fatal("rollback did not apply")
	}
	if sensitiveOf(t, plain, "notes.md") {
		t.Error("an ordinary file was marked sensitive on rollback")
	}
}
