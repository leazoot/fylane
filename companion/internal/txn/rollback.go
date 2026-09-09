package txn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/leazoot/fylane/companion/internal/sandbox"
	"github.com/leazoot/fylane/companion/internal/store"
	"github.com/leazoot/fylane/companion/internal/workspace"
)

// StatusRolledBack is the result status of a successful rollback.
const StatusRolledBack = "rolled_back"

// Rollback reverses an applied change set. Before touching
// anything it verifies that every affected path is still exactly in its
// post-apply state — a file changed since then produces a conflict carrying
// a diff, never a silent overwrite. Rollback is itself approval-gated.
func (e *Engine) Rollback(ctx context.Context, ws *workspace.Workspace, changeSetID string) (*Result, error) {
	if e.Store == nil || e.BackupRoot == "" || e.Approver == nil {
		return nil, fmt.Errorf("txn engine is not fully configured")
	}
	if !ws.Writable() {
		return nil, fmt.Errorf("workspace %s is read-only", ws.ID())
	}

	claimKey := "rollback:" + changeSetID
	e.mu.Lock()
	if e.inflight == nil {
		e.inflight = make(map[string]bool)
	}
	if e.inflight[claimKey] {
		e.mu.Unlock()
		return nil, fmt.Errorf("rollback of %s is already executing", changeSetID)
	}
	e.inflight[claimKey] = true
	e.mu.Unlock()
	defer e.release(claimKey)

	rec, err := e.Store.GetChangeSet(ctx, changeSetID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("unknown change_set_id %q", changeSetID)
	}
	if err != nil {
		return nil, err
	}
	if rec.WorkspaceID != ws.ID() {
		return nil, fmt.Errorf("change set %s does not belong to workspace %s", changeSetID, ws.ID())
	}
	switch rec.Status {
	case store.ChangeSetApplied:
	case store.ChangeSetRolledBack:
		return &Result{Status: StatusRolledBack, ChangeSetID: rec.ID}, nil
	default:
		return &Result{Status: StatusFailed, ChangeSetID: rec.ID,
			Reason: fmt.Sprintf("change set has status %s and cannot be rolled back", rec.Status)}, nil
	}
	if time.Now().After(rec.RollbackDeadline) {
		return &Result{Status: StatusFailed, ChangeSetID: rec.ID,
			Reason: "the rollback window for this change set has expired"}, nil
	}

	var ops []Operation
	if err := json.Unmarshal(rec.Operations, &ops); err != nil {
		return nil, fmt.Errorf("decoding change set operations: %w", err)
	}
	backupDir := filepath.Join(e.BackupRoot, rec.BackupLocation, "files")

	// Verify the workspace is still in the post-apply state.
	steps, conflict, err := planRollback(ws, ops, rec, backupDir)
	if err != nil {
		return nil, err
	}
	if conflict != nil {
		return &Result{Status: StatusConflict, ChangeSetID: rec.ID, Conflict: conflict}, nil
	}

	// Rollback goes through the same local approval as any other write.
	started := time.Now()
	decision, err := e.Approver.Approve(ctx, rollbackApprovalRequest(ws, rec, steps))
	if err != nil {
		return nil, fmt.Errorf("approval: %w", err)
	}
	if decision.Pending {
		return &Result{Status: StatusPending, ChangeSetID: rec.ID,
			Reason: "waiting for local approval; retry this call with the same change_set_id"}, nil
	}
	if !decision.Approved {
		reason := decision.Reason
		if reason == "" {
			reason = "user_rejected"
		}
		return &Result{Status: StatusDenied, ChangeSetID: rec.ID, Reason: reason}, nil
	}

	if err := applyRollback(steps); err != nil {
		e.finish(rec, store.ChangeSetFailed, started, "rollback failed: "+err.Error())
		return &Result{Status: StatusFailed, ChangeSetID: rec.ID, Reason: err.Error()}, nil
	}

	rec.Status = store.ChangeSetRolledBack
	if err := e.Store.UpdateChangeSet(context.WithoutCancel(ctx), rec); err != nil {
		return nil, fmt.Errorf("journaling rollback: %w", err)
	}
	var results []OpResult
	for _, s := range steps {
		results = append(results, OpResult{Path: s.path, Status: s.status})
		e.Store.AppendAuditEvent(context.WithoutCancel(ctx), &store.AuditEvent{
			ChangeSetID: rec.ID, EventType: "rollback_" + s.opType,
			PathRelative: s.path, Result: "ok",
		})
	}
	e.Store.AppendAuditEvent(context.WithoutCancel(ctx), &store.AuditEvent{
		ChangeSetID: rec.ID, EventType: "change_set_rolled_back", Result: "ok",
		DurationMS: time.Since(started).Milliseconds(),
	})
	return &Result{Status: StatusRolledBack, ChangeSetID: rec.ID, Operations: results}, nil
}

// rollbackStep is one planned inverse operation.
type rollbackStep struct {
	opType  string // original operation type
	path    string // path reported in results (the restored path)
	status  string // restored | removed | moved_back
	run     func() error
	preview OpPreview
}

// planRollback verifies current state against the post-apply hashes and
// builds the inverse steps (in reverse operation order).
func planRollback(ws *workspace.Workspace, ops []Operation, rec *store.ChangeSet, backupDir string) ([]*rollbackStep, *Conflict, error) {
	var steps []*rollbackStep
	for i := len(ops) - 1; i >= 0; i-- {
		op := ops[i]
		switch op.Type {
		case OpCreate, OpUpdate:
			// Stored paths passed the sandbox at apply time; resolving again
			// is defense in depth against a tampered journal.
			abs, _, err := ws.Resolve(op.Path, sandbox.OpWrite)
			if err != nil {
				return nil, nil, err
			}
			after := rec.AfterHashes[op.Path]
			current := hashFile(abs)
			if current != after {
				return nil, changedConflict(ws, op.Path, current, backupContent(backupDir, op.Path)), nil
			}
			if op.Type == OpCreate {
				steps = append(steps, &rollbackStep{
					opType: op.Type, path: op.Path, status: "removed",
					run: func() error { return os.Remove(abs) },
					preview: OpPreview{Type: OpDelete, Path: op.Path, Diff: diffText(op.Path, op.Content, ""),
						Sensitive: ws.Sensitive(op.Path)},
				})
			} else {
				old, err := os.ReadFile(filepath.Join(backupDir, filepath.FromSlash(op.Path)))
				if err != nil {
					return nil, nil, fmt.Errorf("backup for %s is missing: %w", op.Path, err)
				}
				mode := fileModeOr(abs, 0o644)
				steps = append(steps, &rollbackStep{
					opType: op.Type, path: op.Path, status: "restored",
					run: func() error { return AtomicWrite(abs, old, mode) },
					preview: OpPreview{Type: OpUpdate, Path: op.Path, Diff: diffText(op.Path, op.Content, string(old)),
						Sensitive: ws.Sensitive(op.Path)},
				})
			}
		case OpMove:
			fromAbs, _, err := ws.Resolve(op.From, sandbox.OpWrite)
			if err != nil {
				return nil, nil, err
			}
			toAbs, _, err := ws.Resolve(op.To, sandbox.OpDelete)
			if err != nil {
				return nil, nil, err
			}
			after := rec.AfterHashes[op.To]
			if current := hashFile(toAbs); current != after {
				return nil, changedConflict(ws, op.To, current, ""), nil
			}
			if fromInfo, err := os.Lstat(fromAbs); err == nil {
				toInfo, statErr := os.Lstat(toAbs)
				if statErr != nil || !os.SameFile(fromInfo, toInfo) {
					return nil, changedConflict(ws, op.From, hashFile(fromAbs), ""), nil
				}
			}
			steps = append(steps, &rollbackStep{
				opType: op.Type, path: op.From, status: "moved_back",
				run: func() error { return os.Rename(toAbs, fromAbs) },
				preview: OpPreview{Type: OpMove, Path: op.To, To: op.From,
					Sensitive: ws.Sensitive(op.To) || ws.Sensitive(op.From)},
			})
		case OpDelete:
			abs, _, err := ws.Resolve(op.Path, sandbox.OpWrite)
			if err != nil {
				return nil, nil, err
			}
			if _, err := os.Lstat(abs); !os.IsNotExist(err) {
				return nil, changedConflict(ws, op.Path, hashFile(abs), backupContent(backupDir, op.Path)), nil
			}
			backupPath := filepath.Join(backupDir, filepath.FromSlash(op.Path))
			info, err := os.Stat(backupPath)
			if err != nil {
				return nil, nil, fmt.Errorf("backup for %s is missing: %w", op.Path, err)
			}
			if info.IsDir() {
				steps = append(steps, &rollbackStep{
					opType: op.Type, path: op.Path, status: "restored",
					run:     func() error { return copyTreeDurable(backupPath, abs) },
					preview: OpPreview{Type: OpCreate, Path: op.Path, Sensitive: ws.Sensitive(op.Path)},
				})
			} else {
				data, err := os.ReadFile(backupPath)
				if err != nil {
					return nil, nil, fmt.Errorf("backup for %s is unreadable: %w", op.Path, err)
				}
				steps = append(steps, &rollbackStep{
					opType: op.Type, path: op.Path, status: "restored",
					run: func() error { return AtomicWrite(abs, data, info.Mode().Perm()) },
					preview: OpPreview{Type: OpCreate, Path: op.Path, Diff: diffText(op.Path, "", string(data)),
						Sensitive: ws.Sensitive(op.Path)},
				})
			}
		}
	}
	return steps, nil, nil
}

// applyRollback runs the inverse steps; a failure restores nothing further
// but reports precisely what failed.
func applyRollback(steps []*rollbackStep) error {
	for _, s := range steps {
		if err := s.run(); err != nil {
			return fmt.Errorf("restoring %s: %w", s.path, err)
		}
	}
	return nil
}

func rollbackApprovalRequest(ws *workspace.Workspace, rec *store.ChangeSet, steps []*rollbackStep) *ApprovalRequest {
	ar := &ApprovalRequest{
		ChangeSetID:   "rollback:" + rec.ID,
		WorkspaceID:   ws.ID(),
		WorkspaceName: ws.Name(),
		Provider:      rec.Provider,
		Summary:       "Roll back change set " + rec.ID + " (" + rec.Summary + ")",
		Kind:          KindWrite,
	}
	for _, s := range steps {
		ar.Operations = append(ar.Operations, s.preview)
	}
	return ar
}

// changedConflict reports a path that moved on from its post-apply state.
// For an ordinary file it carries a diff of what rollback would overwrite
// (show the diff, never overwrite silently); for a sensitive one
// it carries the fact and the hash but not the contents, for the reason
// spelled out below.
func changedConflict(ws *workspace.Workspace, path, currentSHA, restoreContent string) *Conflict {
	c := &Conflict{
		Path:          path,
		Reason:        "changed_since_apply",
		CurrentSHA256: currentSHA,
		Action:        "the file changed after this change set was applied; review the diff and resolve manually",
	}
	if restoreContent == "" {
		return c
	}
	// This diff is the file's pre-image, and the caller it reaches is the
	// remote one: the conflict is returned before Approve is ever called,
	// so no approval and no read confirmation stands between the platform
	// and these bytes. For an ordinary file that is the contract as written —
	// show what a rollback would overwrite instead of overwriting silently.
	// For a sensitive file it is the secret itself, and an exact read of
	// the same path costs a local confirmation (security.md); a write to it
	// is marked high-risk. The caller keeps what it needs to act — the
	// file moved on, and its current hash — and the contents stay here.
	if ws.Sensitive(path) {
		c.Action = "the file changed after this change set was applied; it is a sensitive file, so its previous contents are not included here — review the change on the local machine and resolve manually"
		return c
	}
	c.Diff = diffText(path, "", restoreContent)
	return c
}

// backupContent best-effort reads a backup pre-image for conflict display.
func backupContent(backupDir, rel string) string {
	data, err := os.ReadFile(filepath.Join(backupDir, filepath.FromSlash(rel)))
	if err != nil {
		return ""
	}
	return string(data)
}

func fileModeOr(path string, fallback os.FileMode) os.FileMode {
	if info, err := os.Stat(path); err == nil {
		return info.Mode().Perm()
	}
	return fallback
}
