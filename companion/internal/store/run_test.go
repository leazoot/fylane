package store

import (
	"context"
	"testing"
)

func appendRun(t *testing.T, s *Store, runID, outcome string) *ExecEvent {
	t.Helper()
	// No workspace id: it is a foreign key, and none of these assertions is
	// about workspaces.
	e := &ExecEvent{Argv: []string{"go", "test"}, Outcome: outcome, RunID: runID,
		Provider: "chatgpt", DirRelative: "pkg"}
	if err := s.AppendExecEvent(context.Background(), e); err != nil {
		t.Fatalf("appending %s: %v", outcome, err)
	}
	return e
}

// The whole point of the pairing: a run whose 'started' row has no sibling is
// a command that was running when the Core stopped.
func TestAStartedRunWithNoEndIsAnOrphan(t *testing.T) {
	s := openTestStore(t)
	appendRun(t, s, "run_open", OutcomeStarted)
	appendRun(t, s, "run_closed", OutcomeStarted)
	appendRun(t, s, "run_closed", "ok")

	got, err := s.OrphanedRuns(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("orphans = %d, want only the run that never ended", len(got))
	}
	if got[0].RunID != "run_open" {
		t.Errorf("orphan = %q, want run_open", got[0].RunID)
	}
	// The answer has to carry enough to write the interrupted row with.
	if got[0].Provider != "chatgpt" || got[0].DirRelative != "pkg" || len(got[0].Argv) != 2 {
		t.Errorf("orphan lost the detail needed to describe it: %+v", got[0])
	}
}

// Rows written before migration 0005 have no run_id. They are complete
// records of finished commands and must never be read as open ones — every
// one of them would otherwise become an invented interrupted command on the
// first start after upgrading.
func TestRowsWithoutARunIDAreNeverOrphans(t *testing.T) {
	s := openTestStore(t)
	appendRun(t, s, "", OutcomeStarted)
	appendRun(t, s, "", "ok")

	got, err := s.OrphanedRuns(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("orphans = %d, want none: an unpairable row is not an open one", len(got))
	}
}

// An interrupted row is terminal, so writing one closes the orphan. Without
// that, startup would answer the same run again on every launch.
func TestAnsweringAnOrphanClosesIt(t *testing.T) {
	s := openTestStore(t)
	appendRun(t, s, "run_1", OutcomeStarted)
	appendRun(t, s, "run_1", OutcomeInterrupted)

	got, err := s.OrphanedRuns(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("orphans = %d, want the answered run closed", len(got))
	}
}

// History is what the desktop draws. A 'started' row there would draw every
// command twice — once as the moment it began, once as what it did.
func TestHistoryLeavesOutTheStartedRows(t *testing.T) {
	s := openTestStore(t)
	appendRun(t, s, "run_1", OutcomeStarted)
	appendRun(t, s, "run_1", "ok")

	got, err := s.ListExecEvents(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("history rows = %d, want one row for one command", len(got))
	}
	if got[0].Outcome != "ok" {
		t.Errorf("history row = %q, want the outcome rather than the start", got[0].Outcome)
	}
	if got[0].RunID != "run_1" {
		t.Error("the history row cannot be tied back to its run")
	}
}

func TestFindRunReportsHowARunEnded(t *testing.T) {
	s := openTestStore(t)
	appendRun(t, s, "run_1", OutcomeStarted)
	appendRun(t, s, "run_1", OutcomeInterrupted)

	got, err := s.FindRun(context.Background(), "run_1")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Outcome != OutcomeInterrupted {
		t.Fatalf("FindRun = %+v, want the interrupted row", got)
	}
}

// A run that is still going has only a 'started' row, and that is not an
// answer. Returning it would let task_status report a live command as
// finished with exit code 0.
func TestFindRunSaysNothingWhileARunIsStillOpen(t *testing.T) {
	s := openTestStore(t)
	appendRun(t, s, "run_1", OutcomeStarted)

	got, err := s.FindRun(context.Background(), "run_1")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("FindRun = %+v, want nothing for a run with no outcome yet", got)
	}
}

func TestFindRunIsSilentForAnUnknownID(t *testing.T) {
	s := openTestStore(t)
	for _, id := range []string{"", "run_missing"} {
		got, err := s.FindRun(context.Background(), id)
		if err != nil {
			t.Fatalf("FindRun(%q): %v", id, err)
		}
		if got != nil {
			t.Errorf("FindRun(%q) = %+v, want nothing", id, got)
		}
	}
}
