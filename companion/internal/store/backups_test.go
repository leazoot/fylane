package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// ClearBackups is the only action in the product that takes away the ability
// to undo a write. What it must not take away is anything else.

func applied(id, wsID, backup string, deadline time.Time) *ChangeSet {
	return &ChangeSet{
		ID:             id,
		WorkspaceID:    wsID,
		Provider:       "claude",
		Summary:        "Write notes",
		Operations:     json.RawMessage(`[{"type":"update","path":"a.md","expected_sha256":"aa","content":"x"}]`),
		Status:         ChangeSetApplied,
		BackupLocation: backup,
		AppliedAt:      time.Now().UTC(),
		// A row that still advertises a window is exactly what must not be
		// left behind once its copy is gone.
		RollbackDeadline: deadline,
	}
}

func TestClearBackupsKeepsTheRecordAndRemovesTheUndo(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	w := testWorkspace("ws_0000000000000090")
	if err := s.CreateWorkspace(ctx, w); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	deadline := time.Now().UTC().Add(7 * 24 * time.Hour)
	if err := s.CreateChangeSet(ctx, applied("chg_0000000000000090", w.ID, "chg_0000000000000090", deadline)); err != nil {
		t.Fatalf("CreateChangeSet: %v", err)
	}

	cleared, backups, err := s.ClearBackups(ctx, w.ID)
	if err != nil {
		t.Fatalf("ClearBackups: %v", err)
	}
	if cleared != 1 || len(backups) != 1 || backups[0] != "chg_0000000000000090" {
		t.Fatalf("cleared = %d, backups = %v", cleared, backups)
	}

	// The record of what was written is not what the user asked to remove.
	got, err := s.GetChangeSet(ctx, "chg_0000000000000090")
	if err != nil {
		t.Fatalf("GetChangeSet: %v", err)
	}
	if got.Status != ChangeSetApplied || got.Summary != "Write notes" {
		t.Errorf("the record changed: %+v", got)
	}
	// The window has to go with the copy. A row still naming a deadline would
	// put an undo button on screen that can only fail.
	if !got.RollbackDeadline.IsZero() {
		t.Errorf("RollbackDeadline = %v; want cleared with the copy", got.RollbackDeadline)
	}
	if got.BackupLocation != "" {
		t.Errorf("BackupLocation = %q; want cleared", got.BackupLocation)
	}
}

func TestClearBackupsLeavesTheGateAlone(t *testing.T) {
	// A change set still waiting for approval has been applied to nothing, so
	// it has no copy to reclaim — and clearing its deadline would corrupt a
	// decision the user has not made yet.
	ctx := context.Background()
	s := openTestStore(t)
	w := testWorkspace("ws_0000000000000091")
	if err := s.CreateWorkspace(ctx, w); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	pending := applied("chg_0000000000000091", w.ID, "", time.Time{})
	pending.Status = ChangeSetPending
	pending.AppliedAt = time.Time{}
	if err := s.CreateChangeSet(ctx, pending); err != nil {
		t.Fatalf("CreateChangeSet: %v", err)
	}

	cleared, backups, err := s.ClearBackups(ctx, w.ID)
	if err != nil {
		t.Fatalf("ClearBackups: %v", err)
	}
	if cleared != 0 || len(backups) != 0 {
		t.Errorf("cleared = %d, backups = %v; want the gate untouched", cleared, backups)
	}
	got, err := s.GetChangeSet(ctx, "chg_0000000000000091")
	if err != nil {
		t.Fatalf("GetChangeSet: %v", err)
	}
	if got.Status != ChangeSetPending {
		t.Errorf("status = %q; want the pending change set left alone", got.Status)
	}
}

func TestClearBackupsStaysInsideOneWorkspace(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	mine := testWorkspace("ws_0000000000000092")
	other := testWorkspace("ws_0000000000000093")
	for _, w := range []*Workspace{mine, other} {
		if err := s.CreateWorkspace(ctx, w); err != nil {
			t.Fatalf("CreateWorkspace: %v", err)
		}
	}
	deadline := time.Now().UTC().Add(time.Hour)
	if err := s.CreateChangeSet(ctx, applied("chg_0000000000000092", mine.ID, "b1", deadline)); err != nil {
		t.Fatalf("CreateChangeSet: %v", err)
	}
	if err := s.CreateChangeSet(ctx, applied("chg_0000000000000093", other.ID, "b2", deadline)); err != nil {
		t.Fatalf("CreateChangeSet: %v", err)
	}

	if _, _, err := s.ClearBackups(ctx, mine.ID); err != nil {
		t.Fatalf("ClearBackups: %v", err)
	}
	kept, err := s.GetChangeSet(ctx, "chg_0000000000000093")
	if err != nil {
		t.Fatalf("GetChangeSet: %v", err)
	}
	if kept.BackupLocation != "b2" || kept.RollbackDeadline.IsZero() {
		t.Errorf("another folder's undo copy was cleared: %+v", kept)
	}
}

func TestClearBackupsRefusesAnEmptyWorkspace(t *testing.T) {
	// Without this the scope clause would match every row in the table.
	s := openTestStore(t)
	if _, _, err := s.ClearBackups(context.Background(), ""); err == nil {
		t.Error("ClearBackups accepted an empty workspace id")
	}
}
