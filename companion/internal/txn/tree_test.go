package txn

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTree(t *testing.T, root string, files map[string]int) {
	t.Helper()
	for rel, size := range files {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// The number is the reason F19's second click is worth asking for. Without it
// a node_modules and a src render the same sentence, and a second press on
// the same words is a reflex rather than a decision.
func TestMeasuringATreeCountsEveryDepthAndOnlyTheContent(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]int{
		"a.txt":          10,
		"pkg/b.txt":      20,
		"pkg/deep/c.txt": 30,
		"pkg/deep/d.txt": 40,
	})
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := measureTree(context.Background(), root)
	if got.Files != 4 {
		t.Errorf("files = %d, want 4", got.Files)
	}
	if got.Bytes != 100 {
		t.Errorf("bytes = %d, want 100", got.Bytes)
	}
	// Directories are not files. A count that included them would inflate
	// every number by the shape of the tree rather than its content.
	if got.Partial {
		t.Error("a small tree reported an unfinished count")
	}
}

func TestAnUnfinishedCountSaysSoRatherThanUnderreporting(t *testing.T) {
	root := t.TempDir()
	files := map[string]int{}
	for i := 0; i < 400; i++ {
		files[filepath.ToSlash(filepath.Join("d", string(rune('a'+i%26)), "f"+string(rune('a'+i/26))+".txt"))] = 1
	}
	writeTree(t, root, files)

	// A budget that is already spent: the walk must stop and mark the answer
	// a floor. Reporting the partial count as exact is the failure here —
	// "12 files" for a tree of thousands is worse than no number at all.
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	got := measureTree(ctx, root)
	if !got.Partial {
		t.Error("a count that ran out of budget did not say so")
	}
}

func TestBeyondUndoIsSaidOnlyWhenItIsKnown(t *testing.T) {
	const limit = int64(1000)
	cases := []struct {
		name string
		tree *TreeSize
		want bool
	}{
		{"nothing measured", nil, false},
		{"comfortably under", &TreeSize{Files: 2, Bytes: 10}, false},
		{"over the limit", &TreeSize{Files: 2, Bytes: 2000}, true},
		{"exactly the limit", &TreeSize{Files: 2, Bytes: limit}, true},
		// A floor under the limit is not evidence of anything: the tree may
		// be larger, and warning about a maybe is how warnings stop being
		// read.
		{"unfinished and under", &TreeSize{Files: 2, Bytes: 10, Partial: true}, false},
		// A floor already past it is evidence.
		{"unfinished and already over", &TreeSize{Files: 2, Bytes: 2000, Partial: true}, true},
	}
	for _, c := range cases {
		if got := c.tree.BeyondUndo(limit); got != c.want {
			t.Errorf("%s: BeyondUndo = %v, want %v", c.name, got, c.want)
		}
	}
	// No configured limit means no claim.
	if (&TreeSize{Bytes: 1 << 40}).BeyondUndo(0) {
		t.Error("an unconfigured limit produced a warning")
	}
}

func TestOnlyARecursiveDeleteIsMeasured(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]int{"tree/a.txt": 5})

	plan := []*plannedOp{
		{abs: filepath.Join(root, "tree"), preview: OpPreview{Type: OpDelete, RecursiveDelete: true}},
		{abs: filepath.Join(root, "tree"), preview: OpPreview{Type: OpDelete}},
		{abs: filepath.Join(root, "tree"), preview: OpPreview{Type: OpUpdate}},
	}
	attachTrees(context.Background(), plan)

	if plan[0].preview.Tree == nil || plan[0].preview.Tree.Files != 1 {
		t.Errorf("the recursive delete was not measured: %+v", plan[0].preview.Tree)
	}
	// An empty-directory delete and an update are not asked about. A zero
	// here would render as "an empty directory", which is a different claim
	// from "nobody counted".
	for _, p := range plan[1:] {
		if p.preview.Tree != nil {
			t.Errorf("%s was measured: %+v", p.preview.Type, p.preview.Tree)
		}
	}
}
