package app

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/leazoot/fylane/companion/internal/store"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func addRun(t *testing.T, st *store.Store, runID, outcome string) {
	t.Helper()
	e := &store.ExecEvent{Argv: []string{"npm", "install"}, Outcome: outcome,
		RunID: runID, Provider: "chatgpt", DirRelative: "web"}
	if err := st.AppendExecEvent(context.Background(), e); err != nil {
		t.Fatalf("appending %s: %v", outcome, err)
	}
}

func outcomes(t *testing.T, st *store.Store) []string {
	t.Helper()
	rows, err := st.ListExecEvents(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Outcome)
	}
	return out
}

// The defect this exists for: the Core dies, the child process dies with it,
// the engine's call never returns, and the command leaves no record at all.
// The task screen showed nothing and task_status said "unknown task id" for
// work that had really run and really may have written to the workspace.
func TestARestartAnswersTheCommandItWasRunning(t *testing.T) {
	st := testStore(t)
	addRun(t, st, "run_1", store.OutcomeStarted)

	reconcileRuns(context.Background(), st, quietLog())

	got := outcomes(t, st)
	if len(got) != 1 || got[0] != store.OutcomeInterrupted {
		t.Fatalf("history = %v, want one interrupted row", got)
	}
	e, err := st.FindRun(context.Background(), "run_1")
	if err != nil || e == nil {
		t.Fatalf("FindRun = %+v, %v", e, err)
	}
	// The row has to be able to describe the command, or the screen shows an
	// interrupted something.
	if len(e.Argv) != 2 || e.Provider != "chatgpt" || e.DirRelative != "web" {
		t.Errorf("interrupted row lost the command it describes: %+v", e)
	}
	if e.Reason == "" {
		t.Error("the interrupted row does not say why it exists")
	}
}

// A command that finished is already answered. Writing a second row for it
// would put a phantom interrupted command in the history of every restart.
func TestARestartLeavesFinishedCommandsAlone(t *testing.T) {
	st := testStore(t)
	addRun(t, st, "run_1", store.OutcomeStarted)
	addRun(t, st, "run_1", "ok")

	reconcileRuns(context.Background(), st, quietLog())

	if got := outcomes(t, st); len(got) != 1 || got[0] != "ok" {
		t.Fatalf("history = %v, want the finished command untouched", got)
	}
}

// Startup runs this every time. The second pass must find nothing, or one
// interrupted command grows a new row on every launch forever.
func TestAnsweringTheSameOrphanTwiceAddsNothing(t *testing.T) {
	st := testStore(t)
	addRun(t, st, "run_1", store.OutcomeStarted)

	ctx := context.Background()
	reconcileRuns(ctx, st, quietLog())
	reconcileRuns(ctx, st, quietLog())
	reconcileRuns(ctx, st, quietLog())

	if got := outcomes(t, st); len(got) != 1 {
		t.Fatalf("history = %v, want exactly one answer", got)
	}
}

// A database from before migration 0005 is full of finished commands with no
// run_id. None of them is open, and inventing an interrupted command for each
// on the first launch after upgrading would be the worst possible first
// impression of this feature.
func TestUpgradingDoesNotInventInterruptedCommands(t *testing.T) {
	st := testStore(t)
	addRun(t, st, "", "ok")
	addRun(t, st, "", "failed")
	addRun(t, st, "", "refused")

	reconcileRuns(context.Background(), st, quietLog())

	got := outcomes(t, st)
	if len(got) != 3 {
		t.Fatalf("history = %v, want the three original rows and nothing else", got)
	}
	for _, o := range got {
		if o == store.OutcomeInterrupted {
			t.Fatal("upgrading invented an interrupted command")
		}
	}
}

// The exit code is what a caller reads to decide whether the command worked.
// Zero would say it succeeded, which is the one thing nobody knows.
func TestAnInterruptedCommandDoesNotReportSuccess(t *testing.T) {
	st := testStore(t)
	addRun(t, st, "run_1", store.OutcomeStarted)

	reconcileRuns(context.Background(), st, quietLog())

	e, err := st.FindRun(context.Background(), "run_1")
	if err != nil || e == nil {
		t.Fatalf("FindRun = %+v, %v", e, err)
	}
	if e.ExitCode == 0 {
		t.Error("an interrupted command reported exit code 0")
	}
	// Duration is not carried across: the only number available is how long
	// Fylane was off, which is not how long the command ran.
	if e.DurationMS != 0 {
		t.Errorf("duration = %d, want none rather than an invented one", e.DurationMS)
	}
}
