package txn

import (
	"context"
	"sort"
	"time"

	"github.com/aymanbagabas/go-udiff"

	"github.com/leazoot/fylane/companion/internal/workspace"
)

// Impact on the approval face.
//
// This is the half of the idea that is not borrowed. A review gate that runs
// after the fact can only put "this function has twelve callers" in a report.
// Fylane's gate is before the write lands, so the same number is not a
// finding, it is the thing the user is deciding with.

// impactBudget bounds the whole change set's impact work. It is a hard
// ceiling rather than a per-operation one because what is being protected is
// the approval prompt: a person is waiting for it, and platforms cut the tool
// call off on their own schedule. A change set that runs out of budget shows
// the impact it managed to compute and nothing for the rest.
const impactBudget = 1500 * time.Millisecond

// OpImpact is how far one operation reaches: the symbols it disturbs, and how
// many places outside the file use them.
//
// Absence and zero are different answers and both are said. A nil OpImpact
// means nobody asked — no language server was warm, the file has no server,
// the budget ran out. Callers: 0 means the question was asked and the answer
// is that nothing else uses this. Collapsing the two would make the quiet row
// ambiguous, which is the failure this field exists to avoid.
type OpImpact struct {
	// Symbols are the names asked about, in the order they appear in the file.
	Symbols []string `json:"symbols,omitempty"`
	// Callers counts uses in other files. Uses within this same file are not
	// counted: the user is being shown this file's diff, so what they cannot
	// see is what is elsewhere.
	Callers int `json:"callers"`
	// Partial says the count is a floor — more symbols changed than were
	// asked about, or the budget ended the work early.
	Partial bool `json:"partial,omitempty"`
}

// ImpactRequest names one operation to weigh.
type ImpactRequest struct {
	WorkspaceID string
	// Root is the absolute workspace root. It stays on this machine: the
	// impact travels as a number, and OpPreview goes only to the local
	// approver.
	Root string
	// Path is workspace-relative.
	Path string
	// Lines are the 1-based pre-image lines the operation disturbs. Empty
	// means the whole file, which is what a delete does.
	Lines []int
}

// Impacter answers how far a change reaches.
//
// It returns no error, and that is a decision rather than an omission. There
// is nothing a caller could do with one: a write must not fail because a
// number could not be computed, and the absence of the line is already the
// complete and correct report of "nobody asked". Logging it is not the
// alternative either — the errors carry workspace paths, and paths do not go
// into logs (backend.md).
type Impacter interface {
	Impact(ctx context.Context, req ImpactRequest) *OpImpact
}

// attachImpact fills in the impact of each operation, within one budget for
// the whole change set.
func attachImpact(ctx context.Context, imp Impacter, ws *workspace.Workspace, plan []*plannedOp) {
	if imp == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, impactBudget)
	defer cancel()
	for _, p := range plan {
		if ctx.Err() != nil {
			return
		}
		lines, ok := impactLines(p)
		if !ok {
			continue
		}
		p.preview.Impact = imp.Impact(ctx, ImpactRequest{
			WorkspaceID: ws.ID(), Root: ws.Root(), Path: p.canonical, Lines: lines})
	}
}

// impactLines says which pre-image lines an operation disturbs, and whether
// the question applies to it at all.
func impactLines(p *plannedOp) ([]int, bool) {
	switch {
	case p.op.Type == OpUpdate:
		lines := changedLines(string(p.oldData), p.op.Content)
		return lines, len(lines) > 0
	case p.op.Type == OpDelete && !p.isDir:
		// The whole file goes, so every symbol in it is in question.
		return nil, true
	default:
		// A file being created has no callers yet. A move does not change
		// what a symbol is. A directory delete has no one file to ask about,
		// and it already carries the louder warning of the two.
		return nil, false
	}
}

// changedLines is the pre-image lines an edit touches, 1-based and sorted.
func changedLines(before, after string) []int {
	edits := udiff.Lines(before, after)
	if len(edits) == 0 {
		return nil
	}
	starts := lineStarts(before)
	seen := map[int]bool{}
	var out []int
	for _, e := range edits {
		// End is exclusive. Reading it as a position would pull in the line
		// after the change: replacing "two\n" ends at the first byte of
		// "three", which is not a line the edit touches.
		last := e.End
		if last > e.Start {
			last--
		}
		for ln := lineAt(starts, e.Start); ln <= lineAt(starts, last); ln++ {
			if !seen[ln] {
				seen[ln] = true
				out = append(out, ln)
			}
		}
	}
	sort.Ints(out)
	return out
}

// lineStarts is the byte offset each line begins at.
func lineStarts(s string) []int {
	starts := []int{0}
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			starts = append(starts, i+1)
		}
	}
	return starts
}

// lineAt is the 1-based line containing a byte offset.
func lineAt(starts []int, offset int) int {
	i := sort.SearchInts(starts, offset)
	if i < len(starts) && starts[i] == offset {
		return i + 1
	}
	return i
}
