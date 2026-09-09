package txn

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/leazoot/fylane/companion/internal/store"
)

// Recover cleans up after a crash (database rules): every change
// set left in a non-terminal state is marked failed — its approval or apply
// was interrupted and must be re-requested — and staging temp files next to
// its targets are removed. Returns the number of change sets cleaned.
func (e *Engine) Recover(ctx context.Context) (int, error) {
	stale, err := e.Store.ListChangeSetsByStatus(ctx, store.ChangeSetPending, store.ChangeSetApproved)
	if err != nil {
		return 0, err
	}
	for _, rec := range stale {
		e.cleanTempFiles(ctx, rec)
		rec.Status = store.ChangeSetFailed
		if err := e.Store.UpdateChangeSet(ctx, rec); err != nil {
			return 0, fmt.Errorf("marking change set %s failed: %w", rec.ID, err)
		}
		e.Store.AppendAuditEvent(ctx, &store.AuditEvent{
			ChangeSetID: rec.ID,
			EventType:   "change_set_recovered",
			Result:      "interrupted transaction marked failed",
		})
	}
	return len(stale), nil
}

// cleanTempFiles removes leftover staging files in the directories the
// change set was writing to. Best-effort: recovery must not fail because a
// workspace vanished.
func (e *Engine) cleanTempFiles(ctx context.Context, rec *store.ChangeSet) {
	ws, err := e.Store.GetWorkspace(ctx, rec.WorkspaceID)
	if err != nil {
		return
	}
	var ops []Operation
	if err := json.Unmarshal(rec.Operations, &ops); err != nil {
		return
	}
	dirs := map[string]bool{}
	for _, op := range ops {
		for _, rel := range []string{op.Path, op.To} {
			if rel == "" {
				continue
			}
			dirs[filepath.Dir(filepath.Join(ws.RootPath, filepath.FromSlash(rel)))] = true
		}
	}
	for dir := range dirs {
		matches, err := filepath.Glob(filepath.Join(dir, tmpPattern))
		if err != nil {
			continue
		}
		for _, m := range matches {
			os.Remove(m)
		}
	}
}
