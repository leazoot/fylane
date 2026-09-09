package txn

import (
	"context"
	"io/fs"
	"path/filepath"
	"time"
)

// How much a recursive delete is about to take.
//
// The recursive-delete gate was a boolean until now: the approval face said
// "the whole directory and everything in it" for a node_modules and for a src,
// in the same words. A second click on that sentence adds friction and no
// knowledge — it trains people to click twice. What changes the decision is
// the size of the thing, so the number comes first and the second click is
// gated on having it.

// treeBudget bounds the measuring for a whole change set, not one delete. The
// same reasoning as impactBudget: a person is waiting for this prompt and the
// platform's tool call is on its own clock, so an exact count that arrives ten
// seconds late is the worse trade. A measurement that runs out says so.
const treeBudget = 750 * time.Millisecond

// TreeSize is what a recursive delete is about to remove.
type TreeSize struct {
	// Files counts regular files under the directory, at every depth.
	Files int `json:"files"`
	// Bytes is their total size. Directories themselves are not counted:
	// what a person is deciding about is the content.
	Bytes int64 `json:"bytes"`
	// Partial says both numbers are floors — the budget ended the walk. "At
	// least fifty thousand files" is enough to decide on, and it is honest,
	// which an exact number that never arrives is not.
	Partial bool `json:"partial,omitempty"`
}

// BeyondUndo reports whether the copy this delete makes cannot fit in the
// recycle area, so the undo will not survive.
//
// Only ever true when it is known: a partial measurement whose floor is still
// under the limit answers false, because "we did not finish counting" is not
// evidence of anything. The face says nothing in that case rather than
// warning about a maybe.
func (t *TreeSize) BeyondUndo(limit int64) bool {
	return t != nil && limit > 0 && t.Bytes >= limit
}

// attachTrees measures every recursive delete in the plan. It runs where
// attachImpact does, after the change set is recorded and before the question,
// so a slow walk costs the prompt and never the journal.
func attachTrees(ctx context.Context, plan []*plannedOp) {
	ctx, cancel := context.WithTimeout(ctx, treeBudget)
	defer cancel()
	for _, p := range plan {
		if !p.preview.RecursiveDelete {
			continue
		}
		if ctx.Err() != nil {
			// Out of budget and not yet started: no number rather than a
			// zero, which would read as an empty directory.
			return
		}
		p.preview.Tree = measureTree(ctx, p.abs)
	}
}

// measureTree walks the directory counting regular files and their bytes.
//
// Symlinks are counted as the links they are and never followed: following
// one would count a tree that is not being deleted, and the delete itself does
// not follow them either.
func measureTree(ctx context.Context, root string) *TreeSize {
	size := &TreeSize{}
	err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable corner is not a reason to abandon the count. The
			// number is a floor either way, and saying nothing would be worse
			// than saying "at least this much".
			size.Partial = true
			return nil
		}
		if ctx.Err() != nil {
			size.Partial = true
			return filepath.SkipAll
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			size.Partial = true
			return nil
		}
		size.Files++
		size.Bytes += info.Size()
		return nil
	})
	if err != nil {
		size.Partial = true
	}
	return size
}

// treeLimit is the recycle area's size budget as this engine is configured.
func (e *Engine) treeLimit() int64 {
	if e.MaxBackupBytes > 0 {
		return e.MaxBackupBytes
	}
	return defaultMaxBackupBytes
}
