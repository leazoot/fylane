package txn

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/leazoot/fylane/companion/internal/store"
)

// defaultMaxBackupBytes is the backup-area size budget (7 days or
// 1 GiB, whichever is hit first).
const defaultMaxBackupBytes = int64(1) << 30

// CleanupBackups enforces the backup retention policy: backups whose
// rollback window has passed are removed, and when the backup area still
// exceeds the size budget, the oldest remaining backups have their rollback
// window closed (deadline set to now) and are then removed — a backup is
// never deleted while its rollback window is open.
func (e *Engine) CleanupBackups(ctx context.Context) (removed int, err error) {
	maxBytes := e.MaxBackupBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxBackupBytes
	}
	entries, err := os.ReadDir(e.BackupRoot)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}

	type backup struct {
		rec  *store.ChangeSet
		dir  string
		size int64
	}
	var kept []backup
	var total int64
	now := time.Now()

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(e.BackupRoot, entry.Name())
		rec, err := e.Store.GetChangeSet(ctx, entry.Name())
		if errors.Is(err, store.ErrNotFound) {
			// Orphan directory (e.g. crash between backup and journal).
			if err := os.RemoveAll(dir); err == nil {
				removed++
			}
			continue
		}
		if err != nil {
			return removed, err
		}
		if now.After(rec.RollbackDeadline) {
			if err := os.RemoveAll(dir); err != nil {
				return removed, err
			}
			removed++
			continue
		}
		size := dirSize(dir)
		total += size
		kept = append(kept, backup{rec: rec, dir: dir, size: size})
	}

	// Size pressure: close the oldest rollback windows first, then delete.
	sort.Slice(kept, func(i, j int) bool {
		return kept[i].rec.AppliedAt.Before(kept[j].rec.AppliedAt)
	})
	for _, b := range kept {
		if total <= maxBytes {
			break
		}
		b.rec.RollbackDeadline = now
		if err := e.Store.UpdateChangeSet(ctx, b.rec); err != nil {
			return removed, err
		}
		if err := os.RemoveAll(b.dir); err != nil {
			return removed, err
		}
		e.Store.AppendAuditEvent(ctx, &store.AuditEvent{
			ChangeSetID: b.rec.ID,
			EventType:   "backup_evicted",
			Result:      "size budget exceeded; rollback window closed early",
		})
		total -= b.size
		removed++
	}
	return removed, nil
}

func dirSize(dir string) int64 {
	var total int64
	filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}
