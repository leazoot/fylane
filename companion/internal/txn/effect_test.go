package txn

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// The reconciler is the half that makes the three values worth
// having: without it the engine would only have renamed "we do not know"
// three ways.

func writeFile(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return hashBytes([]byte(content))
}

func TestEffectOfEveryOperationInEveryState(t *testing.T) {
	const before, after, other = "before\n", "after\n", "something else\n"
	beforeSHA, afterSHA := hashBytes([]byte(before)), hashBytes([]byte(after))

	// Each case sets up one path in one of the three states the reconciler
	// has to tell apart, then asks what happened to it.
	for _, tc := range []struct {
		name  string
		build func(t *testing.T, dir string) *plannedOp
		want  Effect
	}{
		{"create that never ran", func(t *testing.T, dir string) *plannedOp {
			return &plannedOp{op: Operation{Type: OpCreate}, canonical: "new.txt",
				abs: filepath.Join(dir, "new.txt"), afterSHA: afterSHA}
		}, NotStarted},
		{"create that was left behind", func(t *testing.T, dir string) *plannedOp {
			writeFile(t, filepath.Join(dir, "new.txt"), after)
			return &plannedOp{op: Operation{Type: OpCreate}, canonical: "new.txt",
				abs: filepath.Join(dir, "new.txt"), afterSHA: afterSHA}
		}, StateChanged},
		{"create whose file is now a third thing", func(t *testing.T, dir string) *plannedOp {
			writeFile(t, filepath.Join(dir, "new.txt"), other)
			return &plannedOp{op: Operation{Type: OpCreate}, canonical: "new.txt",
				abs: filepath.Join(dir, "new.txt"), afterSHA: afterSHA}
		}, OutcomeUnknown},

		{"update that was restored", func(t *testing.T, dir string) *plannedOp {
			writeFile(t, filepath.Join(dir, "f.txt"), before)
			return &plannedOp{op: Operation{Type: OpUpdate}, canonical: "f.txt",
				abs: filepath.Join(dir, "f.txt"), beforeSHA: beforeSHA, afterSHA: afterSHA}
		}, NotStarted},
		{"update that stuck", func(t *testing.T, dir string) *plannedOp {
			writeFile(t, filepath.Join(dir, "f.txt"), after)
			return &plannedOp{op: Operation{Type: OpUpdate}, canonical: "f.txt",
				abs: filepath.Join(dir, "f.txt"), beforeSHA: beforeSHA, afterSHA: afterSHA}
		}, StateChanged},
		{"update whose file vanished", func(t *testing.T, dir string) *plannedOp {
			return &plannedOp{op: Operation{Type: OpUpdate}, canonical: "f.txt",
				abs: filepath.Join(dir, "f.txt"), beforeSHA: beforeSHA, afterSHA: afterSHA}
		}, OutcomeUnknown},

		{"delete that was restored", func(t *testing.T, dir string) *plannedOp {
			writeFile(t, filepath.Join(dir, "f.txt"), before)
			return &plannedOp{op: Operation{Type: OpDelete}, canonical: "f.txt",
				abs: filepath.Join(dir, "f.txt"), beforeSHA: beforeSHA}
		}, NotStarted},
		{"delete that stuck", func(t *testing.T, dir string) *plannedOp {
			return &plannedOp{op: Operation{Type: OpDelete}, canonical: "f.txt",
				abs: filepath.Join(dir, "f.txt"), beforeSHA: beforeSHA}
		}, StateChanged},
		// "It is not there" and "I could not read it" are opposite
		// conclusions for a caller asking whether a delete landed, and
		// hashFile folds both into an empty string. A directory sitting
		// where the file was produces the second without needing chmod,
		// which does not mean the same thing on every platform or for
		// every user.
		{"delete whose path is now a directory", func(t *testing.T, dir string) *plannedOp {
			if err := os.MkdirAll(filepath.Join(dir, "f.txt"), 0o755); err != nil {
				t.Fatal(err)
			}
			return &plannedOp{op: Operation{Type: OpDelete}, canonical: "f.txt",
				abs: filepath.Join(dir, "f.txt"), beforeSHA: beforeSHA}
		}, OutcomeUnknown},
		{"create whose path is now a directory", func(t *testing.T, dir string) *plannedOp {
			if err := os.MkdirAll(filepath.Join(dir, "new.txt"), 0o755); err != nil {
				t.Fatal(err)
			}
			return &plannedOp{op: Operation{Type: OpCreate}, canonical: "new.txt",
				abs: filepath.Join(dir, "new.txt"), afterSHA: afterSHA}
		}, OutcomeUnknown},

		{"delete whose file came back as something else", func(t *testing.T, dir string) *plannedOp {
			writeFile(t, filepath.Join(dir, "f.txt"), other)
			return &plannedOp{op: Operation{Type: OpDelete}, canonical: "f.txt",
				abs: filepath.Join(dir, "f.txt"), beforeSHA: beforeSHA}
		}, OutcomeUnknown},

		{"directory delete that stuck", func(t *testing.T, dir string) *plannedOp {
			return &plannedOp{op: Operation{Type: OpDelete}, canonical: "d", isDir: true,
				abs: filepath.Join(dir, "d")}
		}, StateChanged},
		{"directory delete that was copied back", func(t *testing.T, dir string) *plannedOp {
			writeFile(t, filepath.Join(dir, "d", "x.txt"), before)
			return &plannedOp{op: Operation{Type: OpDelete}, canonical: "d", isDir: true,
				abs: filepath.Join(dir, "d")}
		}, OutcomeUnknown}, // a tree that is back is not thereby proved complete

		{"move that was reversed", func(t *testing.T, dir string) *plannedOp {
			writeFile(t, filepath.Join(dir, "a.txt"), before)
			return &plannedOp{op: Operation{Type: OpMove}, canonical: "a.txt", toCanonical: "b.txt",
				abs: filepath.Join(dir, "a.txt"), toAbs: filepath.Join(dir, "b.txt"),
				beforeSHA: beforeSHA, afterSHA: beforeSHA}
		}, NotStarted},
		{"move that stuck", func(t *testing.T, dir string) *plannedOp {
			writeFile(t, filepath.Join(dir, "b.txt"), before)
			return &plannedOp{op: Operation{Type: OpMove}, canonical: "a.txt", toCanonical: "b.txt",
				abs: filepath.Join(dir, "a.txt"), toAbs: filepath.Join(dir, "b.txt"),
				beforeSHA: beforeSHA, afterSHA: beforeSHA}
		}, StateChanged},
		{"move that left the file in both places", func(t *testing.T, dir string) *plannedOp {
			writeFile(t, filepath.Join(dir, "a.txt"), before)
			writeFile(t, filepath.Join(dir, "b.txt"), before)
			return &plannedOp{op: Operation{Type: OpMove}, canonical: "a.txt", toCanonical: "b.txt",
				abs: filepath.Join(dir, "a.txt"), toAbs: filepath.Join(dir, "b.txt"),
				beforeSHA: beforeSHA, afterSHA: beforeSHA}
		}, OutcomeUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.build(t, t.TempDir())
			if got := effectOf(p); got != tc.want {
				t.Errorf("effectOf = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestReconcileDecidesEveryOperation(t *testing.T) {
	// The property that makes the list safe to read: a path that appears in
	// the plan appears in the effects, with a value from the vocabulary.
	// An operation the reconciler forgot and one it decided as unknown are
	// different failures, and only this keeps them from printing the same.
	dir := t.TempDir()
	plan := []*plannedOp{
		{op: Operation{Type: OpCreate}, canonical: "a.txt", abs: filepath.Join(dir, "a.txt")},
		{op: Operation{Type: OpUpdate}, canonical: "b.txt", abs: filepath.Join(dir, "b.txt")},
		{op: Operation{Type: OpDelete}, canonical: "c.txt", abs: filepath.Join(dir, "c.txt")},
		{op: Operation{Type: OpMove}, canonical: "d.txt", toCanonical: "e.txt",
			abs: filepath.Join(dir, "d.txt"), toAbs: filepath.Join(dir, "e.txt")},
		{op: Operation{Type: "invented_later"}, canonical: "f.txt", abs: filepath.Join(dir, "f.txt")},
	}
	effects := reconcile(plan)
	if len(effects) != len(plan) {
		t.Fatalf("got %d effects for %d operations", len(effects), len(plan))
	}
	for i, e := range effects {
		if !e.Effect.Valid() {
			t.Errorf("operation %d (%s) came back with %q, which is not one of the three",
				i, e.Path, e.Effect)
		}
	}
	// An operation type this function has never been taught about must be
	// reported as undecidable rather than assumed harmless.
	if effects[4].Effect != OutcomeUnknown {
		t.Errorf("an unknown operation type reported %q, want %q", effects[4].Effect, OutcomeUnknown)
	}
	// A move is reported at the path the caller asked about.
	if effects[3].Path != "e.txt" {
		t.Errorf("move effect path = %q, want the destination", effects[3].Path)
	}
}

func TestOnlyTheThreeValuesAreValid(t *testing.T) {
	for _, e := range []Effect{NotStarted, StateChanged, OutcomeUnknown} {
		if !e.Valid() {
			t.Errorf("%q is in the vocabulary but Valid says otherwise", e)
		}
	}
	for _, e := range []Effect{"", "probably", "not_started "} {
		if e.Valid() {
			t.Errorf("%q is not in the vocabulary but Valid accepted it", e)
		}
	}
}

// approverThatMovesTheWorld says yes and, while the caller is waiting,
// changes the disk under the plan. That is not a contrived setup: approval is
// the one step of the fourteen where an arbitrary amount of wall-clock time
// passes with a human in it, and step 11 re-checks precisely because of it.
type approverThatMovesTheWorld struct {
	do func()
}

func (a *approverThatMovesTheWorld) Approve(_ context.Context, _ *ApprovalRequest) (Decision, error) {
	a.do()
	return Decision{Approved: true}, nil
}

func TestAFailedChangeSetSaysWhatItLeftBehind(t *testing.T) {
	f := newFixture(t)
	f.engine.Approver = &approverThatMovesTheWorld{do: func() {
		// b.txt appears while the user is deciding, with content that is
		// neither the before state (it did not exist) nor the after state.
		f.write(t, "b.txt", "written by something else\n")
	}}

	res, err := f.engine.Execute(context.Background(), f.ws, Request{
		Provider: "test", Summary: "two creates",
		Operations: []Operation{
			{Type: OpCreate, Path: "a.txt", Content: "a\n"},
			{Type: OpCreate, Path: "b.txt", Content: "b\n"},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != StatusFailed {
		t.Fatalf("status = %q, want %q", res.Status, StatusFailed)
	}

	got := map[string]Effect{}
	for _, e := range res.Effects {
		got[e.Path] = e.Effect
	}
	if len(got) != 2 {
		t.Fatalf("effects = %+v, want one per operation", res.Effects)
	}
	// This is the whole point of the task. Before it, the result said the
	// same thing about both paths — re-read them — and a.txt had provably
	// not changed.
	if got["a.txt"] != NotStarted {
		t.Errorf("a.txt = %q, want %q: it was created and then removed by the "+
			"restore, so the world is exactly as it was", got["a.txt"], NotStarted)
	}
	if got["b.txt"] != OutcomeUnknown {
		t.Errorf("b.txt = %q, want %q: it is in neither the before state nor "+
			"the after state", got["b.txt"], OutcomeUnknown)
	}
	if content, ok := f.read(t, "a.txt"); ok {
		t.Errorf("a.txt still holds %q; the effect claimed the restore had removed it", content)
	}
}
