package cmdexec

import (
	"context"
	"testing"
)

func (r *recorder) all() []Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Record(nil), r.recs...)
}

// A run has to be visible while it is running, not only once it ends. That is
// the whole mechanism: the Core that dies mid-command writes the first row
// and never the second, and startup reads the gap.
func TestARunIsJournalledBeforeItEndsAndAfter(t *testing.T) {
	runner, rec := newRunner(t)
	root := newWorkspace(t)

	if _, err := runner.Run(context.Background(), Spec{
		Root: root, Argv: []string{"echo", "hi"}, RunID: "run_1",
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := rec.all()
	if len(got) != 2 {
		t.Fatalf("records = %d, want a start and an end", len(got))
	}
	if got[0].Outcome != OutcomeStarted {
		t.Errorf("first record = %q, want %q", got[0].Outcome, OutcomeStarted)
	}
	if got[1].Outcome != OutcomeOK {
		t.Errorf("second record = %q, want %q", got[1].Outcome, OutcomeOK)
	}
	for i, r := range got {
		if r.RunID != "run_1" {
			t.Errorf("record %d run id = %q, want them paired under run_1", i, r.RunID)
		}
	}
}

// A command refused before the process exists never ran. A 'started' row for
// it would make the next startup report an interrupted command that never
// happened — an invented event in an audit log is worse than a missing one.
func TestAnAttemptThatNeverStartedIsNotJournalledAsStarted(t *testing.T) {
	runner, rec := newRunner(t)
	root := newWorkspace(t)

	if _, err := runner.Run(context.Background(), Spec{
		Root: root, Argv: []string{"definitely-not-a-real-program-9f2a"}, RunID: "run_1",
	}); err == nil {
		t.Fatal("running a program that does not exist should fail")
	}

	got := rec.all()
	if len(got) != 1 {
		t.Fatalf("records = %v, want only the refusal", got)
	}
	if got[0].Outcome != OutcomeRefused {
		t.Errorf("record = %q, want %q", got[0].Outcome, OutcomeRefused)
	}
}

// Journalling is opt-in per run, so the paths that do not pass an id — and
// every caller written before it existed — keep writing exactly one row.
func TestARunWithNoIDIsJournalledOnlyOnce(t *testing.T) {
	runner, rec := newRunner(t)
	root := newWorkspace(t)

	if _, err := runner.Run(context.Background(), Spec{
		Root: root, Argv: []string{"echo", "hi"},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := rec.all(); len(got) != 1 || got[0].Outcome != OutcomeOK {
		t.Fatalf("records = %v, want a single outcome row", got)
	}
}
